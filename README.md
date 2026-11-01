<p align="center">
  <img src="docs/assets/a2m-mark.svg" alt="A2M — AI connected to a computer terminal" width="330" />
</p>

# A2M — Agent to Machine

A2M (Agent to Machine) is a local-first MCP bridge built with Rust and Go microservices and a native macOS desktop application. It lets supported AI clients interact with authorized terminal processes, files and developer workflows.

**New to A2M?** Follow the [visual setup and ChatGPT connection guide](docs/ONBOARDING.md) — compact clickable screenshots walk through tunnel setup, device connection, and a successful A2M response.

## Architecture

```
                      +---------------------------------------+
                      |   AI Client (Claude / Cursor / etc.)  |
                      +-------------------+-------------------+
                                          | JSON-RPC 2.0 (stdio)
                                          v
                      +---------------------------------------+
                      |   dc-gateway (Go Microservice)       |
                      |   - MCP JSON-RPC 2.0 Protocol Engine  |
                      |   - Process & PTY Lifecycle Supervisor|
                      |   - Memory-Safe Ring Buffer Pagination|
                      |   - POSIX/Win32 Process Group Control |
                      +--------+---------------------+--------+
                               |                     |
             Unix Domain Socket| (JSON-RPC IPC)      | Local Process Invocation
                               v                     v
+---------------------------------------+   +---------------------------------------+
|  dc-fs-engine (Rust Microservice)     |   |  dc-doc-service (Go Microservice)     |
|  - High-Speed Filesystem Operations   |   |  - Native Excel Reader & Range Editor |
|  - Multi-threaded Regex Search Engine |   |    (qax-os/excelize v2.11)            |
|  - Surgical edit_block with           |   |  - Native PDF Text & Metadata Parser  |
|    Levenshtein Fuzzy Match Fallback   |   |    (zero Chromium/Puppeteer dependency|
|  - Atomic Write & CRLF/LF Normalizer  |   +---------------------------------------+
|  - Multi-Language AST Symbol Indexer  |
|  - Embedded SQLite Knowledge Graph    |
|  - Impact & Blast Radius Evaluator    |
|  - Persistent ADR Governance Engine   |
+---------------------------------------+
```

## Performance & Overhead Comparison (Empirically Measured)

Tested on macOS Apple Silicon arm64 (see [docs/benchmarks/native-vs-original.md](docs/benchmarks/native-vs-original.md)):

| Metric | Reference Node.js/Electron tool | A2M Native (Rust & Go) |
| --- | --- | --- |
| Cold Startup Latency | 1,200 ms – 2,500 ms | **55 ms** |
| Command Roundtrip Latency | 2,050 ms | **6 ms** |
| Idle Memory Consumption | 85 MB – 140 MB | **10.11 MB** |
| Desktop App Binary Size | > 150 MB (Electron runtime) | **111 KB** (Native Cocoa Mach-O) |
| Standalone DMG Installer | > 200 MB | **11 MB** (Self-contained) |
| Process Tree Termination | Flaky Node signals; zombie leaks | Native OS Process Groups (`killpg`) |
| Max Output Buffering | Crashes with V8 `RangeError` (>50MB string) | Circular ring buffer with zero-copy eviction |
| External Telemetry / Quotas | Cloud relay, 10,000 call limit, Paywall | **100% Offline-First. Zero telemetry. Unlimited** |

## Desktop Application (macOS)

The native desktop application (`A2M.app`) provides a complete graphical interface:
- **One-Click Connect / Disconnect:** Connects the local machine to the AI assistant using OpenAI's official `tunnel-client` or private SSE transport.
- **Live Terminal Monitor:** Displays real-time streaming output (`stdout`/`stderr`) of everything the AI runs with search, auto-scroll, ANSI colors, and output export.
- **Interactive Control:** Allows sending input (`stdin`) to prompts or triggering immediate emergency process termination (`Kill Process`).
- **System Tray (Menu Bar):** Quick-status indicator and controls in the macOS menu bar.

Install via DMG:
```bash
open dist/A2M-macos-arm64.dmg
```
Or launch directly:
```bash
open apps/desktop/macos/A2M.app
```

## Build & Test

```bash
# Build all microservices
make build

# Run unit tests across Rust and Go test suites
make test

# Run full end-to-end integration test over stdio
make e2e
```

## Client Configuration

### Claude Desktop (`claude_desktop_config.json`)

```json
{
  "mcpServers": {
    "a2m": {
      "command": "/absolute/path/to/clever-hypatia/bin/a2m"
    }
  }
}
```

### Cursor (`~/.cursor/mcp.json`)

```json
{
  "mcpServers": {
    "a2m": {
      "command": "/absolute/path/to/clever-hypatia/bin/a2m"
    }
  }
}
```

### Tunnel profile names

A2M uses `a2m` and `a2m-2` as the default tunnel-client profile names. If you already have a tunnel-client profile with another name, set `A2M_TUNNEL_PROFILE_1` or `A2M_TUNNEL_PROFILE_2` in your local, untracked environment file; **do not rename or delete an active tunnel profile automatically**. New local configuration and authorization data use the `~/.a2m/` directory. Verify your existing settings after upgrading.

## Tool Catalog

