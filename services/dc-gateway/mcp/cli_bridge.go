package mcp

// Each installed curated CLI is a real, independently discoverable MCP tool.
// Commands are executed directly with argv (never through a shell). No implicit
// installation, elevated OS privilege, or gateway control-plane secret inheritance.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const cliOutputLimit = 64 * 1024

type cliLimitedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (w *cliLimitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := len(p)
	remaining := cliOutputLimit - w.buf.Len()
	if remaining < count {
		if remaining > 0 {
			_, _ = w.buf.Write(p[:remaining])
		}
		w.truncated = true
	} else {
		_, _ = w.buf.Write(p)
	}
	return count, nil
}
func (w *cliLimitedWriter) Text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.buf.String()
	if w.truncated {
		out += "\n[A2M output truncated at 64 KiB]"
	}
	return out
}

func cliToolName(id string) string {
	var b strings.Builder
	b.WriteString("cli_")
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("_run")
	return b.String()
}

func (r *Registry) registerCLIBridge() {
	for _, spec := range capabilitySpecs {
		var executable string
		for _, candidate := range spec.cliNames {
			executable = resolveActiveCLI(candidate)
			if executable != "" {
				break
			}
		}
		if executable == "" {
			continue
		}
		name := cliToolName(spec.id)
		specID := spec.id
		r.register(ToolDefinition{
			Name:        name,
			Description: fmt.Sprintf("Run locally installed %s CLI directly as an argv-based process. %s %s Requires authorizations of the underlying service for credentialed actions. No shell or auto-install. Returns exit status and bounded output.", specID, spec.description, spec.useFor),
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"args":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Literal CLI arguments. Do not include the executable name or shell syntax."},
					"cwd":        map[string]interface{}{"type": "string", "description": "Existing working directory. Must respect configured allowedDirectories if set."},
					"timeout_ms": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 300000, "description": "Command timeout, default 60000 ms."},
				},
				"additionalProperties": false,
			},
		}, func(raw json.RawMessage) (ToolCallResult, error) {
			return r.runCuratedCLI(executable, raw), nil
		})
	}
}

func (r *Registry) runCuratedCLI(executable string, raw json.RawMessage) ToolCallResult {
	var request struct {
		Args      []string `json:"args"`
		CWD       string   `json:"cwd"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if len(raw) > 64*1024 {
		return ErrorResult("CLI arguments too large")
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &request); err != nil {
			return ErrorResult("invalid CLI arguments: expected args as an array of strings")
		}
	}
	if len(request.Args) > 128 {
		return ErrorResult("too many CLI arguments")
	}
	total := 0
	for _, a := range request.Args {
		total += len(a)
		if strings.ContainsRune(a, 0) || len(a) > 16384 || total > 65536 {
			return ErrorResult("CLI argument contains NUL or exceeds allowed size")
		}
	}
	timeout := request.TimeoutMS
	if timeout == 0 {
		timeout = 60000
	}
	if timeout < 0 || timeout > 300000 {
		return ErrorResult("CLI timeout_ms must be between 1 and 300000")
	}
	cwd := request.CWD
	if cwd == "" {
		cwd = r.capabilityWorkspace()
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ErrorResult("working directory unavailable")
		}
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ErrorResult("CLI working directory does not exist")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return ErrorResult("CLI working directory must be a directory")
	}
	pathJSON, _ := json.Marshal(map[string]string{"path": resolved})
	if err := r.validatePathBoundary("cli_cwd", pathJSON); err != nil {
		return ErrorResult(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, request.Args...)
	cmd.Dir = resolved
	cmd.Env = safeExternalEnvironment(nil)
	var output cliLimitedWriter
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrorResult("CLI timed out after " + fmt.Sprint(timeout) + " ms\n" + output.Text())
	}
	if err != nil {
		return ErrorResult(fmt.Sprintf("CLI exited unsuccessfully (%v)\n%s", err, output.Text()))
	}
	return TextResult(output.Text())
}
