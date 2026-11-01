package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
)

// A real stdio JSON-RPC MCP fixture, not a mocked listTools response.
func TestExternalMCPStdioFixture(t *testing.T) {
	if os.Getenv("A2M_MCP_FIXTURE") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == 0 {
			continue
		}
		var result interface{}
		switch request.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]string{"name": "fixture", "version": "1.0"},
			}
		case "tools/list":
			schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"message": map[string]string{"type": "string"}}}
			result = map[string]interface{}{"tools": []ToolDefinition{
				{Name: "echo", Description: "Echos a test string", InputSchema: schema},
				{Name: "forbidden", Description: "Must remain hidden", InputSchema: schema},
			}}
		case "tools/call":
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Message string `json:"message"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Name != "echo" {
				result = ErrorResult("unexpected tool")
			} else {
				result = TextResult("echo:" + params.Arguments.Message)
			}
		default:
			result = map[string]interface{}{}
		}
		reply, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": result})
		fmt.Fprintln(os.Stdout, string(reply))
	}
	os.Exit(0)
}

func setupExternalMCPTest(t *testing.T, approvals string) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configuration := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"fixture": externalMCPLaunch{
				Command: binary,
				Args:    []string{"-test.run=^TestExternalMCPStdioFixture$"},
				Env: map[string]string{
					"A2M_MCP_FIXTURE":       "1",
					"CONTROL_PLANE_API_KEY": "MUST_NOT_FORWARD",
				},
			},
		},
	}
	bytes, _ := json.Marshal(configuration)
	providerFile := filepath.Join(root, "providers.json")
	if err := os.WriteFile(providerFile, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	approvalFile := filepath.Join(root, "approvals.json")
	if err := os.WriteFile(approvalFile, []byte(approvals), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2M_MCP_DISCOVERY_CONFIG", providerFile)
	t.Setenv("A2M_MCP_APPROVALS_FILE", approvalFile)
	cfg := config.NewConfigManager(filepath.Join(root, "gateway.json"))
	if err := cfg.SetValue("capabilityWorkspace", root); err != nil {
		t.Fatal(err)
	}
	r := &Registry{
		handlers:        make(map[string]ToolHandler),
		sessionHandlers: make(map[string]SessionToolHandler),
		callCounts:      make(map[string]int),
		configMgr:       cfg,
		auth:            auth.NewAuthenticator("", auth.RoleAdmin, false),
	}
	r.registerExternalMCP()
	t.Cleanup(r.Close)
	return r, approvalFile
}

func TestExternalMCPApprovalExportsOnlyExplicitTool(t *testing.T) {
	r, _ := setupExternalMCPTest(t, `{"servers":[{"name":"fixture","source":"user","tools":["echo"]}]}`)
	tools := r.ListTools()
	if len(tools) != 1 || tools[0].Name != "ext_user_fixture__echo" {
		t.Fatalf("unexpected forwarded tool list: %+v", tools)
	}
	if len(r.externalProcesses) != 1 || r.externalActive["user:fixture"] != 1 {
		t.Fatalf("approved process not running: %+v", r.externalActive)
	}
	result := r.Dispatch("ext_user_fixture__echo", json.RawMessage(`{"message":"hello"}`))
	if result.IsError || len(result.Content) == 0 || result.Content[0].Text != "echo:hello" {
		t.Fatalf("approved tool call failed: %+v", result)
	}
	denied := r.Dispatch("ext_user_fixture__forbidden", json.RawMessage(`{}`))
	if !denied.IsError {
		t.Fatalf("unapproved provider tool was callable: %+v", denied)
	}
	list := r.externalServerStatus()
	if len(list) != 1 || list[0].State != "active:1" {
		t.Fatalf("inventory did not identify active approved service: %+v", list)
	}
}

func TestExternalMCPMissingApprovalNeverStartsProvider(t *testing.T) {
	r, _ := setupExternalMCPTest(t, `{"servers":[]}`)
	if len(r.ListTools()) != 0 || len(r.externalProcesses) != 0 {
		t.Fatal("provider launched without approval")
	}
	for _, server := range r.externalServerStatus() {
		if server.State != "disabled" {
			t.Fatalf("unapproved service status %q", server.State)
		}
	}
}

func TestExternalMCPUnsafeApprovalPermissionsRejected(t *testing.T) {
	r, file := setupExternalMCPTest(t, `{"servers":[]}`)
	valid := `{"servers":[{"name":"fixture","source":"user","tools":["echo"]}]}`
	if err := os.WriteFile(file, []byte(valid), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	if loaded := loadExternalApprovals(); len(loaded) != 0 {
		t.Fatalf("world-writable approval file accepted: %+v", loaded)
	}
	if len(r.externalProcesses) != 0 {
		t.Fatal("unapproved process was launched")
	}
}

func TestExternalMCPProjectScopeCannotBorrowOtherProjects(t *testing.T) {
	r, _ := setupExternalMCPTest(t, `{"servers":[{"name":"fixture","source":"project","tools":["echo"]}]}`)
	if len(r.externalProcesses) != 0 || len(r.ListTools()) != 0 {
		t.Fatal("a user-scope entry must not satisfy project-scoped approval")
	}
}

func TestExternalMCPControlKeyNotForwarded(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/private/should-not-forward")
	t.Setenv("CONTROL_PLANE_API_KEY", "PARENT_MUST_NOT_FORWARD")
	env := strings.Join(safeExternalEnvironment(map[string]string{
		"CONTROL_PLANE_API_KEY": "MUST_NOT_FORWARD",
		"SENTRY_AUTH_TOKEN":     "APPROVED_PROVIDER_TOKEN",
	}), "\n")
	if strings.Contains(env, "MUST_NOT_FORWARD") ||
		strings.Contains(env, "should-not-forward") ||
		!strings.Contains(env, "SENTRY_AUTH_TOKEN=APPROVED_PROVIDER_TOKEN") {
		t.Fatal("provider environment isolation failed")
	}
}
