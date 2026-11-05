package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
)

func TestCapabilityBridgeDiscoveryAndSafeDefaults(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "review-helper")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillText := "---\nname: review-helper\ndescription: Analyze changes without modifying files\n---\n# Review Helper\nUse git diff to review a workspace.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillText), 0644); err != nil {
		t.Fatal(err)
	}
	secret := "SUPER_PRIVATE_TEST_VALUE"
	configPath := filepath.Join(root, "claude.json")
	configText := `{"mcpServers":{"sentry":{"command":"npx","env":{"SENTRY_AUTH_TOKEN":"` + secret + `"}}},"projects":{"/tmp/other":{"mcpServers":{"local-helper":{"command":"node","args":["hidden-arg"]}}}}}`
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("A2M_SKILL_ROOTS", filepath.Join(root, "skills"))
	t.Setenv("A2M_MCP_DISCOVERY_CONFIG", configPath)
	t.Setenv("A2M_WORKSPACE_ROOT", "")
	r := NewRegistry(nil, nil, config.NewConfigManager(filepath.Join(root, "config.json")), nil)
	toolNames := make(map[string]bool)
	for _, tool := range r.ListTools() {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"capabilities_list", "skills_find", "skills_read", "mcp_servers_list"} {
		if !toolNames[name] {
			t.Errorf("native MCP tool %q missing from tools/list", name)
		}
	}

	catalog := r.Dispatch("capabilities_list", json.RawMessage("{}"))
	if catalog.IsError || len(catalog.Content) == 0 {
		t.Fatalf("capabilities_list failed: %+v", catalog)
	}
	if strings.Contains(catalog.Content[0].Text, secret) {
		t.Fatal("catalog leaked server credentials")
	}

	list := r.Dispatch("mcp_servers_list", json.RawMessage("{}"))
	if list.IsError {
		t.Fatalf("mcp_servers_list failed: %+v", list)
	}
	payload := list.Content[0].Text
	if !strings.Contains(payload, "sentry") {
		t.Fatalf("missing user-scoped server: %s", payload)
	}
	if strings.Contains(payload, "local-helper") {
		t.Fatalf("foreign-project MCP server must not appear in this workspace: %s", payload)
	}
	for _, forbidden := range []string{secret, "SENTRY_AUTH_TOKEN", "hidden-arg", "/tmp/other"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("discovery leaked sensitive configuration: %s", forbidden)
		}
	}
	if !strings.Contains(payload, "disabled") {
		t.Fatal("third-party MCP servers must default disabled")
	}

	found := r.Dispatch("skills_find", json.RawMessage(`{"query":"review"}`))
	if found.IsError || !strings.Contains(found.Content[0].Text, "review-helper") {
		t.Fatalf("skill not discoverable: %+v", found)
	}
	read := r.Dispatch("skills_read", json.RawMessage(`{"id":"0:review-helper"}`))
	if read.IsError || !strings.Contains(read.Content[0].Text, "Use git diff") {
		t.Fatalf("skill not readable: %+v", read)
	}
	escape := r.Dispatch("skills_read", json.RawMessage(`{"id":"0:../secret"}`))
	if !escape.IsError {
		t.Fatal("directory traversal in skills_read was accepted")
	}
}
