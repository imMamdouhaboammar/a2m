package mcp

// Native MCP proxy for a locally installed, explicitly workspace-bound
// Code Review Graph service. No shell, no credentialed cloud servers.
import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var graphReadTools = []string{
	"get_architecture_overview_tool", "get_minimal_context_tool",
	"get_impact_radius_tool", "query_graph_tool", "get_review_context_tool",
	"list_graph_stats_tool", "get_hub_nodes_tool", "get_bridge_nodes_tool",
	"list_flows_tool", "get_flow_tool", "traverse_graph_tool",
	"get_affected_flows_tool", "list_communities_tool", "get_community_tool",
	"find_large_functions_tool", "get_knowledge_gaps_tool",
	"get_surprising_connections_tool", "get_suggested_questions_tool",
	"detect_changes_tool", "get_docs_section_tool", "get_wiki_page_tool",
}

type graphReply struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *ErrorObject    `json:"error"`
}
type graphProxy struct {
	command   *exec.Cmd
	input     io.WriteCloser
	reply     chan graphReply
	root      string
	mu        sync.Mutex
	nextID    int64
	tempIndex string
	cleanup   func()
	indexMu   sync.Mutex
	closeOnce sync.Once
}

func startGraphProcess(command *exec.Cmd, root string) (*graphProxy, error) {
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Never echo tool stderr, which may contain private repository content.
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		return nil, err
	}
	p := &graphProxy{command: command, input: stdin, reply: make(chan graphReply, 32), root: root}
	go func() {
		defer close(p.reply)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 8*1024*1024)
		for scanner.Scan() {
			var response graphReply
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID == 0 {
				continue
			}
			p.reply <- response
		}
	}()
	if _, err := p.callRaw("initialize", map[string]interface{}{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "a2m-capability-bridge", "version": "1.0"},
	}, 8*time.Second); err != nil {
		p.Close()
		return nil, err
	}
	_, _ = io.WriteString(p.input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	return p, nil
}

func (p *graphProxy) callRaw(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	req := map[string]interface{}{"jsonrpc": "2.0", "id": p.nextID, "method": method, "params": params}
	frame, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	frame = append(frame, '\n')
	if _, err = p.input.Write(frame); err != nil {
		return nil, fmt.Errorf("local MCP transport unavailable: %w", err)
	}
	select {
	case response, ok := <-p.reply:
		if !ok {
			return nil, errors.New("local MCP process exited")
		}
		if response.ID != p.nextID {
			return nil, errors.New("local MCP response id mismatch")
		}
		if response.Error != nil {
			return nil, fmt.Errorf("local MCP error: %s", response.Error.Message)
		}
		return response.Result, nil
	case <-time.After(timeout):
		// A stalled MCP is never allowed to block the gateway indefinitely.
		p.Close()
		return nil, errors.New("local MCP operation timed out; adapter stopped")
	}
}

func (p *graphProxy) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
		_ = p.input.Close()
		_ = p.command.Wait()
		if p.cleanup != nil {
			p.cleanup()
		}
	})
}

func (p *graphProxy) listTools() ([]ToolDefinition, error) {
	result, err := p.callRaw("tools/list", map[string]interface{}{}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(result, &listing); err != nil {
		return nil, err
	}
	return listing.Tools, nil
}

func (p *graphProxy) callTool(name string, raw json.RawMessage, timeout time.Duration) (ToolCallResult, error) {
	p.indexMu.Lock()
	defer p.indexMu.Unlock()
	if name == "build_or_update_graph_tool" && p.tempIndex != "" {
		if err := refreshGraphTemporaryIndex(p.root, p.tempIndex); err != nil {
			return ErrorResult("unable to refresh private graph index: " + err.Error()), nil
		}
	}
	var args map[string]json.RawMessage
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return ErrorResult("tool arguments must be an object"), nil
		}
	} else {
		args = make(map[string]json.RawMessage)
	}
	// The backend accepts repo_root overrides. Force it to the user's
	// explicitly configured workspace so a client cannot pivot to another repo.
	if incoming, ok := args["repo_root"]; ok && string(incoming) != "null" {
		var value string
		if json.Unmarshal(incoming, &value) != nil {
			return ErrorResult("invalid repo_root"), nil
		}
		if value != "" {
			canonical, err := filepath.EvalSymlinks(value)
			if err != nil || canonical != p.root {
				return ErrorResult("repo_root is outside the approved A2M workspace"), nil
			}
		}
	}
	repoJSON, _ := json.Marshal(p.root)
	args["repo_root"] = repoJSON
	// Guard optional changed_files even though the backend runs read-only.
	if changed, ok := args["changed_files"]; ok && string(changed) != "null" {
		var paths []string
		if json.Unmarshal(changed, &paths) != nil {
			return ErrorResult("invalid changed_files"), nil
		}
		for _, path := range paths {
			if path == "" || filepath.IsAbs(path) {
				return ErrorResult("changed_files must be workspace-relative"), nil
			}
			clean := filepath.Clean(path)
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return ErrorResult("changed_files path escapes workspace"), nil
			}
			if !graphPathWithinRoot(p.root, clean) {
				return ErrorResult("changed_files symlink escapes workspace"), nil
			}
		}
	}
	result, err := p.callRaw("tools/call", map[string]interface{}{"name": name, "arguments": args}, timeout)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	var output ToolCallResult
	if err := json.Unmarshal(result, &output); err != nil {
		return ErrorResult("invalid MCP tool response"), nil
	}
	return output, nil
}

