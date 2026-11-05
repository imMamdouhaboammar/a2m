package mcp

// Featured capability metadata helps AI clients choose tools semantically.
// A2M deliberately does not parse user intent or hardcode trigger synonyms.
// The client reasons about the task; the gateway reports local availability
// and enforces independently authorized execution boundaries.
import (
	"os"
	"os/exec"
	"path/filepath"
)

type featuredCapability struct {
	ID           string `json:"id"`
	Priority     string `json:"priority"`
	Description  string `json:"description"`
	UseFor       string `json:"use_for"`
	Installed    bool   `json:"installed"`
	Executable   string `json:"executable,omitempty"`
	SkillID      string `json:"skill_id,omitempty"`
	Status       string `json:"status"`
	MCPAvailable bool   `json:"mcp_available"`
	SuggestedUse string `json:"suggested_use"`
}

// resolveActiveCLI returns the executable currently selected by the machine's
// PATH/package-manager installation; it never installs or pins versions.
func resolveActiveCLI(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, path := range []string{
		filepath.Join(home, ".bun", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".cargo", "bin", name),
		filepath.Join("/opt/homebrew/bin", name),
		filepath.Join("/usr/local/bin", name),
	} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}

func (r *Registry) featuredCapabilities() []featuredCapability {
	skillID := ""
	for _, s := range discoverSkills(r.capabilityWorkspace()) {
		if s.Name == "omni-skill" {
			skillID = s.ID
			break
		}
	}
	entries := []featuredCapability{
		{
			ID: "omni-skill", Priority: "premium",
			Description:  "Installed OmniSkill framework for designing, building, evaluating, improving, porting, and packaging agent skills.",
			UseFor:       "Agent-skill authoring and lifecycle work, including creation of SKILL.md packages and cross-host evaluations, not ordinary application programming.",
			SuggestedUse: "Read the installed OmniSkill skill instructions, then use the current local omni-skill CLI and its route/init/validate/eval tools as appropriate.",
			SkillID:      skillID,
		},
		{
			ID: "xerj", Priority: "premium",
			Description:  "Local XERJ codebase indexing, code search, definitions, and repository memory.",
			UseFor:       "Onboarding and understanding an existing or new code repository; inspect index health, then initialize or incrementally index an authorized workspace when needed.",
			SuggestedUse: "Check XERJ node health and index state. Prefer definitions and ranked code search when healthy; never index outside an approved workspace or blindly re-index every tool call.",
		},
		{
			ID: "rea", Priority: "premium",
			Description:  "Locally installed REA application reverse engineering and evidence-backed analysis CLI/MCP.",
			UseFor:       "Reverse engineering applications, binaries, user experiences, web bundles, and observed behavior.",
			SuggestedUse: "Prefer explicitly approved REA MCP tools when advertised. If disabled, report that approval/reconnection is required; do not launch it as an authorization bypass.",
		},
	}
	for i := range entries {
		c := &entries[i]
		c.Executable = resolveActiveCLI(c.ID)
		c.Installed = c.Executable != ""
		if !c.Installed {
			c.Status = "not_installed"
			continue
		}
		switch c.ID {
		case "xerj":
			if checkXerjDaemonHealthy(xerjServerURL()) {
				c.Status = "node_healthy"
				c.MCPAvailable = r.xerjToolCount() > 0
			} else {
				c.Status = "node_unavailable"
				c.MCPAvailable = false
			}
		case "rea":
			c.MCPAvailable = r.externalActive["user:rea"] > 0
			if c.MCPAvailable {
				c.Status = "mcp_operator_approved"
			} else {
				c.Status = "cli_installed_mcp_not_approved"
			}
		default:
			c.Status = "cli_installed"
		}
	}
	return entries
}
