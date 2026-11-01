# Filesystem, IPC & Native Packaging Lessons

---

## FL-007: APFS Case-Insensitive Binary Collision in App Bundle

### Context
In `scripts/build_desktop.sh`, the packaging script compiles the native macOS Cocoa launcher (`main.swift`) into `A2M.app/Contents/MacOS/A2M` and bundles helper binaries alongside it.

### What happened
Attempting to launch `A2M.app` via `open apps/desktop/macos/A2M.app` failed with error `-10825`. Inspecting `A2M.app/Contents/MacOS/A2M` revealed that the compiled Mach-O binary (83 KB) had been overwritten by a 508-byte bash wrapper script!

### Observable symptom
```text
_LSOpenURLsWithCompletionHandler() failed with error -10825.
$ ls -lh A2M.app/Contents/MacOS/
-rwxr-xr-x  1 user  staff   508B Oct 30 12:52 A2M
```

### Impact
The macOS desktop application was rendered unexecutable and failed to launch on any Apple Silicon system.

### Incorrect assumption
Assumed that outputting compiled binary `A2M` (uppercase) and copying CLI script `a2m` (lowercase) into the same directory would keep two separate files on the filesystem.

### Root cause
**Confirmed**. The default Apple File System (APFS) and Windows NTFS are case-preserving but **case-insensitive**. Writing `cp bin/a2m` into `Contents/MacOS/` clobbered `A2M` without generating a warning or error.

### Why the architecture allowed it
The build script used overlapping names between the native desktop binary and the CLI wrapper script without namespace separation.

### Fix
Disambiguated names:
1. Named the native Cocoa binary `A2MApp` and updated `CFBundleExecutable` in `Info.plist` to `A2MApp`.
2. Kept `a2m` as the CLI script.
3. Added ad-hoc code signing (`codesign --force --deep --sign - A2M.app`).

### Verification
`open apps/desktop/macos/A2M.app` and direct execution verified successful launch, clean dock presence, and tray menu integration.

### Prevention rule
Never co-locate build artifacts that differ only by character casing in the same directory on case-insensitive filesystems (macOS APFS / Windows NTFS).

### Related code
`scripts/build_desktop.sh`, `apps/desktop/macos/main.swift`, `apps/desktop/macos/A2M.app/Contents/Info.plist`

### Related tests
`scripts/build_desktop.sh` verification step

### Related lessons
[`FL-008`](#fl-008)

### Status
**Resolved**

---

## FL-008: Shared Unix Domain Socket Overwrite & DoS Across Instances

### Context
`services/dc-gateway/multiplex/hub.go` binds a Unix Domain Socket to provide local IPC communication between the gateway daemon and CLI utilities (`a2m ps`, `a2m watch`, `a2m attach`).

### What happened
Running `tests/e2e_test.sh` while the gateway daemon was active caused subsequent CLI commands (`a2m ps`) to fail with `dial unix /tmp/a2m-session.sock: connect: no such file or directory` or `connection refused`.

### Observable symptom
```text
Error: could not connect to A2M control socket at /tmp/a2m-session.sock (unix). Is A2M running? (dial unix /tmp/a2m-session.sock: connect: connection refused)
```

### Impact
Denial of Service for local CLI monitoring and control: running any test or secondary CLI instance destroyed the IPC socket of the primary daemon.

### Incorrect assumption
Assumed that a hardcoded global path (`/tmp/a2m-session.sock`) was acceptable and that calling `os.Remove(addr)` on startup was harmless.

### Root cause
**Confirmed**. In `hub.Start()`, `os.Remove(addr)` was called unconditionally before `net.Listen("unix", addr)`. When a short-lived test instance started, it unlinked the active daemon's socket inode from `/tmp`. When the test exited, the socket was permanently gone. Furthermore, sockets had default `umask` permissions without restricting access to the user.

### Why the architecture allowed it
IPC socket lifecycle was treated as an afterthought without ownership isolation or mutex fencing.

### Fix
1. Updated socket path to `/tmp/a2m-session.sock` (with user UID support).
2. Applied `os.Chmod(addr, 0700)` immediately after binding to restrict socket access to the current OS user.
3. Maintained a symlink for legacy compatibility without deleting active listener sockets.

### Verification
Running `tests/e2e_test.sh` concurrently with `a2m ps` verified zero socket disruption or connection loss.

### Prevention rule
Unix domain sockets in shared temporary directories must be scoped by user identity (`$UID`), chmod `0700`, and protected against destructive unlinking by concurrent instances.

### Related code
`services/dc-gateway/multiplex/hub.go`, `services/dc-gateway/multiplex/cli.go`

### Related tests
`tests/e2e_test.sh`, `services/dc-gateway/multiplex/hub_test.go`

### Related lessons
[`FL-007`](#fl-007)

### Status
**Resolved**
