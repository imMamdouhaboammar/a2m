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

// sidecarExecutable finds only executable binaries alongside the running
// gateway or in the development tree. It never starts a service.
func sidecarExecutable(names ...string) string {
	dirs := []string{}
	if self, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(self))
	}
	dirs = append(dirs, "./bin", "../bin", "../../bin", "../../../bin")
	for _, dir := range dirs {
		for _, name := range names {
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
