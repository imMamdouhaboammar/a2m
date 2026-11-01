# Concurrency and Process Lifecycle Lessons

---

## FL-001: Concurrent Stream Writer Race on Multiplex Hub

### Context
In `services/dc-gateway/multiplex/hub.go`, a Unix Domain Socket and TCP multiplexing hub broadcasts terminal output and command events to attached observer clients while simultaneously reading and writing responses to interactive client sessions.

### What happened
Running `go test -race ./...` produced a fatal `WARNING: DATA RACE` report indicating concurrent unsynchronized access to `bufio.Writer.Available()` and `bufio.Writer.WriteString()`.

### Observable symptom
```text
WARNING: DATA RACE
Write at 0x00c00010c2c0 by goroutine 34:
  bufio.(*Writer).Available()
  bufio.(*Writer).WriteString()
  github.com/imMamdouhaboammar/a2m/services/dc-gateway/multiplex.(*MultiplexHub).sendLine()
Previous write at 0x00c00010c2c0 by goroutine 28:
  bufio.(*Writer).WriteString()
  github.com/imMamdouhaboammar/a2m/services/dc-gateway/multiplex.(*MultiplexHub).handleClient()
```

### Impact
High risk of heap corruption, corrupted output lines delivered to terminal watchers, and panics under concurrent multi-client usage.

### Incorrect assumption
Assumed that because client network socket writes were buffered through `bufio.Writer`, concurrent invocations of `sendLine()` from broadcast events and client request handlers would not interleave destructive state mutations.

### Root cause
**Confirmed**. `clientSubscription` contained a single `*bufio.Writer` without mutual exclusion. Goroutine A called `h.Broadcast()` while Goroutine B called `h.sendLine()` directly from the client dispatch loop.

### Why the architecture allowed it
The subscription structure lumped connection state, filters, and stream serialization into a single struct without separating I/O concurrency domains.

### Fix
Added dedicated mutual exclusion locks:
- `writeMu sync.Mutex`: Protects all write operations on `sub.writer`.
- `stateMu sync.RWMutex`: Protects subscription filter state (`watchAll`, `attachedPID`).

```go
type clientSubscription struct {
    conn        net.Conn
    writer      *bufio.Writer
    writeMu     sync.Mutex
    watchAll    bool
    attachedPID int
    stateMu     sync.RWMutex
}
```

### Verification
`go test -race -count=1 ./...` in `services/dc-gateway/multiplex` passed with 0 data race warnings.

### Prevention rule
Any network or stream writer shared across multiple goroutines must have explicit, dedicated serial write mutual exclusion.

### Reusable lesson
Never assume an I/O buffer or stream writer in Go is safe for concurrent use. Wrap all buffered writers with a mutex if more than one goroutine can emit messages.

### Related code
`services/dc-gateway/multiplex/hub.go`

### Related tests
`services/dc-gateway/multiplex/hub_test.go::TestMultiplexHubLifecycleAndWatch`

