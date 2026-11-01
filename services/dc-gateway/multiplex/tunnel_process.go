package multiplex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func parseListenerPID(output string) (int, error) {
	for _, line := range strings.Split(output, "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 1 {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no tunnel daemon listener PID found")
}

// stopExternalTunnelDaemon targets only the process currently listening on
// tunnel-client's loopback port (default 8080) and verifies its executable before signalling.
func stopExternalTunnelDaemon() error {
	return stopExternalTunnelDaemonOnPort(8080)
}

// stopExternalTunnelDaemonOnPort stops the verified tunnel-client on a specific port.
func stopExternalTunnelDaemonOnPort(port int) error {
	if port <= 0 {
		port = 8080
	}
	result, err := exec.Command("lsof", "-nP", "-t", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return fmt.Errorf("locate tunnel daemon on port %d: %w", port, err)
	}
	pid, err := parseListenerPID(string(result))
	if err != nil {
		return err
	}
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to terminate the dashboard gateway")
	}
	result, err = exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil || filepath.Base(strings.TrimSpace(string(result))) != "tunnel-client" {
		return fmt.Errorf("listener PID %d on port %d is not a verified tunnel-client; refusing to stop", pid, port)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
