package multiplex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

func dialControlSocket(socketPath string) (net.Conn, error) {
	if socketPath == "" {
		socketPath = "/tmp/a2m-session.sock"
		if runtime.GOOS == "windows" {
			socketPath = "127.0.0.1:58433"
		}
	}

	network := "unix"
	addr := socketPath
	if strings.Contains(addr, ":") || runtime.GOOS == "windows" {
		network = "tcp"
		if !strings.Contains(addr, ":") {
			addr = "127.0.0.1:58433"
		}
	}

	conn, err := net.Dial(network, addr)
	if err != nil {
		// For custom socket overrides, retry the standard A2M session socket.
		if network == "unix" && addr != "/tmp/a2m-session.sock" {
			if legacyConn, legacyErr := net.Dial("unix", "/tmp/a2m-session.sock"); legacyErr == nil {
				return legacyConn, nil
			}
		}
		return nil, fmt.Errorf("could not connect to A2M control socket at %s (%s). Is A2M running? (%w)", addr, network, err)
	}
	return conn, nil
}

func RunPs(socketPath string) error {
	conn, err := dialControlSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	_, _ = conn.Write([]byte(`{"action":"list"}` + "\n"))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("error reading sessions: %w", err)
	}

	var sessions []process.SessionSummary
	if err := json.Unmarshal(line, &sessions); err != nil {
		return fmt.Errorf("invalid response from daemon: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No active or historical A2M process sessions found.")
		return nil
	}

	fmt.Printf("%-8s  %-16s  %-12s  %s\n", "PID", "STATE", "RUNTIME", "COMMAND")
	fmt.Println(strings.Repeat("-", 75))
	for _, s := range sessions {
		runtimeStr := fmt.Sprintf("%dms", s.RuntimeMs)
		if s.RuntimeMs > 1000 {
			runtimeStr = fmt.Sprintf("%.1fs", float64(s.RuntimeMs)/1000.0)
		}
		cmdTrunc := s.Command
		if len(cmdTrunc) > 40 {
			cmdTrunc = cmdTrunc[:37] + "..."
		}
		fmt.Printf("%-8d  %-16s  %-12s  %s\n", s.PID, s.State, runtimeStr, cmdTrunc)
	}
	return nil
}

func RunWatch(socketPath string) error {
	conn, err := dialControlSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	_, _ = conn.Write([]byte(`{"action":"watch"}` + "\n"))
	reader := bufio.NewReader(conn)

	// Consume handshake line
	_, _ = reader.ReadBytes('\n')

	fmt.Println("\033[1;32m[A2M]\033[0m Live multiplex watch started. Streaming all AI process outputs (Ctrl+C to stop)...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\033[1;33m[A2M]\033[0m Watcher detached.")
		_ = conn.Close()
		os.Exit(0)
	}()

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil
		}

		var ev SessionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		color := "\033[36m" // Cyan for stdout
		if ev.Stream == "stderr" {
			color = "\033[31m" // Red for stderr
		}

		ts := ev.Timestamp.Format("15:04:05")
		fmt.Printf("%s%s [PID %d | %s]\033[0m %s\n", color, ts, ev.PID, ev.Stream, ev.Text)
	}
}

func RunAttach(socketPath string, pid int) error {
	conn, err := dialControlSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	attachMsg, _ := json.Marshal(ControlMessage{
		Action: "attach",
		PID:    pid,
	})
	_, _ = conn.Write(append(attachMsg, '\n'))

	reader := bufio.NewReader(conn)
	// Handshake response
	_, _ = reader.ReadBytes('\n')

	fmt.Printf("\033[1;32m[A2M]\033[0m Attached to PID %d.\n", pid)
	fmt.Println("Type commands to interact with process stdin. Press Ctrl+C to detach.")
	fmt.Println(strings.Repeat("-", 60))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Printf("\n\033[1;33m[A2M]\033[0m Detached from PID %d (process continues running).\n", pid)
		_ = conn.Close()
		os.Exit(0)
	}()

	// Stdin forwarder
	go func() {
		stdinReader := bufio.NewReader(os.Stdin)
		for {
			inputLine, err := stdinReader.ReadString('\n')
			if err != nil {
				return
			}
			msg, _ := json.Marshal(ControlMessage{
				Action: "stdin",
				PID:    pid,
				Data:   inputLine,
			})
			_, _ = conn.Write(append(msg, '\n'))
		}
	}()

	// Output listener
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil
		}

		var ev SessionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		fmt.Println(ev.Text)
	}
}

func RunKill(socketPath string, pid int) error {
	conn, err := dialControlSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	msg, _ := json.Marshal(ControlMessage{
		Action: "kill",
		PID:    pid,
	})
	_, _ = conn.Write(append(msg, '\n'))
	fmt.Printf("\033[1;32m[A2M]\033[0m Terminated process group PID %d.\n", pid)
	return nil
}

func RunStatus(serverURL string) error {
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8765"
	}
	resp, err := http.Get(serverURL + "/api/status")
	if err != nil {
		return fmt.Errorf("could not connect to A2M daemon at %s: %w", serverURL, err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	bytes, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(bytes))
	return nil
}

func RunConnect(serverURL string) error {
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8765"
	}
	resp, err := http.Post(serverURL+"/api/connect", "application/json", nil)
	if err != nil {
		return fmt.Errorf("connect request failed: %w", err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	fmt.Printf("\033[1;32m[A2M Relay]\033[0m Status: %v\n", data["status"])
	return nil
}

func RunDisconnect(serverURL string, killAll bool) error {
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8765"
	}
	endpoint := serverURL + "/api/disconnect"
	if killAll {
		endpoint += "?killAll=true"
	}
	resp, err := http.Post(endpoint, "application/json", nil)
	if err != nil {
		return fmt.Errorf("disconnect request failed: %w", err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	fmt.Printf("\033[1;33m[A2M Relay]\033[0m Status: %v\n", data["status"])
	return nil
}
