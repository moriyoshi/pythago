# Worker Processes (`workers.Spawn`, formerly `asgi.Spawn`)

## Summary
With the GIL one process runs Python on one core at a time, so `asgi` and `wsgi` scale
across cores the way uvicorn and gunicorn do: several worker processes accepting from one
listening socket. Package `workers` (moved out of `asgi` on 2026-11-17, since it serves any
`http.Handler`) has `workers.Spawn`, a supervisor that never starts Python; it spawns (fork plus exec)
n copies of its own executable, passes the listener as fd 3, restarts workers that die and
stops them on shutdown. With each worker's GOMAXPROCS sized to its CPU share, 4 workers
reach 234k req/s on 4 cores, 87% of uvicorn `--workers 4`, the same ratio as on one core.

## Key Facts
- **Spawn, never fork.** `fork()` copies only the calling thread; the Go runtime's
  scheduler, `sysmon`, GC workers and netpoller are gone in the child and any lock they
  held stays locked. Go itself only forks immediately followed by exec
  (`syscall.ForkExec`).
- **Socket passing:** `(*net.TCPListener).File()` makes a duplicate; `exec.Cmd.ExtraFiles`
  puts it at fd 3 in each child (close-on-exec cleared only on that copy).
  `PYTHAGO_WORKER=<index>` marks the child as a worker (`PYTHAGO_ASGI_WORKER` before the
  move).
- **`workers.InheritedListener()`** returns the inherited listener (`net.FileListener`), or
  nil with no error when the process is not a worker. It closes fd 3 and unsets the
  marker, so the app's own subprocesses do not take themselves for workers; later calls
  return nil. Call it before `py.Init`: Python copies the environment at initialization,
  so the marker would otherwise stay in `os.environ` for the app's own pythago
  subprocesses.
- **`workers.Index()`** (was `asgi.WorkerIndex`) returns the index (0 to n-1), or -1 when not a worker. Call it
  before `InheritedListener` (which unsets the marker).
- **`workers.Spawn(ctx, l, n, Options)`**; `Options{Grace, Command, MaxEarlyExits}` (was
  `SpawnOptions`).
  Defaults: `Grace` 30 s, `MaxEarlyExits` 5, `Command` nil (this executable with
  `os.Args[1:]`).
- **One shared accept queue:** a dying worker loses no queued connection.
- **Restart with backoff:** per worker, starting at 100 ms and doubling up to 30 s while it
  keeps dying within 5 s of starting (`earlyExit`); reset after a longer life.
- **Give up after `MaxEarlyExits` consecutive early exits:** Spawn stops every worker and
  returns `asgi: worker %d exited %d times in a row within %v of starting (last: %v);
  giving up`.
- **Shutdown:** on ctx done, SIGINT or SIGTERM, workers get SIGTERM, then SIGKILL after
  `Grace`.
- **Supervisor death:** `Pdeathsig: SIGTERM`, with every child started from one
  permanently locked OS thread, because Pdeathsig follows the thread that started the
  child.
- **GOMAXPROCS:** unless the environment sets it, each worker gets
  `GOMAXPROCS = max(1, runtime.NumCPU()/n)` (affinity-aware). Without this, 4 workers on 4
  cores did 162k instead of 234k req/s.
- **`asgiserve -processes n`:** the first process listens and supervises before `py.Init`.

## Details

### Why a forked child cannot serve
In a forked pythago child the glue's fork handler already makes Python threads use libc and
makes Go functions raise `RuntimeError`. Plain Python and the stock asyncio loop therefore
work there, but `GoEventLoop` (whose poll and I/O are Go) and the net/http server cannot.
Hence fork plus exec.

### Program shape (`asgi/process.go` package comment)
```go
cfg := py.Config{...}
asgi.Configure(&cfg)
if py.IsPythonCommand() { os.Exit(py.Main(cfg, os.Args)) }
l, err := workers.InheritedListener()
if l == nil && processes > 1 {
	l, err = net.Listen("tcp", addr)
	err = workers.Spawn(ctx, l, processes) // supervises; Python is never started here
	return
}
if l == nil { l, err = net.Listen("tcp", addr) }
py.Init(cfg); srv, err := asgi.New("app:app", opts); http.Serve(l, srv)
```
Workers must shut down on SIGTERM (`http.Server.Shutdown`, then `Server.Close`).

### Supervision mechanics
- `Spawn` requires a listener with `File()`; otherwise
  `workers: Spawn: %T has no file descriptor to pass on`. `n < 1` is an error.
- A dedicated goroutine calls `runtime.LockOSThread()` and is never unlocked; every
  `cmd.Start()` goes through it, so Pdeathsig is tied to a thread that lives as long as the
  supervisor (Go may otherwise retire threads).
- Each child's environment is the parent's plus `PYTHAGO_WORKER=<i>` and, if absent,
  `GOMAXPROCS`. Stdout and stderr are inherited unless `Command` set them.
- A failing initial start SIGKILLs the started workers and returns
  `workers: starting worker %d: %w`. A failing restart is retried with the backoff.
- `shutdown` sends SIGTERM to the running workers, waits for their exits, and SIGKILLs
  those still running after `Grace`.

### Rejected alternatives
- **`SO_REUSEPORT`:** per-socket queues are reset when a worker dies (its queued
  connections are lost), and hashing can unbalance load.
- **systemd's `LISTEN_FDS`:** `LISTEN_PID` must be set after fork and before exec, which
  `os/exec` cannot do.

