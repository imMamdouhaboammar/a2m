package mcp

import (
	"bufio"
	"encoding/json"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestExplicitApprovedGraphRepositoriesAreRoutedIntoIsolatedProcesses(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	outside := t.TempDir()
	for _, root := range []string{first, second} {
		if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("initialize graph fixture: %v %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package test\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", root, "add", "main.go").CombinedOutput(); err != nil {
			t.Fatalf("prepare private index fixture: %v %s", err, out)
		}
	}
	canonicalFirst, _ := filepath.EvalSymlinks(first)
	canonicalSecond, _ := filepath.EvalSymlinks(second)
	t.Setenv("A2M_WORKSPACE_ROOT", canonicalFirst)
	t.Setenv("A2M_FAKE_GRAPH_HELPER", "yes")
	cfg := config.NewConfigManager(filepath.Join(t.TempDir(), "graph-config.json"))
	if err := cfg.SetValue("capabilityWorkspaces", []string{second}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "mock-graph.sh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+strconv.Quote(executable)+" -test.run=TestGraphMCPHelper\n"), 0700); err != nil {
		t.Fatal(err)
	}
	firstProxy, err := startCodeGraphProxy(canonicalFirst, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{
		configMgr: cfg, graphProcess: firstProxy, graphExecutable: wrapper,
		graphProcesses: map[string]*graphProxy{canonicalFirst: firstProxy},
	}
	defer registry.Close()

	args := json.RawMessage(`{"repo_root":` + strconv.Quote(canonicalSecond) + `,"target":"a"}`)
	secondProxy, err := registry.graphProxyForArgs(args)
	if err != nil || secondProxy == firstProxy {
		t.Fatalf("approved second repository did not get isolated graph proxy: %v", err)
	}
	reused, err := registry.graphProxyForArgs(args)
	if err != nil || reused != secondProxy {
		t.Fatal("graph process should be cached for the exact approved repository")
	}
	output, err := secondProxy.callTool("query_graph_tool", args, 3*time.Second)
	if err != nil || output.IsError || !strings.Contains(output.Content[0].Text, canonicalSecond) {
		t.Fatalf("secondary graph did not receive its own enforced root: %+v %v", output, err)
	}
	defaultProxy, err := registry.graphProxyForArgs(json.RawMessage(`{"target":"a"}`))
	if err != nil || defaultProxy != firstProxy {
		t.Fatal("omitted repo_root must retain default workspace compatibility")
	}
	outsideArgs := json.RawMessage(`{"repo_root":` + strconv.Quote(outside) + `}`)
	if _, err := registry.graphProxyForArgs(outsideArgs); err == nil {
		t.Fatal("an unrelated repository was accepted")
	}
	if _, err := registry.graphProxyForArgs(json.RawMessage(`{"repo_root":42}`)); err == nil {
		t.Fatal("non-string graph root accepted")
	}
	escaped := filepath.Join(second, "external.go")
	if err := os.Symlink(filepath.Join(outside, "private.go"), escaped); err != nil {
		t.Fatal(err)
	}
	malicious := json.RawMessage(`{"changed_files":["external.go"]}`)
	rejected, err := secondProxy.callTool("query_graph_tool", malicious, 3*time.Second)
	if err != nil || !rejected.IsError {
		t.Fatal("changed_files symlink escaping approved root was permitted")
	}
}

func TestRealGraphBackendSupportsApprovedSecondaryRepository(t *testing.T) {
	if os.Getenv("A2M_TEST_REAL_GRAPH") != "1" {
		t.Skip("explicit opt-in real Code Review Graph integration")
	}
	if _, err := exec.LookPath("code-review-graph"); err != nil {
		t.Skip("Code Review Graph executable is not installed")
	}
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		cmds := [][]string{
			{"init", "-q"},
		}
		for _, args := range cmds {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git init %s: %s: %v", root, out, err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("def answer():\n    return 42\n"), 0600); err != nil {
			t.Fatal(err)
		}
		commands := [][]string{
			{"add", "main.py"},
			{"-c", "user.name=A2M Test", "-c", "user.email=a2m-test@example.invalid", "commit", "-qm", "fixture"},
		}
		for _, args := range commands {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git setup %s: %s: %v", root, out, err)
			}
		}
	}
	first, _ := filepath.EvalSymlinks(roots[0])
	second, _ := filepath.EvalSymlinks(roots[1])
	t.Setenv("A2M_WORKSPACE_ROOT", first)
	t.Setenv("A2M_MCP_APPROVALS_PATH", filepath.Join(t.TempDir(), "no-approvals.json"))
	cfg := config.NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	if err := cfg.SetValue("capabilityWorkspaces", []string{second}); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(nil, nil, cfg, nil)
	defer registry.Close()
	rootArg := json.RawMessage(`{"repo_root":` + strconv.Quote(second) + `,"full_rebuild":true}`)
	updated := registry.Dispatch("code_graph_refresh", rootArg)
	if updated.IsError {
		t.Fatalf("real secondary graph refresh rejected: %+v", updated)
	}
	contextArgs := json.RawMessage(`{"repo_root":` + strconv.Quote(second) + `,"task":"inspect local fixture"}`)
	context := registry.Dispatch("crg_get_minimal_context_tool", contextArgs)
	if context.IsError || len(context.Content) == 0 {
		t.Fatalf("real secondary graph read rejected: %+v", context)
	}
	denied := registry.Dispatch("crg_get_minimal_context_tool", json.RawMessage(`{"repo_root":"/etc","task":"escape"}`))
	if !denied.IsError {
		t.Fatal("real graph permitted unapproved path")
	}
}
