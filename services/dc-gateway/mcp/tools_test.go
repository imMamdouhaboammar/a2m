package mcp

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/ipc"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

func TestToolRegistryListingAndDispatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_gateway_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgPath := filepath.Join(tempDir, "config.json")
	cfgMgr := config.NewConfigManager(cfgPath)
	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/test_sock.sock")

	registry := NewRegistry(sup, fsClient, cfgMgr, nil)
	tools := registry.ListTools()

	if len(tools) < 20 {
		t.Fatalf("expected at least 20 tools registered, got %d", len(tools))
	}

	// Verify required core tools exist
	required := []string{
		"start_process",
		"read_process_output",
		"interact_with_process",
		"kill_process",
		"force_terminate",
		"list_sessions",
		"read_file",
		"write_file",
		"edit_block",
		"start_search",
		"get_config",
		"set_config_value",
		"get_usage_stats",
	}

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	for _, reqName := range required {
		if !toolMap[reqName] {
			t.Errorf("missing required tool: %s", reqName)
		}
	}

	// Test get_config dispatch
	cfgResult := registry.Dispatch("get_config", json.RawMessage("{}"))
	if cfgResult.IsError {
		t.Fatalf("get_config returned error: %v", cfgResult.Content)
	}
	if len(cfgResult.Content) == 0 || !strings.Contains(cfgResult.Content[0].Text, "commandTimeoutMs") {
		t.Errorf("unexpected get_config output: %v", cfgResult.Content)
	}

	// Test set_config_value dispatch
	setArgs := json.RawMessage(`{"key": "commandTimeoutMs", "value": 45000}`)
	setResult := registry.Dispatch("set_config_value", setArgs)
	if setResult.IsError {
		t.Fatalf("set_config_value returned error: %v", setResult.Content)
	}

	// Re-verify get_config has updated value
	cfgResult2 := registry.Dispatch("get_config", json.RawMessage("{}"))
	if !strings.Contains(cfgResult2.Content[0].Text, "45000") {
		t.Errorf("expected updated timeout 45000, got: %s", cfgResult2.Content[0].Text)
	}

	// Test usage stats
	statsResult := registry.Dispatch("get_usage_stats", json.RawMessage("{}"))
	if statsResult.IsError || !strings.Contains(statsResult.Content[0].Text, "toolCalls") {
		t.Errorf("unexpected get_usage_stats result: %v", statsResult)
	}
}

func TestProcessToolsExecution(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_gateway_test_proc")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgMgr := config.NewConfigManager(filepath.Join(tempDir, "config.json"))
	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/test_sock.sock")
	registry := NewRegistry(sup, fsClient, cfgMgr, nil)

	// Execute echo via start_process
	args := json.RawMessage(`{"command": "echo test_gateway_process", "timeout_ms": 2000}`)
	res := registry.Dispatch("start_process", args)
	if res.IsError {
		t.Fatalf("start_process returned error: %v", res.Content)
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "pid") {
		t.Fatalf("expected PID in output: %v", res.Content)
	}

	// List sessions via tool
	listRes := registry.Dispatch("list_sessions", json.RawMessage("{}"))
	if listRes.IsError || !strings.Contains(listRes.Content[0].Text, "echo test_gateway_process") {
		t.Errorf("list_sessions failed to report session: %v", listRes)
	}
}

