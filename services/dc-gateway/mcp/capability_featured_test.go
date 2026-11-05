package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeaturedCapabilitiesResolveCurrentLocalCLIs(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"omni-skill", "rea", "xerj", "e2e", "ui-master-engine", "gbrain", "gitleaks", "replica-square", "gsd-core"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	capabilities := (&Registry{}).featuredCapabilities()
	if len(capabilities) < 27 {
		t.Fatalf("expected 26 featured tools: %+v", capabilities)
	}
	for i, name := range []string{"omni-skill", "xerj", "rea"} {
		entry := capabilities[i]
		if entry.ID != name || !entry.Installed || entry.Executable != filepath.Join(binDir, name) {
			t.Fatalf("wrong current installation for %q: %+v", name, entry)
		}
		if entry.UseFor == "" || entry.Description == "" || entry.Priority != "premium" {
			t.Fatalf("missing semantic usage metadata: %+v", entry)
		}
	}
	byID := make(map[string]featuredCapability)
	for _, item := range capabilities {
		if _, duplicate := byID[item.ID]; duplicate {
			t.Fatalf("duplicate capability %s", item.ID)
		}
		byID[item.ID] = item
		if item.Description == "" || item.UseFor == "" || item.Category == "" {
			t.Fatalf("incomplete capability %+v", item)
		}
	}
	if got := byID["ui-master-rig"].Executable; got != filepath.Join(binDir, "ui-master-engine") {
		t.Fatalf("ui-master-rig alias not resolved: %s", got)
	}
	if got := byID["gsd"].Executable; got != filepath.Join(binDir, "gsd-core") {
		t.Fatalf("GSD package binary not resolved: %s", got)
	}
	if got := byID["e2e"].Executable; got != filepath.Join(binDir, "e2e") {
		t.Fatalf("TesterArmy e2e CLI not resolved: %s", got)
	}
	if got := byID["replica-square"].Executable; got != filepath.Join(binDir, "replica-square") {
		t.Fatalf("replica-square not resolved: %s", got)
	}
	if len(byID["rea"].WorksWith) != 1 || byID["rea"].WorksWith[0] != "replica-square" || len(byID["rea"].Workflow) < 3 {
		t.Fatal("REA dual-engine workflow not advertised")
	}
	if len(byID["replica-square"].WorksWith) != 1 || byID["replica-square"].WorksWith[0] != "rea" {
		t.Fatal("replica-square companion link missing")
	}
	if byID["replica-square"].MCPAvailable {
		t.Fatal("unapproved replica-square MCP cannot be callable")
	}
	if byID["resend"].MCPAvailable || byID["supabase"].MCPAvailable {
		t.Fatal("unapproved cloud MCP incorrectly exposed as callable")
	}
	// No stale hardcoded path: a changed installation resolves on the next query.
	nextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nextDir, "omni-skill"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", nextDir)
	updated := (&Registry{}).featuredCapabilities()
	if updated[0].Executable != filepath.Join(nextDir, "omni-skill") {
		t.Fatalf("installed tool path did not refresh: %+v", updated[0])
	}
}

func TestFeaturedCapabilitiesDoNotImplyMCPAuthorization(t *testing.T) {
	capabilities := (&Registry{}).featuredCapabilities()
	for _, c := range capabilities {
		if c.ID == "rea" && c.MCPAvailable {
			t.Fatal("REA should not be marked available without active operator-approved MCP tools")
		}
	}
}

func TestCapabilitiesListExposesAdvisoryPremiumCatalog(t *testing.T) {
	r := &Registry{
		handlers:        make(map[string]ToolHandler),
		sessionHandlers: make(map[string]SessionToolHandler),
	}
	r.registerCapabilities()
	handler := r.handlers["capabilities_list"]
	if handler == nil {
		t.Fatal("capability inventory tool is not registered")
	}
	result, err := handler(nil)
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("inventory unavailable: %+v %v", result, err)
	}
	var payload struct {
		CapabilitySelection string               `json:"capability_selection"`
		Featured            []featuredCapability `json:"featured_capabilities"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Featured) < 27 || payload.Featured[0].ID != "omni-skill" || payload.Featured[1].ID != "xerj" || payload.Featured[2].ID != "rea" {
		t.Fatalf("missing or out-of-order featured capabilities: %+v", payload.Featured)
	}
	if !strings.Contains(payload.CapabilitySelection, "AI client") {
		t.Fatalf("semantic routing ownership unclear: %q", payload.CapabilitySelection)
	}
}

// TestGeminiREAUsesCanonicalAgentSkill proves that the user's Gemini alias
// shares one canonical skill definition with A2M's global Agent Skill index.
// The discovery tool never copies, rewrites or runs the skill.
func TestGeminiREAUsesCanonicalAgentSkill(t *testing.T) {
	home := t.TempDir()
	canonicalRoot := filepath.Join(home, ".agents", "skills")
	canonicalSkill := filepath.Join(canonicalRoot, "rea")
	if err := os.MkdirAll(canonicalSkill, 0700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: rea\ndescription: >\n  Evidence-led reverse engineering and replica-square collaboration.\n---\n# REA and Replica-Square\n"
	if err := os.WriteFile(filepath.Join(canonicalSkill, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	geminiRoot := filepath.Join(home, ".gemini", "config", "skills")
	if err := os.MkdirAll(geminiRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonicalSkill, filepath.Join(geminiRoot, "rea")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("A2M_SKILL_ROOTS", canonicalRoot)

	var id string
	for _, skill := range discoverSkills("") {
		if skill.Name == "rea" {
			id = skill.ID
		}
	}
	if id != "0:rea" {
		t.Fatalf("expected canonical REA skill ID, got %q", id)
	}
	loaded, err := readSkillByID(id, "")
	if err != nil || loaded != body {
		t.Fatalf("canonical skill read failed: %v, %q", err, loaded)
	}

	byID := map[string]featuredCapability{}
	for _, c := range (&Registry{}).featuredCapabilities() {
		byID[c.ID] = c
	}
	if byID["rea"].SkillID != id || byID["replica-square"].SkillID != id {
		t.Fatalf("both engines must reference the same live REA skill: %+v %+v", byID["rea"], byID["replica-square"])
	}
	if _, err := os.ReadFile(filepath.Join(geminiRoot, "rea", "SKILL.md")); err != nil {
		t.Fatalf("Gemini alias no longer resolves to canonical skill: %v", err)
	}
}
