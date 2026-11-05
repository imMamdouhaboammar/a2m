package mcp

// The external adapter consumes an operator-owned, per-tool approval manifest.
// MCP inventory alone never grants permission to start a process or call a tool.
import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type externalMCPApproval struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Tools  []string `json:"tools"`
}

type externalMCPLaunch struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

var externalToolName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)

// Trust approvals are deliberately not part of MCP set_config_value.
// The operator edits this file locally, then explicitly restarts the worker.
func externalApprovalFile() string {
	if path, set := os.LookupEnv("A2M_MCP_APPROVALS_FILE"); set {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".a2m", "mcp-approvals.json")
}

func loadExternalApprovals() []externalMCPApproval {
	path := externalApprovalFile()
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 64*1024 {
		return nil
	}
	var manifest struct {
		Servers []externalMCPApproval `json:"servers"`
	}
	bytes, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || json.Unmarshal(bytes, &manifest) != nil || len(manifest.Servers) > 64 {
		return nil
	}
	return manifest.Servers
}

func externalDiscoveryPaths() []string {
	if path, present := os.LookupEnv("A2M_MCP_DISCOVERY_CONFIG"); present {
		if path == "" {
			return nil
		}
		return []string{path}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".cursor", "mcp.json"),
		filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
	}
}

// A project-scoped server is selected only from the exact approved workspace;
// similarly named servers configured for other projects are not runnable.
func findExternalLaunch(name, source, workspace string) (externalMCPLaunch, bool) {
	if name == "" || (source != "user" && source != "project") {
		return externalMCPLaunch{}, false
	}
	for _, path := range externalDiscoveryPaths() {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 8*1024*1024 {
			continue
		}
		var root struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
			Projects   map[string]struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			} `json:"projects"`
		}
		if json.Unmarshal(data, &root) != nil {
			continue
		}
		var selected json.RawMessage
		if source == "user" {
			selected = root.MCPServers[name]
		} else if workspace != "" {
			// Claude projects use absolute paths as keys.
			if project, ok := root.Projects[workspace]; ok {
				selected = project.MCPServers[name]
			}
		}
		if len(selected) == 0 {
			continue
		}
		var config externalMCPLaunch
		if json.Unmarshal(selected, &config) != nil || strings.TrimSpace(config.Command) == "" {
			return externalMCPLaunch{}, false
		}
		return config, true
	}
	return externalMCPLaunch{}, false
}

func safeExternalEnvironment(in map[string]string) []string {
	// Never inherit arbitrary gateway or desktop environment variables, even
	// names that do not look like secrets (for example SSH_AUTH_SOCK).
	env := make([]string, 0, len(in)+12)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG", "LC_ALL", "SHELL"} {
		if value, found := os.LookupEnv(key); found {
			env = append(env, key+"="+value)
		}
	}
	// A provider only receives its explicitly configured credential values,
	// never the gateway/tunnel's own control-plane credentials by inheritance.
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "" || strings.ContainsAny(key, "=\x00") ||
			strings.EqualFold(key, "CONTROL_PLANE_API_KEY") ||
			strings.EqualFold(key, "DC_AUTH_TOKEN") {
			continue
		}
		env = append(env, key+"="+in[key])
	}
	return env
}

func externalServerID(name, source string) string {
	return source + ":" + name
}

func externalPrefix(name, source string) string {
	var b strings.Builder
	b.WriteString("ext_")
	b.WriteString(source)
	b.WriteByte('_')
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("__")
	return b.String()
}

func (r *Registry) registerExternalMCP() {
	workspace := r.capabilityWorkspace()
	if workspace == "" {
		return
	}
	r.externalActive = make(map[string]int)
	seen := make(map[string]bool)
	for _, approval := range loadExternalApprovals() {
		id := externalServerID(approval.Name, approval.Source)
		if seen[id] || approval.Name == "" || len(approval.Tools) == 0 || len(approval.Tools) > 128 {
			continue
		}
		seen[id] = true
		selected := make(map[string]bool)
		for _, name := range approval.Tools {
			if externalToolName.MatchString(name) {
				selected[name] = true
			}
		}
		if len(selected) == 0 {
			continue
		}
		launch, found := findExternalLaunch(approval.Name, approval.Source, workspace)
		if !found {
			continue
		}
		executable, err := exec.LookPath(launch.Command)
		if err != nil {
			continue
		}
		cmd := exec.Command(executable, launch.Args...)
		cmd.Dir = workspace
		cmd.Env = safeExternalEnvironment(launch.Env)
		proxy, err := startGraphProcess(cmd, workspace)
		if err != nil {
			continue
		}
		upstream, err := proxy.listTools()
		if err != nil {
			proxy.Close()
			continue
		}
		prefix := externalPrefix(approval.Name, approval.Source)
		count := 0
		for _, definition := range upstream {
			original := definition.Name
			mapped := prefix + original
			if !selected[original] || !externalToolName.MatchString(original) ||
				len(mapped) > 128 || r.handlers[mapped] != nil {
				continue
			}
			definition.Name = mapped
			if len(definition.Description) > 800 {
				definition.Description = definition.Description[:800]
			}
			definition.Description = fmt.Sprintf("Operator-approved MCP (%s, %s): %s",
				approval.Name, approval.Source, definition.Description)
			upstreamName := original
			r.register(definition, func(args json.RawMessage) (ToolCallResult, error) {
				return proxy.callExternalTool(upstreamName, args)
			})
			count++
		}
		if count == 0 {
			proxy.Close()
			continue
		}
		r.externalProcesses = append(r.externalProcesses, proxy)
		r.externalActive[id] = count
	}
}

func (p *graphProxy) callExternalTool(name string, raw json.RawMessage) (ToolCallResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return ErrorResult("invalid external MCP tool arguments"), nil
	}
	payload, err := p.callRaw("tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	}, 30*time.Second)
	if err != nil {
		return ErrorResult("approved local MCP provider unavailable or timed out"), nil
	}
	var result ToolCallResult
	if json.Unmarshal(payload, &result) != nil {
		return ErrorResult("invalid approved MCP provider response"), nil
	}
	return result, nil
}

func (r *Registry) externalToolNames() []string {
	names := make([]string, 0)
	for _, def := range r.tools {
		if strings.HasPrefix(def.Name, "ext_") {
			names = append(names, def.Name)
		}
	}
	return names
}

func (r *Registry) externalServerStatus() []discoveredServer {
	servers := listDiscoveredMCPServers(r.capabilityWorkspace())
	for i := range servers {
		id := externalServerID(servers[i].Name, servers[i].Source)
		if r.externalActive[id] > 0 {
			servers[i].State = fmt.Sprintf("active:%d", r.externalActive[id])
		}
	}
	return servers
}
