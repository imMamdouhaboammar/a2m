package config

import (
	"path/filepath"
	"testing"
)

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
