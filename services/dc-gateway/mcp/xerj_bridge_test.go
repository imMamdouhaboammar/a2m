package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestXerjExecutableDetection(t *testing.T) {
	exe := findXerjExecutable()
	if exe == "" {
		t.Skip("xerj binary not found in standard paths; skipping live binary test")
	}
	if !strings.HasSuffix(exe, "xerj") {
		t.Errorf("unexpected xerj executable path: %s", exe)
	}
}

func TestXerjDaemonHealth(t *testing.T) {
	url := xerjServerURL()
	healthy := checkXerjDaemonHealthy(url)
	if !healthy {
		t.Skip("xerj daemon not currently running on " + url)
	}
}

func TestXerjBridgeIntegrationWithLiveNode(t *testing.T) {
	exe := findXerjExecutable()
	if exe == "" {
		t.Skip("xerj binary not installed")
	}
	if !checkXerjDaemonHealthy(xerjServerURL()) {
		t.Skip("xerj daemon not running")
	}

	r := NewRegistry(nil, nil, nil, nil)
	defer r.Close()

	if !r.isXerjAvailable() {
		t.Fatalf("expected XERJ bridge to be available when binary and daemon are ready")
	}

	toolNames := make(map[string]bool)
	for _, tool := range r.ListTools() {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{
		"xerj_search",
		"xerj_def",
		"xerj_autoindex",
		"xerj_memory_store",
		"xerj_memory_recall",
		"xerj_code_search",
		"xerj_map",
		"xerj_brain_overview",
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q to be registered by xerj bridge", name)
		}
	}

	// 1. Verify capabilities_list contains xerj details
	caps := r.Dispatch("capabilities_list", json.RawMessage("{}"))
	if caps.IsError || len(caps.Content) == 0 {
		t.Fatalf("capabilities_list failed: %+v", caps)
	}
	if !strings.Contains(caps.Content[0].Text, `"xerj"`) || !strings.Contains(caps.Content[0].Text, `"daemon_healthy":true`) {
		t.Errorf("capabilities_list does not report healthy xerj: %s", caps.Content[0].Text)
	}

	// 2. Verify xerj_def lookup
	defRes := r.Dispatch("xerj_def", json.RawMessage(`{"symbol":"Registry"}`))
	if defRes.IsError || len(defRes.Content) == 0 {
		t.Fatalf("xerj_def failed: %+v", defRes)
	}
	if !strings.Contains(defRes.Content[0].Text, "services/dc-gateway/mcp/tools.go") {
		t.Errorf("xerj_def did not return expected path: %s", defRes.Content[0].Text)
	}

	// 3. Verify xerj_search query
	searchRes := r.Dispatch("xerj_search", json.RawMessage(`{"query":"Registry","size":3}`))
	if searchRes.IsError || len(searchRes.Content) == 0 {
		t.Fatalf("xerj_search failed: %+v", searchRes)
	}
	if !strings.Contains(searchRes.Content[0].Text, "tools.go") && !strings.Contains(searchRes.Content[0].Text, "Registry") {
		t.Errorf("xerj_search did not find expected code passage: %s", searchRes.Content[0].Text)
	}
}

func TestXerjDefMissingSymbol(t *testing.T) {
	r := NewRegistry(nil, nil, nil, nil)
	defer r.Close()

	res := r.Dispatch("xerj_def", json.RawMessage(`{}`))
	if !res.IsError {
		t.Errorf("expected error when symbol is omitted")
	}
}
