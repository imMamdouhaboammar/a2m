# A2M Capability Bridge | Implementation and verification

Date: 2026-10-30
Status: built, integration-tested, staged as a signed macOS app; active tunnel worker intentionally not restarted.

## Purpose

A2M is an MCP capability bridge, not an autonomous AI agent. The AI client performs reasoning; A2M exposes existing machine-local capabilities using standard MCP tool definitions and executes only approved operations.

## Shipping behavior

- `capabilities_list`: live inventory of local installed skills, approved code-graph tools, and third-party MCP configuration names.
- `skills_find`: lazy searchable metadata from local `SKILL.md` folders. The tool does not install or execute skills.
- `skills_read`: reads one skill by an opaque root-scoped id. Symlinks and traversal outside the approved skill root are rejected; file size capped at 48 KiB.
- `mcp_servers_list`: names and scope from locally configured MCP servers. No credentials, environment values, command lines, or launch arguments are emitted. Third-party servers remain disabled pending explicit approval.
- `crg_*`: 21 native tools proxied from the installed Code Review Graph MCP service. `repo_root` may select an exact operator-approved repository root; each root gets its own lazily started local process and private Git index. Unapproved roots and `changed_files` traversal/symlink escapes are rejected. Omitted `repo_root` retains the primary workspace.
- `code_graph_refresh`: refreshes the primary workspace by default or the exact approved `repo_root` when supplied (new clients must refresh the MCP tool schema). It does not modify project source files or the real Git index.
- `/api/capabilities`: current sanitized inventory for macOS Settings. Page displays real data and distinguishes discovered from enabled.
- When the native CRG adapter launches successfully, the six unsafe legacy heuristic graph tools are removed from the MCP listing: `index_codebase`, `get_architecture`, `query_symbols`, `trace_call_path`, `assess_impact`, `correlate_error`. The independent ADR tool remains.
- Local gateway stops and disposes of its graph child process on orderly shutdown.

## Workspace policy

The selected project is stored in the local gateway configuration:

```json
{
  "capabilityWorkspace": "/path/to/your/a2m-project",
  "capabilityWorkspaces": [
    "/path/to/your/second-project",
    "/path/to/your/third-project"
  ]
}
```

The A2M Go config manager uses `~/.a2m/config.json`. `capabilityWorkspace` remains the default root; `capabilityWorkspaces` is an **explicit** extra-root allowlist (up to 16 unique existing absolute directories, canonicalized through symlinks). You can set it through the authorized `set_config_value` tool without approving the parent directory. Each requested `repo_root` must match the canonical root of one of these entries exactly. Unknown paths, parents, siblings and symlink escapes remain inaccessible to the graph adapter. Updating the allowlist in a running upgraded gateway takes effect on the next graph request; installing upgraded binaries requires a controlled worker reconnection first.

Use `A2M_WORKSPACE_ROOT` to override only the default root for isolated tests. The extra list does **not** change `allowedDirectories` and is not an authorization to edit any repository. Independent repositories have independent on-demand CRG processes and private indexes. `code_graph_refresh` can target an extra root after clients refresh its tool schema; existing `crg_*` tools already accept `repo_root`.

Code Review Graph 2.3.6 normally performs full builds using Git-tracked files. A2M prepares a **private temporary Git index** containing intent-to-add entries for untracked source files. This index is passed only to the CRG process through `GIT_INDEX_FILE`. The real `.git/index` stays untouched. The private index is rebuilt before each graph refresh to include newly added files.

This implementation deliberately does not run or enable credentialed cloud MCP servers, execute Skill scripts, or fetch/install unknown executables. A future explicit trust/approval workflow is required for third-party MCP passthrough.

## Verification on this Mac

- `go test ./... -count=1`: PASS (gateway, all packages).
- `go vet ./...`: PASS.
- `node --check UI/a2m-desktop-v2/app.js`: PASS.
- MCP subprocess fixture: initialize, tools/list, tools/call, forced repo root, traversal rejection: PASS.
- Private Git index fixture: new/untracked files indexed while original Git index stays unchanged: PASS.
- HTTP `/api/capabilities` test: real JSON response, no raw server secrets: PASS.
- Isolated real Code Review Graph 2.3.6 MCP: 21 read-only tools plus explicit graph refresh present.
- Full build: 62 source files, 508 nodes, 7,625 relationships before graph postprocessing; newly created `mcp/capability.go` included with 11 discovered nodes.
- Signed macOS bundle's gateway on isolated local port: 58 total MCP tools, 21 native CRG tools, no six legacy heuristic tools. Local Settings card served from packaged assets.
- Child process and private temporary index removed after SIGTERM to the final isolated gateway process.

## Deployment state

- New `bin/a2m-gateway` was atomically installed for future worker launches.
- Original binary backed up as `bin/a2m-gateway.pre-capability-20261030`.
- New signed bundle staged at `/tmp/a2m-capability-app.a07Q2l/A2M.app`.
- Preview DMG: `dist/A2M-CapabilityBridge-preview-2026-10-30-v2.dmg`.
- Existing `apps/desktop/macos/A2M.app` and the running Gateway on port 8765 were not replaced.
- Existing tunnel-client and MCP Worker were not stopped. Their tool registry stays as originally loaded until they are reconnected to the new binary.