| Tool | Service | Description |
| --- | --- | --- |
| `start_process` | `dc-gateway` | Execute command or launch long-running process with output streaming |
| `read_process_output` | `dc-gateway` | Read process output buffer with offset, length, and tail pagination |
| `interact_with_process` | `dc-gateway` | Write to process stdin and wait for prompt response |
| `kill_process` | `dc-gateway` | Graceful SIGTERM process termination |
| `force_terminate` | `dc-gateway` | Forceful SIGKILL process tree termination |
| `list_sessions` | `dc-gateway` | List active and historical process execution sessions |
| `list_processes` | `dc-gateway` | Host system process snapshot |
| `read_file` | `dc-fs-engine` | Line-bounded paginated file reader |
| `read_multiple_files` | `dc-fs-engine` | Batch file reader |
| `write_file` | `dc-fs-engine` | Atomic file writer with rewrite and append modes |
| `edit_block` | `dc-fs-engine` | Surgical string replacement with Levenshtein fuzzy match fallback |
| `create_directory` | `dc-fs-engine` | Recursive directory creation |
| `list_directory` | `dc-fs-engine` | Bounded directory traversal with metadata |
| `move_file` | `dc-fs-engine` | Safe file move and rename |
| `get_file_info` | `dc-fs-engine` | File metadata, permissions, and MIME type |
| `start_search` | `dc-fs-engine` | Concurrent ripgrep-grade regex search for files or content |
| `get_more_search_results` | `dc-fs-engine` | Cursor-based search session pagination |
| `stop_search` | `dc-fs-engine` | Early cancellation of active search session |
| `list_searches` | `dc-fs-engine` | Registry of active search sessions |
| `get_config` | `dc-gateway` | Retrieve server configuration |
| `set_config_value` | `dc-gateway` | Persist server configuration updates |
| `get_usage_stats` | `dc-gateway` | Operational uptime and tool execution metrics |
| `get_recent_tool_calls` | `dc-gateway` | Recent tool execution audit ring buffer |
| `excel_read_sheet` | `dc-doc-service` | Read cell matrix or range from `.xlsx` workbook |
| `excel_list_sheets` | `dc-doc-service` | List worksheet names in `.xlsx` workbook |
| `excel_edit_range` | `dc-doc-service` | Modify cells or ranges in `.xlsx` worksheets |
| `docx_read` | `dc-doc-service` | Extract body text, headers, and footers from `.docx` files |
| `docx_edit` | `dc-doc-service` | Surgical find-and-replace in `.docx` body, headers, or footers |
| `pdf_extract_text` | `dc-doc-service` | Native text and metadata extraction from PDF files |
| `auth_status` | `dc-gateway` | Inspect active MCP authorization status, role, and capabilities |
| `auth_login` | `dc-gateway` | Authenticate session using security token (`DC_AUTH_TOKEN`) |
| `index_codebase` | `dc-fs-engine` | Index workspace or directory into the persistent AST knowledge graph |
| `get_architecture` | `dc-fs-engine` | High-level project architecture, language breakdown, entrypoints, and core symbols |
| `query_symbols` | `dc-fs-engine` | Search code symbols (functions, structs, classes, interfaces, enums) with sub-millisecond precision |
| `trace_call_path` | `dc-fs-engine` | Trace call chains and execution paths between source and target symbols |
| `assess_impact` | `dc-fs-engine` | Evaluate blast radius, callers, and regression risk before modifying code |
| `manage_adr` | `dc-fs-engine` | Manage Architecture Decision Records (ADRs) to preserve design decisions and governance |
| `correlate_error` | `dc-gateway` | Correlate compiler errors or test failure stack traces with AST nodes and dependent callers |

## Security & Access Control (RBAC)

A2M includes a 100% offline-first security gateway with constant-time token verification (`crypto/subtle`) and role-based capability gating:

* **Environment Configuration:**
  * `DC_AUTH_TOKEN`: Sets the expected pre-shared security token.
  * `DC_REQUIRE_AUTH=true`: Enforces mandatory authentication for all MCP tool calls.
  * `DC_AUTH_ROLE`: Sets the access role:
    * `admin` (default): Unrestricted access to all 31 tools.
    * `workspace`: Permits all filesystem operations and standard developer commands; blocks destructive administrative tools (`force_terminate`, `set_config_value`).
    * `read_only`: Prohibits all mutating operations (`write_file`, `edit_block`, `start_process`, `kill_process`); permits only read and search tools.

## Multi-Party Multiplexed Bridge (Human + AI Dual-Terminal)

A2M natively bridges the AI client and the human operator through zero-overhead local IPC sockets and Server-Sent Events (SSE). While an AI assistant is running tasks, the human developer can observe and interact with live processes from any terminal or browser window:

### 1. Terminal CLI Multi-Party Commands

```bash
# List active and recent processes launched by the AI agent
a2m ps

# Stream real-time colored stdout and stderr from all AI processes
a2m watch

# Attach directly to a running AI process; watch output and send stdin from your keyboard
a2m attach <PID>
```

### 2. Standing HTTP / SSE Server & Web Dashboard

Launch A2M as a standing background daemon providing both MCP SSE transport and an interactive browser dashboard:

```bash
a2m server --port 8765
```

* **Live Web Dashboard:** Open `http://127.0.0.1:8765` in your browser to inspect running processes, view real-time log streams, inject terminal input, and terminate tasks.
* **SSE MCP Transport:** Connect remote or local AI clients via HTTP Server-Sent Events at `http://127.0.0.1:8765/sse` (messages posted to `/message?sessionId=...`).
* **REST Management API:** Programmatic endpoints for external dashboards and scripts:
  * `GET /api/sessions`: Returns active process sessions.
  * `GET /api/stream`: Real-time SSE event stream for stdout/stderr/exits.
  * `POST /api/stdin`: Send interactive text into a process stdin (`{"pid": 1234, "input": "y\n"}`).
  * `POST /api/kill`: Send SIGTERM to a running process (`{"pid": 1234}`).
