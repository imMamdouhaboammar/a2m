package mcp

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGraphMCPHelper(t *testing.T) {
	if os.Getenv("A2M_FAKE_GRAPH_HELPER") != "yes" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil || request.ID == 0 {
			continue
		}
		result := interface{}(map[string]interface{}{})
		switch request.Method {
		case "tools/list":
			result = map[string]interface{}{"tools": []ToolDefinition{{
				Name: "query_graph_tool", Description: "Read a code graph", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"repo_root": map[string]string{"type": "string"}}},
			}}}
		case "tools/call":
			var args struct {
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &args)
			bytes, _ := json.Marshal(args.Arguments)
			result = TextResult(string(bytes))
		}
		bytes, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": result})
		os.Stdout.Write(append(bytes, '\n'))
	}
	os.Exit(0)
}

func TestNativeGraphAdapterIsWorkspaceBound(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestGraphMCPHelper")
	cmd.Env = append(os.Environ(), "A2M_FAKE_GRAPH_HELPER=yes")
	p, err := startGraphProcess(cmd, root)
	if err != nil {
		t.Fatalf("MCP handshake failed: %v", err)
	}
	defer p.Close()
	tools, err := p.listTools()
	if err != nil || len(tools) != 1 || tools[0].Name != "query_graph_tool" {
		t.Fatalf("native tools/list failed: %v %+v", err, tools)
	}
	valid, err := p.callTool("query_graph_tool", json.RawMessage(`{"target":"foo"}`), 3*time.Second)
	if err != nil || valid.IsError {
		t.Fatalf("valid MCP tools/call failed: %v %+v", err, valid)
	}
	if !strings.Contains(valid.Content[0].Text, root) {
		t.Fatalf("MCP worker did not receive forced workspace root: %+v", valid)
	}
	outside := filepath.Join(filepath.Dir(root), "unapproved")
	bytes, _ := json.Marshal(map[string]interface{}{"repo_root": outside})
	blocked, _ := p.callTool("query_graph_tool", bytes, 3*time.Second)
	if !blocked.IsError {
		t.Fatal("MCP attempted access to an unapproved repository")
	}
	traversal, _ := p.callTool("query_graph_tool", json.RawMessage(`{"changed_files":["../../secrets"]}`), 3*time.Second)
	if !traversal.IsError {
		t.Fatal("changed_files traversed above approved workspace")
	}
}
