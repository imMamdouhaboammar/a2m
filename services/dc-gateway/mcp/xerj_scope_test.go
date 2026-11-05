package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXerjIndexTargetStaysWithinApprovedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	subdir := filepath.Join(workspace, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}

	for _, input := range []string{"", ".", "sub", subdir} {
		path, err := xerjIndexTarget(workspace, input)
		if err != nil || path == "" {
			t.Fatalf("safe path %q rejected: %v", input, err)
		}
	}
	for _, input := range []string{outside, "..", filepath.Join("escape"), "nonexistent"} {
		if path, err := xerjIndexTarget(workspace, input); err == nil {
			t.Fatalf("unsafe or nonexistent path %q was accepted as %q", input, path)
		}
	}
}
