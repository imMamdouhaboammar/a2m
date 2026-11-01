# A2M: Rust & Go Microservices Architecture

## 1. System Overview & Technology Stack

The monolithic Node.js implementation (with 50+ npm dependencies and 52MB asset bloat) is partitioned into high-performance, purpose-built Rust and Go microservices communicating over ultra-low-latency IPC (Unix Domain Sockets on POSIX, Named Pipes on Windows, or direct in-process FFI/shared memory).

```
                      +---------------------------------------+
                      |   AI Client (Claude / Cursor / etc.)  |
                      +-------------------+-------------------+
                                          | JSON-RPC 2.0 (stdio)
                                          v
                      +---------------------------------------+
                      |   dc-gateway (Go Microservice)       |
                      |   - MCP JSON-RPC 2.0 Server           |
                      |   - Process & PTY Lifecycle Supervisor|
                      |   - Session Ring Buffers & Pagination |
                      |   - Host Environment & Signal Control |
                      +--------+---------------------+--------+
                               |                     |
             Unix Domain Socket| (JSON-RPC IPC)      | IPC / Subprocess
                               v                     v
+---------------------------------------+   +---------------------------------------+
|  dc-fs-engine (Rust Microservice)     |   |  dc-doc-service (Go Microservice)     |
|  - Fast Filesystem Operations         |   |  - Native Excel Reader & Range Editor |
|  - Multi-threaded Regex Search Engine |   |    (powered by qax-os/excelize)       |
|  - Surgical edit_block with           |   |  - Native PDF Text & Metadata Parser  |
|    Levenshtein Fuzzy Match Fallback   |   |    (zero Chromium/Puppeteer dependency|
|  - Atomic Write & Line Ending Normalizer  +---------------------------------------+
+---------------------------------------+
```

### Technology Selection Rationale

| Microservice | Language | Primary Libraries | Justification |
| --- | --- | --- | --- |
| `dc-gateway` | Go (1.24+) | `os/exec`, `syscall`, `creack/pty` | Superb concurrent goroutine management, frictionless POSIX/Windows process group handling, robust stdio multiplexing, instant compilation. |
| `dc-fs-engine` | Rust (2021/2024 edition) | `tokio`, `regex`, `ignore`, `similar`, `strsim`, `walkdir`, `serde_json` | Unmatched memory safety, zero-cost abstractions, ripgrep-grade SIMD search throughput, sub-millisecond Levenshtein string fuzzy matching. |
| `dc-doc-service` | Go | `github.com/xuri/excelize/v2`, `ledongthuc/pdf` | `excelize` is the industry benchmark for .xlsx manipulation without COM or Node dependencies. |
| `orchestrator` | Rust / Go | Unified CLI launcher | Single standalone binary that embeds or launches and supervises child microservices transparently. |

## 2. Component Boundaries & IPC Protocol

### Inter-Service Communication (IPC)
- **Transport:** Unix Domain Socket at `/tmp/a2m-ipc.sock` (or Windows Named Pipe `\\.\pipe\a2m-ipc`).
- **Framing:** Line-delimited JSON-RPC 2.0 (`\n`).
- **Gateway Dispatch:**
  - `start_process`, `read_process_output`, `interact_with_process`, `kill_process`, `force_terminate`, `list_sessions`, `list_processes` -> handled locally in `dc-gateway`.
  - `read_file`, `write_file`, `edit_block`, `start_search`, `get_more_search_results`, `stop_search`, `list_directory`, `move_file`, `get_file_info` -> delegated to `dc-fs-engine` via IPC.
  - `excel_*`, `pdf_*` -> delegated to `dc-doc-service`.

## 3. Data Shapes & State Models

### Process Session (`dc-gateway`)
```go
type Session struct {
    PID           int                    `json:"pid"`
    Command       string                 `json:"command"`
    State         ProcessState           `json:"state"` // running, waiting_for_input, completed, terminated, failed
    ExitCode      *int                   `json:"exit_code,omitempty"`
    StartedAt     time.Time              `json:"started_at"`
    CompletedAt   *time.Time             `json:"completed_at,omitempty"`
    Buffer        *CircularOutputBuffer  `json:"-"`
    ReadWatermark int                    `json:"read_watermark"`
    ProcessGroup  int                    `json:"pgid"`
}
```

### Search Session (`dc-fs-engine`)
```rust
pub struct SearchSession {
    pub id: String,
    pub root_path: PathBuf,
    pub pattern: String,
    pub is_content_search: bool,
    pub results: Vec<SearchResultItem>,
    pub is_complete: bool,
    pub outcome: SearchOutcome,
    pub start_time: Instant,
}
```

### Fuzzy Match Replacement Contract (`edit_block`)
```rust
pub struct EditBlockParams {
    pub file_path: PathBuf,
    pub old_string: Option<String>,
    pub new_string: Option<String>,
    pub expected_replacements: usize,
    pub range: Option<String>,
    pub content: Option<serde_json::Value>,
}

pub struct EditBlockResult {
    pub success: bool,
    pub replacements: usize,
    pub diff: String,
    pub similarity: f64,
}
```

## 4. Critical Edge-Case Mitigations (The Parts That Bite)

1. **Process Tree Leaks:**
   - On POSIX, `SysProcAttr: &syscall.SysProcAttr{Setpgid: true}` ensures child is process group leader. `syscall.Kill(-pgid, syscall.SIGKILL)` kills all children unconditionally.
   - On Windows, process is assigned to an OS Job Object configured with `JobObjectExtendedLimitInformation` and `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.
2. **Command Security Bypass on Newlines:**
   - Strict command splitter treats `;`, `&&`, `||`, `|`, `&`, `\n`, and `\r\n` as command boundaries, recursing into subshells `$()` and backticks.
3. **CRLF vs LF Preservation:**
   - `dc-fs-engine` inspects file line ending format prior to edit; replaces newlines in `new_string` with detected native line ending of the target file.
4. **Circular Buffer Eviction:**
   - Output buffer is pre-allocated up to 50MB circular slice. Exceeding bytes drop oldest full lines cleanly without reallocation or string duplication.

## 5. Build and Execution Sequencing

1. **Phase 1: Rust High-Speed FS & Search Engine (`dc-fs-engine`)**
   - Implement file read/write, directory traversal, ripgrep-grade regex search, Levenshtein fuzzy edit_block.
   - Prove with native Rust unit and integration test suite.
2. **Phase 2: Go MCP Gateway & Process Supervisor (`dc-gateway`)**
   - Implement MCP JSON-RPC stdio protocol parser, process runner with PTY/sessions, circular buffers, signal trees.
   - Prove with Go unit and process lifecycle test suite.
3. **Phase 3: IPC Bridge & Document Engine (`dc-doc-service`)**
   - Wire Unix domain socket IPC between Gateway and Engines.
   - Implement Excel and PDF reader/editor tools.
4. **Phase 4: Unified Binary & End-to-End Validation**
   - Provide standalone zero-dependency launcher `a2m`.
   - Run end-to-end integration tests over MCP stdio.
