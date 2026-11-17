# Session Learnings: A2M Tunnel Connection & CLI Symlink Diagnostics

**Date:** 2026-11-17  
**Scope:** A2M Desktop & CLI Gateway  

### 1. Tunnel Client Profile Path Integrity (FL-019)
- **Quote:** `"CHECK mcp_command_executable FAIL stdio MCP executable /Users/.../clever-hypatia/bin/desktop-commander was not found"`
- **Confidence:** high (confirmed by `tunnel-client doctor`)
- **Target:** `failure-lessons/desktop-and-remote-tunneling.md#fl-019`
- **Learning:** When relocating or renaming workspaces, external profile YAMLs (`~/.config/tunnel-client/*.yaml`) and `~/.a2m/config.json` keep legacy paths. Always run `tunnel-client doctor` preflight assertions and capture child process stderr on spawn failures rather than letting the gateway fail silently after a 2.5s port poll timeout.

### 2. Symlink-Aware BASH_SOURCE Dereferencing (FL-020)
- **Quote:** `"/Users/.../.local/bin/a2m: line 24: /Users/.../.local/bin/dc-gateway: No such file or directory"`
- **Confidence:** high (falsified and verified via `readlink` loop)
- **Target:** `failure-lessons/desktop-and-remote-tunneling.md#fl-020`
- **Learning:** Never use `dirname "${BASH_SOURCE[0]}"` directly in scripts meant to be placed on user `$PATH` via symlinks. Always dereference symlinks recursively with `readlink` before establishing `SCRIPT_DIR`.