### Why MaxEarlyExits exists
The first benchmark ran workers built against the 3.14t test image with a 3.13
environment. Every worker failed at startup
(`environment ... has no .../python3.14t/site-packages`), and Spawn restarted them every
30 s (the backoff cap) forever; the benchmark script hung. gunicorn halts in that situation,
and so does Spawn now.

### GOMAXPROCS per worker
Each worker's Go runtime defaulted to GOMAXPROCS = 4 on the 4 cores: 16 Ps and many more
threads. 4 workers, 4 cores:

| per-worker GOMAXPROCS | req/s |
|---|---|
| 4 (the old default) | 161.6-163.7k |
| 2 | 195.7-195.8k |
| **1** | **234.1-234.8k** |

With the per-worker default and nothing set: 233.8-235.7k. One process inline did
150.2-150.7k. Rule: size each process's runtime to its share of the machine; several Go
runtimes on the same cores must not each take all of them.

### Results
Quiet host (load 1-5), CPython 3.13.18, `app.py` `/`, 512 connections, 4 cores, req/s
(inline requests, the default):

| | req/s |
|---|---|
| `asgiserve -processes 4` | 233.8-235.7k |
| uvicorn + uvloop `--workers 4` | 267.9-269.6k |
| `asgiserve` (one process) | 150.2-150.7k |

The 87% ratio equals the per-core ratio (58.5k against 72k on one core, see
[asgi-inline-and-flat-combining.md](asgi-inline-and-flat-combining.md)). Workers scale
linearly, so the remaining gap is a per-core matter.

## Files
- `workers/workers.go` (was `asgi/process.go`) — `Spawn`, `Options`, `InheritedListener`,
  `Index`, `workerEnv` (`PYTHAGO_WORKER`), `listenerFD` (3), `earlyExit` (5 s), `shutdown`,
  `signalAll`.
- `workers/attr_linux.go`, `workers/attr_darwin.go` — Pdeathsig, and the darwin kqueue
  parent watch.
- `workers/workers_test.go` — `TestMain` (a plain Go handler when it inherits a listener:
  no Python, no test image), `TestSpawn`, `TestSpawnGivesUp`, `TestSpawnErrors`.
- `internal/tools/asgiserve`, `internal/tools/wsgiserve` — `-processes n`.

## Test Coverage
Run with `go test ./workers` (no test image needed; also part of `ci/unit.sh` and
`ci/darwin.sh`).

- **`TestSpawn`:** 2 workers both answer with their pid, index and marker; indexes are 0
  and 1 and the marker is gone after `InheritedListener`; one is SIGKILLed; the other keeps
  serving and a replacement with the same index appears; cancelling makes Spawn return,
  well within `Grace` (60 s), with every worker gone.
  - Controls (2026-11-17, after the move): no restart → `no replacement worker served
    within 30 s`; no SIGTERM at shutdown → `Spawn took 1m0.003s to stop its workers` (the
    asgi version's control only saw orphans, because the test then allowed for Grace);
    marker kept → `still has the marker "0"`; index 0 for all → `worker indexes
    map[0:true]`; no give-up → test timeout message; `n < 1` unchecked → timeout.
  - A mutant that does not compile is not a control (the first no-restart mutant).
- **`TestSpawnGivesUp`:** workers are `sh -c 'exit 3'` (via `Options.Command`) with
  `MaxEarlyExits: 3`; Spawn must return a giving-up error. Control, no give-up check: the
  test times out.
- **Manual checks with `asgiserve`:** 3 workers shared 200 requests (114/72/14); a
  SIGKILLed worker was replaced after about 2 s; SIGTERM to the supervisor stopped
  everything; SIGKILL to the supervisor took its workers down too (parent-death signal)
  and the port closed.

## Pitfalls
- **Never hand a listener's `File()` to `exec.Cmd.ExtraFiles`.** That `*os.File` is one Go
  put into non-blocking mode, so `Fd()`, which `os.StartProcess` calls for every
  ExtraFiles entry, switches the descriptor to blocking mode, and `O_NONBLOCK` belongs to
  the open file description that the supervisor and every worker share. Each start made
  the workers' listener blocking until some new worker's `FileListener` set it back; a
  worker could block inside `accept(2)`, where `Close` cannot reach, and ignore SIGTERM
  (TestSpawn, about 1 run in 40). `listenerFile` passes an `os.NewFile` of a plain
  `F_DUPFD_CLOEXEC` duplicate, which `Fd()` leaves alone; `TestSpawnKeepsListenerNonblocking`
  checks the flag after starting a worker that never touches it (`sleep`).
- **Never `fork()` without exec in a Go process.** The child has one thread and no Go
  runtime machinery.
- **Pdeathsig is per thread, not per process.** Start children from one locked thread that
  never exits, or a retired thread kills its children.
- **Unset the worker marker after taking the listener,** or subprocesses of the app think
  they are workers and grab fd 3.
- **Call `WorkerIndex()` before `InheritedListener()`;** the latter unsets the marker.
- **A supervisor must stop restarting a worker that cannot start** (bad configuration,
  import error, a 3.13 environment with a 3.14t image).
- **N Go runtimes each sized for all cores oversubscribe them;** an explicit `GOMAXPROCS`
  in the environment overrides the per-worker share.
- **`pkill -f '<pattern>'` matches the invoking shell** (exit 144) when benchmarking
  workers; see [process-lessons.md](process-lessons.md).