// Check existing ancestors as well as the final path. A missing target or
// dangling symlink must not bypass containment through EvalSymlinks failure.
func graphPathWithinRoot(root, relative string) bool {
	path := filepath.Join(root, relative)
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			rel, err := filepath.Rel(root, resolved)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if !os.IsNotExist(err) {
			return false
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

func safeGraphEnv() []string {
	var result []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") ||
			strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "API_KEY") ||
			strings.Contains(upper, "COOKIE") || strings.Contains(upper, "PRIVATE_KEY") || upper == "XERJ_AUTH" {
			continue
		}
		result = append(result, kv)
	}
	return result
}

func (r *Registry) Close() {
	if r != nil {
		r.graphMu.Lock()
		for _, proxy := range r.graphProcesses {
			proxy.Close()
		}
		if r.graphProcess != nil {
			r.graphProcess.Close()
		}
		r.graphMu.Unlock()
		for _, provider := range r.externalProcesses {
			provider.Close()
		}
		r.closeXerj()
	}
}

func (r *Registry) codeGraphToolCount() int {
	n := 0
	for _, def := range r.tools {
		if strings.HasPrefix(def.Name, "crg_") {
			n++
		}
	}
	return n
}

func (r *Registry) capabilityWorkspace() string {
	if workspace := configuredWorkspace(); workspace != "" {
		return workspace
	}
	if r.configMgr == nil {
		return ""
	}
	requested := r.configMgr.GetConfig().CapabilityWorkspace
	if requested == "" {
		return ""
	}
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return ""
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return ""
	}
	return canonical
}

// graphProxyForArgs enforces exact operator-approved repository roots.
// Each repository has its own process and generated index; we never retarget
// an existing process to a different repository.
func (r *Registry) graphProxyForArgs(raw json.RawMessage) (*graphProxy, error) {
	primary := r.capabilityWorkspace()
	if primary == "" || r.graphProcess == nil {
		return nil, errors.New("Code Review Graph workspace is not configured")
	}
	selected := primary
	if len(raw) > 0 && string(raw) != "null" {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return nil, errors.New("graph arguments must be an object")
		}
		if value, exists := args["repo_root"]; exists && string(value) != "null" {
			var requested string
			if json.Unmarshal(value, &requested) != nil {
				return nil, errors.New("invalid repo_root")
			}
			if requested != "" {
				canonical, err := filepath.EvalSymlinks(requested)
				if err != nil {
					return nil, errors.New("repo_root is outside the approved A2M workspaces")
				}
				selected = canonical
			}
		}
	}
	if selected != primary {
		approved := false
		if r.configMgr != nil {
			for _, root := range r.configMgr.GetConfig().CapabilityWorkspaces {
				canonical, err := filepath.EvalSymlinks(root)
				if err == nil && canonical == selected {
					approved = true
					break
				}
			}
		}
		if !approved {
			return nil, errors.New("repo_root is outside the approved A2M workspaces")
		}
	}
	r.graphMu.Lock()
	defer r.graphMu.Unlock()
	if selected == primary {
		return r.graphProcess, nil
	}
	if existing := r.graphProcesses[selected]; existing != nil {
		return existing, nil
	}
	if r.graphExecutable == "" {
		return nil, errors.New("Code Review Graph executable unavailable")
	}
	proxy, err := startCodeGraphProxy(selected, r.graphExecutable)
	if err != nil {
		return nil, fmt.Errorf("Code Review Graph launch failed: %w", err)
	}
	if r.graphProcesses == nil {
		r.graphProcesses = make(map[string]*graphProxy)
	}
	r.graphProcesses[selected] = proxy
	return proxy, nil
}

