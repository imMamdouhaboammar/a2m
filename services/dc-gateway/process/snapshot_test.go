package process

import (
	"strings"
	"testing"
	"time"
)

func TestSnapshotOutputPreservesAgentReadWatermark(t *testing.T) {
	supervisor := NewSupervisor()
	result, err := supervisor.StartProcess("printf 'snapshot-alpha\\nsnapshot-beta\\n'", 1500, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	supervisor.mu.RLock()
	session := supervisor.sessions[result.PID]
	supervisor.mu.RUnlock()
	session.Mu.RLock()
	before := session.ReadWatermark
	session.Mu.RUnlock()

	lines, err := supervisor.SnapshotOutput(result.PID, 10)
	if err != nil {
		t.Fatal(err)
	}
	session.Mu.RLock()
	after := session.ReadWatermark
	session.Mu.RUnlock()
	if before != after {
		t.Fatalf("snapshot consumed output watermark: before=%d, after=%d", before, after)
	}
	if len(lines) == 0 || !strings.Contains(lines[0].Text, "snapshot-alpha") {
		t.Fatalf("snapshot content: %+v", lines)
	}
	if _, err := supervisor.SnapshotOutput(-1, 10); err == nil {
		t.Fatal("missing session should fail")
	}
}

func TestMultipleOutputListenersReceiveSameEvents(t *testing.T) {
	supervisor := NewSupervisor()
	a := make(chan string, 4)
	b := make(chan string, 4)
	stopA := supervisor.AddBroadcaster(func(_ int, _ string, _ string, line string) { a <- line })
	stopB := supervisor.AddBroadcaster(func(_ int, _ string, _ string, line string) { b <- line })
	defer stopA()
	defer stopB()
	_, err := supervisor.StartProcess("printf 'a2m-broadcast-test\\n'", 1500, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, ch := range []chan string{a, b} {
		select {
		case line := <-ch:
			if !strings.Contains(line, "a2m-broadcast-test") {
				t.Errorf("subscriber %d saw unexpected %q", i, line)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("subscriber %d missed output", i)
		}
	}
}
