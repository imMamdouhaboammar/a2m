package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/ipc"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

type ToolHandler func(args json.RawMessage) (ToolCallResult, error)
type SessionToolHandler func(sessionID string, args json.RawMessage) (ToolCallResult, error)

type Registry struct {
	tools             []ToolDefinition
	handlers          map[string]ToolHandler
	sessionHandlers   map[string]SessionToolHandler
	supervisor        *process.Supervisor
	fsClient          *ipc.FsEngineClient
	configMgr         *config.ConfigManager
	auth              *auth.Authenticator
	startTime         time.Time
	callCounts        map[string]int
	recentCalls       []map[string]interface{}
	mu                sync.RWMutex
	graphProcess      *graphProxy
	graphProcesses    map[string]*graphProxy
	graphExecutable   string
	graphMu           sync.Mutex
	externalProcesses []*graphProxy
	externalActive    map[string]int
	externalApproved  map[string]bool
	xerjProcess       *graphProxy
	xerjExecutable    string
	xerjMu            sync.Mutex
}

func NewRegistry(
	sup *process.Supervisor,
	fsClient *ipc.FsEngineClient,
	cfgMgr *config.ConfigManager,
	authMgr *auth.Authenticator,
) *Registry {
	if authMgr == nil {
		authMgr = auth.NewAuthenticator("", auth.RoleAdmin, false)
	}
	r := &Registry{
		handlers:        make(map[string]ToolHandler),
		sessionHandlers: make(map[string]SessionToolHandler),
		supervisor:      sup,
		fsClient:        fsClient,
		configMgr:       cfgMgr,
		auth:            authMgr,
		startTime:       time.Now(),
		callCounts:      make(map[string]int),
		recentCalls:     make([]map[string]interface{}, 0, 100),
	}

	r.registerAll()
	r.registerCapabilities()
	r.registerCLIBridge()
	r.registerCodeReviewGraph()
	r.registerXerj()
	r.registerExternalMCP()
	return r
}

func (r *Registry) ListTools() []ToolDefinition {
	return r.tools
}

func (r *Registry) Dispatch(name string, args json.RawMessage) ToolCallResult {
	return r.DispatchWithSession("", name, args)
}

