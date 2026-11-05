package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillFrontmatterMultilineDescriptionIsSearchable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "omni-skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	definition := "---\nname: omni-skill\ndescription: >\n  Dynamic router for creating and evaluating agent skills\n  across ChatGPT and Claude.\n---\n# Overview\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	name, description := parseSkillFrontmatter(filepath.Join(dir, "SKILL.md"))
	if name != "omni-skill" || !strings.Contains(description, "creating and evaluating agent skills") || strings.Contains(description, ">") {
		t.Fatalf("bad YAML-folded description: name=%q description=%q", name, description)
	}
}

func TestSkillFrontmatterDoesNotTreatNestedKeysAsTopLevel(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "SKILL.md")
	content := "---\nname: real-skill\ndescription: Create durable skills\nmetadata:\n  description: nested-untrusted-value\n---\n"
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	name, description := parseSkillFrontmatter(p)
	if name != "real-skill" || description != "Create durable skills" {
		t.Fatalf("nested metadata incorrectly overridden: %q %q", name, description)
	}
}
