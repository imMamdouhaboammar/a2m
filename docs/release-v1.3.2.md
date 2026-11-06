# A2M v1.3.2 Release Notes & Architecture Guide

Release Date: 2026-11-06  
Release Tag: `v1.3.2`  
License: MIT  

---

## Highlights in v1.3.2

A2M (Agent to Machine) v1.3.2 brings automated sidecar daemon lifecycle management, enriched security and machine-access visibility, dynamic directory authorization, operator binary overrides, and desktop UI reliability enhancements.

### 1. Automated Rust Filesystem Daemon Lifecycle
- **Self-Healing IPC Startup (`EnsureRunning`)**: The Go gateway (`a2m-gateway`) automatically detects if the background Rust Filesystem & Search Engine (`a2m-fs` / `dc-fs-engine`) is running over the Unix socket (`/tmp/a2m-ipc.sock`) or TCP port. If absent, it automatically spawns the daemon with captured diagnostics.
- **Graceful Process Shutdown**: Orderly SIGINT/SIGTERM termination handlers in the gateway ensure the child filesystem engine process is reaped cleanly, avoiding orphaned background workers and socket lock contention.
- **Race-Free Daemon Spawning**: Stderr output during daemon launch is synchronized through a concurrency-safe thread buffer (`safeBuffer`), preventing data races during dial timeouts.

### 2. Enriched Authorization & Machine-Access Status (`auth_status`)
- Extended `auth_status` tool response with machine-readable access indicators:
  - `machine_access_enabled`: Indicates whether machine access, terminal execution, and file operations are active.
  - `remote_access`: Reports active remote connectivity status.
  - `authRequired`: Flags whether token authentication is required by server configuration.
  - `status_message`: Context-aware guidance tailored to the active role (`admin`, `workspace`, or `read_only`).
- Unauthenticated sessions receive actionable instructions on how to call `auth_login` with `DC_AUTH_TOKEN`.
- Environments with disabled authentication (`DC_REQUIRE_AUTH=false`) automatically grant machine access without prompting for unnecessary tokens.

### 3. Dynamic Scoping & Operator Binary Overrides
- **Dynamic Allowed Directories (`A2M_ALLOWED_DIRECTORIES`)**: Operators can supply additional colon-delimited allowed paths via the environment variable; duplicate paths are automatically deduplicated against stored configuration.
- **Explicit Sidecar Paths**: Operators can specify exact binaries using `A2M_FS_BINARY` and `A2M_DOC_BINARY`, bypassing default lookups while enforcing regular-file executable permissions.
- **Symlink-Aware Path Resolution**: Executable discovery evaluates symlink targets to resolve binaries correctly across brew, package managers, and macOS `.app` bundle directories.

### 4. Desktop UI Status & Binary Resolution
- **macOS Desktop Launcher (`A2M.app`)**: Resolves both `a2m-gateway` and legacy `dc-gateway` binaries inside bundle resources, local `bin/`, and Homebrew paths.
- **Dashboard Service Health**: The desktop web dashboard (`UI/a2m-desktop-v2/app.js`) recognizes `installed (on-demand)` and `ready` states alongside `running`, `healthy`, and `reachable` as healthy indicators.

---

## Capability Catalog & MCP Tool Matrix