func (r *Registry) DispatchWithSession(sessionID string, name string, args json.RawMessage) ToolCallResult {
	if err := r.auth.AuthorizeSessionTool(sessionID, name); err != nil {
		return ErrorResult(err.Error())
	}

	if err := r.validatePathBoundary(name, args); err != nil {
		return ErrorResult(err.Error())
	}

	r.mu.Lock()
	r.callCounts[name]++
	callRecord := map[string]interface{}{
		"tool":      name,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	r.recentCalls = append(r.recentCalls, callRecord)
	if len(r.recentCalls) > 100 {
		r.recentCalls = r.recentCalls[1:]
	}
	r.mu.Unlock()

	if sessHandler, exists := r.sessionHandlers[name]; exists {
		res, err := sessHandler(sessionID, args)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return res
	}

	handler, exists := r.handlers[name]
	if !exists {
		return ErrorResult(fmt.Sprintf("Unknown tool: %s", name))
	}

	res, err := handler(args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return res
}

func (r *Registry) validatePathBoundary(toolName string, args json.RawMessage) error {
	if r.configMgr == nil {
		return nil
	}
	cfg := r.configMgr.GetConfig()
	if len(cfg.AllowedDirectories) == 0 {
		return nil
	}

	isAllowed := func(p string) bool {
		if strings.TrimSpace(p) == "" {
			return false
		}
		// Resolve symlinks to prevent directory traversal escapes
		resolvedPath := p
		if eval, err := filepath.EvalSymlinks(p); err == nil {
			resolvedPath = eval
		} else {
			// If target doesn't exist yet, resolve closest existing ancestor
			dir := filepath.Dir(p)
			for dir != "." && dir != "/" && dir != "" {
				if evalDir, err := filepath.EvalSymlinks(dir); err == nil {
					rel, _ := filepath.Rel(dir, p)
					resolvedPath = filepath.Join(evalDir, rel)
					break
				}
				dir = filepath.Dir(dir)
			}
		}

		cleanPath, err := filepath.Abs(resolvedPath)
		if err != nil {
			cleanPath = filepath.Clean(resolvedPath)
		}
		for _, allowed := range cfg.AllowedDirectories {
			evalAllowed := allowed
			if eval, err := filepath.EvalSymlinks(allowed); err == nil {
				evalAllowed = eval
			}
			cleanAllowed, err := filepath.Abs(evalAllowed)
			if err != nil {
				cleanAllowed = filepath.Clean(evalAllowed)
			}
			rel, err := filepath.Rel(cleanAllowed, cleanPath)
			if err == nil && !strings.HasPrefix(rel, "..") {
				return true
			}
		}
		return false
	}

	var pathPayload struct {
		Path        string   `json:"path"`
		FilePath    string   `json:"file_path"`
		FilePathAlt string   `json:"filePath"`
		Source      string   `json:"source"`
		Destination string   `json:"destination"`
		Paths       []string `json:"paths"`
	}
	if err := json.Unmarshal(args, &pathPayload); err != nil {
		return nil
	}

	targetFile := pathPayload.FilePath
	if targetFile == "" {
		targetFile = pathPayload.FilePathAlt
	}

	if targetFile != "" && !isAllowed(targetFile) {
		return fmt.Errorf("filesystem boundary violation: file_path '%s' is outside configured allowedDirectories", targetFile)
	}
	if pathPayload.Path != "" && !isAllowed(pathPayload.Path) {
		return fmt.Errorf("filesystem boundary violation: path '%s' is outside configured allowedDirectories", pathPayload.Path)
	}
	if pathPayload.Source != "" && !isAllowed(pathPayload.Source) {
		return fmt.Errorf("filesystem boundary violation: source path '%s' is outside configured allowedDirectories", pathPayload.Source)
	}
	if pathPayload.Destination != "" && !isAllowed(pathPayload.Destination) {
		return fmt.Errorf("filesystem boundary violation: destination path '%s' is outside configured allowedDirectories", pathPayload.Destination)
	}
	for _, p := range pathPayload.Paths {
		if !isAllowed(p) {
			return fmt.Errorf("filesystem boundary violation: path '%s' is outside configured allowedDirectories", p)
		}
	}

	switch toolName {
	case "read_file", "write_file", "list_directory", "create_directory", "get_file_info", "excel_read_sheet", "excel_list_sheets", "excel_edit_range", "docx_read", "docx_edit", "pdf_extract_text":
		if pathPayload.Path == "" {
			return fmt.Errorf("filesystem boundary violation: path parameter is required for tool '%s'", toolName)
		}
	case "edit_block":
		if targetFile == "" {
			return fmt.Errorf("filesystem boundary violation: file_path parameter is required for tool 'edit_block'")
		}
	case "move_file":
		if pathPayload.Source == "" || pathPayload.Destination == "" {
			return fmt.Errorf("filesystem boundary violation: source and destination are required for 'move_file'")
		}
	case "read_multiple_files":
		if len(pathPayload.Paths) == 0 {
			return fmt.Errorf("filesystem boundary violation: paths parameter is required for 'read_multiple_files'")
		}
	}

	return nil
}

func (r *Registry) register(def ToolDefinition, handler ToolHandler) {
	r.tools = append(r.tools, def)
	r.handlers[def.Name] = handler
}

func (r *Registry) registerWithSession(def ToolDefinition, handler SessionToolHandler) {
	r.tools = append(r.tools, def)
	r.sessionHandlers[def.Name] = handler
}

func (r *Registry) registerAll() {
	// 1. start_process
	r.register(
		ToolDefinition{
			Name:        "start_process",
			Description: "Execute a terminal command or spawn a long-running process.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Command to execute",
					},
					"timeout_ms": map[string]interface{}{
						"type":        "number",
						"description": "Execution timeout in milliseconds",
					},
					"shell": map[string]interface{}{
						"type":        "string",
						"description": "Optional custom shell binary",
					},
				},
				"required": []string{"command"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Command   string `json:"command"`
				TimeoutMs int    `json:"timeout_ms"`
				Shell     string `json:"shell"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			res, err := r.supervisor.StartProcess(params.Command, params.TimeoutMs, params.Shell)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			bytes, _ := json.Marshal(res)
			return TextResult(string(bytes)), nil
		},
	)

	// 2. read_process_output
	r.register(
		ToolDefinition{
			Name:        "read_process_output",
			Description: "Read output from a running or completed process session with pagination.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid": map[string]interface{}{
						"type":        "number",
						"description": "Process PID",
					},
					"offset": map[string]interface{}{
						"type":        "number",
						"description": "0=from last read watermark, >0=absolute line index, <0=tail lines",
					},
					"length": map[string]interface{}{
						"type":        "number",
						"description": "Max lines to return",
					},
				},
				"required": []string{"pid"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				PID    int `json:"pid"`
				Offset int `json:"offset"`
				Length int `json:"length"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			res, err := r.supervisor.ReadProcessOutput(params.PID, params.Offset, params.Length)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			bytes, _ := json.Marshal(res)
			return TextResult(string(bytes)), nil
		},
	)

	// 3. interact_with_process
	r.register(
		ToolDefinition{
			Name:        "interact_with_process",
			Description: "Send standard input to a running process session.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid": map[string]interface{}{
						"type": "number",
					},
					"input": map[string]interface{}{
						"type": "string",
					},
					"timeout_ms": map[string]interface{}{
						"type": "number",
					},
				},
				"required": []string{"pid", "input"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				PID       int    `json:"pid"`
				Input     string `json:"input"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			res, err := r.supervisor.InteractWithProcess(params.PID, params.Input, params.TimeoutMs)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			bytes, _ := json.Marshal(res)
			return TextResult(string(bytes)), nil
		},
	)

	// 4. kill_process
	r.register(
		ToolDefinition{
			Name:        "kill_process",
			Description: "Gracefully terminate a process session (SIGTERM).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid": map[string]interface{}{"type": "number"},
				},
				"required": []string{"pid"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				PID int `json:"pid"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			err := r.supervisor.TerminateProcess(params.PID, false)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(fmt.Sprintf("Process %d terminated gracefully", params.PID)), nil
		},
	)

	// 5. force_terminate
	r.register(
		ToolDefinition{
			Name:        "force_terminate",
			Description: "Immediately kill a process and its child tree (SIGKILL).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid": map[string]interface{}{"type": "number"},
				},
				"required": []string{"pid"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				PID int `json:"pid"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			err := r.supervisor.TerminateProcess(params.PID, true)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(fmt.Sprintf("Process tree for PID %d force-terminated", params.PID)), nil
		},
	)

	// 6. list_sessions
	r.register(
		ToolDefinition{
			Name:        "list_sessions",
			Description: "List all active and completed terminal process sessions.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			list := r.supervisor.ListSessions()
			bytes, _ := json.Marshal(map[string]interface{}{"sessions": list})
			return TextResult(string(bytes)), nil
		},
	)

	// 7. list_processes
	r.register(
		ToolDefinition{
			Name:        "list_processes",
			Description: "List running host operating system processes.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			list, err := r.supervisor.ListProcesses()
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			bytes, _ := json.Marshal(map[string]interface{}{"processes": list})
			return TextResult(string(bytes)), nil
		},
	)

	// 8. read_file
	r.register(
		ToolDefinition{
			Name:        "read_file",
			Description: "Read file content with line offset and count pagination.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string"},
					"offset": map[string]interface{}{"type": "number", "default": 0},
					"length": map[string]interface{}{"type": "number", "default": 1000},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.read_file", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 9. read_multiple_files
	r.register(
		ToolDefinition{
			Name:        "read_multiple_files",
			Description: "Read multiple files in a single call.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"paths": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"required": []string{"paths"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.read_multiple_files", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 10. write_file
	r.register(
		ToolDefinition{
			Name:        "write_file",
			Description: "Atomically write or append content to a file.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string"},
					"mode":    map[string]interface{}{"type": "string", "enum": []string{"rewrite", "append"}, "default": "rewrite"},
				},
				"required": []string{"path", "content"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.write_file", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 11. edit_block
	r.register(
		ToolDefinition{
			Name:        "edit_block",
			Description: "Surgically edit file content using exact string match with Levenshtein fuzzy match fallback.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path":             map[string]interface{}{"type": "string"},
					"old_string":            map[string]interface{}{"type": "string"},
					"new_string":            map[string]interface{}{"type": "string"},
					"expected_replacements": map[string]interface{}{"type": "number", "default": 1},
				},
				"required": []string{"file_path", "old_string", "new_string"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.edit_block", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 12. create_directory
	r.register(
		ToolDefinition{
			Name:        "create_directory",
			Description: "Create a directory recursively.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.create_directory", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 13. list_directory
	r.register(
		ToolDefinition{
			Name:        "list_directory",
			Description: "List entries in a directory up to a specified depth.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":  map[string]interface{}{"type": "string"},
					"depth": map[string]interface{}{"type": "number", "default": 2},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.list_directory", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 14. move_file
	r.register(
		ToolDefinition{
			Name:        "move_file",
			Description: "Move or rename a file or directory.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source":      map[string]interface{}{"type": "string"},
					"destination": map[string]interface{}{"type": "string"},
				},
				"required": []string{"source", "destination"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.move_file", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 15. get_file_info
	r.register(
		ToolDefinition{
			Name:        "get_file_info",
			Description: "Get detailed file metadata, size, permissions, and MIME type.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("fs.get_file_info", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 16. start_search
	r.register(
		ToolDefinition{
			Name:        "start_search",
			Description: "Perform fast ripgrep-grade regex search across files or file contents.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":              map[string]interface{}{"type": "string"},
					"pattern":           map[string]interface{}{"type": "string"},
					"search_type":       map[string]interface{}{"type": "string", "enum": []string{"files", "content"}, "default": "files"},
					"file_pattern":      map[string]interface{}{"type": "string"},
					"ignore_case":       map[string]interface{}{"type": "boolean", "default": true},
					"max_results":       map[string]interface{}{"type": "number"},
					"include_hidden":    map[string]interface{}{"type": "boolean", "default": false},
					"context_lines":     map[string]interface{}{"type": "number", "default": 5},
					"early_termination": map[string]interface{}{"type": "boolean"},
					"literal_search":    map[string]interface{}{"type": "boolean", "default": false},
				},
				"required": []string{"path", "pattern"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("search.start_search", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 17. get_more_search_results
	r.register(
		ToolDefinition{
			Name:        "get_more_search_results",
			Description: "Retrieve the next page of search results for an active search session.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"sessionId": map[string]interface{}{"type": "string"},
					"offset":    map[string]interface{}{"type": "number", "default": 0},
					"length":    map[string]interface{}{"type": "number", "default": 100},
				},
				"required": []string{"sessionId"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("search.get_more_search_results", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 18. stop_search
	r.register(
		ToolDefinition{
			Name:        "stop_search",
			Description: "Cancel an ongoing search session early.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"sessionId": map[string]interface{}{"type": "string"},
				},
				"required": []string{"sessionId"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("search.stop_search", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 19. list_searches
	r.register(
		ToolDefinition{
			Name:        "list_searches",
			Description: "List all active and recent search sessions.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("search.list_searches", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 20. get_config
	r.register(
		ToolDefinition{
			Name:        "get_config",
			Description: "Retrieve current server configuration.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			cfg := r.configMgr.GetConfig()
			bytes, _ := json.Marshal(map[string]interface{}{"config": cfg})
			return TextResult(string(bytes)), nil
		},
	)

	// 21. set_config_value
	r.register(
		ToolDefinition{
			Name:        "set_config_value",
			Description: "Update a server configuration setting.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string"},
					"value": map[string]interface{}{},
				},
				"required": []string{"key", "value"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Key   string      `json:"key"`
				Value interface{} `json:"value"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			if err := r.configMgr.SetValue(params.Key, params.Value); err != nil {
				return ErrorResult(err.Error()), nil
			}
			_ = r.configMgr.Save()
			return TextResult(fmt.Sprintf("Configuration key '%s' updated successfully", params.Key)), nil
		},
	)

	// 22. get_usage_stats
	r.register(
		ToolDefinition{
			Name:        "get_usage_stats",
			Description: "Get server uptime and tool call execution metrics.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			r.mu.RLock()
			uptime := time.Since(r.startTime).Seconds()
			counts := make(map[string]int)
			for k, v := range r.callCounts {
				counts[k] = v
			}
			r.mu.RUnlock()

			data := map[string]interface{}{
				"uptimeSeconds": uptime,
				"toolCalls":     counts,
			}
			bytes, _ := json.Marshal(data)
			return TextResult(string(bytes)), nil
		},
	)

	// 23. get_recent_tool_calls
	r.register(
		ToolDefinition{
			Name:        "get_recent_tool_calls",
			Description: "Audit recent tool calls executed by the server.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"maxResults": map[string]interface{}{"type": "number", "default": 50},
				},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			r.mu.RLock()
			calls := r.recentCalls
			r.mu.RUnlock()

			data := map[string]interface{}{
				"recentCalls": calls,
			}
			bytes, _ := json.Marshal(data)
			return TextResult(string(bytes)), nil
		},
	)

	// 24. excel_read_sheet
	r.register(
		ToolDefinition{
			Name:        "excel_read_sheet",
			Description: "Read cell data from an Excel (.xlsx) worksheet without Node.js dependencies.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":  map[string]interface{}{"type": "string"},
					"sheet": map[string]interface{}{"type": "string"},
					"range": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path  string `json:"path"`
				Sheet string `json:"sheet"`
				Range string `json:"range"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			cmdArgs := []string{"excel-read", "--path", params.Path}
			if params.Sheet != "" {
				cmdArgs = append(cmdArgs, "--sheet", params.Sheet)
			}
			if params.Range != "" {
				cmdArgs = append(cmdArgs, "--range", params.Range)
			}
			out, err := callDocService(cmdArgs...)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 25. excel_list_sheets
	r.register(
		ToolDefinition{
			Name:        "excel_list_sheets",
			Description: "List worksheet names in an Excel (.xlsx) workbook.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			out, err := callDocService("excel-sheets", "--path", params.Path)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 26. pdf_extract_text
	r.register(
		ToolDefinition{
			Name:        "pdf_extract_text",
			Description: "Extract text and metadata from PDF files natively.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			out, err := callDocService("pdf-extract", "--path", params.Path)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 27. excel_edit_range
	r.register(
		ToolDefinition{
			Name:        "excel_edit_range",
			Description: "Write a 2D matrix of cell values into an Excel (.xlsx) worksheet.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string"},
					"sheet":      map[string]interface{}{"type": "string"},
					"start_cell": map[string]interface{}{"type": "string", "default": "A1"},
					"data": map[string]interface{}{
						"type":        "array",
						"description": "2D array of string cell values",
						"items": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
				"required": []string{"path", "data"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path      string     `json:"path"`
				Sheet     string     `json:"sheet"`
				StartCell string     `json:"start_cell"`
				Data      [][]string `json:"data"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			if params.StartCell == "" {
				params.StartCell = "A1"
			}
			dataJSON, err := json.Marshal(params.Data)
			if err != nil {
				return ErrorResult(fmt.Sprintf("invalid data format: %v", err)), nil
			}

			cmdArgs := []string{"excel-edit", "--path", params.Path, "--start-cell", params.StartCell, "--data-json", string(dataJSON)}
			if params.Sheet != "" {
				cmdArgs = append(cmdArgs, "--sheet", params.Sheet)
			}
			out, err := callDocService(cmdArgs...)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 28. docx_read
	r.register(
		ToolDefinition{
			Name:        "docx_read",
			Description: "Read paragraphs, headers, and footers from Word (.docx) documents natively.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			out, err := callDocService("docx-read", "--path", params.Path)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 29. docx_edit
	r.register(
		ToolDefinition{
			Name:        "docx_edit",
			Description: "Search and replace text in Word (.docx) document body, headers, or footers.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string"},
					"find":    map[string]interface{}{"type": "string"},
					"replace": map[string]interface{}{"type": "string"},
					"target": map[string]interface{}{
						"type":        "string",
						"description": "Target scope: all, document, headers, footers (default: all)",
						"enum":        []string{"all", "document", "headers", "footers"},
					},
				},
				"required": []string{"path", "find", "replace"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Path    string `json:"path"`
				Find    string `json:"find"`
				Replace string `json:"replace"`
				Target  string `json:"target"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			if params.Target == "" {
				params.Target = "all"
			}
			cmdArgs := []string{"docx-edit", "--path", params.Path, "--find", params.Find, "--replace", params.Replace, "--target", params.Target}
			out, err := callDocService(cmdArgs...)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(out)), nil
		},
	)

	// 30. auth_status
	r.registerWithSession(
		ToolDefinition{
			Name:        "auth_status",
			Description: "Inspect active MCP authorization status, role, and capabilities.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		func(sessionID string, args json.RawMessage) (ToolCallResult, error) {
			st := r.auth.GetSessionStatus(sessionID)
			bytes, err := json.MarshalIndent(st, "", "  ")
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(bytes)), nil
		},
	)

	// 31. auth_login
	r.registerWithSession(
		ToolDefinition{
			Name:        "auth_login",
			Description: "Authenticate the active MCP session using a security token (DC_AUTH_TOKEN).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"token": map[string]interface{}{
						"type":        "string",
						"description": "Security token or pre-shared key",
					},
				},
				"required": []string{"token"},
			},
		},
		func(sessionID string, args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			if r.auth.AuthenticateSession(sessionID, params.Token) {
				return TextResult(fmt.Sprintf("Authentication successful. Active role: %s", r.auth.GetRole())), nil
			}
			return ErrorResult("Authentication failed: invalid token"), nil
		},
	)

	// 32. index_codebase
	r.register(
		ToolDefinition{
			Name:        "index_codebase",
			Description: "Index a workspace or directory into the persistent AST knowledge graph.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Root directory to index (default: current directory '.')",
					},
				},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.index_codebase", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 33. get_architecture
	r.register(
		ToolDefinition{
			Name:        "get_architecture",
			Description: "Retrieve high-level project architecture, language breakdown, entry points, and core symbols without consuming file reading tokens.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Root directory (default: current directory '.')",
					},
				},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.get_architecture", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 34. query_symbols
	r.register(
		ToolDefinition{
			Name:        "query_symbols",
			Description: "Search code symbols (functions, structs, classes, interfaces, enums) across the codebase with sub-millisecond precision.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search term for symbol name, signature, or docstring",
					},
					"kind": map[string]interface{}{
						"type":        "string",
						"description": "Filter by kind: function, method, struct, class, interface, enum, type",
					},
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Filter by file path substring",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum results to return (default: 50)",
					},
				},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.query_symbols", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 35. trace_call_path
	r.register(
		ToolDefinition{
			Name:        "trace_call_path",
			Description: "Trace call chains and execution paths between source and target symbols across services.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source": map[string]interface{}{
						"type":        "string",
						"description": "Source symbol name or ID",
					},
					"target": map[string]interface{}{
						"type":        "string",
						"description": "Target symbol name or ID",
					},
					"max_depth": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum call chain depth to traverse (default: 5)",
					},
				},
				"required": []string{"source", "target"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.trace_call_path", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 36. assess_impact
	r.register(
		ToolDefinition{
			Name:        "assess_impact",
			Description: "Evaluate the blast radius, callers, and regression risk before modifying code in a file or symbol.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "File path intended for modification",
					},
					"symbol_name": map[string]interface{}{
						"type":        "string",
						"description": "Target symbol name (optional)",
					},
					"line": map[string]interface{}{
						"type":        "integer",
						"description": "Target line number within file (optional)",
					},
				},
				"required": []string{"file_path"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.assess_impact", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 37. manage_adr
	r.register(
		ToolDefinition{
			Name:        "manage_adr",
			Description: "Manage Architecture Decision Records (ADRs) to preserve design decisions and governance across agent sessions.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"create", "list", "get", "delete"},
						"description": "ADR operation",
					},
					"id": map[string]interface{}{
						"type":        "string",
						"description": "ADR identifier (e.g. ADR-0001)",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "Title of the decision record",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"proposed", "accepted", "rejected", "superseded"},
						"description": "Status of the decision",
					},
					"context": map[string]interface{}{
						"type":        "string",
						"description": "Background context and problem statement",
					},
					"decision": map[string]interface{}{
						"type":        "string",
						"description": "Chosen architecture decision",
					},
					"consequences": map[string]interface{}{
						"type":        "string",
						"description": "Positive and negative consequences of the decision",
					},
					"affected_symbols": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Symbols affected by this decision",
					},
				},
				"required": []string{"action"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			res, err := r.fsClient.Call("memory.manage_adr", args)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult(string(res)), nil
		},
	)

	// 38. correlate_error
	r.register(
		ToolDefinition{
			Name:        "correlate_error",
			Description: "Correlate compiler errors or test failure stack traces with AST nodes and dependent callers.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"error_text": map[string]interface{}{
						"type":        "string",
						"description": "Compiler error message or test failure output",
					},
				},
				"required": []string{"error_text"},
			},
		},
		func(args json.RawMessage) (ToolCallResult, error) {
			var params struct {
				ErrorText string `json:"error_text"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return ErrorResult(err.Error()), nil
			}
			correlated := r.correlateTrace(params.ErrorText)
			bytes, _ := json.MarshalIndent(correlated, "", "  ")
			return TextResult(string(bytes)), nil
		},
	)
}

func (r *Registry) correlateTrace(errorText string) map[string]interface{} {
	type TraceMatch struct {
		FilePath string      `json:"file_path"`
		Line     int         `json:"line"`
		RawMatch string      `json:"raw_match"`
		Impact   interface{} `json:"impact,omitempty"`
	}

	lines := strings.Split(errorText, "\n")
	matches := make([]TraceMatch, 0)
	seen := make(map[string]bool)

	for _, l := range lines {
		lTrim := strings.TrimSpace(l)
		tokens := strings.Fields(lTrim)
		for _, tok := range tokens {
			tok = strings.Trim(tok, `"',()[]{}<>`)
			colonIdx := strings.LastIndex(tok, ":")
			if colonIdx > 0 && colonIdx < len(tok)-1 {
				candidateFile := tok[:colonIdx]
				ext := filepath.Ext(candidateFile)
				switch ext {
				case ".go", ".rs", ".ts", ".js", ".py", ".swift", ".c", ".cpp":
					var lineNum int
					if _, err := fmt.Sscanf(tok[colonIdx+1:], "%d", &lineNum); err == nil && lineNum > 0 {
						key := fmt.Sprintf("%s:%d", candidateFile, lineNum)
						if !seen[key] {
							seen[key] = true
							tm := TraceMatch{
								FilePath: candidateFile,
								Line:     lineNum,
								RawMatch: lTrim,
							}
							if r.fsClient != nil {
								assessArgs, _ := json.Marshal(map[string]interface{}{
									"file_path": candidateFile,
									"line":      lineNum,
								})
								if impRaw, err := r.fsClient.Call("memory.assess_impact", assessArgs); err == nil {
									var impVal interface{}
									_ = json.Unmarshal(impRaw, &impVal)
									tm.Impact = impVal
								}
							}
							matches = append(matches, tm)
						}
					}
				}
			}
		}
	}

	return map[string]interface{}{
		"total_matches": len(matches),
		"trace_targets": matches,
	}
}

func callDocService(args ...string) ([]byte, error) {
	execPath, err := os.Executable()
	binPath := ""
	if err == nil {
		candidate := filepath.Join(filepath.Dir(execPath), "dc-doc-service")
		if _, err := os.Stat(candidate); err == nil {
			binPath = candidate
		}
	}
	if binPath == "" {
		candidates := []string{"./bin/dc-doc-service", "bin/dc-doc-service", "../bin/dc-doc-service", "../../bin/dc-doc-service", "../../../bin/dc-doc-service"}
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				if abs, err := filepath.Abs(cand); err == nil {
					binPath = abs
					break
				}
			}
		}
	}
	if binPath == "" {
		binPath = "dc-doc-service"
	}

	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dc-doc-service error: %s (%w)", string(out), err)
	}
	return out, nil
}
