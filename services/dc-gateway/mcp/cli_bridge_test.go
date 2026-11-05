package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
)

func fixtureCLIRegistry(t *testing.T, script string) (*Registry, string) {
	t.Helper()
	binDir := t.TempDir()
	home := t.TempDir()
	path := filepath.Join(binDir, "rea")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", home)
	t.Setenv("CONTROL_PLANE_API_KEY", "gateway-private-secret")
	cfg := config.NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	r := &Registry{handlers: make(map[string]ToolHandler), sessionHandlers: make(map[string]SessionToolHandler), callCounts: make(map[string]int),
		auth: auth.NewAuthenticator("", auth.RoleAdmin, false), configMgr: cfg}
	r.registerCLIBridge()
	return r, path
}

func TestCLIBridgeExportsRealInstalledExecutableAndPassesArgumentsLiterally(t *testing.T) {
	r, path := fixtureCLIRegistry(t, "printf 'arg1=%s\narg2=%s\ncwd=%s\nsecret=%s\n' \"$1\" \"$2\" \"$PWD\" \"$CONTROL_PLANE_API_KEY\"")
	found := false
	for _, tool := range r.ListTools() {
		if tool.Name == "cli_rea_run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("REA not exported as MCP tool; installed at %s", path)
	}
	probe := filepath.Join(t.TempDir(), "injection-should-not-exist")
	malicious := "$(touch " + probe + ")"
	payload, _ := json.Marshal(map[string]interface{}{"args": []string{"--help", malicious}, "cwd": t.TempDir()})
	got := r.Dispatch("cli_rea_run", payload)
	if got.IsError || len(got.Content) == 0 {
		t.Fatalf("CLI call failed: %+v", got)
	}
	output := got.Content[0].Text
	if !strings.Contains(output, "arg1=--help") || !strings.Contains(output, "arg2="+malicious) {
		t.Fatalf("CLI argument transformation: %s", output)
	}
	if !strings.Contains(output, "secret=") || strings.Contains(output, "gateway-private-secret") {
		t.Fatal("gateway secret leaked to CLI child")
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("shell injection executed")
	}
}

func TestCLIBridgeRejectsMissingExecutableAndInvalidArguments(t *testing.T) {
	r, _ := fixtureCLIRegistry(t, "printf ok")
	if got := r.Dispatch("cli_resend_run", json.RawMessage("{}")); !got.IsError {
		t.Fatalf("invented unavailable executable: %+v", got)
	}
	if got := r.Dispatch("cli_rea_run", json.RawMessage("{\"args\":[\"ok\",5]}")); !got.IsError {
		t.Fatalf("invalid argument types accepted: %+v", got)
	}
	if got := r.Dispatch("cli_rea_run", json.RawMessage("{\"args\":[\"bad\\u0000\"]}")); !got.IsError {
		t.Fatalf("NUL argument accepted: %+v", got)
	}
}

func TestCLIBridgeEnforcesConfiguredFilesystemBoundary(t *testing.T) {
	r, _ := fixtureCLIRegistry(t, "printf ok")
	approved := t.TempDir()
	if err := r.configMgr.SetValue("allowedDirectories", []string{approved}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	payload, _ := json.Marshal(map[string]interface{}{"cwd": outside, "args": []string{"--help"}})
	if got := r.Dispatch("cli_rea_run", payload); !got.IsError {
		t.Fatalf("CLI escaped filesystem boundary: %+v", got)
	}
}

func TestCLIBridgeReportsNonzeroExitAsError(t *testing.T) {
	r, _ := fixtureCLIRegistry(t, "exit 13")
	if got := r.Dispatch("cli_rea_run", json.RawMessage("{\"args\":[]}")); !got.IsError || !strings.Contains(got.Content[0].Text, "13") {
		t.Fatalf("lost CLI exit status: %+v", got)
	}
}

func TestFeaturedCapabilitiesAdvertiseActualCLIInvocationTool(t *testing.T) {
	r, _ := fixtureCLIRegistry(t, "printf ok")
	found := false
	for _, item := range r.featuredCapabilities() {
		if item.ID == "rea" {
			found = true
			if !item.CLIAvailable || item.InvocationTool != "cli_rea_run" || item.Status != "cli_mcp_callable" {
				t.Fatalf("registered executable missing callable metadata: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("REA missing from catalog")
	}
}
