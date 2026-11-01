# Historical upstream MCP reconnaissance map

Target: `wonderwhy-er/DesktopCommanderMCP` (npm: `@wonderwhy-er/desktop-commander`, v0.3.1)
Platform: Cross-platform Desktop MCP Server (macOS, Linux, Windows)
Protocol: Model Context Protocol (JSON-RPC 2.0 via stdio)
Core Loop: AI agent requests file manipulation or shell command -> Server executes securely within authorized bounds -> Streams buffered paginated response back to AI client.

## Sources

| Source | Identifier / URL | Key Findings Extracted |
| --- | --- | --- |
| GitHub Repository | `https://github.com/wonderwhy-er/DesktopCommanderMCP` | 27 tools, 150+ tests, custom stdio transport, config manager, session tracking |
| Package Manifest | `package.json` | 50+ npm dependencies, Node.js >=22.12 requirement, 52MB video in repo, telemetry baggage |
| Tool Schemas | `src/tools/schemas.ts` | Complete Zod definitions for process, file, search, edit, doc, and config tools |
| Terminal Manager | `src/terminal-manager.ts` | Process tree termination, ring buffer output capping (50MB), line eviction, tail reading |
| Edit Block Engine | `src/tools/edit.ts` | Fuzzy matching (Levenshtein ratio >=0.7), line ending normalization, occurrence enforcement |
| Search Manager | `src/search-manager.ts` | Ripgrep wrapper with streaming buffer, pagination, timeout enforcement, session storage |
| Public Issues & PRs | Issues #956, #957, PR #967 | Windows `WINDIR` environment bug, newline separator bypass in `extractCommands`, EBADF leaks |

## Tool Inventory (MCP Interface Surface)

| Tool Identifier | Category | Input Schema Summary | Return Shape | Concurrency / State |
| --- | --- | --- | --- | --- |
| `start_process` | Process | `command` (string), `timeout_ms` (number), `shell`? (string) | `{ pid, output, processState, waitCappedAtMs }` | Spawns child process, stores in session registry |
| `read_process_output` | Process | `pid` (number), `offset`? (number), `length`? (number) | `{ output, hasMore, nextOffset, processState }` | Reads from session ring-buffer with pagination |
| `interact_with_process` | Process | `pid` (number), `input` (string), `timeout_ms`? (number) | `{ output, processState }` | Writes to process stdin, waits for prompt/tail |
| `force_terminate` | Process | `pid` (number) | `{ success: boolean, message: string }` | Sends SIGKILL / process tree termination |
| `kill_process` | Process | `pid` (number > 0) | `{ success: boolean, message: string }` | Sends SIGTERM / graceful termination |
| `list_processes` | Process | None | `{ processes: [{ pid, name, cpu, memory, cmd }] }` | System snapshot of running processes |
| `list_sessions` | Process | None | `{ sessions: [{ pid, command, status, runtime }] }` | Active & completed MCP process sessions |
| `read_file` | Filesystem | `path` (string), `offset`? (number), `length`? (number) | `{ content, totalLines, hasMore, nextOffset }` | Line-bounded text read with offset pagination |
| `read_multiple_files` | Filesystem | `paths` (string[]) | `{ files: [{ path, content, error? }] }` | Batch file reader |
| `write_file` | Filesystem | `path` (string), `content` (string), `mode` ("rewrite" \| "append") | `{ success: boolean, bytesWritten: number }` | Atomic write via temp file + rename |
| `edit_block` | Filesystem | `file_path` (string), `old_string`? (string), `new_string`? (string), `expected_replacements`? (number) | `{ success: boolean, replacements: number, diff: string }` | Exact or fuzzy Levenshtein match replacement |
| `create_directory` | Filesystem | `path` (string) | `{ success: boolean, path: string }` | Recursive directory creation |
| `list_directory` | Filesystem | `path` (string), `depth`? (number) | `{ entries: [{ name, path, isDirectory, size, mtime }] }` | Bounded recursive directory listing |
| `move_file` | Filesystem | `source` (string), `destination` (string) | `{ success: boolean, source: string, destination: string }` | Safe filesystem move/rename |
| `get_file_info` | Filesystem | `path` (string) | `{ size, isDirectory, isFile, permissions, mtime, mimeType }` | File metadata & MIME detection |
| `start_search` | Search | `path` (string), `pattern` (string), `searchType` ("files" \| "content"), `filePattern`?, `ignoreCase`?, `maxResults`? | `{ sessionId, totalMatches, results, isComplete }` | Spawns search worker, returns initial page |
| `get_more_search_results` | Search | `sessionId` (string), `offset`? (number), `length`? (number) | `{ results, totalMatches, hasMore, isComplete }` | Paginated cursor into search session results |
| `stop_search` | Search | `sessionId` (string) | `{ success: boolean, sessionId: string }` | Cancels in-flight search task |
| `list_searches` | Search | None | `{ searches: [{ sessionId, path, pattern, status }] }` | Active search registry |
| `get_config` | Config | `origin`? ("ui" \| "llm") | `{ config: Record<string, any> }` | Current server configuration |
| `set_config_value` | Config | `key` (string), `value` (string \| number \| boolean \| array \| null) | `{ success: boolean, key: string, value: any }` | Persisted config update |
| `get_usage_stats` | Metrics | None | `{ toolCalls: Record<string, number>, uptimeSeconds: number }` | Operational metrics |
| `get_recent_tool_calls` | History | `maxResults`? (number), `toolName`? (string) | `{ calls: [{ toolName, timestamp, status }] }` | In-memory audit ring-buffer |