## Important remaining work

1. Create a deliberate trust approval UI for third-party local MCP servers and their individual tool permissions. Current inventory lists them but does not auto-run them.
2. Adopt newer MCP transport and skills extension negotiation when client compatibility is established. Existing transport remains compatible with current A2M clients.
3. Update/re-register ChatGPT's static plugin tool manifest if that client must show all new native tool schemas. Changes to a live Go MCP endpoint alone cannot rewrite a previously registered plugin manifest.
4. Verify client reconnection after planned maintenance, and test network loss/retry and multiple concurrent coding sessions before promising always-on operation.
5. The active project has an unusually dirty Git tree. No files were staged, reset, committed or overwritten beyond the explicitly documented bridge, UI, generated graph index and separate preview artifacts.

## Safer activation sequence

1. Keep the current tunnel until the user chooses a reconnection window.
2. Quit the old A2M app and stop only its local Gateway on port 8765 after verifying its exact PID. Do **not** stop the unrelated tunnel-client/worker except in a planned client re-pairing step.
3. Open the preview A2M.app. Verify Settings > Capability Bridge, `/api/capabilities`, and MCP tools/list.
4. Reconnect the AI client/worker deliberately to pick up the new `bin/a2m-gateway`. Confirm fresh `tools/list` includes `crg_*`.
5. Confirm no credentialed cloud MCP servers were activated without approval.


## Live deployment and acceptance (2026-10-30)

The updated bundle is now installed in place at `apps/desktop/macos/A2M.app`, with the original bundle preserved at `apps/desktop/macos/A2M.app.pre-capability-20261030`. The original MCP worker binary is preserved as `bin/a2m-gateway.pre-capability-20261030`. The new binary resides at `bin/a2m-gateway`.

- Restarted desktop app and HTTP Gateway. The live local API at `http://127.0.0.1:8765/api/status` reported `connectionState: Connected` with 58 registry tools.
- Performed a controlled restart of the tunnel client and its MCP worker. The replacement worker reconnected to the configured control plane, and remote tool calls resumed. `tunnel-client health --port 8080 --require-control-plane-poll --json` returned OK.
- Launched the installed binary as an independent MCP stdio client acceptance test, not just an HTTP dashboard check. MCP `initialize` and `tools/list` returned 58 tools, including 21 `crg_*` tools, plus `capabilities_list`, `skills_find`, `skills_read`, `mcp_servers_list`, and `code_graph_refresh`.
- Direct `tools/call` to `capabilities_list` and `crg_get_minimal_context_tool` succeeded.
- `skills_find` returned relevant local definitions. The `skills_read` traversal probe `0:../../.ssh` was rejected.
- Nine configured third-party MCP entries remain discovered but disabled; no provider credentials or command configurations were emitted by the list tool.

**Client registration caveat:** A live MCP server can advertise tools dynamically, but the ChatGPT tool definitions already loaded by an existing conversation may remain cached. The current conversation's tool catalog did not refresh to expose the new `crg_*` names, despite the replacement worker and MCP stdio acceptance passing. A fresh ChatGPT session or connector tool-registration refresh is a separate acceptance gate; until it is tested, do not claim native `crg_*` availability inside this particular ChatGPT conversation.

**Operational caveat:** The tunnel process was recovered using an isolated temporary, credential-inheriting restart supervisor. It currently runs; a persistent, secure at-login runtime supervisor is not yet configured. Reboot persistence must be tested separately. Avoid writing the runtime API key into scripts or launchd property lists.

**Release gate:** Code graph and local skill discovery are functional. Third-party MCP execution is intentionally still opt-in; inventory discovery alone is not third-party tool availability. Do not label the complete broad Capability Bridge feature finished until approved third-party servers have a constrained execution adapter and end-to-end client tests.


## Operator-approved external MCP passthrough (implemented after live-client E2E)

The Go Gateway now supports **real stdio JSON-RPC MCP tool passthrough** for installed providers, preserving each provider's native tool input schema and MCP response. Unlike inventory discovery, this actually starts the provider subprocess, performs MCP \`initialize\` and \`tools/list\`, registers the approved tools under names such as \`ext_user_example__tool_name\`, and forwards \`tools/call\`. A real subprocess fixture proves the complete path.

