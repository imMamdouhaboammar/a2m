package config

import (
	"path/filepath"
	"testing"
)

func TestApprovedGraphRepositoriesPersistAndRequireExistingDirectories(t *testing.T) {
	cfg := NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	first, second := t.TempDir(), t.TempDir()
	if err := cfg.SetValue("capabilityWorkspaces", []string{first, second}); err != nil {
		t.Fatalf("explicit graph repository allowlist rejected: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	fresh := NewConfigManager(cfg.configPath)
	roots := fresh.GetConfig().CapabilityWorkspaces
	canonicalFirst, _ := filepath.EvalSymlinks(first)
	canonicalSecond, _ := filepath.EvalSymlinks(second)
	if len(roots) != 2 || roots[0] != canonicalFirst || roots[1] != canonicalSecond {
		t.Fatalf("graph repository allowlist did not persist: %#v", roots)
	}
	if err := cfg.SetValue("capabilityWorkspaces", []string{first, filepath.Join(first, "nonexistent")}); err == nil {
		t.Fatal("nonexistent graph repo should not be authorized")
	}
	if err := cfg.SetValue("capabilityWorkspaces", []string{first, first}); err == nil {
		t.Fatal("duplicate graph root should not be authorized")
	}
	if got := cfg.GetConfig().CapabilityWorkspaces; len(got) != 2 || got[1] != canonicalSecond {
		t.Fatal("invalid graph repo update mutated existing approval")
	}
}

func TestCapabilityWorkspacePersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	mgr := NewConfigManager(p)
	workspace := t.TempDir()
	if err := mgr.SetValue("capabilityWorkspace", workspace); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Save(); err != nil {
		t.Fatal(err)
	}
	fresh := NewConfigManager(p)
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.GetConfig().CapabilityWorkspace; got != canonical {
		t.Fatalf("workspace setting was not restored: got %q, want %q", got, canonical)
	}
}
