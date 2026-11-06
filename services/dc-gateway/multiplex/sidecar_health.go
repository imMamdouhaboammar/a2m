package multiplex

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// sidecarExecutable only discovers sibling executables from the gateway's
// own installation directory. Never execute a binary found under an untrusted
// working directory just because a dashboard status request was received.
func sidecarExecutable(names ...string) string {
	for _, envKey := range []string{"A2M_DOC_BINARY", "A2M_FS_BINARY"} {
		if envVal := os.Getenv(envKey); envVal != "" {
			for _, name := range names {
				if filepath.Base(envVal) == name {
					info, err := os.Stat(envVal)
					if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
						return envVal
					}
				}
			}
		}
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	dirs := []string{filepath.Dir(self)}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		realDir := filepath.Dir(real)
		if realDir != dirs[0] {
			dirs = append(dirs, realDir)
		}
	}
	for _, dir := range dirs {
		for _, name := range names {
			if name != filepath.Base(name) || name == "." || name == ".." {
				continue
			}
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				return candidate
			}
		}
	}
	return ""
}

var docHealthCache struct {
	sync.Mutex
	binary   string
	modified time.Time
	size     int64
	expires  time.Time
	ready    bool
}

// documentServiceReady probes a trusted on-demand document binary, caching
// short-lived readiness to keep dashboard polling inexpensive.
func documentServiceReady(binary string) bool {
	if binary == "" {
		return false
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return false
	}

	// The document worker is a short-lived CLI. Probe once and reuse the
	// result briefly; status polling must not spawn processes repeatedly.
	docHealthCache.Lock()
	defer docHealthCache.Unlock()
	if docHealthCache.binary == binary &&
		docHealthCache.size == info.Size() &&
		docHealthCache.modified.Equal(info.ModTime()) &&
		time.Now().Before(docHealthCache.expires) {
		return docHealthCache.ready
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, runErr := exec.CommandContext(ctx, binary, "health").Output()
	var response struct {
		Service string `json:"service"`
		Status  string `json:"status"`
		Mode    string `json:"mode"`
	}
	ready := runErr == nil && len(output) <= 4096 &&
		json.Unmarshal(output, &response) == nil &&
		response.Service == "a2m-docs" &&
		response.Status == "ready" &&
		response.Mode == "on-demand"

	docHealthCache.binary = binary
	docHealthCache.size = info.Size()
	docHealthCache.modified = info.ModTime()
	docHealthCache.expires = time.Now().Add(10 * time.Second)
	docHealthCache.ready = ready
	return ready
}

// probeSidecarsAt reports actual IPC availability separately from an installed
// on-demand filesystem worker and verifies the document worker's health.
func probeSidecarsAt(socketPath, fsBinary, docBinary string) map[string]string {
	fsState := "unavailable"
	conn, err := net.DialTimeout("unix", socketPath, 125*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		fsState = "reachable"
	} else if fsBinary != "" {
		fsState = "installed (on-demand)"
	}

	docState := "unavailable"
	if documentServiceReady(docBinary) {
		docState = "ready (on-demand)"
	}
	return map[string]string{
		"gateway":    "running",
		"fsEngine":   fsState,
		"docService": docState,
	}
}