The adapter is intentionally **off by default**. To enable a trusted provider, the machine operator must create \`~/.a2m/mcp-approvals.json\` with filesystem permissions \`0600\`, using a minimal manifest:

\`\`\`json
{
  "servers": [
    {
      "name": "your-configured-server-name",
      "source": "user",
      "tools": ["exact_upstream_tool_name"]
    }
  ]
}
\`\`\`

- \`name\` must match a server already configured in Claude/Cursor MCP configuration, not an executable invented by the AI client.
- \`source\` is \`user\` for user-wide configured servers, or \`project\` for the exact approved A2M workspace's project configuration. Project names from any other project are excluded.
- \`tools\` is an **exact allowlist** of upstream MCP tool names. A server with no approved tools is never started. Tools omitted from this list cannot appear in \`tools/list\` or dispatch.
- Only stdio subprocess MCP configurations using \`command\` and \`args\` are currently supported. HTTP/streamable HTTP/remote MCP endpoints are **not** treated as local executables.
- The process runs with the same macOS user's permissions: this is a tool-exposure boundary, **not** an operating-system sandbox. Treat any approved provider and its command as trusted software. The provider receives only a small environment allowlist plus its explicitly configured \`env\` values; \`CONTROL_PLANE_API_KEY\` and \`DC_AUTH_TOKEN\` are blocked, including if present in provider configuration.
- The process's stderr is not reflected into MCP responses, and stdout is parsed as MCP JSON-RPC. Calls time out; child processes are terminated when the gateway exits.
- No MCP tool permits modifying the approval manifest via the normal \`set_config_value\` API. The operator must alter the file directly and **restart the MCP worker** for changes to the exported schemas to take effect.
- The native Settings page shows live active server/tool counts, and distinguishes discovered/disabled from approved/running.
- A native AI client must re-list/refresh tools after approved-server changes, as its prior tool manifest may be cached.
- The approval file is configuration metadata, not a secret store. Keep provider credentials in the existing provider configuration or the operator's secure credential facilities.

**Verified with local real-process fixture:** one allowed \`echo\` tool appears, retains its schema and successfully handles \`tools/call\`; a second advertised \`forbidden\` tool cannot be invoked; an empty approval manifest launches no provider; group/world-writable approvals fail closed; a user-scoped server cannot satisfy project-scoped approval; and the tunnel control-plane key is not forwarded. These tests do not imply that all nine discovered providers have been individually activated or proven compatible. Those remain disabled until selected by the operator.

### Ongoing limits

- Third-party MCP tool use is native after explicit approval and worker restart; automatic approval or bulk credential-sharing is not supported.
- Local skills are discoverable/readable but are untrusted instructions, not executable MCP tools by themselves.
- The Code Review Graph backend still has no semantic embeddings; its native graph and structural queries work.
- Managed tunnel-client persistence across a macOS reboot still needs an independently verified supervisor.


## Approved multi-repository graph routing — 2026-11-01

**Previously observed failure:** Calling `crg_get_minimal_context_tool` for `/Users/mamdouhaboammar/Documents/funnelscript` returned `repo_root is outside the approved A2M workspace` because the gateway only bound Code Review Graph to `capabilityWorkspace=/Users/mamdouhaboammar/Documents/antigravity/clever-hypatia`. This was the intended old single-root security boundary, not a broken graph index.

**Fix:** Backward-compatible `capabilityWorkspaces` allowlist supplies explicitly approved additional exact roots. The gateway selects a dedicated graph subprocess and its own private Git index for each approved root, lazily on the first read/refresh. No default-root change, wildcard parent approval or global filesystem enablement. `changed_files` inputs continue to reject absolute and traversing paths, with an added missing-target/dangling-symlink ancestor check. The Code Review Graph tool schema still supports `repo_root`; `code_graph_refresh` now also accepts an optional `repo_root` in newly registered MCP schemas. The current `allowedDirectories` filesystem policy is unchanged.

**Operator configuration for this machine:** Preserve `capabilityWorkspace` as the A2M repository and add these exact extra roots (without approving their common parent):
- `/Users/mamdouhaboammar/Documents/funnelscript`
- `/Users/mamdouhaboammar/Documents/funnelscript-research`

**Actual verification:** Config allowlist persisted and rejected duplicates/nonexistent directories; fake MCP subprocess tests exercised both approved roots, cached separate processes, default compatibility, unauthorized path rejection and dangling-symlink rejection; real installed Code Review Graph subprocess successfully built and queried a second *temporary* Git repository via the new exact-root route, and rejected `/etc`. Go tests, vet, Rust tests, document service and JS syntax check passed. The real integration test is opt-in via `A2M_TEST_REAL_GRAPH=1 go test ./mcp -run TestRealGraphBackendSupportsApprovedSecondaryRepository -count=1 -v` to avoid invoking extra external processes during ordinary Go tests.

**Codex Review boundary:** A separate `codex review` failure returned HTTP 401. A local, credential-free `codex login status` check confirmed `Not logged in` on this Mac. This is an external Codex authentication requirement, not something A2M graph routing can or should bypass. The operator must complete `codex login` in an authorized interactive local terminal and confirm `codex login status` before independent AI review can run. A2M Code Review Graph can still supply structural review context without Codex login, but does not replace independent AI review sign-off. Never copy credentials into repository files, chatbot logs or process arguments.

**Activation:** Once the tested new gateway binary is installed, a controlled reconnect of the affected MCP worker is needed to load the updated schema/handlers. An existing ChatGPT session may retain its previous `code_graph_refresh` schema, so new clients may need to refresh their A2M MCP tool registration. Do not kill an active tunnel solely to change a graph allowlist. Indexes may need an explicit first `code_graph_refresh` after a new project is approved.