func startCodeGraphProxy(workspace, executable string) (*graphProxy, error) {
	allow := append(append([]string{}, graphReadTools...), "build_or_update_graph_tool")
	cmd := exec.Command(executable, "serve", "--repo", workspace, "--tools", strings.Join(allow, ","))
	cmd.Dir = workspace
	cmd.Env = safeGraphEnv()
	privateIndex, cleanup, indexErr := createGraphTemporaryIndex(workspace)
	if indexErr != nil {
		return nil, fmt.Errorf("private graph index unavailable: %w", indexErr)
	}
	cmd.Env = append(withoutGitIndexEnv(cmd.Env), "GIT_INDEX_FILE="+privateIndex)
	proxy, err := startGraphProcess(cmd, workspace)
	if err != nil {
		cleanup()
		return nil, err
	}
	proxy.tempIndex, proxy.cleanup = privateIndex, cleanup
	return proxy, nil
}

func (r *Registry) suppressLegacyGraph() {
	legacy := map[string]bool{"index_codebase": true, "get_architecture": true, "query_symbols": true, "trace_call_path": true, "assess_impact": true, "correlate_error": true}
	kept := make([]ToolDefinition, 0, len(r.tools))
	for _, definition := range r.tools {
		if legacy[definition.Name] {
			delete(r.handlers, definition.Name)
			continue
		}
		kept = append(kept, definition)
	}
	r.tools = kept
}

func (r *Registry) registerCodeReviewGraph() {
	workspace := r.capabilityWorkspace()
	if workspace == "" {
		return
	}
	executable, err := exec.LookPath("code-review-graph")
	if err != nil {
		for _, candidate := range []string{"/opt/homebrew/bin/code-review-graph", "/usr/local/bin/code-review-graph"} {
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				executable = candidate
				err = nil
				break
			}
		}
	}
	if err != nil {
		return
	}
	proxy, err := startCodeGraphProxy(workspace, executable)
	if err != nil {
		return
	}
	r.graphExecutable = executable
	r.graphProcesses = map[string]*graphProxy{workspace: proxy}
	discovered, err := proxy.listTools()
	if err != nil {
		proxy.Close()
		return
	}
	r.graphProcess = proxy
	readAllowed := make(map[string]bool)
	for _, name := range graphReadTools {
		readAllowed[name] = true
	}
	foundRefresh := false
	for _, definition := range discovered {
		original := definition.Name
		if original == "build_or_update_graph_tool" {
			foundRefresh = true
			continue
		}
		if !readAllowed[original] {
			continue
		}
		definition.Name = "crg_" + original
		definition.Description = "Local, explicitly approved repository Code Review Graph: " + definition.Description
		toolName := original
		r.register(definition, func(args json.RawMessage) (ToolCallResult, error) {
			selected, err := r.graphProxyForArgs(args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return selected.callTool(toolName, args, 30*time.Second)
		})
	}
	if foundRefresh {
		r.register(ToolDefinition{
			Name:        "code_graph_refresh",
			Description: "Index the exact approved repository (default A2M workspace). Mutates only that repository's generated graph index, never source files.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"full_rebuild": map[string]interface{}{"type": "boolean", "default": false},
					"repo_root":    map[string]interface{}{"type": "string", "description": "Exact operator-approved canonical repository root; omit for default workspace"},
				},
				"additionalProperties": false,
			},
		}, func(args json.RawMessage) (ToolCallResult, error) {
			var input struct {
				FullRebuild bool   `json:"full_rebuild"`
				RepoRoot    string `json:"repo_root"`
			}
			if len(args) > 0 && json.Unmarshal(args, &input) != nil {
				return ErrorResult("invalid refresh arguments"), nil
			}
			selected, err := r.graphProxyForArgs(args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			payload, _ := json.Marshal(map[string]interface{}{"full_rebuild": input.FullRebuild})
			return selected.callTool("build_or_update_graph_tool", payload, 120*time.Second)
		})
	}
	if r.codeGraphToolCount() > 0 {
		r.suppressLegacyGraph()
	}
}
