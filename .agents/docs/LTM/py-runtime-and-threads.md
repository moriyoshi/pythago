# Package `py`: Runtime and Goroutine-Backed Threads

## Summary
Package `py` is the public Go-to-Python API. Each Python thread runs on a goroutine locked to
its own fresh OS thread, and goroutines enter Python through `Do`, bounded by admission
slots. The thread start/join handshake and the thread cap live in C (`glue/pythago_py.c`),
and spare threads make thread start about as fast as stock CPython. M1 (runtime hardening,
done 2026-11-15) added `Run`, `OnMain`, `Config.Modules`, `Config.Environment`,
multiprocessing defaults and Python-owned signals, so torch and vLLM run through the public
API alone.

## Key Facts
- **At most one Python thread state per OS thread** (invariant). On 3.14t a second one
  corrupts refcounts through BRC (see
  [virtual-threads-research.md](virtual-threads-research.md)). `Do` reuses the OS thread's
  existing GILState thread state; a new Python thread drops the thread state `Do` cached on
  its OS thread before running.
- **Python threads:** `pthread_create` inside libpython is wrapped
  (`__wrap_pthread_create` in `glue/pythago_glue.c`) to `pythago_py_spawn`, which hands the
  thread to a spare goroutine or asks Go to start one. Each gets a fresh OS thread that
  exits after join or detach.
- **`Do(fn)`** takes an admission slot (`Config.Slots`) before `runtime.LockOSThread`, then
  attaches this OS thread's thread state, creating one if needed. Nested calls (C TLS
  `depth > 0`) skip locking and admission.
- **`Release(fn)`** detaches the thread state and gives back the admission slot while `fn`
  runs; it retakes both afterwards.
- **OS-thread bound under admission is slots + GOMAXPROCS**, not slots: while slot holders
  sit in C, Go starts threads for the other goroutines.
- **Thread cap:** `Config.MaxThreads` (default 4096). At the cap the spawn returns EAGAIN and
  Python raises `RuntimeError("can't start new thread")`. `debug.SetMaxThreads` is raised to
  `MaxThreads + spares + slots + 256` (a larger existing limit is kept).
- **Spare threads:** `Config.SpareThreads` (0 means 4; negative disables). A spare is a
  goroutine already locked to its own OS thread, waiting in C on a futex.
- **Finalize** waits for every outer `Do` (a RWMutex), marks the C side stopping, wakes the
  spares to exit, and calls `Py_FinalizeEx` on the dedicated main-thread goroutine.

## Details

### Layout
- `py/runtime.go` — `Config`, `Init`, `Finalize`, `Main`, `IsPythonCommand`, and the
  dedicated main-thread goroutine (`mainThread`, locked for life; it owns the main thread
  state and initializes and finalizes the interpreter).
- `py/thread.go` — `Do`, `Release`, `threadStart` (the Go spawn hook), `SetThreadLimit`,
  `Stats`/`ReadStats`.
- `py/process.go` — `RegisterImage`, `Run`, `OnMain`, `ErrMainBusy`, environment
  activation.
- `py/object.go`, `convert.go`, `errors.go`, `func.go`, `run.go` — objects, Go-Python value
  conversion, `*py.Error` with traceback, Go functions and modules, Exec/Eval/Import.
- `py/capi.go` — symtab lookup. Go-to-C calls originally went through `purego.SyscallN`;
  they now go through `py/ccall.go` and `ccall_{amd64,arm64}.s` (one `runtime.cgocall` of
  an assembly thunk). `purego.RegisterFunc` remains only for float arguments and results
  (`PyFloat_FromDouble`, `PyFloat_AsDouble`). See [performance.md](performance.md) and
  [embedding-cpython.md](embedding-cpython.md).
- `py/fsimport.go`, `py/internal/extshim` — the `fs.FS` importer and the extension shim.
- C side: `glue/pythago_py.c` (spike original: `.agents/attic/spike/glue/pythago_py.c`).

### Entering Python: `Do`, nesting, `Release`
- `Do` first checks C TLS `depth` (`pythago_py_depth`). `depth > 0` means this OS thread is
  already inside Python on Go's behalf, so the calling goroutine must be the one locked to
  it; `Do` just runs `fn` on the same thread state.
- `depth` is set by `pythago_py_enter` (inside `Do`), for a whole Python thread by
  `pythago_py_thread_main`, and by the C trampoline `pythago_py_fastcall` around every Go
  function called from Python.
- Otherwise `Do` read-locks `inflight` (so `Finalize` can wait it out), takes a slot from
  the `slots` channel (or returns `ErrNotRunning` once `closing` is closed), locks the OS
  thread, and calls `pythago_py_enter(1)`.
- `pythago_py_enter`: if a thread state is attached, only `depth++`. Otherwise it takes
  `PyGILState_GetThisThreadState()`, or creates one with
  `PyThreadState_New(PyInterpreterState_Main())` (which binds the GILState slot), caches it
  in C TLS `owned`, and registers it with a `pthread_key` whose destructor (`owned_dtor`)
  frees it when the OS thread exits. Returns -1 (`ErrNoThreadState`) if none could be
  created.
