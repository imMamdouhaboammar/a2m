package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPDiscoveryRespectsSelectedWorkspace(t *testing.T) {
	approved := t.TempDir()
	foreign := t.TempDir()
	configFile := filepath.Join(t.TempDir(), "providers.json")
	fixture := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"global-helper": map[string]string{"command": "echo"},
		},
		"projects": map[string]interface{}{
			approved: map[string]interface{}{"mcpServers": map[string]interface{}{
				"workspace-helper": map[string]string{"command": "echo"},
			}},
			foreign: map[string]interface{}{"mcpServers": map[string]interface{}{
				"foreign-helper": map[string]string{"command": "echo"},
			}},
		},
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2M_MCP_DISCOVERY_CONFIG", configFile)

	actual := listDiscoveredMCPServers(approved)
	names := make(map[string]bool)
	for _, server := range actual {
		names[server.Name] = true
	}
	if !names["global-helper"] || !names["workspace-helper"] || names["foreign-helper"] {
		t.Fatalf("wrong workspace inventory: %+v", actual)
	}
	withoutWorkspace := listDiscoveredMCPServers("")
	if len(withoutWorkspace) != 1 || withoutWorkspace[0].Name != "global-helper" {
		t.Fatalf("missing workspace must not expose any project-scoped provider: %+v", withoutWorkspace)
	}
}
