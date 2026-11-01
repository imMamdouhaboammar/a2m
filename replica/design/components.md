# Desktop Commander Component Specifications

## 1. Process Supervisor (`dc-gateway::process`)
- **Role:** Spawns, tracks, buffers, and terminates native host processes.
- **Inputs:** `command`, `timeout_ms`, `shell`, `input`.
- **States:**
  - `running`: process spawned and executing.
  - `waiting_for_input`: prompt detected on stdout/stderr, waiting for stdin.
  - `completed`: process exited with code.
  - `terminated`: killed by user signal (SIGTERM/SIGKILL).
- **Invariants:**
  - On Unix, every child is spawned in its own process group (`setpgid(0, 0)`).
  - Termination sends signal to negative PGID (`-PID`) to guarantee whole tree terminates.
  - On Windows, uses Job Objects with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.
  - Buffer strictly bounded by circular ring buffer to prevent out-of-memory errors.

## 2. Fast Filesystem & Search Engine (`dc-fs-engine`)
- **Role:** High-speed file operations, ripgrep-grade content search, and fuzzy diff editing.
- **Capabilities:**
  - `read_file`: paginated text reads with line offset and count limits.
  - `write_file`: atomic temporary file creation + atomic rename with fsync.
  - `edit_block`: exact match search/replace with fallback to sliding window Levenshtein fuzzy match.
  - `start_search`: concurrent directory walk with `.gitignore` awareness, literal and regex modes, context line capture.
- **Invariants:**
  - Line ending preservation: detects CRLF vs LF and matches replacement endings.
  - Occurrence enforcement: matches must equal `expected_replacements`, failing safely if ambiguous.
  - Sandbox confinement: canonical path resolution checks against `allowed_directories`.

## 3. Document Engine (`dc-doc-service`)
- **Role:** High-fidelity spreadsheet and document queries.
- **Capabilities:**
  - `excel_read_sheet`: reads rows and columns from `.xlsx` files without Node.js dependencies.
  - `excel_edit_range`: updates specific cell formulas and values, saving valid XML packages.
  - `pdf_extract_text`: extracts text content and metadata from PDF files natively.

## 4. MCP Transport Bridge (`dc-gateway::mcp`)
- **Role:** Exposes JSON-RPC 2.0 interface compliant with Model Context Protocol (2024-11-05).
- **Transports:** `stdio` (stdin/stdout) for desktop clients, with optional Unix domain socket / SSE server.
- **Error Handling:** Standard JSON-RPC error codes (-32700, -32600, -32601, -32602, -32603) and structured `isError: true` tool responses.
