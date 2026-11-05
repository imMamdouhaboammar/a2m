package mcp

// Featured capability metadata helps AI clients select tools by task semantics.
// This catalog is descriptive: CLI presence never authorizes actions, and
// unapproved external MCP providers are never launched by inventory discovery.
import (
	"os"
	"os/exec"
	"path/filepath"
)

type featuredCapability struct {
	ID             string   `json:"id"`
	Category       string   `json:"category"`
	Priority       string   `json:"priority"`
	Description    string   `json:"description"`
	UseFor         string   `json:"use_for"`
	CLIAvailable   bool     `json:"cli_available"`
	InvocationTool string   `json:"invocation_tool,omitempty"`
	Installed      bool     `json:"installed"`
	Executable     string   `json:"executable,omitempty"`
	SkillID        string   `json:"skill_id,omitempty"`
	Status         string   `json:"status"`
	MCPConfigured  bool     `json:"mcp_configured"`
	MCPAvailable   bool     `json:"mcp_available"`
	SuggestedUse   string   `json:"suggested_use"`
	SourceURL      string   `json:"source_url,omitempty"`
	WorksWith      []string `json:"works_with,omitempty"`
	Workflow       []string `json:"workflow,omitempty"`
}

type capabilitySpec struct {
	id, category, description, useFor, suggestion string
	cliNames                                      []string
	mcpNames                                      []string
	skillName, sourceURL                          string
}

