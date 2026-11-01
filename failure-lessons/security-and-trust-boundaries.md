# Security, Authentication & Trust Boundary Lessons

---

## FL-004: Filesystem Boundary Bypass via Parameter Name Mismatch

### Context
In `services/dc-gateway/mcp/tools.go`, `validatePathBoundary()` enforces the `allowedDirectories` security sandbox across all tool invocations before dispatching to the underlying filesystem engine.

### What happened
While `read_file` correctly blocked access to files outside `allowedDirectories`, `edit_block` successfully edited sensitive files outside the sandbox (e.g. `/tmp/forbidden/secret.txt`).

### Observable symptom
```text
1. read_file on forbidden file isError: true, message: filesystem boundary violation: path '/tmp/forbidden/secret.txt' is outside configured allowedDirectories
2. edit_block on forbidden file isError: false, message: {"diff":"...-super secret data\n+super pwned data\n","success":true}
```

### Impact
High-severity security bypass: an AI agent or prompt injection payload could overwrite arbitrary files on the host disk even when the user configured strict sandbox restrictions.

### Incorrect assumption
Assumed that unmarshaling JSON arguments into `struct { Path string; Source string; Destination string; Paths []string }` would cover all file paths across all registered tools.

### Root cause
**Confirmed**. `edit_block` defines its parameter as `"file_path"`, not `"path"`. Because `"file_path"` was absent from the validation struct, `pathPayload.Path` defaulted to `""`, causing the boundary check to return `nil` (allowed).

### Why the architecture allowed it
The admission validation gate was implemented as an ad-hoc unmarshaler rather than deriving from the canonical JSON-RPC tool schema, and it failed open on empty strings.

### Fix
1. Expanded the path validation struct to parse `file_path`.
2. Fail-closed: Ensure every tool operating on files explicitly maps its path parameters into the admission gate.

### Verification
Re-evaluated during independent audit (`scratch/verify_path_boundary_flaw.go`): `read_file` was blocked as expected, but `edit_block` with parameter `file_path` executed successfully on `/tmp/forbidden/secret.txt`, overwriting the file. The documented fix was absent from `services/dc-gateway/mcp/tools.go`.

### Prevention rule
Validation admission filters must fail closed and map all parameter naming conventions used by registered tools.

### Related code
`services/dc-gateway/mcp/tools.go`

### Related tests
`services/dc-gateway/mcp/tools_test.go::TestAllowedDirectoriesEnforcement`