| Tool | Engine | Role Boundary | Description |
|---|---|---|---|
| `start_process` | `a2m-gateway` | `workspace`, `admin` | Launch long-running or interactive shell commands with streaming output |
| `read_process_output` | `a2m-gateway` | `read_only`, `workspace`, `admin` | Read process output buffer with offset, length, and tail pagination |
| `interact_with_process` | `a2m-gateway` | `workspace`, `admin` | Write to process stdin and receive interactive prompt response |
| `kill_process` | `a2m-gateway` | `workspace`, `admin` | Graceful SIGTERM process termination |
| `force_terminate` | `a2m-gateway` | `admin` | Forceful SIGKILL process group tree termination |
| `list_sessions` | `a2m-gateway` | `read_only`, `workspace`, `admin` | List active and historical process execution sessions |
| `list_processes` | `a2m-gateway` | `read_only`, `workspace`, `admin` | Host system process snapshot |
| `read_file` | `a2m-fs` | `read_only`, `workspace`, `admin` | Line-bounded paginated file reader |
| `read_multiple_files` | `a2m-fs` | `read_only`, `workspace`, `admin` | Batch file reader |
| `write_file` | `a2m-fs` | `workspace`, `admin` | Atomic file writer with rewrite and append modes |
| `edit_block` | `a2m-fs` | `workspace`, `admin` | Surgical string replacement with Levenshtein fuzzy match fallback |
| `create_directory` | `a2m-fs` | `workspace`, `admin` | Recursive directory creation |
| `list_directory` | `a2m-fs` | `read_only`, `workspace`, `admin` | Bounded directory traversal with metadata |
| `move_file` | `a2m-fs` | `workspace`, `admin` | Safe file move and rename |
| `get_file_info` | `a2m-fs` | `read_only`, `workspace`, `admin` | File metadata, permissions, and MIME type |
| `start_search` | `a2m-fs` | `read_only`, `workspace`, `admin` | Concurrent ripgrep-grade regex search for files or content |
| `get_more_search_results` | `a2m-fs` | `read_only`, `workspace`, `admin` | Cursor-based search session pagination |
| `stop_search` | `a2m-fs` | `read_only`, `workspace`, `admin` | Early cancellation of active search session |
| `list_searches` | `a2m-fs` | `read_only`, `workspace`, `admin` | Registry of active search sessions |
| `excel_read_sheet` | `a2m-docs` | `read_only`, `workspace`, `admin` | Read cell matrix or range from `.xlsx` workbook |
| `excel_list_sheets` | `a2m-docs` | `read_only`, `workspace`, `admin` | List worksheet names in `.xlsx` workbook |
| `excel_edit_range` | `a2m-docs` | `workspace`, `admin` | Modify cells or ranges in `.xlsx` worksheets |
| `docx_read` | `a2m-docs` | `read_only`, `workspace`, `admin` | Extract body text, headers, and footers from `.docx` files |
| `docx_edit` | `a2m-docs` | `workspace`, `admin` | Surgical find-and-replace in `.docx` documents |
| `pdf_extract_text` | `a2m-docs` | `read_only`, `workspace`, `admin` | Native text and metadata extraction from PDF files |
| `auth_status` | `a2m-gateway` | Public | Inspect active MCP authorization status, role, and capabilities |
| `auth_login` | `a2m-gateway` | Public | Authenticate session using security token (`DC_AUTH_TOKEN`) |
| `capabilities_list` | `a2m-gateway` | `read_only`, `workspace`, `admin` | Live inventory of local installed skills and capability tools |
| `skills_find` | `a2m-gateway` | `read_only`, `workspace`, `admin` | Search metadata from local `SKILL.md` skill definitions |
| `skills_read` | `a2m-gateway` | `read_only`, `workspace`, `admin` | Read bounded, root-scoped skill instructions |
| `mcp_servers_list` | `a2m-gateway` | `read_only`, `workspace`, `admin` | List names and scopes of configured external MCP servers |

---

## Installation & Upgrading

### macOS Application (Apple Silicon arm64)

1. Download `A2M-macos-arm64.dmg` from the release assets.
2. Open the DMG and drag **A2M** to your `/Applications` directory.
3. Launch A2M from Applications or Spotlight.

### CLI Package (Darwin arm64)

```bash
# Extract the binary release archive
tar -xzf a2m-darwin-arm64.tar.gz
cd a2m-darwin-arm64

# Run A2M directly or link to your PATH
./bin/a2m status
```

### Verification & Checksums

Compare downloaded assets against `checksums.txt`:

```bash
shasum -a 256 -c checksums.txt
```