// Curated task-specific affordances, not a prompt router or synonym table.
// A provider only becomes executable through the existing per-tool approval
// mechanism. A CLI may require further user authorization for actual actions.
var capabilitySpecs = []capabilitySpec{
	{"omni-skill", "agent_skills", "Author, validate, evaluate, port and package agent skills.", "Skill design and lifecycle management, not routine application coding.", "Read its discovered SKILL.md first; choose route/init/validate/eval as needed.", []string{"omni-skill"}, nil, "omni-skill", ""},
	{"xerj", "code_intelligence", "Definition-first code search, semantic retrieval and workspace indexing.", "Onboard or investigate a codebase with an already healthy XERJ node.", "Check node health; index only an approved workspace when necessary.", []string{"xerj"}, []string{"xerj"}, "", ""},
	{"rea", "reverse_engineering", "Binary and application reverse engineering, execution and call analysis.", "Inspect binaries, apps and API artifacts using an evidence-first process, with Replica-Square for authorized clean-room rebuilding.", "Read the discovered REA SKILL.md (shared with the Gemini /rea skill); select target-specific REA tools, then use replica-square only if reconstruction is requested and authorized.", []string{"rea"}, []string{"rea"}, "rea", ""},
	{"replica-square", "reverse_engineering", "Clean-room reconstruction, evidence-led product replicas, WCAG token validation and parity audits.", "After a REA evidence ledger exists, develop independent implementations and verify IP clearance and behavior.", "Read the shared REA SKILL.md and inspect replica-square route/architect/design/diff/sweep help; execute only within authorized scope.", []string{"replica-square"}, []string{"replica-square"}, "rea", ""},
	{"gbrain", "agent_memory", "Persistent agent memory and knowledge graph.", "Record or retrieve long-lived development context with explicit scope and consent.", "Review data retention and write permissions before storing project information.", []string{"gbrain"}, []string{"gbrain"}, "", ""},
	{"repowise", "code_intelligence", "Repository change-risk, architecture and dead-code analysis.", "Review impact, structural problems and unused code before a merge.", "Run scoped read-only inspection first and corroborate findings against source.", []string{"repowise"}, []string{"repowise"}, "", ""},
	{"gsd", "agent_planning", "Get Stuff Done development planning and phase orchestration.", "Structure and execute an approved multi-step engineering plan.", "Check @opengsd/gsd-core installation and documented entrypoint; do not fetch packages implicitly.", []string{"gsd", "gsd-core"}, []string{"gsd"}, "", ""},
	{"21st", "design", "21st.dev UI component discovery and generation.", "Explore or source reusable web UI component designs.", "Check installed 21st CLI and service authorization before external requests.", []string{"21st"}, []string{"21st"}, "", ""},
	{"ui-master-rig", "design", "Native UI design, WCAG contrast and browser-extension audits.", "Inspect interface visual quality and accessibility compliance.", "Use the installed ui-master-engine executable, not a guessed command name.", []string{"ui-master-engine"}, []string{"ui-master-rig"}, "", ""},
	{"supabase", "cloud", "Supabase project database, auth, functions and storage management.", "Work on authorized Supabase resources; CLI and hosted MCP are distinct.", "Prefer approved connector/MCP tools; verify project and scope before mutations.", []string{"supabase"}, []string{"supabase"}, "", ""},
	{"resend", "cloud", "Resend email delivery, domain and sender management.", "Manage authorized email service configurations and outbound email.", "Requires an explicitly approved provider and approval for sending messages.", []string{"resend"}, []string{"resend"}, "", ""},
	{"playwright", "browser_testing", "Playwright browser automation and deterministic web tests.", "Verify user flows, DOM behavior, rendering and browser regressions.", "Use installed Playwright in an authorized test environment.", []string{"playwright"}, []string{"playwright"}, "", ""},
	{"e2e", "browser_testing", "TesterArmy agentic end-to-end testing for web and mobile.", "Create and run asserted E2E journeys, optionally using agent actions.", "Use locally installed e2e runner and its skill; do not execute init/login or download browsers silently.", []string{"e2e"}, []string{"e2e"}, "e2e", "https://github.com/tester-army/e2e"},
	{"claude", "agent_cli", "Claude Code terminal software engineering agent.", "Agent-assisted code analysis and implementation under project policies.", "Check existing authentication and permission boundaries; do not automate privileged approvals.", []string{"claude"}, nil, "", ""},
	{"get-fable", "agent_cli", "Agent orchestration, skill governance and development lifecycle.", "Organize scoped agent work and skill assessments.", "Inspect local CLI help and follow repository constraints.", []string{"get-fable"}, nil, "", ""},
	{"agent-kernel", "agent_cli", "Agent policies, execution gates and environment governance.", "Inspect or enforce coding-agent execution policies.", "Do not change security policy without explicit user authorization.", []string{"agent-kernel"}, nil, "", ""},
	{"browser-use", "agent_cli", "Browser control from an agent-enabled terminal.", "Interact with authorized web applications and UI journeys.", "Review account, browsing and external-action permissions first.", []string{"browser-use"}, nil, "", ""},
	{"pi", "agent_cli", "Terminal development agent for multi-step code changes.", "Implement and verify bounded software tasks.", "Avoid competing agents or writes outside the approved workspace.", []string{"pi"}, nil, "", ""},
	{"bun", "development", "JavaScript/TypeScript runtime and package manager.", "Build, test and manage JS and TS projects.", "Use existing project lockfiles and verify install scripts before dependency changes.", []string{"bun"}, nil, "", ""},
	{"uv", "development", "Fast Python environment and dependency manager.", "Resolve Python dependencies and run tools in isolated environments.", "Respect existing Python lockfiles and environment policies.", []string{"uv"}, nil, "", ""},
	{"deno", "development", "Secure JavaScript and TypeScript runtime.", "Execute and validate Deno projects with explicit permissions.", "Do not grant network or filesystem permissions without need.", []string{"deno"}, nil, "", ""},
	{"docker", "development", "Container image build and isolated runtime.", "Build and test isolated local development services.", "Inspect Dockerfile, mounts and privileges before launching containers.", []string{"docker"}, nil, "", ""},
	{"gh", "development", "GitHub CLI for PRs, workflows and repository operations.", "Inspect branches, open PRs, review CI and merge when authorized.", "Respect branch protections and verify remote states after mutations.", []string{"gh"}, nil, "", ""},
	{"gcloud", "infrastructure", "Google Cloud resource and deployment management.", "Inspect or operate explicitly authorized Google Cloud projects.", "Verify active account and project; require approval for changes and cost exposure.", []string{"gcloud"}, nil, "", ""},
	{"cloudflared", "infrastructure", "Cloudflare Tunnel network connectivity.", "Set up or inspect authorized secure tunnels.", "Avoid exposing local services or changing public routes without approval.", []string{"cloudflared"}, nil, "", ""},
	{"gitleaks", "security", "Scan Git worktrees and history for leaked credentials.", "Run read-only secret scanning before commits and releases.", "Do not print detected secrets into assistant output or logs.", []string{"gitleaks"}, nil, "", ""},
	{"ffmpeg", "media", "Audio and video processing and transcoding.", "Inspect, transcode or analyze video and sound assets.", "Use bounded local commands and preserve original media.", []string{"ffmpeg"}, nil, "", ""},
}

