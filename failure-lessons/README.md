# Failure Lessons: Agent to Machine (A2M)

> **The Core Invariant:**
> *"Pay for an engineering mistake once. After that, the project should remember it."*

This repository maintains a durable, verifiable ledger of real engineering failures, security boundaries, race conditions, and packaging bugs discovered and resolved during the development and evaluation of **Agent to Machine (A2M)**.

---

## 🧭 Navigation & Topics

| Document | Topic Area | Key Invariants Captured |
|---|---|---|
| [`lessons-index.md`](lessons-index.md) | **Master Index** | Complete inventory of all lessons, status, and enforcement rules. |
| [`concurrency-and-lifecycle.md`](concurrency-and-lifecycle.md) | **Concurrency & Process Lifecycle** | Data races on multiplex streams, `cmd.Wait()` reader race, and unsignaled latency stall. |
| [`security-and-trust-boundaries.md`](security-and-trust-boundaries.md) | **Security, Auth & Trust Boundaries** | Permissive CORS RCE, parameter mismatch bypass in `edit_block`, global auth state bleed. |
| [`filesystem-and-packaging.md`](filesystem-and-packaging.md) | **Filesystem, IPC & Native Packaging** | APFS case collision overwriting Mach-O, and shared UDS unlinking DoS. |
| [`desktop-and-remote-tunneling.md`](desktop-and-remote-tunneling.md) | **Desktop GUI & Remote Tunneling** | Decoupled UI state vs live daemon state, remote relay port mismatches, and packaging collisions. |

---

## 📋 Rules We Now Enforce

1. **Deterministic Channel Signaling:** Every synchronization channel awaited with a timeout must be deterministically closed or notified on the primary completion path.
2. **Pipe EOF Synchronization:** Never release command output upon `cmd.Wait()` return without synchronizing on pipe reader completion (`sync.WaitGroup`).
3. **Fail-Closed Boundary Validation:** Admission gates for filesystem or resource access must reject unknown parameter schemas and never fail open on zero-value unmarshaled structs.
4. **No Cross-Session Auth Leakage:** Authenticating a specific session must never mutate process-global authentication state.
5. **No Wildcard CORS on System Endpoints:** Local HTTP daemons capable of executing commands or reading files must never emit `Access-Control-Allow-Origin: *` and must validate `Host` and `Origin` headers.
6. **Case-Insensitive Packaging Hygiene:** Never place build artifacts differing only by letter case in the same bundle directory targeting macOS APFS or Windows NTFS.
7. **User-Scoped Unix Domain Sockets:** Sockets in shared directories like `/tmp` must be user-scoped (`$UID`), chmod `0700`, and checked for active listeners before unlinking.
8. **Authoritative UI State Binding:** Companion desktop management GUIs must probe daemon PIDs and socket reachability on launch and heartbeat; never initialize connection state to an arbitrary hardcoded default.
9. **Single-Contract Tunnel Port Binding:** Remote MCP relays and local service daemons must enforce a shared port configuration schema with preflight socket ownership verification before establishing public tunnels.
10. **Dual-Binding Rebrand Migration:** When rebranding operational services and IPC endpoints, always maintain backward-compatible symlinks and fallback discovery lookups for existing client configurations.

---

## 🔄 Update Trigger

Update this directory whenever:
- A new failure or regression is reproduced and isolated.
- An existing invariant is challenged or refined.
- A new platform packaging or security requirement is codified.