func TestDocumentToolsExecution(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_doc_tools_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgMgr := config.NewConfigManager(filepath.Join(tempDir, "config.json"))
	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/test_sock.sock")
	registry := NewRegistry(sup, fsClient, cfgMgr, nil)

	// Build dc-doc-service binary path for test
	binDir, _ := filepath.Abs("../../../bin")
	docServiceBin := filepath.Join(binDir, "dc-doc-service")
	if _, err := os.Stat(docServiceBin); err != nil {
		_ = os.MkdirAll(binDir, 0755)
		srcDir, _ := filepath.Abs("../../dc-doc-service")
		cmd := exec.Command("go", "build", "-o", docServiceBin, "main.go")
		cmd.Dir = srcDir
		_ = cmd.Run()
	}
	_ = os.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// 1. Test docx tools
	docxPath := filepath.Join(tempDir, "test.docx")
	// Create minimal zip for docx
	f, err := os.Create(docxPath)
	if err != nil {
		t.Fatalf("failed to create docx: %v", err)
	}
	zw := zipWriter(f, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello World Document</w:t></w:r></w:p></w:body></w:document>`,
	})
	_ = zw.Close()
	_ = f.Close()

	readArgs, _ := json.Marshal(map[string]string{"path": docxPath})
	readRes := registry.Dispatch("docx_read", readArgs)
	if readRes.IsError {
		t.Fatalf("docx_read failed: %v", readRes.Content)
	}
	if !strings.Contains(readRes.Content[0].Text, "Hello World Document") {
		t.Errorf("docx_read output missing expected text: %s", readRes.Content[0].Text)
	}

	// Edit docx
	editArgs, _ := json.Marshal(map[string]string{
		"path":    docxPath,
		"find":    "Hello World",
		"replace": "Greetings A2M",
	})
	editRes := registry.Dispatch("docx_edit", editArgs)
	if editRes.IsError {
		t.Fatalf("docx_edit failed: %v", editRes.Content)
	}

	// Verify edit
	readRes2 := registry.Dispatch("docx_read", readArgs)
	if !strings.Contains(readRes2.Content[0].Text, "Greetings A2M") {
		t.Errorf("expected updated docx text, got: %s", readRes2.Content[0].Text)
	}
}

func zipWriter(f *os.File, files map[string]string) *zip.Writer {
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	return zw
}

func TestRegistryAuthEnforcement(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_auth_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgMgr := config.NewConfigManager(filepath.Join(tempDir, "config.json"))
	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/test_sock.sock")
	authMgr := auth.NewAuthenticator("secret_token_123", auth.RoleAdmin, true)

	registry := NewRegistry(sup, fsClient, cfgMgr, authMgr)

	// 1. Unauthenticated dispatch should fail on protected tool
	blockedRes := registry.Dispatch("start_process", json.RawMessage(`{"command":"echo test"}`))
	if !blockedRes.IsError {
		t.Fatalf("expected start_process to be rejected before authentication")
	}
	if !strings.Contains(blockedRes.Content[0].Text, "authentication required") {
		t.Errorf("unexpected error message: %s", blockedRes.Content[0].Text)
	}

	// 2. auth_status should succeed unauthenticated
	statusRes := registry.Dispatch("auth_status", json.RawMessage(`{}`))
	if statusRes.IsError || !strings.Contains(statusRes.Content[0].Text, `"authenticated": false`) {
		t.Fatalf("auth_status failed: %v", statusRes)
	}

	// 3. auth_login with wrong token fails
	wrongRes := registry.Dispatch("auth_login", json.RawMessage(`{"token":"wrong"}`))
	if !wrongRes.IsError {
		t.Fatalf("expected wrong token to be rejected")
	}

	// 4. auth_login with correct token succeeds
	loginRes := registry.Dispatch("auth_login", json.RawMessage(`{"token":"secret_token_123"}`))
	if loginRes.IsError || !strings.Contains(loginRes.Content[0].Text, "Authentication successful") {
		t.Fatalf("login failed: %v", loginRes)
	}

	// 5. Protected tool now succeeds
	allowedRes := registry.Dispatch("start_process", json.RawMessage(`{"command":"echo auth_success", "timeout_ms": 2000}`))
	if allowedRes.IsError {
		t.Fatalf("start_process failed after authentication: %v", allowedRes)
	}
}

func TestAllowedDirectoriesEnforcement(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_boundary_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	allowedFolder := filepath.Join(tempDir, "allowed")
	disallowedFolder := filepath.Join(tempDir, "forbidden")
	_ = os.MkdirAll(allowedFolder, 0755)
	_ = os.MkdirAll(disallowedFolder, 0755)

	cfgMgr := config.NewConfigManager(filepath.Join(tempDir, "config.json"))
	_ = cfgMgr.SetValue("allowedDirectories", []string{allowedFolder})

	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/test_sock.sock")
	defer fsClient.Close()
	authMgr := auth.NewAuthenticator("", auth.RoleAdmin, false)

	registry := NewRegistry(sup, fsClient, cfgMgr, authMgr)

	// Attempt to access path outside allowed directory
	forbiddenFile := filepath.Join(disallowedFolder, "secret.txt")
	res := registry.Dispatch("read_file", json.RawMessage(`{"path":"`+forbiddenFile+`"}`))
	if !res.IsError {
		t.Fatalf("expected filesystem boundary violation for forbidden path")
	}
	if !strings.Contains(res.Content[0].Text, "filesystem boundary violation") {
		t.Errorf("expected boundary violation message, got: %s", res.Content[0].Text)
	}

	// Verify edit_block with file_path is strictly checked
	editRes := registry.Dispatch("edit_block", json.RawMessage(`{"file_path":"`+forbiddenFile+`","old_string":"a","new_string":"b"}`))
	if !editRes.IsError {
		t.Fatalf("expected filesystem boundary violation for edit_block with forbidden file_path")
	}
	if !strings.Contains(editRes.Content[0].Text, "filesystem boundary violation") {
		t.Errorf("expected boundary violation message for edit_block, got: %s", editRes.Content[0].Text)
	}

	// Verify symlink traversal escape is blocked
	symlinkPath := filepath.Join(allowedFolder, "escape_link.txt")
	_ = os.Symlink(forbiddenFile, symlinkPath)
	defer os.Remove(symlinkPath)

	symRes := registry.Dispatch("read_file", json.RawMessage(`{"path":"`+symlinkPath+`"}`))
	if !symRes.IsError {
		t.Fatalf("expected filesystem boundary violation for symlink escaping to forbidden path")
	}
}

func TestCodebaseMemoryToolsRegistrationAndCorrelate(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_memory_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgMgr := config.NewConfigManager(filepath.Join(tempDir, "config.json"))
	sup := process.NewSupervisor()
	fsClient := ipc.NewFsEngineClient("/tmp/nonexistent_sock.sock")
	defer fsClient.Close()
	authMgr := auth.NewAuthenticator("", auth.RoleAdmin, false)

	registry := NewRegistry(sup, fsClient, cfgMgr, authMgr)
	tools := registry.ListTools()

	expectedMemoryTools := []string{
		"index_codebase",
		"get_architecture",
		"query_symbols",
		"trace_call_path",
		"assess_impact",
		"manage_adr",
		"correlate_error",
	}

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	for _, name := range expectedMemoryTools {
		if !toolMap[name] {
			t.Errorf("missing expected memory tool: %s", name)
		}
	}

	// Test correlate_error
	trace := `panic: runtime error: index out of range [3] with length 2
goroutine 1 [running]:
main.ProcessOrder(0x1400012000)
	/home/example/app/main.go:42 +0x64
main.main()
	/home/example/app/server.go:118 +0x28`

	res := registry.Dispatch("correlate_error", json.RawMessage(`{"error_text":`+strconvQuote(trace)+`}`))
	if res.IsError {
		t.Fatalf("correlate_error failed: %v", res)
	}
	if !strings.Contains(res.Content[0].Text, "main.go") {
		t.Errorf("expected correlated trace to identify main.go, got: %s", res.Content[0].Text)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
