package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func findXerjExecutable() string {
	if custom := os.Getenv("XERJ_BIN"); custom != "" {
		if info, err := os.Stat(custom); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return custom
		}
	}
	if path, err := exec.LookPath("xerj"); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	var candidates []string
	if err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "xerj"))
	}
	candidates = append(candidates, "/usr/local/bin/xerj", "/opt/homebrew/bin/xerj")
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	return ""
}

func xerjServerURL() string {
	if u := os.Getenv("XERJ_URL"); u != "" {
		return u
	}
	return "http://localhost:9200"
}

// xerjCommandEnvironment passes XERJ-specific credentials only to the
// installed local XERJ subprocess, never the gateway's control-plane secrets.
func xerjCommandEnvironment(endpoint string) []string {
	env := make([]string, 0)
	for _, item := range safeGraphEnv() {
		if strings.HasPrefix(item, "XERJ_AUTH=") || strings.HasPrefix(item, "XERJ_URL=") {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "XERJ_URL="+endpoint)
	auth := strings.TrimSpace(os.Getenv("XERJ_AUTH"))
	apiKey := strings.TrimSpace(os.Getenv("XERJ_API_KEY"))
	if apiKey == "" && strings.HasPrefix(auth, "ApiKey ") {
		apiKey = strings.TrimSpace(strings.TrimPrefix(auth, "ApiKey "))
	}
	if apiKey != "" {
		env = append(env, "XERJ_API_KEY="+apiKey)
	}
	if auth == "" && apiKey != "" {
		auth = "ApiKey " + apiKey
	}
	if auth != "" {
		env = append(env, "XERJ_AUTH="+auth)
	}
	return env
}

func checkXerjDaemonHealthy(endpoint string) bool {
	client := http.Client{Timeout: 1500 * time.Millisecond}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	auth := strings.TrimSpace(os.Getenv("XERJ_AUTH"))
	if auth == "" {
		if key := strings.TrimSpace(os.Getenv("XERJ_API_KEY")); key != "" {
			auth = "ApiKey " + key
		}
	}
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	resp, err := client.Do(request)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// xerjIndexTarget constrains indexing to the approved workspace or an
// existing descendant directory, resolving symlinks before checking scope.
func xerjIndexTarget(workspace, requested string) (string, error) {
	approved, err := filepath.EvalSymlinks(workspace)
	if err != nil || approved == "" {
		return "", fmt.Errorf("approved XERJ workspace unavailable")
	}
	target := approved
	if strings.TrimSpace(requested) != "" {
		target = strings.TrimSpace(requested)
		if !filepath.IsAbs(target) {
			target = filepath.Join(approved, target)
		}
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("XERJ index path is not an existing directory")
	}
	rel, err := filepath.Rel(approved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("XERJ index path is outside approved workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("XERJ index path must be a directory")
	}
	return resolved, nil
}

func xerjCommandResult(output []byte, commandErr error) ToolCallResult {
	if commandErr != nil {
		// An executable can print useful output and still exit non-zero.
		// Never report a failed CLI command as a successful tool call.
		return ErrorResult(fmt.Sprintf("XERJ CLI command failed (%v); check node health, credentials, and logs", commandErr))
	}
	return TextResult(string(output))
}

func (r *Registry) registerXerj() {
	executable := findXerjExecutable()
	if executable == "" {
		return
	}
	r.xerjExecutable = executable

	workspace := r.capabilityWorkspace()
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}

	url := xerjServerURL()
	cmd := exec.Command(executable, "mcp")
	cmd.Dir = workspace
	cmd.Env = xerjCommandEnvironment(url)

	proxy, err := startGraphProcess(cmd, workspace)
	if err != nil {
		return
	}

	upstream, err := proxy.listTools()
	if err != nil {
		proxy.Close()
		return
	}

	r.xerjMu.Lock()
	r.xerjProcess = proxy
	r.xerjMu.Unlock()

	for _, definition := range upstream {
		toolName := definition.Name
		upstreamName := toolName

		if toolName == "xerj_search" {
			r.register(definition, func(args json.RawMessage) (ToolCallResult, error) {
				enhancedArgs := args
				if len(args) > 0 && string(args) != "null" {
					var rawMap map[string]interface{}
					if err := json.Unmarshal(args, &rawMap); err == nil && rawMap != nil {
						idx, hasIdx := rawMap["index"]
						if !hasIdx || idx == nil || idx == "" {
							rawMap["index"] = "ax-docs,ax-*"
							if marshaled, err := json.Marshal(rawMap); err == nil {
								enhancedArgs = marshaled
							}
						}
					}
				}
				return proxy.callExternalTool(upstreamName, enhancedArgs)
			})
			continue
		}

		r.register(definition, func(args json.RawMessage) (ToolCallResult, error) {
			return proxy.callExternalTool(upstreamName, args)
		})
	}

	// High-productivity companion tools for AI agents
	r.register(ToolDefinition{
		Name:        "xerj_def",
		Description: "Go-to-definition: locate symbol definition, file:line, and signature using XERJ neural & AST index without full repository grepping.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"symbol": map[string]string{
					"type":        "string",
					"description": "Symbol name (function, class, struct, method, type) to find definition for",
				},
			},
			"required":             []string{"symbol"},
			"additionalProperties": false,
		},
	}, func(args json.RawMessage) (ToolCallResult, error) {
		var input struct {
			Symbol string `json:"symbol"`
		}
		if err := json.Unmarshal(args, &input); err != nil || strings.TrimSpace(input.Symbol) == "" {
			return ErrorResult("symbol parameter is required"), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		defCmd := exec.CommandContext(ctx, executable, "def", strings.TrimSpace(input.Symbol))
		defCmd.Dir = workspace
		defCmd.Env = xerjCommandEnvironment(url)
		out, err := defCmd.CombinedOutput()
		return xerjCommandResult(out, err), nil
	})

	r.register(ToolDefinition{
		Name:        "xerj_autoindex",
		Description: "Index or re-index the repository with XERJ (discovers files, builds AST symbols, lexical/neural index, and knowledge graph).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]string{
					"type":        "string",
					"description": "Workspace path or folder to index (defaults to approved workspace)",
				},
			},
			"additionalProperties": false,
		},
	}, func(args json.RawMessage) (ToolCallResult, error) {
		var input struct {
			Path string `json:"path"`
		}
		if len(args) > 0 && json.Unmarshal(args, &input) != nil {
			return ErrorResult("invalid XERJ index path"), nil
		}
		target, err := xerjIndexTarget(workspace, input.Path)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()

		indexCmd := exec.CommandContext(ctx, executable, "autoindex", target)
		indexCmd.Dir = workspace
		indexCmd.Env = xerjCommandEnvironment(url)
		out, err := indexCmd.CombinedOutput()
		return xerjCommandResult(out, err), nil
	})
}

func (r *Registry) closeXerj() {
	r.xerjMu.Lock()
	defer r.xerjMu.Unlock()
	if r.xerjProcess != nil {
		r.xerjProcess.Close()
		r.xerjProcess = nil
	}
}

func (r *Registry) xerjToolCount() int {
	n := 0
	for _, def := range r.tools {
		if strings.HasPrefix(def.Name, "xerj_") {
			n++
		}
	}
	return n
}

func (r *Registry) isXerjAvailable() bool {
	return r.xerjExecutable != "" && r.xerjToolCount() > 0
}