- `Release` calls `pythago_py_release`, which detaches and hands back the slot this thread
  holds; Go returns the slot to the channel. On exit it retakes a slot and calls
  `pythago_py_reacquire`.
- Default `Config.Slots`: GOMAXPROCS on free-threaded builds, `min(4, GOMAXPROCS)` with
  the GIL, where more slots only add GIL contention.
- Work that `fn` hands to another goroutine which itself calls `Do` needs its own slot.
  Waiting for it inside `fn` can deadlock when slots run out; wait inside `Release`.

### Python threads: the C handshake
All of start, join, detach, the cap and the counters are in C; Go keeps only `threadStart`.
- Each goroutine-backed thread has a `calloc`'d record (`pythread`) with three futex flags:
  `started`, `done`, `released`. Records are registered by `pthread_t` in a
  mutex-protected hash (`registry`).
- `pthread_create` -> `pythago_py_spawn`:
  - hands the record to a waiting spare with one futex wake (no Go call); or, without a
    spare, calls the non-blocking Go hook `threadStart`, which starts a goroutine,
    `runtime.LockOSThread`s it (never unlocked), and calls `pythago_py_thread_main(t)`;
  - then waits on `started` for the child's `pthread_t`.
- The child (`pythago_py_thread_main`) publishes `pthread_self`, registers itself, sets
  `started`, asks for a replacement spare (`spares_refill`), drops any thread state `Do`
  cached on this OS thread (`owned_drop`), runs CPython's thread function with `depth = 1`,
  sets `done`, and waits for `released` before freeing its record. The goroutine and its OS
  thread then exit, running TLS destructors as a libc-created thread would.
- Join and detach (`__wrap_pthread_join`, `__wrap_pthread_detach`) set `released` in C.
  `pthread_exit` on such a thread parks in C.
- The cap (`limit`, default 4096, `pythago_py_set_limit`) and counters (`live`,
  `started_count`, thread states created/deleted) are C atomics, read by `py.ReadStats`.

### Spare threads
- A spare asks Go for its replacement only after it has published `started`, so OS-thread
  creation is off the parent's critical path. Without a spare, the direct spawn path is used.
- **Every Python thread still gets a fresh OS thread** that exits after join. Reusing Ms was
  rejected: thread-local state in mimalloc, OpenSSL, extensions and the locale would pass
  between Python threads.
- `pythago_py_started` fills the spares after initialization; `pythago_py_stopping`
  (before `Py_FinalizeEx`) wakes them to exit, and exiting threads then leave their cached
  thread state alone.

### Why the handshake moved to C, and what spares bought
Breakdown before the change (300 idle starts, 3.14t): the Go hook took 2 µs, until the child
ran 49 µs, the parent's wake 8 µs. The channel handshake was never the cost. Go creates a
fresh OS thread for every locked goroutine (via its template thread, because the caller is a
locked M), about 50 µs. Moving to C alone gained little (165 -> 131–144 µs); spares removed
the OS-thread creation from the critical path.

Start+join latency against stock CPython (2026-11-15, arm64 host):

| start+join | stock | Go-side handshake | C handshake + spares |
|---|---|---|---|
| 3.14t idle | 74–118 µs | 165 µs | 73–139 µs |
| 3.14t, 40 threads blocked | 65–114 µs | 201 µs | 112–168 µs |
| 3.14t, under a convoy | 0.1 ms | 0.2–0.3 ms | 0.1 ms |
| 3.13 idle | 48–49 µs | | 72–95 µs |
| 3.13, 40 threads blocked | 26–49 µs | | 60–69 µs |

Other measurements (same setup):
- Lock ping-pong equals stock (11–12 µs per round trip).
- Oversubscribed lock convoys (48 threads on one lock, 20 cores) are CPython's own: stock
  2.6–10.3 s, `py` 6–10.3 s. The VT glue makes no difference.
