# Desktop Commander Feedback & User Pain Point Analysis

Analysis of public issues, pull requests, and real-world usage patterns across macOS, Linux, and Windows deployments.

## Top Themes

### 1. Process Management & Termination Instability
- **Frequency:** High (18+ issues and PRs addressing orphan processes and signal leaks).
- **Core Complaint:** When commands time out or when an MCP client disconnects, child process trees stay alive in the background, consuming 100% CPU or holding open file locks.
- **Evidence:**
  - Issue #956: File descriptors left open cause EBADF on Windows during sequential file edits.
  - Test suites (`test-terminate-process-tree.js`, `test-signal-process-group.js`) contain hundreds of lines of monkey-patches trying to manage process groups in Node.js.
  - Windows environment variables (`WINDIR`, `SystemRoot`) frequently drop in child processes launched by Node `child_process.spawn`.

### 2. High Memory Footprint & V8 String Limits
- **Frequency:** High (Issue #768, PR #967).
- **Core Complaint:** Large terminal outputs (e.g. build logs, extensive test runs) exceed V8 single-string limits, throwing `RangeError: Invalid string length` and crashing the entire server.
- **Evidence:**
  - `terminal-manager.ts` requires a complex 50MB string eviction algorithm that still blocks the Node event loop during large string allocations.

### 3. Node.js Runtime Dependency & Slow Cold-Start Latency
- **Frequency:** Critical operational friction.
- **Core Complaint:** Forcing a runtime requirement of Node.js >= 22.12.0 breaks installations on standard enterprise systems running Node 18 or 20 LTS. Cold start takes 1.2s to 2.5s before the MCP `initialize` handshake completes.
- **Evidence:**
  - `package.json` specifies `"node": ">=22.12.0"`.
  - Over 50 transitive npm dependencies (`@supabase/supabase-js`, `sharp`, `exceljs`, `pizzip`, `unpdf`, `tiptap`).

### 4. Telemetry and Unwanted Tracking
- **Frequency:** Privacy blocker.
- **Core Complaint:** Users running local AI agents on proprietary codebases do not want telemetry reports (`trackTools.ts`, `track-installation.js`, `device-authenticator.ts`) pinging external endpoints.
- **Evidence:**
  - Original codebase contains dedicated telemetry contracts, rollouts, and device tracking modules.

### 5. Command Parsing & Advisory Security Bypass
- **Frequency:** Correctness defect.
- **Core Complaint:** Command splitting did not treat newlines as separators, missing blocked commands following line breaks.
- **Evidence:**
  - Issue #957: `extractCommands separator set omits newline — advisory blocklist misses commands after a line break`.
