package multiplex

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeSidecarsOnDemandAndReachable(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "a2m-sidecar-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "ipc.sock")
	fsPath := filepath.Join(dir, "fs-engine")
	docsPath := filepath.Join(dir, "doc-service")
	if err := os.WriteFile(fsPath, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	response := "#!/bin/sh\nprintf '%s\\n' '{\"service\":\"a2m-docs\",\"status\":\"ready\",\"mode\":\"on-demand\"}'\n"
	if err := os.WriteFile(docsPath, []byte(response), 0700); err != nil {
		t.Fatal(err)
	}

	idle := probeSidecarsAt(socketPath, fsPath, docsPath)
	if idle["fsEngine"] != "installed (on-demand)" || idle["docService"] != "ready (on-demand)" {
		t.Fatalf("expected accurate idle service states, got %+v", idle)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	online := probeSidecarsAt(socketPath, fsPath, docsPath)
	if online["fsEngine"] != "reachable" || online["docService"] != "ready (on-demand)" {
		t.Fatalf("expected actual socket availability, got %+v", online)
	}
}

func TestProbeSidecarsDoesNotInventHealth(t *testing.T) {
	dir := t.TempDir()
	docsPath := filepath.Join(dir, "bad-doc-service")
	if err := os.WriteFile(docsPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"status\":\"ready\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	states := probeSidecarsAt(filepath.Join(dir, "absent.sock"), "", docsPath)
	if states["fsEngine"] != "unavailable" || states["docService"] != "unavailable" {
		t.Fatalf("missing socket and invalid health must not appear healthy: %+v", states)
	}
}
