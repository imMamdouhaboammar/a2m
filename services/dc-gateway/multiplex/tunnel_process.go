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
// tunnel-client's loopback port and verifies its executable before signalling.
// Never use broad pkill -f against unrelated command lines.
func stopExternalTunnelDaemon() error {
	result, err := exec.Command("lsof", "-nP", "-t", "-iTCP:8080", "-sTCP:LISTEN").Output()
	if err != nil {
		return fmt.Errorf("locate tunnel daemon: %w", err)
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
		return fmt.Errorf("listener PID %d is not a verified tunnel-client; refusing to stop", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
