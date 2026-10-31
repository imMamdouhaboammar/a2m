package multiplex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

type EventType string

const (
	EventOutput EventType = "output"
	EventStart  EventType = "start"
	EventExit   EventType = "exit"
)

type SessionEvent struct {
	Source    string    `json:"source,omitempty"`
	Type      EventType `json:"type"`
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	Stream    string    `json:"stream,omitempty"`
	Text      string    `json:"text,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type ControlMessage struct {
	Action string `json:"action"` // "list", "watch", "attach", "stdin", "kill"
	PID    int    `json:"pid,omitempty"`
	Data   string `json:"data,omitempty"`
}

type clientSubscription struct {
	conn        net.Conn
	sendCh      chan string
	done        chan struct{}
	closeOnce   sync.Once
	watchAll    bool
	attachedPID int
	stateMu     sync.RWMutex
}

func (sub *clientSubscription) close() {
	sub.closeOnce.Do(func() {
		close(sub.done)
		_ = sub.conn.Close()
	})
}

func (sub *clientSubscription) writeLoop() {
	for {
		select {
		case line, ok := <-sub.sendCh:
			if !ok {
				return
			}
			_ = sub.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, err := sub.conn.Write([]byte(line + "\n"))
			if err != nil {
				sub.close()
				return
			}
		case <-sub.done:
			return
		}
	}
}

type MultiplexHub struct {
	supervisor *process.Supervisor
	socketPath string
	listener   net.Listener
	clients    map[net.Conn]*clientSubscription
	mu         sync.RWMutex
}

func NewMultiplexHub(supervisor *process.Supervisor, socketPath string) *MultiplexHub {
	if socketPath == "" {
		socketPath = "/tmp/a2m-session.sock"
		if runtime.GOOS == "windows" {
			socketPath = "127.0.0.1:58433"
		}
	}

	h := &MultiplexHub{
		supervisor: supervisor,
		socketPath: socketPath,
		clients:    make(map[net.Conn]*clientSubscription),
	}

	// Register broadcaster on supervisor
	supervisor.SetBroadcaster(func(pid int, cmd string, stream string, text string) {
		h.Broadcast(SessionEvent{
			Type:      EventOutput,
			PID:       pid,
			Command:   cmd,
			Stream:    stream,
			Text:      text,
			Timestamp: time.Now(),
		})
	})

	return h
}

func (h *MultiplexHub) Start() error {
	network := "unix"
	addr := h.socketPath
	if strings.Contains(addr, ":") || runtime.GOOS == "windows" {
		network = "tcp"
		if !strings.Contains(addr, ":") {
			addr = "127.0.0.1:58433"
		}
	} else {
		_ = os.Remove(addr)
	}

	ln, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("failed to listen on multiplex control socket %s (%s): %w", addr, network, err)
	}
	h.listener = ln

	if network == "unix" {
		_ = os.Chmod(addr, 0700)
		if addr == "/tmp/a2m-session.sock" {
			// Only the default gateway may manage the compatibility alias.
			// Custom test sockets must never replace the live monitor socket.
			// Do NOT clobber an active socket if another daemon/gateway is listening on it!
			if c, err := net.Dial("unix", "/tmp/desktop-commander-session.sock"); err == nil {
				_ = c.Close()
			} else {
				_ = os.Remove("/tmp/desktop-commander-session.sock")
				_ = os.Symlink(addr, "/tmp/desktop-commander-session.sock")
			}
		}
	}

	go h.acceptLoop()
	return nil
}

func (h *MultiplexHub) Close() {
	if h.listener != nil {
		_ = h.listener.Close()
	}
	h.mu.Lock()
	for _, sub := range h.clients {
		sub.close()
	}
	h.clients = make(map[net.Conn]*clientSubscription)
	h.mu.Unlock()
}

func (h *MultiplexHub) acceptLoop() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}

		h.mu.Lock()
		sub := &clientSubscription{
			conn:        conn,
			sendCh:      make(chan string, 256),
			done:        make(chan struct{}),
			watchAll:    false,
			attachedPID: 0,
		}
		h.clients[conn] = sub
		h.mu.Unlock()

		go sub.writeLoop()
		go h.handleClient(conn, sub)
	}
}

func (h *MultiplexHub) handleClient(conn net.Conn, sub *clientSubscription) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
		sub.close()
	}()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var msg ControlMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Action {
		case "list":
			sessions := h.supervisor.ListSessions()
			data, _ := json.Marshal(sessions)
			h.sendLine(sub, string(data))

		case "watch":
			sub.stateMu.Lock()
			sub.watchAll = true
			sub.attachedPID = 0
			sub.stateMu.Unlock()
			h.sendLine(sub, `{"status":"watching_all_sessions"}`)

		case "attach":
			sub.stateMu.Lock()
			sub.watchAll = false
			sub.attachedPID = msg.PID
			sub.stateMu.Unlock()
			h.sendLine(sub, fmt.Sprintf(`{"status":"attached_to_pid","pid":%d}`, msg.PID))

		case "stdin":
			if msg.PID > 0 && msg.Data != "" {
				_, _ = h.supervisor.InteractWithProcess(msg.PID, msg.Data, 0)
			}

		case "kill":
			if msg.PID > 0 {
				_ = h.supervisor.TerminateProcess(msg.PID, true)
			}
		}
	}
}

func (h *MultiplexHub) Broadcast(ev SessionEvent) {
	bytes, err := json.Marshal(ev)
	if err != nil {
		return
	}
	payload := string(bytes)

	h.mu.RLock()
	var targets []*clientSubscription
	for _, sub := range h.clients {
		sub.stateMu.RLock()
		shouldSend := sub.watchAll || (sub.attachedPID > 0 && sub.attachedPID == ev.PID)
		sub.stateMu.RUnlock()

		if shouldSend {
			targets = append(targets, sub)
		}
	}
	h.mu.RUnlock()

	for _, sub := range targets {
		h.sendLine(sub, payload)
	}
}

func (h *MultiplexHub) sendLine(sub *clientSubscription, text string) {
	select {
	case sub.sendCh <- text:
	default:
		// Queue saturated (slow consumer): drop to preserve hub throughput and prevent deadlock
	}
}