### Related lessons
[`FL-003`](#fl-003)

### Status
**Resolved**

---

## FL-002: Command Latency Stall due to Unsignaled Completion Channel

### Context
In `services/dc-gateway/process/supervisor.go`, `StartProcess` spawns a command and attempts to wait up to a short duration (2,000 ms) to capture immediate command output before returning to the caller.

### What happened
Every short-lived command (like `echo hello` or `pwd`) suffered an artificial ~2,000 ms penalty, inflating execution time from ~6 ms to ~2,010 ms.

### Observable symptom
Command latency was stuck at ~2,050 ms on every single tool call, even for sub-millisecond shell commands.

### Impact
Severely degraded agent responsiveness, causing AI tool calls to feel sluggish and wasting hundreds of seconds across typical pairing sessions.

### Incorrect assumption
Assumed that creating channel `initialWaitDone := make(chan struct{})` and awaiting it in `select { case <-time.After(waitLimit): case <-initialWaitDone: }` was sufficient to wake up when `cmd.Wait()` returned.

### Root cause
**Confirmed**. The goroutine executing `cmd.Wait()` never signaled or closed `initialWaitDone`. The `select` statement was forced to block until `time.After(2000 * time.Millisecond)` expired on every single run.

### Why the architecture allowed it
The test suite tested only functional correctness (that output matched), without asserting that execution latency for trivial commands must complete within < 50 ms.

### Fix
Explicitly closed the channel when `cmd.Wait()` returned:

```go
go func() {
    waitErr := cmd.Wait()
    ...
    close(initialWaitDone)
}()
```

### Verification
Empirical benchmark (`scripts/benchmark.sh`) proved command latency dropped from 2,010 ms to 6 ms (340x speedup).

### Prevention rule
Any completion channel awaited in a `select` with a timeout must be deterministically closed or notified on the primary event path, backed by a regression test asserting maximum elapsed time.

### Related code
`services/dc-gateway/process/supervisor.go`

### Related tests
`services/dc-gateway/process/supervisor_test.go::TestProcessLifecycleAndPagination`

### Related lessons
[`FL-003`](#fl-003)

### Status
**Resolved**

---

## FL-003: Pipe Reader Race with `cmd.Wait()` Under High Concurrency

### Context
When commands run under high concurrency (e.g. 20 parallel workers), `cmd.Wait()` exits as soon as the operating system process terminates.

### What happened
Under stress testing with 20 parallel processes, 40% of executions returned empty `""` output for quick commands (`echo stress_test`), even though the process completed with exit code 0.

### Observable symptom
```text
Trial 3: 1/20 workers experienced empty/truncated output!
Trial 4: 3/20 workers experienced empty/truncated output!
Failure rate: 2/5 trials failed under concurrency=20
```

### Impact
Nondeterministic missing command output when agents spawn parallel background tasks or concurrent commands.

### Incorrect assumption
Assumed that when `cmd.Wait()` returns, stdout/stderr pipe readers have finished copying all bytes into the output buffer.

### Root cause
**Confirmed**. Go's `cmd.StdoutPipe()` documentation explicitly warns that reads from the pipe must complete before `cmd.Wait()` releases resources. Closing `initialWaitDone` on `cmd.Wait()` raced with asynchronous `captureStream` goroutines that were still scanning trailing pipe buffers.

### Why the architecture allowed it
`captureStream` ran in detached background goroutines without a `sync.WaitGroup` to coordinate completion before releasing the output buffer.

### Fix
Add a `sync.WaitGroup` tracking the stdout and stderr stream capture goroutines, ensuring buffers are fully populated before the initial wait result is read.

### Verification
Re-evaluated during independent audit (`scratch/test_concurrency_race.go`): 10 rounds of 25 parallel workers produced 13 empty output results out of 250 executions (5.2% failure rate). The documented `sync.WaitGroup` was absent from `supervisor.go`, proving the defect remains active in source code.

### Prevention rule
Never release process output buffers upon `cmd.Wait()` return without synchronizing on pipe reader EOF completion.

### Related code
`services/dc-gateway/process/supervisor.go`

### Related tests
`services/dc-gateway/process/supervisor_test.go`

### Related lessons
[`FL-002`](#fl-002), [`FL-012`](#fl-012), [`FL-017`](#fl-017)

### Status
**Open Defect (Falsified in Source)**

---

## FL-012: Slow Consumer Deadlock in Multiplex Broadcasting

### Context
In `services/dc-gateway/multiplex/hub.go`, `Broadcast()` distributes process stdout/stderr events to connected watcher sockets by iterating through registered clients and calling `h.sendLine(sub, payload)`.

### What happened
When a connected watcher (e.g. an external terminal viewer or web dashboard) pauses or reads slowly, its operating-system socket buffer fills up. `Broadcast()` blocks inside `sub.writer.Flush()` while holding the read lock `h.mu.RLock()`. Because `supervisor.captureStream` invokes `broadcaster` synchronously on every line of process stdout, the execution of commands on the workstation halts completely.

### Observable symptom
```text
Client 1 subscribed: {"status":"watching_all_sessions"}
[CONFIRMED DEADLOCK/BLOCKING]: Broadcast BLOCKED after 37 messages because Client 1 stopped reading!
```

### Impact
Critical denial of service: a single slow or stalled client halts command execution for all users and agents, freezing terminal tasks on the host.

### Incorrect assumption
Assumed that buffered writes via `bufio.Writer` and standard socket writes are fast enough to be executed synchronously inside a broadcast loop while holding subsystem locks.

### Root cause
**Confirmed**. Direct synchronous socket writes within a broadcast loop without timeouts or asynchronous client dispatch queues will block whenever a single TCP/Unix socket buffer saturates. In Go's `sync.RWMutex`, active readers block new writers, and pending writers block all new readers, cascading into total server deadlock.

### Why the architecture allowed it
The broadcast path lacked bounded per-client queues and eviction policies for slow readers.

### Fix
Decouple client delivery using non-blocking buffered channels (`chan string` with capacity e.g. 256). In `Broadcast()`, use a non-blocking `select`: if the client channel is full, drop the message or disconnect the slow client.

### Verification
Stress benchmark `scratch/test_hub_slow_consumer.go`: 50,000 broadcast messages must complete in <50ms without stalling even if an attached watcher never reads.

### Prevention rule
Broadcast loops must never perform synchronous network I/O while holding shared mutexes; use non-blocking channel dispatch and shed slow consumers.

### Related code
`services/dc-gateway/multiplex/hub.go`

### Related tests
`services/dc-gateway/multiplex/hub_test.go::TestMultiplexHubNonblockingBroadcast`

### Related lessons
[`FL-001`](#fl-001), [`FL-003`](#fl-003)

### Status
**Open Defect (Falsified in Audit)**

---

## FL-013: Daemon Process Leak (Zombie Spawning) via Detached IPC Auto-Start

### Context
In `services/dc-gateway/ipc/client.go`, `ensureDaemon()` attempts to connect to `dc-fs-engine` on a Unix domain socket. If the connection fails, it launches `exec.Command(c.binaryPath, "daemon", "--socket", addr).Start()`.

### What happened
The gateway started `dc-fs-engine` into the background but never stored the `cmd.Process` handle, never created a supervisory reaper, and never registered cleanup hooks. Repeated restarts or test invocations caused orphaned `dc-fs-engine daemon` processes to accumulate indefinitely.

### Observable symptom
```text
ps aux | grep dc-fs-engine:
mamdouhaboammar 4676  0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 4791  0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 4844  0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 4909  0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 5622  0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 26707 0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/a2m-ipc.sock
mamdouhaboammar 30792 0.0 0.0 ... bin/dc-fs-engine daemon --socket /tmp/non_existent_socket_12345.sock
```

### Impact
Host resource depletion, file descriptor exhaustion, and stale daemon processes serving obsolete code versions.

### Incorrect assumption
Assumed that child background processes would terminate automatically when the parent gateway process exited.

### Root cause
**Confirmed**. In POSIX systems, `exec.Command().Start()` spawns an independent process. Without attaching it to the parent lifecycle via POSIX subreaper, `prctl(PR_SET_PDEATHSIG)`, or explicit SIGTERM handling in `Close()`, the process is reparented to `launchd` / `init` (PID 1) and runs forever.

### Why the architecture allowed it
The IPC client was written as a pure network client without owning daemon lifecycle management.

### Fix
Store `cmd *exec.Cmd` inside `FsEngineClient`, implement `Close() error` that sends `SIGTERM` and waits for process termination, and register process lifecycle hooks in `main.go`.

### Verification
Process census assertion: verify zero orphan `dc-fs-engine` processes remain after gateway termination.

### Prevention rule
Any component that spawns a background child process must own its termination lifecycle.

### Related code
`services/dc-gateway/ipc/client.go`

### Related tests
`services/dc-gateway/ipc/client_test.go`

### Related lessons
[`FL-008`](filesystem-and-packaging.md#fl-008)

### Status
**Open Defect (Falsified in Audit)**
