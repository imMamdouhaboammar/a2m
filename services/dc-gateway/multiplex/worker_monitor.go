package multiplex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

const (
	defaultWorkerSocket = "/tmp/desktop-commander-session.sock"
	maxWorkerLines      = 250
	maxWorkerPIDs       = 64
	maxWorkerTextSize   = 4096
)

var (
	monitorBearer     = regexp.MustCompile(`(?i)(\bBearer\s+)([A-Za-z0-9._~-]{10,})`)
	monitorAssignment = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|password|secret|token)\s*[:=]\s*["']?)([^\s"',;&]+)`)
	monitorSecret     = regexp.MustCompile(`(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{15,}|AKIA[A-Z0-9]{16})`)
)

// Redaction reduces accidental exposure in the desktop UI. It cannot recognize
// every secret and must never be advertised as a complete data-loss control.
func redactMonitorText(value string) string {
	value = monitorBearer.ReplaceAllString(value, "${1}[redacted]")
	value = monitorAssignment.ReplaceAllString(value, "${1}[redacted]")
	value = monitorSecret.ReplaceAllString(value, "[redacted]")
	if len(value) > maxWorkerTextSize {
		value = value[:maxWorkerTextSize] + " [truncated for desktop monitor]"
	}
	return value
}

// WorkerMonitor observes the independently spawned MCP stdio worker through
// its existing user-only Unix domain socket. It never consumes the worker's
// agent-facing output cursor, and does not control tunnel-client lifecycle.
type WorkerMonitor struct {
	socket      string
	mu          sync.RWMutex
	lines       map[int][]process.SnapshotLine
	order       []int
	subscribers map[uint64]func(SessionEvent)
	nextID      uint64
	started     sync.Once
	cancel      context.CancelFunc
	done        chan struct{}
	connected   atomic.Bool
}

func NewWorkerMonitor(socket string) *WorkerMonitor {
	if socket == "" {
		socket = defaultWorkerSocket
	}
	return &WorkerMonitor{
		socket:      socket,
		lines:       make(map[int][]process.SnapshotLine),
		subscribers: make(map[uint64]func(SessionEvent)),
		done:        make(chan struct{}),
	}
}

func (m *WorkerMonitor) Start() {
	m.started.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		go m.watch(ctx)
	})
}

func (m *WorkerMonitor) Close() {
	if m.cancel != nil {
		m.cancel()
		select {
		case <-m.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (m *WorkerMonitor) Connected() bool { return m.connected.Load() }

// Reject missing, symlinked and group/world-readable sockets. In particular,
// do not follow the dashboard gateway's compatibility symlink to itself.
func (m *WorkerMonitor) dial() (net.Conn, error) {
	info, err := os.Lstat(m.socket)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("MCP worker socket is not a private Unix socket")
	}
	return net.DialTimeout("unix", m.socket, 350*time.Millisecond)
}

func (m *WorkerMonitor) ListSessions() ([]process.SessionSummary, error) {
	conn, err := m.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1200 * time.Millisecond))
	if err := json.NewEncoder(conn).Encode(ControlMessage{Action: "list"}); err != nil {
		return nil, err
	}
	var sessions []process.SessionSummary
	if err := json.NewDecoder(conn).Decode(&sessions); err != nil {
		return nil, fmt.Errorf("MCP worker session list unavailable: %w", err)
	}
	if sessions == nil {
		sessions = []process.SessionSummary{}
	}
	for i := range sessions {
		sessions[i].Source = "mcp-worker"
		sessions[i].Command = redactMonitorText(sessions[i].Command)
	}
	return sessions, nil
}

func (m *WorkerMonitor) Snapshot(pid int) ([]process.SnapshotLine, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lines, ok := m.lines[pid]
	if !ok {
		return nil, false
	}
	copied := append([]process.SnapshotLine(nil), lines...)
	return copied, true
}

func (m *WorkerMonitor) Subscribe(callback func(SessionEvent)) func() {
	m.mu.Lock()
	m.nextID++
	id := m.nextID
	m.subscribers[id] = callback
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subscribers, id)
		m.mu.Unlock()
	}
}

func (m *WorkerMonitor) add(ev SessionEvent) {
	ev.Source = "mcp-worker"
	ev.Command = redactMonitorText(ev.Command)
	ev.Text = redactMonitorText(ev.Text)
	m.mu.Lock()
	if _, exists := m.lines[ev.PID]; !exists {
		if len(m.order) >= maxWorkerPIDs {
			old := m.order[0]
			m.order = m.order[1:]
			delete(m.lines, old)
		}
		m.order = append(m.order, ev.PID)
	}
	m.lines[ev.PID] = append(m.lines[ev.PID], process.SnapshotLine{
		Timestamp: ev.Timestamp, Stream: ev.Stream, Text: ev.Text,
	})
	if len(m.lines[ev.PID]) > maxWorkerLines {
		m.lines[ev.PID] = m.lines[ev.PID][len(m.lines[ev.PID])-maxWorkerLines:]
	}
	listeners := make([]func(SessionEvent), 0, len(m.subscribers))
	for _, callback := range m.subscribers {
		listeners = append(listeners, callback)
	}
	m.mu.Unlock()
	for _, callback := range listeners {
		callback(ev)
	}
}

func (m *WorkerMonitor) watch(ctx context.Context) {
	defer close(m.done)
	for ctx.Err() == nil {
		err := m.watchOnce(ctx)
		m.connected.Store(false)
		if err == nil && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (m *WorkerMonitor) watchOnce(ctx context.Context) error {
	conn, err := m.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(conn).Encode(ControlMessage{Action: "watch"}); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(conn, 128*1024)
	handshake, err := reader.ReadSlice('\n')
	if err != nil || !strings.Contains(string(handshake), "watching_all_sessions") {
		return errors.New("MCP worker refused watch subscription")
	}
	m.connected.Store(true)
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return err
		}
		var ev SessionEvent
		if json.Unmarshal(line, &ev) != nil || ev.PID <= 0 || ev.Type != EventOutput {
			continue
		}
		m.add(ev)
	}
	return nil
}

// Only an exact, currently tracked process in the remote MCP supervisor may
// be terminated. The legacy hub does not acknowledge kill, so this is a request
// only; the UI must refresh and confirm the observed process state.
func (m *WorkerMonitor) Kill(pid int) error {
	sessions, err := m.ListSessions()
	if err != nil {
		return err
	}
	permitted := false
	for _, session := range sessions {
		if session.PID == pid && (session.State == process.StateRunning || session.State == process.StateWaitingForInput) {
			permitted = true
			break
		}
	}
	if !permitted {
		return fmt.Errorf("PID %d is not an active MCP worker session", pid)
	}
	conn, err := m.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(750 * time.Millisecond))
	return json.NewEncoder(conn).Encode(ControlMessage{Action: "kill", PID: pid})
}
