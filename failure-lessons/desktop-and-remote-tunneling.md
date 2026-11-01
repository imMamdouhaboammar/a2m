# Desktop Application and Remote Tunneling Lessons

---

## FL-009: Decoupled UI State vs. Live Background Daemon State (False Disconnected Illusion)

### Context
In the macOS Native Cocoa GUI (`apps/desktop/macos/main.swift`), an administrative dashboard monitors system health, active processes, and remote MCP connectivity. Simultaneously, background daemons (`tunnel-client` and `a2m-gateway`) can be managed externally or persist across GUI launches.

### What happened
The user launched the newly compiled desktop application (`A2M.app`). The UI rendered its initial state displaying a red "Disconnected" badge and a green "Connect" button. However, in the background, `tunnel-client` (OpenAI A2M tunnel) and `a2m-gateway` were already running and servicing requests. When ChatGPT was prompted, it connected via the active tunnel and executed shell commands (`pwd`, `whoami`, `uname -s`) with full success, despite the desktop application indicating the device was disconnected.

### Observable symptom
The GUI window explicitly claimed status: **Disconnected**, while external AI clients concurrently executed remote shell commands on the workstation and received terminal outputs in real time.

### Impact
Critical security transparency failure. The user operates under the false belief that remote AI access is closed, while the machine remains actively connected and vulnerable to remote execution.

### Incorrect assumption
Assumed that the desktop GUI application was the sole initiator and single source of truth for the tunnel connection, and that defaulting in-memory UI state (`var isConnected = false`) was safe on application startup.

### Root cause
**Confirmed**. The Swift GUI maintained an isolated, ephemeral UI state variable (`isConnected`) that was not initialized by probing the underlying operating system process table, listening sockets, or gateway health endpoints. The background daemons were completely decoupled from the GUI lifecycle.

### Why the architecture allowed it
The system lacked an authoritative bidirectional state reconciliation loop between the desktop presentation layer and the background daemon processes.

### Fix
Implemented authoritative process and socket discovery on launch and via a 2-second heartbeat timer (`checkActiveDaemonState()`):
1. Query the local gateway health endpoint (`GET http://127.0.0.1:8765/status`) or IPC socket (`/tmp/a2m-session.sock`).
2. Check for running tunnel client processes via PID inspection (`pgrep -f "tunnel-client"` or gateway tunnel registry).
3. Reactively update the UI badge ("Connected (External Daemon Active)") and adjust the primary action button to "Disconnect / Terminate".

### Verification
When `A2M.app` launches while background daemons are running, it queries the gateway, detects active connections, and immediately displays "Connected" with live session telemetry, eliminating the split-brain illusion.

### Prevention rule
Never initialize remote access control UI states to hardcoded default values. All connection status indicators must be reactively reconciled against live daemon PIDs, socket reachability, and network transport telemetry before rendering.

### Reusable lesson
Companion desktop dashboards that manage or observe background daemons must treat the operating system process table and IPC sockets as the single source of truth, not their own internal widget state.

### Related code
`apps/desktop/macos/main.swift`, `services/dc-gateway/multiplex/cli.go`, `services/dc-gateway/multiplex/server.go`

### Related tests
`tests/e2e_test.sh`, `services/dc-gateway/multiplex/hub_test.go`

