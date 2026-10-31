# AGENTS.md — Desktop Commander Agentic Constitution & Harness Protocol

Operational contract for autonomous AI agents connecting via Desktop Commander MCP.

---

## 1. Operating Foundations: Get Things Done (GTD) Protocol

Every task executed through Desktop Commander must advance through verified gates before declaring completion.

```
┌─────────────────┐     ┌──────────────────┐     ┌───────────────────┐     ┌──────────────────┐
│ 1. Capture      │ ──▶ │ 2. Ready Gate    │ ──▶ │ 3. Surgical Action│ ──▶ │ 4. Done Gate     │
│ Outcome & Facts │     │ Single Next Step │     │ edit_block / exec │     │ Observable Proof │
└─────────────────┘     └──────────────────┘     └───────────────────┘     └──────────────────┘
```

### The Ready Gate
Before modifying any source file:
1. **Outcome First:** State the exact deliverable and acceptance criteria.
2. **Fact Grounding:** Locate relevant symbols via `start_search`; inspect line ranges with `read_file`. Never improvise file structures or signatures from memory.
3. **Single Next Action:** Select exactly one discrete step to execute next.

### The Done Gate
A task is never complete because code was written or explanations are persuasive:
* **Observable Evidence Required:** A passing command execution (`make test`, `cargo check`, `go test`) with exit code 0 captured via `start_process` and `read_process_output`.
* **Zero Orphan Leaks:** Verify all spawned background servers or test processes are cleanly terminated via `kill_process` or `force_terminate`.

---

## 2. Diagnostics Protocol: Deep Digger Invariants

When commands, tests, or builds fail, halt speculative editing and follow the systematic root-cause investigation procedure.

### Rules of Investigation
1. **Never Confuse Symptom with Root Cause:** A compiler or runtime error marks where an invariant broke, not necessarily where the bug originated.
2. **Failure Fingerprinting:**
   * Capture the exact failing command and arguments.
   * Extract the tail error trace using `read_process_output(pid, offset=-50)`.
   * Preserve the raw error signature in the brief.
3. **Competing Hypotheses:** Formulate at least two distinct, plausible hypotheses that explain the symptom before modifying code.
4. **Discriminating Experiments:** Change exactly one variable per attempt. Before executing, define what observation would refute each hypothesis.
5. **The Circuit Breaker:** After two consecutive failed fix attempts on the same failure signature, stop all edits. Re-read the broader code context (`read_file`), run a minimal reproducer, or restore a clean git checkpoint.
6. **Counterfactual Verification (A/B/A):**
   * Prove the failure reproduces on the baseline.
   * Apply the minimal fix and verify the failure clears.
   * Run the full regression test suite to ensure zero collateral breaks.

---

## 3. Tool Usage & Efficiency Directives

Optimize token bandwidth and execution speed using native primitives:

| Intent | Tool Standard | Anti-Pattern (Banned) |
| --- | --- | --- |
| **Locating Code** | `start_search` with regex or literal patterns | Listing entire directory trees recursively with `list_directory` |
| **Inspecting Code** | `read_file` with `offset` and `length` (20–100 lines) | Reading entire 1,000+ line files into memory |
| **Modifying Code** | `edit_block` with 3–5 lines of context and exact `expected_replacements` | Overwriting whole files with `write_file` for small diffs |
| **Running Commands** | `start_process` with bounded `timeout_ms` | Running unbounded commands that hang stdio indefinitely |
| **Reading Logs** | `read_process_output` with negative `offset` (e.g. `-50`) | Reading 5,000 lines of build logs from offset 0 |
| **Document Work** | `excel_read_sheet`, `excel_edit_range`, `docx_edit` | Generating ad-hoc Python/Node scripts to edit Office files |

---

## 4. Built-in Harness Tooling (`scripts/harness/`)

The repository includes executable harness utilities designed to be invoked directly via `start_process`:

### 1. Work Brief Manager (`scripts/harness/brief.sh`)
Maintains an execution ledger in `.agent/brief.json` tracking intent, blockers, hypotheses, and next actions.
```bash
./scripts/harness/brief.sh init "Rebuild auth middleware"
./scripts/harness/brief.sh set-outcome "Pass all 12 JWT unit tests"
./scripts/harness/brief.sh next "Implement signature verification in auth.go"
./scripts/harness/brief.sh status
```

### 2. Checkpoint & Safety Guard (`scripts/harness/checkpoint.sh`)
Creates lightweight, reversible Git snapshots before multi-file refactoring.
```bash
./scripts/harness/checkpoint.sh save "pre-refactor"
./scripts/harness/checkpoint.sh list
./scripts/harness/checkpoint.sh restore "pre-refactor"
```

### 3. Verification Oracle & Minimizer (`scripts/harness/verify.sh`)
Executes validation suites, isolates failure signatures, and reports observable pass/fail status.
```bash
./scripts/harness/verify.sh test
./scripts/harness/verify.sh e2e
./scripts/harness/verify.sh full
```
