package mcp

// CRG 2.3.6 full builds enumerate git-tracked files. A private temporary
// Git index lets CRG see newly created source files without staging the user's
// work or changing .git/index. This remains a private implementation detail.
import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var graphSourceExtensions = map[string]bool{
	".go": true, ".rs": true, ".swift": true, ".py": true, ".js": true,
	".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".java": true, ".kt": true, ".kts": true, ".rb": true, ".php": true,
	".cs": true, ".scala": true, ".sc": true, ".sh": true, ".bash": true,
	".ex": true, ".exs": true, ".vue": true, ".svelte": true, ".dart": true,
	".lua": true, ".zig": true, ".m": true, ".mm": true,
}

func createGraphTemporaryIndex(workspace string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "a2m-crg-index-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	index := filepath.Join(dir, "git-index")
	if err := refreshGraphTemporaryIndex(workspace, index); err != nil {
		cleanup()
		return "", nil, err
	}
	return index, cleanup, nil
}

func refreshGraphTemporaryIndex(workspace, index string) error {
	// Always read the real index via the fixed, restricted git command.
	// Never use GIT_INDEX_FILE while resolving the source index.
	indexCmd := exec.Command("git", "-C", workspace, "rev-parse", "--git-path", "index")
	indexCmd.Env = withoutGitIndexEnv(os.Environ())
	output, err := indexCmd.Output()
	if err != nil {
		return fmt.Errorf("git index not found: %w", err)
	}
	realIndex := strings.TrimSpace(string(output))
	if !filepath.IsAbs(realIndex) {
		realIndex = filepath.Join(workspace, realIndex)
	}
	source, err := os.Open(realIndex)
	if err != nil {
		return fmt.Errorf("git index unavailable: %w", err)
	}
	defer source.Close()
	destination, err := os.OpenFile(index, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(destination, source)
	closeErr := destination.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	env := append(withoutGitIndexEnv(os.Environ()), "GIT_INDEX_FILE="+index)
	untrackedCmd := exec.Command("git", "-C", workspace, "ls-files", "--others", "--exclude-standard", "-z")
	untrackedCmd.Env = env
	untracked, err := untrackedCmd.Output()
	if err != nil {
		return fmt.Errorf("untracked discovery failed: %w", err)
	}
	sourceFiles := make([]string, 0, 100)
	for _, entry := range bytes.Split(untracked, []byte{0}) {
		if len(entry) == 0 || len(sourceFiles) >= 2500 {
			continue
		}
		relative := string(entry)
		if !graphSourceExtensions[strings.ToLower(filepath.Ext(relative))] {
			continue
		}
		if filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
			continue
		}
		full := filepath.Join(workspace, relative)
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
			continue
		}
		sourceFiles = append(sourceFiles, relative)
	}
	for start := 0; start < len(sourceFiles); start += 100 {
		end := start + 100
		if end > len(sourceFiles) {
			end = len(sourceFiles)
		}
		args := append([]string{"-C", workspace, "add", "-N", "--"}, sourceFiles[start:end]...)
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("temporary graph index update failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func withoutGitIndexEnv(environ []string) []string {
	result := make([]string, 0, len(environ))
	for _, value := range environ {
		if !strings.HasPrefix(value, "GIT_INDEX_FILE=") {
			result = append(result, value)
		}
	}
	return result
}