### Related lessons
[`FL-005`](security-and-trust-boundaries.md#fl-005), [`FL-008`](filesystem-and-packaging.md#fl-008)

### Status
**Resolved**

---

## FL-010: Remote MCP Relay Decoupling and Port Mapping Mismatch

### Context
Remote MCP connectivity relies on bridging public reverse tunnels (such as Cloudflare, A2M Relay, or custom WebSocket tunnels) to the local A2M gateway over loopback TCP ports (e.g. 8080 or 8765).

### What happened
When running legacy A2M alongside A2M, tunnel configurations referenced hardcoded port 8080 from configuration profiles (`--profile a2m`), while the native Go gateway was launched on port 8765. In other instances, orphaned Node.js processes occupied port 8080, causing tunnel requests to either fail with connection refused or proxy into stale, unmonitored Node.js runtimes.

### Observable symptom
External AI clients failed to connect, returned `502 Bad Gateway`, or experienced silent drops during tool execution, without any error reported in the native Go gateway logs.

### Impact
Intermittent connectivity failure for remote AI agents, masking whether failures were caused by tunnel authentication, network firewalls, or local daemon crashes.

### Incorrect assumption
Assumed that running a tunnel profile would automatically detect or discover the dynamically assigned port of the native Go gateway.

### Root cause
**Confirmed**. Configuration fragmentation. The tunnel client relied on static profile definitions (`~/.config/tunnel-client/profiles.json`), while the gateway binary accepted arbitrary `--port` flags with no centralized registration or loopback port discovery.

### Why the architecture allowed it
Absence of a shared configuration and discovery manifest (`~/.a2m/config.json`) shared across the CLI, desktop app, gateway, and tunnel client.

### Fix
Created unified CLI orchestration (`a2m connect`):
1. Preflights the target port and verifies ownership via HTTP handshake (`/healthz`).
2. Terminates any orphaned or stale processes on the assigned port.
3. Automatically launches the tunnel client with explicit target port configuration matching the gateway.

### Verification
Running `a2m connect` performs preflight checks, binds the gateway, passes the exact verified port to `tunnel-client`, and validates bidirectional roundtrip communication.

### Prevention rule
Network relays and local service gateways must share a single configuration schema with strict preflight binding assertions. Never run decoupled tunnel clients without asserting the local target port is owned by the intended process.

### Reusable lesson
When wrapping external tunneling utilities, always assert target service identity and port ownership before opening the public relay.

### Related code
`services/dc-gateway/multiplex/cli.go`, `scripts/build_desktop.sh`

### Related tests
`tests/e2e_test.sh`

### Related lessons
[`FL-006`](security-and-trust-boundaries.md#fl-006), [`FL-009`](#fl-009)

### Status
**Resolved**

---

## FL-011: APFS Case-Insensitive Binary Collision in App Bundle Generation

### Context
The macOS packaging pipeline (`scripts/build_desktop.sh`) compiles a native Swift Cocoa application into an app bundle (`apps/desktop/macos/A2M.app`) and bundles command-line utilities into `Contents/MacOS/`.

### What happened
`swiftc` compiled the Cocoa GUI executable into `A2M.app/Contents/MacOS/A2M`. Subsequently, the script attempted to copy the CLI executable `bin/a2m` into the same directory:
`cp bin/a2m apps/desktop/macos/A2M.app/Contents/MacOS/`.
Because macOS APFS is case-preserving but case-insensitive, the lowercase shell wrapper `a2m` overwrote the uppercase Mach-O binary `A2M`. The resulting file was a 508-byte shell script.

### Observable symptom
Executing `open apps/desktop/macos/A2M.app` failed immediately with macOS error:
`LSOpenURLsWithRole() failed with error -10825`.
Inspecting the binary revealed:
`file A2M.app/Contents/MacOS/A2M` -> `POSIX shell script text executable, ASCII text`.

### Impact
Total failure of the native desktop application to launch; corrupt release bundle distributed inside DMG packages.

### Incorrect assumption
Assumed that Unix case-sensitivity applied to macOS filesystem paths, allowing `A2M` (the Cocoa binary) and `a2m` (the CLI wrapper) to coexist in the same directory.

### Root cause
**Confirmed**. Default APFS volumes on macOS are case-insensitive. Files named `A2M` and `a2m` resolve to the exact same filesystem inode.

### Why the architecture allowed it
The packaging script lacked bundle artifact type validation and did not isolate GUI binaries from CLI helper tools.

### Fix
1. Disambiguated the Mach-O binary name to `A2MApp`.
2. Updated `Info.plist`:
   ```xml
   <key>CFBundleExecutable</key>
   <string>A2MApp</string>
   ```
3. Compiled the Swift application with `-o A2MApp`.
4. Placed CLI utilities into `Contents/Resources/bin/a2m` or isolated directories.

### Verification
`scripts/build_desktop.sh` verified:
- `A2MApp` is an 83 KB Mach-O 64-bit executable.
- `codesign --force --deep --sign - A2M.app` succeeded with exit code 0.
- `open apps/desktop/macos/A2M.app` launched the GUI window cleanly.

### Prevention rule
All build and packaging scripts targeting macOS must account for APFS case-insensitivity. Never rely on uppercase vs. lowercase differentiation for files placed in the same directory.

### Reusable lesson
Build systems operating across platforms or packaging for macOS must enforce case-collision linting and verify output file sizes and MIME types post-copy.

### Related code
`scripts/build_desktop.sh`, `apps/desktop/macos/Info.plist`

### Related tests
`tests/e2e_test.sh`

### Related lessons
[`FL-007`](filesystem-and-packaging.md#fl-007)

### Status
**Resolved**