## User Execution Flows

### F01: Long-Running Terminal Command Execution
1. AI issues `start_process` with command: `cargo test --all` and timeout: `10000`.
2. Server spawns process under session registry, capturing stdout/stderr into ring buffer.
3. If command finishes within initial wait window (e.g. 2s), output is returned immediately with `processState: "completed"`.
4. If still running, server returns `pid` and `processState: "running"` with initial output.
5. AI calls `read_process_output` with `pid: 1234` and `offset: 0` to retrieve incremental chunks.
6. When done or hanging, AI calls `force_terminate` with `pid: 1234`.

### F02: Surgical File Modification (`edit_block`)
1. AI reads file slice with `read_file`.
2. AI issues `edit_block` with `file_path`, `old_string`, `new_string`, and `expected_replacements: 1`.
3. Server loads file, normalizes line endings, scans for exact match.
4. If exact match found with count == expected_replacements, performs atomic write.
5. If exact match fails, runs sliding window fuzzy search calculating Levenshtein similarity ratio.
6. If similarity >= 0.70 and unique best match exists, applies replacement and returns diff.
7. If match ambiguous or similarity < 0.70, returns actionable failure explaining mismatched character codes.

### F03: Ripgrep-Grade Codebase Search
1. AI issues `start_search` with `path: "."`, `pattern: "struct Session"`, `searchType: "content"`.
2. Server dispatches search engine; streams results into session storage.
3. Returns initial 100 matches + context lines and a `sessionId`.
4. AI queries `get_more_search_results` with `sessionId` and pagination offset.
5. Once satisfied, session automatically expires or is explicitly closed via `stop_search`.

## Inferred Domain Model

```
Session
  id: string / int (pid)
  command: string
  status: Running | Completed | Terminated | Failed
  exit_code: Option<i32>
  started_at: DateTime
  buffer: RingBuffer<Line>
  last_read_watermark: usize

SearchSession
  id: Uuid
  root_path: PathBuf
  pattern: Regex
  search_type: Files | Content
  results: Vec<SearchResult>
  is_complete: bool
  outcome: Completed | TimedOut | MaxResults | Stopped | Failed

ServerConfig
  allowed_directories: Vec<PathBuf>
  blocked_commands: Vec<string>
  command_timeout_ms: u64
  file_read_line_limit: usize
  max_search_results: usize
  shell: Option<string>
```
