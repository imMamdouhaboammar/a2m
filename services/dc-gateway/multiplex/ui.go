package multiplex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

// UI assets live in the desktop application's Resources directory after
// packaging. During development the canonical sources stay under UI/.
func uiAssetDirectory() (string, error) {
	var candidates []string
	if configured := os.Getenv("A2M_UI_DIR"); configured != "" {
		candidates = append(candidates, configured)
	} else {
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates,
				filepath.Join(filepath.Dir(exe), "..", "Resources", "UI", "a2m-desktop-v2"))
		}
		if cwd, err := os.Getwd(); err == nil {
			for _, relative := range []string{
				"UI/a2m-desktop-v2", "../UI/a2m-desktop-v2",
				"../../UI/a2m-desktop-v2", "../../../UI/a2m-desktop-v2",
			} {
				candidates = append(candidates, filepath.Join(cwd, relative))
			}
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(filepath.Join(candidate, "index.html"))
		if err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("A2M UI assets not found; set A2M_UI_DIR or package Contents/Resources/UI")
}

func (s *Server) serveUIAsset(w http.ResponseWriter, name, mimeType string) {
	dir, err := uiAssetDirectory()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		http.Error(w, "A2M UI asset unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = w.Write(content)
}

func (s *Server) handleUIStyles(w http.ResponseWriter, r *http.Request) {
	s.serveUIAsset(w, "styles.css", "text/css; charset=utf-8")
}

func (s *Server) handleUIScript(w http.ResponseWriter, r *http.Request) {
	s.serveUIAsset(w, "app.js", "text/javascript; charset=utf-8")
}

// HTTP process snapshots intentionally do not advance the MCP process reader's
// cursor. Inspecting output in the desktop UI must not consume agent output.
func (s *Server) handleProcessOutput(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
	if err != nil || pid <= 0 {
		http.Error(w, "valid pid required", http.StatusBadRequest)
		return
	}
	limit := 250
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n < 1 || n > 500 {
			http.Error(w, "limit must be between 1 and 500", http.StatusBadRequest)
			return
		}
		limit = n
	}
	if r.URL.Query().Get("source") == "mcp-worker" {
		if s.worker == nil {
			http.Error(w, "MCP worker monitor unavailable", http.StatusServiceUnavailable)
			return
		}
		sessions, err := s.worker.ListSessions()
		if err != nil {
			http.Error(w, "MCP worker unavailable", http.StatusServiceUnavailable)
			return
		}
		tracked := false
		for _, item := range sessions {
			if item.PID == pid {
				tracked = true
				break
			}
		}
		if !tracked {
			http.Error(w, "MCP worker process not found", http.StatusNotFound)
			return
		}
		lines, _ := s.worker.Snapshot(pid)
		if lines == nil {
			lines = []process.SnapshotLine{}
		}
		if len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"pid": pid, "source": "mcp-worker", "lines": lines, "scope": "observed since monitor connection; terminal output redacted for common credentials"})
		return
	}
	lines, err := s.supervisor.SnapshotOutput(pid, limit)
	if err != nil {
		http.Error(w, "tracked process not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"pid": pid, "lines": lines})
}
