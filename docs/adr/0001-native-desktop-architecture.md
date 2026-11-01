# ADR 0001: Native Desktop Architecture & Security Model

## Status
Accepted

## Context
A2M requires a lightweight, security-sensitive desktop application for macOS and Windows. The solution must:
1. Provide a native GUI with one-click connect/disconnect for AI clients (ChatGPT, Claude, Cursor, and MCP clients).
2. Display a live terminal monitor with streaming stdout/stderr, process state tracking, interactive stdin input, and an emergency kill switch.
3. Run 100% offline-first locally, with zero telemetry and no vendor lock-in or artificial monthly call caps.
4. Seamlessly supervise the existing Go MCP gateway (`dc-gateway`), Rust filesystem/search engine (`dc-fs-engine`), and Go document service (`dc-doc-service`).
5. Support secure outbound tunneling (via OpenAI's `tunnel-client` or private SSE relay) without requiring users to expose unauthenticated inbound ports to the public internet.

## Framework Evaluation

| Criteria | Wry / Native Cocoa WebKit (Rust) | Wails v2 (Go) | Tauri v2 (Rust + Webview) | Electron |
| --- | --- | --- | --- | --- |
| Memory Footprint | ~15 MB | ~25 MB | ~20 MB | 120-200 MB |
| Cold Startup | < 50 ms | < 80 ms | < 70 ms | 1.5 - 3.0 s |
| Binary Overhead | ~8 MB | ~12 MB | ~10 MB | > 100 MB |
| Sidecar Integration | Direct Rust FFI / Subprocess | Native Go integration | Native Rust sidecar manager | Node child_process |
| System Tray / Menu Bar | Native NSStatusItem | Native Wails Tray | Native Tray API | Electron Tray |
| Maintenance & Anti-Slop | Minimal code, maximum control | Minimal Go code | Standard desktop toolkit | Heavy npm dependency sprawl |

## Decision
1. **Core Microservices Execution Model:** Retain the dual Rust (`dc-fs-engine`) and Go (`dc-gateway`, `dc-doc-service`) microservices communicating over loopback Unix domain sockets and memory-safe IPC.
2. **Desktop Controller Architecture:**
   - The desktop controller manages sidecar lifecycle, daemon status, and process supervision.
   - For immediate native execution without bloated dependencies, implement the desktop management daemon and live webview bridge, with native Cocoa/WebKit window bindings and System Tray integration.
3. **Security Invariants:**
   - Local HTTP/SSE management endpoints must bind exclusively to `127.0.0.1` (loopback) by default.
   - Per-session authentication tokens and RBAC contexts replace process-global authentication booleans.
   - `allowedDirectories` configuration is strictly enforced at the gateway admission gate before dispatching to filesystem or document engines.
   - Process supervision must be completely data-race free under `go test -race`.
   - Command blocking is enforced recursively across subshells and newline-separated command strings.
