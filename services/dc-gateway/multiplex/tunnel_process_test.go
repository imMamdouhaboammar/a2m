package multiplex

import "testing"

func TestParseListenerPID(t *testing.T) {
	pid, err := parseListenerPID(" 22333\n")
	if err != nil || pid != 22333 {
		t.Fatalf("listener PID: got %d, %v", pid, err)
	}
	if _, err := parseListenerPID("no listener\n"); err == nil {
		t.Fatal("must reject an unidentified tunnel daemon")
	}
}