- The start-heavy convoy workload `mixed.py`: before spares about 10 s vs stock 2–7 s
  (slower start/exit tipped CPython's convoy more reliably); after, medians about 7.0 s vs
  7.0 s.
- Remaining gap: about 25–45 µs per start+join on 3.13; the 3.14t blocked-thread case is
  noisy. The spare handoff is one futex wake, so the rest is probably wake-up latency on
  both sides plus the spare refill competing for CPU.

### M1: runtime hardening
- **`RegisterImage(symtab)`:** dist packages register their symtab when imported, so
  `Config.Symtab` is optional.
- **Extension shim** in `py/internal/extshim`, loaded in `Init`. `Config.NoExtensions` opts
  out (needed on glibc < 2.28); otherwise `Init` fails with
  `py: extension shim (set Config.NoExtensions on glibc older than 2.28): ...`.
- **`Run(cfg, fn)`:** if `IsPythonCommand()`, runs `Main` (the process is a
  `sys.executable` child); otherwise `Init`, `fn`, `Finalize`, and exits.
- **`Config.Modules`** are created in `setup`, so `sys.executable` children
  (multiprocessing workers) get them too. `Config.Setup` and `Config.Environment` apply to
  children the same way.
- **`OnMain(fn)`** is served by the main-thread loop next to `Finalize`. It runs `fn`
  directly when already on the main thread, and returns `ErrMainBusy` under `Main` (the
  command line occupies the main thread).
- **`Config.Environment`** (venv-like): `site.addsitedir` of
  `lib/pythonX.Y<abiflags>/site-packages` (so `.pth` files work), `bin/` first on PATH,
  `sys.prefix` and `VIRTUAL_ENV`.
- **`Config.MultiprocessingStart`** defaults to `"forkserver"`. It changes
  multiprocessing's default context without fixing it, so `set_start_method` still works.
  On 3.14 (whose Linux default is already forkserver) it does nothing and needs no import.
  `"default"` keeps Python's own default. In a forked child Go functions cannot run.
- **`Config.PythonSignals`:** Python owns SIGINT (raising KeyboardInterrupt); Go keeps every
  other signal. CPython installs its SIGINT handler only if SIGINT is still `SIG_DFL` when
  its signal module initializes, and Go has already installed one, so the glue
  (`glue/pythago_glue.c`) resets SIGINT to `SIG_DFL` first. The first version omitted
  this and did nothing.
- **`internal/tools/pyrun`** (spike `pyrun` originally) uses only the public API (`py.Run`,
  `PYRUN_ENV` -> `Config.Environment`). Through it: torch (basic, DataLoader default,
  fork, spawn, `torch.compile`) and vLLM (generation; the OpenAI server as
  `pyrun -m vllm.entrypoints.openai.api_server`, chat answer, SIGTERM exit 0).

## Files
- `py/runtime.go` — `Config` (Slots, MaxThreads, SpareThreads, Environment, Modules,
  PythonSignals, MultiprocessingStart, NoExtensions, Setup), `Init`, `Main`, `mainThread`.
- `py/thread.go` — `Do`, `Release`, `threadStart`, `SetThreadLimit`, `ReadStats`.
- `py/process.go` — `RegisterImage`, `Run`, `OnMain`, `ErrMainBusy`, environment
  activation.
- `py/capi.go`, `py/ccall.go`, `py/ccall_*.s` — C symbol table and Go-to-C calls.
- `glue/pythago_py.c` — `pythago_py_enter`/`leave`/`release`/`reacquire`,
  `pythago_py_spawn`, `pythago_py_thread_main`, spares, registry, cap, `pythago_py_fastcall`.
- `glue/pythago_glue.c` — `__wrap_pthread_create`/`join`/`detach`/`exit`, SIGINT reset.
- `py/internal/extshim/extshim.go` — the SHN_ABS extension shim.
- `internal/tools/pyrun` — runs a script or module through `py.Run`.

## Test Coverage
`py/py_test.go` (needs the test image; see `AGENTS.md`):
- threads and admission: `TestPythonThreads`, `TestThreadLimit`, `TestGoroutines`
  (2000 goroutines), `TestRelease`, `TestNestedUnderLoad`, `TestThreadStateFreedAtThreadExit`,
  `TestUnclosedObjectsReleased`, `TestMixedThreads`, `TestDaemonAtFinalize` (a daemon thread
  still running at `Finalize`); `TestPythonThreads` also covers a detached thread
  (`_thread.start_new_thread`);
- M1: `TestModulesInChildren`, `TestOnMain`, `TestEnvironment`,
  `TestMultiprocessingStartMethod`, `TestPythonSignals`, plus `TestSysExecutable`,
  `TestMultiprocessingSpawn`, `TestMultiprocessingForkserver`, `TestSignalRestore`,
  `TestForkChild`, `TestExtensionShim`.

Run:
```
GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0 GOWORK=$PWD/.work/go.work go test ./py
ci/unit.sh gil && ci/unit.sh ft
```
Thread tests were run at `GOMAXPROCS` 1, 2/4 and default (3.13 30/30, 3.14t 24/24 at the
time). GOMAXPROCS=1 is the configuration that exposed the `Release` slot bug.

## Pitfalls
- `Release` must give back the admission slot. When it kept it, with `GOMAXPROCS=1` (one slot
  on the GIL build) one goroutine blocked in Go stalled every other `Do`.
- Do not assert "OS threads <= slots" in tests; the real bound is slots + GOMAXPROCS.
- Never create a second thread state on an OS thread: reuse the GILState slot, and drop the
  cached `Do` thread state before a Python thread runs on that OS thread.
- Do not reuse OS threads between Python threads to save start time; per-thread C state
  would leak between them.
- Without `Config.PythonSignals`, Go keeps SIGINT; with it, SIGINT no longer reaches
  `os/signal`. From `Do` (not the main thread) Python cannot install signal handlers.
- Under `Main` the main thread runs the command line, so `OnMain` returns `ErrMainBusy`.
- Exceeding Go's own thread limit is fatal, not an exception; keep `debug.SetMaxThreads`
  above `MaxThreads` plus spares and slots (Init does this).
