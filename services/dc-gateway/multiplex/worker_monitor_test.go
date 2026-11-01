package multiplex

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/desktop-commander/dc-gateway/process"
)

func shortWorkerTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "a2mwm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "worker.sock")
}

func awaitWorkerMonitor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("worker monitor condition not reached before deadline")
}

func TestWorkerMonitorReadsRealWorkerSessionsAndOutput(t *testing.T) {
	socket := shortWorkerTestSocket(t)
	supervisor := process.NewSupervisor()
	hub := NewMultiplexHub(supervisor, socket)
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	monitor := NewWorkerMonitor(socket)
	monitor.Start()
	defer monitor.Close()
	awaitWorkerMonitor(t, 3*time.Second, monitor.Connected)

	cmd := "printf 'a2m-worker-observer-line\\n'"
	result, err := supervisor.StartProcess(cmd, 1500, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitWorkerMonitor(t, 3*time.Second, func() bool {
		lines, ok := monitor.Snapshot(result.PID)
		return ok && len(lines) > 0 && strings.Contains(lines[0].Text, "a2m-worker-observer-line")
	})
	sessions, err := monitor.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, session := range sessions {
		if session.PID == result.PID && session.Command == cmd {
			found = true
			if session.Source != "mcp-worker" {
				t.Errorf("wrong session source: %q", session.Source)
			}
		}
	}
	if !found {
		t.Fatalf("remote session %d missing", result.PID)
	}
}

func TestWorkerMonitorRecoversAfterWorkerRestart(t *testing.T) {
	socket := shortWorkerTestSocket(t)
	hub := NewMultiplexHub(process.NewSupervisor(), socket)
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}

	monitor := NewWorkerMonitor(socket)
	monitor.Start()
	defer monitor.Close()
	awaitWorkerMonitor(t, 3*time.Second, monitor.Connected)

	hub.Close()
	awaitWorkerMonitor(t, 3*time.Second, func() bool { return !monitor.Connected() })

	nextSupervisor := process.NewSupervisor()
	nextHub := NewMultiplexHub(nextSupervisor, socket)
	if err := nextHub.Start(); err != nil {
		t.Fatal(err)
	}
	defer nextHub.Close()

	awaitWorkerMonitor(t, 5*time.Second, monitor.Connected)
	result, err := nextSupervisor.StartProcess("printf 'reconnect-ok\\n'", 1500, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitWorkerMonitor(t, 3*time.Second, func() bool {
		lines, ok := monitor.Snapshot(result.PID)
		return ok && len(lines) > 0 && lines[0].Text == "reconnect-ok"
	})
}

func TestWorkerMonitorRejectsMissingOrSymlinkSocket(t *testing.T) {
	socket := shortWorkerTestSocket(t)
	monitor := NewWorkerMonitor(socket)
	if _, err := monitor.ListSessions(); err == nil {
		t.Fatal("missing socket should fail")
	}
	target := filepath.Join(t.TempDir(), "fake")
	if err := os.Symlink(target, socket); err != nil {
		t.Fatal(err)
	}
	if _, err := monitor.ListSessions(); err == nil {
		t.Fatal("symlink socket should fail closed")
	}
}

func TestWorkerMonitorRedactsCommonCredentials(t *testing.T) {
	input := "Authorization: Bearer abcdefghijklmnopqrst token=very-sensitive-secret sk-proj-abcdefghijklmnopqrstuvwxyz123456"
	output := redactMonitorText(input)
	for _, raw := range []string{"abcdefghijklmnopqrst", "very-sensitive-secret", "sk-proj-abcdefghijklmnopqrstuvwxyz123456"} {
		if strings.Contains(output, raw) {
			t.Errorf("secret leaked: %s", raw)
		}
	}
	if !strings.Contains(output, "[redacted]") {
		t.Fatalf("redaction marker absent: %s", output)
	}
}

func TestWorkerMonitorRemoteTerminateRequiresTrackedRunningPID(t *testing.T) {
	socket := shortWorkerTestSocket(t)
	supervisor := process.NewSupervisor()
	hub := NewMultiplexHub(supervisor, socket)
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	monitor := NewWorkerMonitor(socket)
	if err := monitor.Kill(999999); err == nil {
		t.Fatal("untracked process must not be terminated")
	}
	if _, err := net.Dial("unix", socket); err != nil {
		t.Fatalf("test hub unavailable: %v", err)
	}
}