// resolveActiveCLI locates the currently installed executable; no CLI is
// installed, version-pinned or invoked by catalog discovery.
func resolveActiveCLI(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	paths := []string{
		filepath.Join(home, ".bun", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".cargo", "bin", name),
		filepath.Join(home, ".deno", "bin", name),
		filepath.Join("/opt/homebrew/bin", name),
		filepath.Join("/usr/local/bin", name),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}

func (r *Registry) featuredCapabilities() []featuredCapability {
	skills := discoverSkills(r.capabilityWorkspace())
	providers := listDiscoveredMCPServers(r.capabilityWorkspace())
	result := make([]featuredCapability, 0, len(capabilitySpecs))
	for _, spec := range capabilitySpecs {
		item := featuredCapability{
			ID: spec.id, Category: spec.category, Priority: "premium",
			Description: spec.description, UseFor: spec.useFor,
			SuggestedUse: spec.suggestion, SourceURL: spec.sourceURL,
		}
		for _, binary := range spec.cliNames {
			if item.Executable = resolveActiveCLI(binary); item.Executable != "" {
				break
			}
		}
		item.Installed = item.Executable != ""
		if item.Installed {
			name := cliToolName(item.ID)
			if r.handlers != nil && r.handlers[name] != nil {
				item.CLIAvailable = true
				item.InvocationTool = name
			}
		}
		if item.ID == "rea" {
			item.WorksWith = []string{"replica-square"}
			item.Workflow = []string{"REA target inspection and evidence extraction", "Artifact hashes and evidence ledger", "Replica-Square clean-room synthesis when requested", "Independent parity, accessibility and IP clearance verification"}
		} else if item.ID == "replica-square" {
			item.WorksWith = []string{"rea"}
			item.Workflow = []string{"Consume verified REA evidence ledger", "Design and synthesize independent implementation", "Diff functional behavior and audit for proprietary identifiers"}
		}
		for _, skill := range skills {
			if skill.Name == spec.skillName && spec.skillName != "" {
				item.SkillID = skill.ID
				break
			}
		}
		for _, provider := range providers {
			for _, name := range spec.mcpNames {
				if provider.Name != name {
					continue
				}
				item.MCPConfigured = true
				if r.externalActive[provider.Source+":"+provider.Name] > 0 {
					item.MCPAvailable = true
				}
			}
		}
		switch {
		case item.ID == "xerj" && item.Installed:
			if checkXerjDaemonHealthy(xerjServerURL()) {
				item.Status = "node_healthy"
				item.MCPAvailable = item.MCPAvailable || r.xerjToolCount() > 0
			} else {
				item.Status = "node_unavailable"
				item.MCPAvailable = false
			}
		case item.MCPAvailable:
			item.Status = "mcp_operator_approved"
		case item.CLIAvailable:
			item.Status = "cli_mcp_callable"
		case item.MCPConfigured && item.Installed:
			item.Status = "cli_installed_mcp_not_approved"
		case item.MCPConfigured:
			item.Status = "mcp_configured_not_approved"
		case item.Installed && len(spec.mcpNames) > 0:
			item.Status = "cli_installed_mcp_not_configured"
		case item.Installed:
			item.Status = "cli_installed"
		case len(spec.mcpNames) > 0:
			item.Status = "mcp_not_configured"
		default:
			item.Status = "not_installed"
		}
		result = append(result, item)
	}
	return result
}
