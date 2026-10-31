package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphTemporaryIndexIncludesNewFilesWithoutStaging(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	tracked := filepath.Join(root, "tracked.go")
	if err := os.WriteFile(tracked, []byte("package example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "--", "tracked.go").CombinedOutput(); err != nil {
		t.Fatalf("git add tracked: %s %v", out, err)
	}
	pending := filepath.Join(root, "pending.go")
	if err := os.WriteFile(pending, []byte("package example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	index, cleanup, err := createGraphTemporaryIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	list := func(withTemp bool) string {
		cmd := exec.Command("git", "-C", root, "ls-files", "--", "pending.go")
		if withTemp {
			cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		}
		bytes, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(bytes))
	}
	if list(false) != "" {
		t.Fatal("test fixture untracked file was already staged")
	}
	if list(true) != "pending.go" {
		t.Fatal("new source file is missing from temporary graph index")
	}
	if err := os.WriteFile(filepath.Join(root, "created_later.rs"), []byte("fn main() {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := refreshGraphTemporaryIndex(root, index); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "ls-files", "--", "created_later.rs")
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "created_later.rs" {
		t.Fatalf("refresh missed new file: %q %v", out, err)
	}
	if list(false) != "" {
		t.Fatal("real git index was modified")
	}
}
