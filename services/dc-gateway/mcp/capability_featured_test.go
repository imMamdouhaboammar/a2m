package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeaturedCapabilitiesResolveCurrentLocalCLIs(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"omni-skill", "rea", "xerj"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	capabilities := (&Registry{}).featuredCapabilities()
	if len(capabilities) != 3 {
		t.Fatalf("expected 3 featured tools: %+v", capabilities)
	}
	for i, name := range []string{"omni-skill", "xerj", "rea"} {
		entry := capabilities[i]
		if entry.ID != name || !entry.Installed || entry.Executable != filepath.Join(binDir, name) {
			t.Fatalf("wrong current installation for %q: %+v", name, entry)
		}
		if entry.UseFor == "" || entry.Description == "" || entry.Priority != "premium" {
			t.Fatalf("missing semantic usage metadata: %+v", entry)
		}
	}
	// No stale hardcoded path: a changed installation resolves on the next query.
	nextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nextDir, "omni-skill"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", nextDir)
	updated := (&Registry{}).featuredCapabilities()
	if updated[0].Executable != filepath.Join(nextDir, "omni-skill") {
		t.Fatalf("installed tool path did not refresh: %+v", updated[0])
	}
}

func TestFeaturedCapabilitiesDoNotImplyMCPAuthorization(t *testing.T) {
	capabilities := (&Registry{}).featuredCapabilities()
	for _, c := range capabilities {
		if c.ID == "rea" && c.MCPAvailable {
			t.Fatal("REA should not be marked available without active operator-approved MCP tools")
		}
	}
}

func TestCapabilitiesListExposesAdvisoryPremiumCatalog(t *testing.T) {
	r := &Registry{
		handlers:        make(map[string]ToolHandler),
		sessionHandlers: make(map[string]SessionToolHandler),
	}
	r.registerCapabilities()
	handler := r.handlers["capabilities_list"]
	if handler == nil {
		t.Fatal("capability inventory tool is not registered")
	}
	result, err := handler(nil)
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("inventory unavailable: %+v %v", result, err)
	}
	var payload struct {
		CapabilitySelection string               `json:"capability_selection"`
		Featured            []featuredCapability `json:"featured_capabilities"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Featured) != 3 || payload.Featured[0].ID != "omni-skill" || payload.Featured[1].ID != "xerj" || payload.Featured[2].ID != "rea" {
		t.Fatalf("missing or out-of-order featured capabilities: %+v", payload.Featured)
	}
	if !strings.Contains(payload.CapabilitySelection, "AI client") {
		t.Fatalf("semantic routing ownership unclear: %q", payload.CapabilitySelection)
	}
}