### Related lessons
[`FL-005`](#fl-005), [`FL-017`](#fl-017)

### Status
**Open Defect (Falsified in Source)**

---

## FL-005: Global Authentication State Leak in Multi-Session Daemon

### Context
`services/dc-gateway/auth/auth.go` manages authentication and role-based permissions (`admin`, `workspace`, `read_only`).

### What happened
When Client A authenticated with a valid token on `sessionA`, any subsequent request from Client B omitting `sessionId` was automatically treated as authenticated and granted full administrative access. In addition, when an SSE session called `auth_login`, it authenticated session `""`, leaving the active SSE session unauthenticated while opening admin access to anonymous callers.

### Observable symptom
```text
Step 1: SSE session calls auth_login with valid token -> returned "Authentication successful".
Step 2: SSE session calls start_process -> returned "authentication required: session is not authenticated".
Step 3: Attacker calls start_process with sessionID="" -> EXECUTED with full admin role.
```

### Impact
Critical privilege escalation: logging into one session (e.g., local desktop dashboard) stripped authentication protection from the entire server, allowing any unauthenticated network caller to execute arbitrary commands while locking out legitimate authenticated sessions.

### Incorrect assumption
Assumed that setting `a.authenticated = true` would serve as a convenience for single-user stdio mode without realizing the same `Authenticator` instance was shared across multi-party HTTP/SSE endpoints.

### Root cause
**Confirmed**. In `AuthenticateSession`, if the token was valid, `a.authenticated = true` was set globally alongside `a.sessions[sessionID]`. And in `AuthorizeSessionTool`, if `sessionID == ""`, it fell back to checking `a.authenticated`. Furthermore, `tools.go` `auth_login` called `Authenticate("")` without passing the caller's session ID.

### Why the architecture allowed it
Single-client stdio mode and multi-client HTTP mode shared the same mutable state structure without enforcing session-scoped tenancy.

### Fix
Scope authentication checks strictly to the active `SessionID`. In multi-session server mode, eliminate global `a.authenticated` flag mutations and require `auth_login` to pass the caller's session ID.

### Verification
Re-evaluated during independent audit (`scratch/verify_auth_flaw.go`): confirmed SSE sessions remain blocked while unauthenticated callers without session ID are granted admin execution.

### Prevention rule
Session authentication state must never bleed into process-global state.

### Related code
`services/dc-gateway/auth/auth.go`, `services/dc-gateway/mcp/tools.go`

### Related tests
`services/dc-gateway/auth/auth_test.go::TestCrossSessionIsolation`

### Related lessons
[`FL-004`](#fl-004), [`FL-006`](#fl-006), [`FL-017`](#fl-017)

### Status
**Open Defect (Falsified in Source)**

---

## FL-006: Drive-By Remote Command Execution via Permissive CORS

### Context
`services/dc-gateway/multiplex/server.go` provides an HTTP and SSE server listening on `127.0.0.1:8765` for Web dashboard monitoring and MCP SSE communication.

### What happened
Adversarial testing revealed that sending an HTTP POST from `Origin: https://malicious-site.example.com` to `http://127.0.0.1:8765/message` executed `start_process` and returned the command output directly to the cross-origin caller.

### Observable symptom
```text
HTTP/1.1 200 OK
Access-Control-Allow-Origin: *
{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"pid\":29805,\"output\":\"drive_by_rce_verified\\n\"}"}]}}
```

### Impact
Critical 10.0 CVSS Drive-By RCE: Visiting any malicious website in any browser allowed attacker JavaScript to execute arbitrary terminal commands on the user's host machine.

### Incorrect assumption
Assumed that binding to `127.0.0.1` prevented remote attacks, ignoring that Web browsers on the host machine freely dispatch requests to `127.0.0.1` unless restricted by CORS and Host header validation.

### Root cause
**Confirmed**. Gateway set `Access-Control-Allow-Origin: *` unconditionally, did not check the `Origin` header, did not check the `Host` header, and ran with `requireAuth = false` by default.

### Why the architecture allowed it
The HTTP endpoints were designed for ease of local prototyping without defense-in-depth against browser-based local network pivoting.

### Fix
1. Remove wildcard CORS (`*`). Allow only loopback origins or explicitly paired frontend clients.
2. Validate the `Host` header to protect against DNS rebinding attacks.
3. Enforce token authentication on all sensitive endpoints (`/message`, `/api/stdin`, `/api/kill`).

### Verification
Re-evaluated during independent audit: active server `127.0.0.1:8765` responded to cross-origin POST with `Access-Control-Allow-Origin: *` and executed `echo drive_by_rce_verified` with exit code 0.

### Prevention rule
Local HTTP daemons exposing system execution capabilities must never emit wildcard CORS (`*`) and must validate Host and Origin headers against an explicit loopback whitelist.

### Related code
`services/dc-gateway/multiplex/server.go`

### Related tests
`services/dc-gateway/multiplex/hub_test.go`

### Related lessons
[`FL-005`](#fl-005), [`FL-017`](#fl-017)

### Status
**Open Defect (Falsified in Source)**

---

## FL-016: Shell Tokenizer Bypass via Whitespace and Subshell Nesting

### Context
In `services/dc-gateway/process/supervisor.go`, `ValidateCommand()` checks commands against `blockedCommands` (`rm -rf /`, `:(){ :|:& };:`) using `ExtractCommands()`.

### What happened
While `"rm -rf /"` was blocked, minor syntax variations such as `"rm  -rf /"` (extra space), `"/bin/rm -rf /"` (absolute path), `"sh -c 'rm -rf /'"` (nested subshell), and `"rm -rf /*"` (glob wildcard) were accepted and executed.

### Observable symptom
```text
scratch/verify_command_bypass.go:
BLOCKED: "rm -rf /"
ALLOWED (BYPASS): "rm  -rf /"
ALLOWED (BYPASS): "/bin/rm -rf /"
ALLOWED (BYPASS): "sh -c 'rm -rf /'"
ALLOWED (BYPASS): "rm -rf /*"
```

### Impact
False sense of security: destructive or dangerous commands circumvent security policy filters through basic formatting adjustments.

### Incorrect assumption
Assumed that splitting commands on string separators (`;`, `&&`, `||`, `\n`) and checking `strings.ToLower(cmd)` against a map would catch forbidden commands.

### Root cause
**Confirmed**. String equality cannot model execution semantics. Quotes prevent sub-token extraction, whitespace variations alter map keys, and path variations evade single-binary lookups.

### Why the architecture allowed it
Absence of an AST shell tokenizer or canonical argument array normalization.

### Fix
Normalize whitespace, canonicalize binary paths, inspect subshell strings within quotes, and enforce an argument-based execution sandbox.

### Verification
Regression test asserting rejection of all syntactic variants of blocked commands.

### Prevention rule
Security policies must never enforce command restrictions using raw string equality on shell command lines.

### Related code
`services/dc-gateway/process/supervisor.go`

### Related tests
`services/dc-gateway/process/supervisor_test.go`

### Related lessons
[`FL-004`](#fl-004)

### Status
**Open Defect (Falsified in Audit)**

---

## FL-017: Documentation Resolution Drift & Premature Attestation

### Context
Repository maintenance of durable failure ledgers (`failure-lessons/` and `docs/solutions/`).

### What happened
Documentation declared FL-003, FL-004, FL-005, and FL-006 as "Resolved" when the underlying source code in `services/dc-gateway` was never patched.

### Observable symptom
Auditors reading documentation believed known vulnerabilities were closed, but adversarial test scripts reproduced 100% of the failure signatures in the live checkout.

### Impact
Dangerous false security posture and compliance breakdown. Release decisions could be improperly authorized based on self-attested documentation rather than machine-verified test proof.

### Incorrect assumption
Assumed that authoring the fix plan and documentation was equivalent to committing and verifying the implementation.

### Root cause
**Confirmed**. Inverted verification discipline: documentation was written ahead of implementation, and no automated verification CI pipeline gated documentation status claims.

### Why the architecture allowed it
Documentation and code resided in the same repository without automated cross-check scripts that enforce that every "Resolved" lesson links to a passing test exercising the fix.

### Fix
Adopt Red-Green Falsification invariant: a lesson cannot be marked "Resolved" without a machine-checked regression test running in CI that fails on unpatched code and passes on patched code.

### Prevention rule
Never mark a defect or vulnerability resolved without fresh observable execution evidence matching the active Git commit SHA.

### Reusable lesson
Documentation that drifts ahead of code is more dangerous than missing documentation because it breeds unearned trust.

### Related code
`failure-lessons/lessons-index.md`

### Related tests
Harness verification test suite

### Related lessons
All FL items

### Status
**Codified Invariant**


---

## FL-018: Single-Root Code Graph Denied Independently Authorized Repositories

**Observed:** A2M's native Code Review Graph adapter forced every `repo_root` to the one configured `capabilityWorkspace`. Attempting to inspect either FunnelScript repository yielded `repo_root is outside the approved A2M workspace`. The rejection was an intentional old security rule, but it prevented simultaneous review of separate approved projects.

**Resolution:** Add a distinct `capabilityWorkspaces` exact-root allowlist, retain the original default, and lazily launch a separately isolated Code Review Graph MCP process plus temporary Git index for each extra root. Validate canonical paths against the allowlist rather than accepting parents, wildcard roots or arbitrary directories. A selected root still passes through the proxy's independent enforcement before upstream `tools/call`. The existing filesystem `allowedDirectories` and source mutation privileges are not broadened.

**Additional discovered security defect:** Existing `changed_files` validation checked `filepath.EvalSymlinks(path)` only when resolution succeeded. A dangling symlink pointing outside the workspace made resolution fail and was silently admitted. The new test reproduced the bypass before correction. Check the nearest resolvable existing ancestor and reject a dangling symlink. Reject other non-ENOENT errors fail-closed.

**Tests:** `TestApprovedGraphRepositoriesPersistAndRequireExistingDirectories`, `TestExplicitApprovedGraphRepositoriesAreRoutedIntoIsolatedProcesses`, `TestNativeGraphAdapterIsWorkspaceBound`, and the opt-in `TestRealGraphBackendSupportsApprovedSecondaryRepository` (real Code Review Graph against two temporary Git repositories). Go, Rust, documentation and MCP E2E regression tests also passed.

**External prerequisite, not an A2M graph bug:** `codex login status` returned `Not logged in`; the prior `codex review` HTTP 401 cannot be repaired by widening A2M's file access or disabling remote authentication. The user must authenticate interactively with Codex. Native Code Review Graph read tooling does not certify an independent AI code review.

**Activation boundary:** Upgraded gateway binaries and saved approved-root settings require the existing MCP worker to reconnect before that running worker exposes new behavior. A successful isolated gateway acceptance test does not by itself establish an already-running tunnel has reloaded.
