# Project To-Dos

Open items, moved from the "Open items" section of `JOURNAL.md` on 2026-11-17. Re-verify an item against the tree before acting on it, and resolve or remove it once addressed.

## Open Items

- [ ] **Default `-static-tls`:** waiting for the user's go-ahead. It falls back on its own when
  glibc lacks room.
- [ ] **LTO mode on glibc < 2.30:** bao cannot bind weak *code* references from archive input
  (Go's linker cannot import weakly). Shared mode is unaffected.
- [ ] **asgi:**
  - permessage-deflate (off on purpose);
  - fewer Go↔Python calls per request: 6 Go→C calls (from 58) after the container
    builder and decoded arguments. What remains is 4 event-tuple builds per request;
    the next cost is in Python and net/http, so profile before going further;
  - refcount fast paths (Codex's proposal): about 0.7% per request, measured
    2026-11-17; only if a later profile shows them;
  - batching responses per loop iteration.
- [ ] **CPython 3.14t: a daemon thread still tearing down at finalization is cleared twice**
  (stock CPython 3.14.10t too). After `_Py_brc_remove_thread`, its own `PyThreadState_Clear`
  detaches in `_PyMem_AbandonDelayed` (contended `mem_free_queue.mutex`, or a delayed decref
  running a finalizer); `_Py_Finalize`'s stop-the-world counts it as stopped,
  `_PyThreadState_RemoveExcept` takes it and `_PyThreadState_DeleteList` clears it again.
  Stock reproducer: `.agents-workspace/tmp/race314/repro/delayed_del.py` (SIGSEGV 10/10,
  control 0/10). An upstream issue is drafted (journal 2026-11-17, tackle-todos); filing it
  waits for the user. asgi no longer exposes it (`Server.Close` joins its workers);
  application daemon threads still can. — *source: journal 2026-11-17, darwin finalize crash*
- [ ] **Default GOMAXPROCS of at least 2 when Python shares the core (user decision):** on
  one core, `GOMAXPROCS=2` lifts GoEventLoop rr with 64 TLS connections 78.9k → 107.6k
  req/s (+37%, past the stock loop's 95.3k) and costs bulk 3-5%: with one P, the Python
  thread returning from C waits for the reader goroutines. Raising it changes every
  pythago program's scheduling, so it needs the user's go-ahead. Measured since
  (bench/http, one core): it makes `asgi` and `wsgi` slower almost everywhere (`asgi`
  hello c64 56.2k → 50.2k, 16 KiB upload 36.0k → 17.3k; `wsgi` 64 KiB responses 41.2k →
  9.4k), so as a global default it would hurt the servers; at most an aio-only choice.
  The wsgi slowdown is the GIL/P convoy (journal 2026-11-18), not parallelism.
  — *source: journal 2026-11-17, read credits; journal 2026-11-17, HTTP server benchmark*
- [ ] **GIL/P convoy in `py` (wsgi, and any Do-heavy server):** a goroutine waiting for
  the GIL in C keeps its P; the GIL's next holder returns from C without a P and sleeps
  holding the GIL until sysmon retakes one, so the core idles. (a) deferred known-length
  writes done (64 KiB at GMP=2: 9.2k → 37.1k); (b) slot priority for Release done
  2026-11-18 but changes no throughput (it only cuts OS threads 75 → 26). What works is
  (c1), a 1-token GIL gate so at most one Do/Release goroutine waits in C (streamed 64 KiB
  at GMP=2 9.8k → 31.9k, 200 µs + 64 KiB at GMP=1 2.6k → 4.4k; 1 KiB at GMP=2 −8%,
  1 KiB-chunk p99 60 → 138 ms); prototype in `.agents-workspace/tmp/r3/convoy/gate/`,
  user decision 2026-11-18: not now, keep (b). — *source: journal 2026-11-18, wsgi GIL/P convoy; journal 2026-11-18,
  mitigation (b)*
- [ ] **`wsgi` over TLS: whole-run stalls, then never again (not conntrack):** `pythago
  wsgi` over TLS at the default GOMAXPROCS completed 0 requests with 0 errors in 1-2 s
  windows, 4 times in the first 108 runs (2026-11-17, 19:25-19:30 JST); none in about 620
  TLS runs since. Each hit every connection of one httpload process for 1.5-3 s and
  ended without an error. Not a full conntrack table: the kernel log has `table full`
  lines at 17:46 and 22:28-22:30 (both TestSpawn loops) and none in between. The cause is
  unknown. A trap (`.agents-workspace/tmp/tlsstall/trap.py`: fresh server per cycle, the
  smoke run's loads, gdb + goroutine dumps when the server's I/O stops for 0.3 s) ran 300
  cycles (1,800 runs, 23:07-23:53 with the host loaded): no stall. `bench/http` records
  SYN retransmits, connect times, other processes' CPU on the server core and
  `conntrack_pct`, so check those when it recurs. — *source: journal
  2026-11-17, HTTP server benchmark; journal 2026-11-17, conntrack table*
- [ ] **gdb and the extension shim's name (Linux), provisional fix in:** the shim used to be
  dlopened as `/proc/self/fd/N` from a memfd closed right after, and gdb opens that name
  in its own process, finds its own pipe and blocks forever with the target stopped.
  `py/internal/extshim` now loads it from a file in a new temporary directory, removed
  once loaded (link map: `/tmp/pythago-shim-*/libpython-pythago.so`, gdb only warns);
  `TestExtensionShimLinkMapName` holds it. The source is configurable:
  `Config.ExtensionShimDir` (no fallback when set) and `Config.ExtensionShimMemfd`
  (`TestExtensionShimSource`). A memfd, the default's fallback when no temporary
  directory is writable or the one the option asks for, still hangs gdb. Permanent
  options: keep the memfd open and load it as `/proc/<pid>/fd/N` (gdb then reads the
  shim too), or rename the link map entry after loading. Workaround for old binaries:
  gdb `set sysroot DIR` with only `lib`, `usr` and `home` symlinked in DIR. — *source:
  journal 2026-11-17, gdb and the extension shim; journal 2026-11-18, load source*
- [ ] **Go-built read-only containers (user idea, 2026-11-17):** a read-only sequence or
  mapping type in the glue over a C array of the values Go describes (as for
  `pythago_py_build`), materializing each item on first access (cached; atomic on 3.14t),
  so items an application never reads are never created. Not for WSGI `environ` or the
  ASGI `scope` and header lists (built-in dict/list required by PEP 3333, frameworks
  mutate them, `json` needs real dicts); candidates are pythago's own event tuples and
  request records and Go channel values (the borrowed keyword view landed as `Kwargs`,
  2026-11-19). Not
  for the asgi path: Python reads every item pythago passes there; the bound is about
  60 ns, 0.4% of a request (measured 2026-11-18). Measure first for the remaining
  candidate (channel values). — *source: user, 2026-11-17*
- [ ] **`cryptography` shim (later, user request 2026-11-17):** a module providing the
  pyca/cryptography API (the subset applications use: hazmat primitives, x509, Fernet)
  backed by Go's crypto, for where the native wheel cannot load (C extensions on macOS are
  not supported yet; Windows) and alongside Go-side TLS. Scope and API coverage to be
  decided. — *source: user, 2026-11-17*
- [ ] **Native amd64 hardware runs** (CI): amd64 has only run under qemu.
- [ ] **GitHub Actions:** `ci` failed on every job of c85fb11: anonymous api.github.com
  calls got 403 (shared-runner rate limit) and `go: downloading` lines counted as vet
  findings. Fixed in the tree (token to api.github.com only, `permissions: contents: read`,
  `go mod download` before vet), unverified until the next push. `gpu-nightly` waits for a
  `[self-hosted, linux, gpu]` runner, which is not registered. — *source: journal 2026-11-17, tackle-todos*
- [ ] **M5 rest:** darwin C extensions (done 2026-11-18, startup cost open), then Windows (pure Python first; bao rejects bigobj
  COFF, so count the lowered `libpython.obj`'s sections first). darwin pure Python passes
  `ci/darwin.sh` since 2026-11-17. For Windows, bao v0.0.8 (#42) has
  `-windows-dll` / `build.Options.Windows.DLL`, the counterpart of `-darwin-dylib` with
  `-wrap` on the DLL's imports: a shared-mode path from PBS's `python3X.dll`, not yet run
  on Windows.
  **Windows, 2026-11-18:** extract-and-load decided; `py` ported and passing under
  Wine 11.22 on windows/arm64 (3.13, 3.14, 3.14t; `ci/windows-wine.sh gil|ft`). Remaining:
  - (done 2026-11-18: `aio`, `asgi`, `wsgi`, `workers` pass under Wine);
  - Wine does not let os.Root refuse symlink escapes (asgi PathSendRoots sub-case skipped
    there): check on Windows itself;
  - Windows CI jobs (windows-11-arm, windows-2025, Server Core on windows-2025) are
    written but have not run: they need a push; windows/amd64 (prebuilt DLLs, layouts) is
    first exercised there;
  - check that a real console Ctrl+C reaches os/signal with `PythonSignals` off;
  - (done 2026-11-18: `Config.ImageCacheDir`, stale images removed under an in-use
    marker); confirm the marker's NTFS share-mode behaviour on Windows itself (the CI
    jobs run `TestImageCache*`).
  — *source: journal 2026-11-18, Windows port of py*
- [ ] **Only if VTs are revived** (frozen; see the 2026-11-15 decision):
  - Locks held by a VT deferred during a nested run still deadlock a pinned waiter. Needs Go
    callbacks off the carrier stack, with C-API calls forwarded to the VT.
  - GC cost scales with live thread states: share heaps per carrier, bound live thread
    states.
  - Fail-closed thread-state recycle and handle-table identity, before turning pooling
    back on.
  - Save and restore the OpenSSL error queue per VT, don't park inside foreign C
    callbacks, use ≥ 64 KiB stack guards.
  - Socket edge cases: sticky O_NONBLOCK, MSG_WAITALL, short sends, one dup per wait.
  - amd64 context switch, darwin validation.
- [ ] **Remove the experimental combiner before a release:** the glue's `pythago_comb_*` functions and `value_new`'s NULL-arena path that only they use (and `py/spike_combiner.go`), unless the combiner design is revived. *source: JOURNAL "A C-resident combiner is slower than combining from Go"*
- [ ] **Finalize hang after asgi tests on darwin/amd64 3.14t (seen once, old binary):**
  not reproduced in 890 runs of the current tree (2026-11-18). `internal/testwatch` now
  fails a hung Init or Finalize after a bound with native threads and goroutines, so a
  recurrence shows up as a tier failure. Hypothesis: finalization waited on a lock held
  by a daemon thread parked in `PyThread_hang_thread`. Reading darwin samples needs
  libpython symbol names (bao-local `bao_<hash>_local_N`): a symbol map from bao, if
  wanted. Evidence: `.agents-workspace/tmp/r3/maclhang/`. — *source: journal
  2026-11-18, Finalize hang; journal 2026-11-18, TestMain watchdog*
- [ ] **Wine 11.22 arm64 regression zeroes V16-V31 on SetThreadContext without
  CONTEXT_FLOATING_POINT (reported 2026-11-20):** Wine commit `5484caec5e` (MR !12587), still at
  master 2026-11-18; 11.21 is clean and a one-hunk revert fixes it (journal 2026-11-19).
  It caused the lost asgi chunk under Wine. (a) Reported to WineHQ Bugzilla (ntdll) by
  the user on 2026-11-20 from `.agents-workspace/tmp/r5/winechunk/WINE-BUG-DRAFT.txt`;
  watch it for a fix. (b) The
  Wine tier is pinned to 11.21 (`ci/windows-wine.Dockerfile`); move past 11.22 only to a
  release that fixes it. — *source: journal
  2026-11-19, Wine 11.22 regression*
- [ ] **asgi per-core gap:** inline asgi does 58.5k req/s against uvicorn's 72k on one core (87% with 4 processes); the remaining cost is mostly net/http's per-request user time. *source: JOURNAL "Spawned worker processes for asgi"*

Added by good-sleep on 2026-11-17 (journal Work summary, 2026-11-14 to 11-17):

- [ ] **Free-threaded (3.14t) inline scaling:** 2.88× on 4 cores after interning,
  per-worker scope values and recycled requests (2026-11-18: hello 174k → 204k req/s;
  212.5k and 25.8 allocs/req after the dispatch arena; uvicorn 4.26×, net/http 3.34×).
  Measured 2026-11-18 and not adopted: idle-worker dispatch (0.999-1.002 at 2 and 4
  cores). GOMAXPROCS 2N on N cores: +5.1% at 4 cores, +7.9% at 2, −8% at 1 (gil −6%),
  p99 +40% at 4 cores; most of it is the spare P (keeping `Workers` = cores keeps the
  throughput and about 60% of the p99). Options, each a default change for the user:
  `Workers` defaulting to the core count with a spare P; a C-side respond queue. Also:
  a GOGC note (GOGC=800 measured +8-13% before). `scope["asgi"]` stays one dict per
  worker (user decision 2026-11-18). — *source: journal 2026-11-18, 3.14t inline asgi;
  journal 2026-11-18, idle-worker dispatch*
- [ ] **Borrow arena leftovers (steps 1-4 landed 2026-11-19/20; design
  `.agents/docs/arena-design.md`):** the unexplained LoopThread hello cost on 3.13 after
  step 3 (ratio 0.989); a test that catches SharedLane publishing a chunk before its
  counter (control S2 passes today); wsgi's stray allocation in `write`'s `UnsafeBytes`
  (C should have decoded the argument), `held []segment` per use, and the rest of its 12
  allocations per request. — *source: journal 2026-11-20, asgi requests on arena scopes;
  journal 2026-11-20, wsgi requests on arena scopes*
- [ ] **OwnedBuffer and body-path leftovers (lease costs cut and the inline first chunk
  moved to the request's lane, 2026-11-20; design `.agents/docs/arena-design.md`
  section 8):** LoopThread mode still reads the first chunk with `readChunk`, because
  reading to the declared length there raised 100 B throughput 6-8% but doubled p99
  (ft 5.9 to 11.7 ms), unexplained; each Go→Python aio buffer message still makes 2 owned
  Objects (the view, and aio's result tuple from `py.From`). Not done, by decision: a
  memoryview request body (ASGI requires `bytes`, and the prototype was no faster).
  — *source: journal 2026-11-20, OwnedBuffer leases*
- [x] **Go-owned `bytes` (the fusion): landed as an opt-in, `py.Config.FusedBytes`
  (2026-11-20).** Allocator `internal/fusion` (per-chunk Go allocations, released when
  idle), glue hook and reader check, `asgi` bodies; `.agents/docs/arena-design.md`
  section 15. Linux tiers passed (`ci/unit.sh gil` and `ft`, arm64 and amd64 under qemu);
  all 12 `glue/prebuilt` objects rebuilt and reproduced.
  - [x] **Gate (2026-11-20, gate 9):** every tier passed on the fixed tree: `ci/unit.sh`
    gil and ft, `ci/windows-wine.sh` gil and ft, `ci/darwin.sh` gil and ft on arm64 and
    amd64, each with its fusion passes. Gate 8 found the Windows ft startup-check deadlock
    under `py.Main` from `init()` (journal 2026-11-20), fixed in `py/fusion.go`.
  - [ ] **Unexplained ~2% (90 ns) on 100-byte inline uploads against proto18,** in process
    (5020 against 4930 ns/op, gil core 7); not the main-interpreter check (measured
    without it). GIL `FreePath/inst` +3% against proto18, of which about 2 points appear
    with no hook installed (binary placement); no `perf` access on the host to count
    instructions.
  - [ ] **Free-threaded, up to `idleKeep` (2) empty chunks stay held** until some
    collection gives them a grace: the sweeper runs one only when more wait. With
    `gc.disable()` `PyGC_Collect` does nothing, so chunks change class or go back only
    after an explicit `gc.collect()`.
  - [ ] **A LoopThread post never converted (the loop closed) keeps its slot,** as a lease
    keeps its request.
  - [x] **`py.Main` honors `-X tracemalloc[=N]` (2026-11-20).** It sets
    `config->tracemalloc` back to -1 after the command line, so CPython reads the option.
    `PYTHONTRACEMALLOC` stays ignored, as in `python -I`. Tests:
    `TestSysExecutableTracemalloc`, and `TestFusedBytesTracemallocAtInit` now uses the
    real flag (the test-only `initTracemalloc` hook is gone).
  - [x] **`py.Main` honors `-X dev`, `-X faulthandler` (3.13), `-X perf`/`perf_jit` and
    `-X int_max_str_digits` (2026-11-20).** `pythago_config_new` preinitializes from the
    command line, which decides dev mode and the allocators' debug hooks, and
    `pythago_config_set_argv` resets the fields the isolated config pins. Tests:
    `TestSysExecutableXOptions`, and `TestFusedBytesDevMode` for the fusion over the debug
    hooks.
  - [ ] **Later:** a public `FusedBytes` type; fused `wsgi` bodies and `aio` channel
    `bytes`; bodies over 64 KiB; 3.15 support after `TestFusedBytesStaleReader` passes
    against its headers (section 13's per-release checklist).
  - *source: journal 2026-11-20, the fusion landed as an opt-in*
- [ ] **Free-threaded vLLM** once abi3t (3.15) or cp314t wheels exist. — *source: "vLLM check"*
- [ ] **`torch.compile` and vLLM without a disk stdlib:** they need Python's C headers, which the embedded stdlib lacks; possibly intentional (use `PYTHONHOME`). — *source: "M2 (product layout, `pythago-dist`, stdlib from `fs.FS`) done"*
- [ ] **libffi closures on 16/64 KiB-page arm64 kernels** under bao's `-linux-max-text-alignment` cap: unverified. — *source: "Spike conclusions" / Accepted risks*
- [ ] **Private CPython API on 3.15:** re-check `_init_main` and the internal headers the glue uses (thread hooks, stats). — *source: "Spike conclusions" / Accepted risks*
- [ ] **macOS extension shim, after the lazy load (2026-11-18):** the shim loads at the
  first extension import, dlopen of a library not loaded yet, C API lookup through ctypes,
  subinterpreter creation or fork (pyrun empty script and `import ctypes` 0.043 s, were
  0.40 and 0.38 s). Done 2026-11-19: a failed load looks like stock ctypes (`hasattr`
  False, cause on attribute/item lookups); non-Py names by attribute or item through
  `CDLL(None)` don't load it; a subclass shadowing `_handle` loads it eagerly. **User
  decision:** the first-load cost is dyld's library validation (`F_CHECK_LV`), not ours; a
  patched-trie constant-content shim (scratch prototype,
  `.agents-workspace/tmp/r5/shim3/exp/proto/`) cuts it to 0.24 s with a new file each time
  (A) or about 3.5 ms with a persistent cache file dyld loads (B: location, config and
  trust rule to decide; would allow loading eagerly again and retiring the lazy hook).
  Left: non-Py names through `in_dll`, `_FuncPtr`, prototypes and `dlsym` still load it;
  the cause is attached only on attribute/item paths; first fork pays the load; Go/purego
  loading of a Py*-binding library must trigger the load itself; `PY_TIMEOUT_MAX` not
  exported (outside `^_?Py`, Linux too); no lib-dynload. The hook relies on ctypes's
  `CDLL.__getitem__`, `_handle` and `CDLL._load_library`: re-check on each CPython bump.
  Executable exports would need Go linker or bao work. — *source: journal 2026-11-18,
  macOS shim loads lazily; journal 2026-11-19, macOS shim failed load*
- [ ] **Subinterpreters, leftovers (done 2026-11-19 via a frozen `encodings` stub):** on
  Windows a subinterpreter's sys.path entries become absolute (`Z:\pythago-fs\...`), imports
  still work; files of 2 GiB or more and non-UTF-8 paths are left out of the
  subinterpreter index; the arena path for deflated zip entries and `Config.Stdlib`
  mounts has no test. If subinterpreters ever reach Go, `Do` needs an interpreter check.
  — *source: journal 2026-11-19, subinterpreters*
- [x] **`GoEventLoop` has `add_reader`/`add_writer` (2026-11-21).** It keeps a GoSelector
  whose watchers post `EV_READY` into the loop's completion queue; `zmq.asyncio` runs on
  every loop and in ASGI apps. The `sock_*` methods are still `BaseEventLoop`'s
  (`NotImplementedError`). — *source: journal 2026-11-21, GoEventLoop readers and writers*
- [x] **Free-threaded + fusion SIGSEGV in `TestMixedThreads` (2026-11-21, fixed in the
  glue, not bao).** `pythago_py_enter` stored the Do thread state under `owned_key` before
  `pythago_py_started` had created the key: still 0, which is the native image's TLS key
  (bao). The fusion's startup check enters Python that early, on a goroutine's OS thread
  that then goes back to Go's pool; once bao's lookup cache missed on it, its TLS went to
  the thread state's memory. The key is now created on first use; regression test
  `TestThreadStateKeyNotNativeTLS`. The "a thread that ran a Python thread must not be
  reused" rule behind `runThread` came from this bug's crash rates: the reusing design
  passes with the fix; `runThread` stays (below).
  — *source: journal 2026-11-21, the TestMixedThreads SIGSEGV was the glue's TLS key*
- [x] **`runThread` vs the earlier keep design (2026-11-21): keep `runThread`** (the
  user's decision). Do threads stay alive (pyreqwest); a Python thread's OS thread exits
  at its end, as with libc. — *source: journal 2026-11-21, `runThread` kept*
- [ ] **CI follow-ups (2026-11-21):** the next run must show:
  - windows/arm64 `TestTLS` passing (echo pattern fixed);
  - darwin amd64 `TestIOProxyConcurrent` passing (peak in-flight check);
  - `TestThreadStateFreedAtThreadExit`: failed on Windows only, twice (arm64 gil in run
    44732288299, amd64 Server Core ft in 44853895032). It now waits 30 s, keeps off the
    main thread, prints deletions slower than 5 s to stderr and says on failure whether
    the OS thread still exists: read that from the next failure or stderr line.
  - aio `TestPyreqwestThreads` under Wine ft: once "invalid HTTP version parsed"
    (GoSelectorEventLoop, multithreaded) in gate 20, then 30/30; watch whether it recurs.
  - the first run on `ubuntu-26.04`/`-arm` runners (caches start cold; the other
    architecture is built, not run: `PYTHAGO_OTHER_ARCH=build`).
  - workers `TestSpawn` on native Windows: it now waits for the killed worker to be gone
    (run 44856520533 lost a request to it); not reproducible under Wine.
- [ ] **workers on Windows: `socketListener.Accept` returns AcceptEx's `ERROR_NETNAME_DELETED`
  and `WSAECONNRESET`** (a client reset before the accept completed), which Go's own accept
  retries (`internal/poll` `acceptOne`). `http.Server.Serve` stops on them (on Windows
  `syscall.Errno.Temporary` covers neither), so a worker would exit. Not seen failing; retry
  them in the loop. — *source: journal 2026-11-21, CI 44856520533*

  — *source: journal 2026-11-21, CI runs 44732288299, 44784093416 and 44853895032*
- [ ] **types-pythago, user decisions (2026-11-21):** publishing to PyPI; running
  `ci/typecheck.sh` in GitHub Actions (it needs uv and network access); a LICENSE copy in
  `typing/`, since the sdist carries none; checking with pyright too (only mypy 2.4.1 is
  run). — *source: journal 2026-11-21, type annotations and types-pythago*
- [ ] **Native Windows CI runs the wheel tests for the first time:** pyreqwest and pyzmq
  passed only under Wine arm64 (pyzmq skipped there: libzmq's `mrs pmccntr_el0` is an
  illegal instruction under Wine, stock CPython too). The amd64 and arm64 runners, and
  Server Core, are untested with the wheels. — *source: journal 2026-11-21, wheel tests*
- [ ] **Upstream: pyreqwest (mimalloc) crashes when first imported on a thread that
  exits** (stock CPython 3.14t, 10/10; 0/10 from the main thread): mimalloc keeps the
  thread it initialized on as its main thread by thread pointer, and a later thread on
  that cached stack shares the main heap. pythago no longer exits Do threads (journal
  2026-11-21), but an app that first imports such an extension on a Python thread (an
  ASGI worker, then `Server.Close` and a new server) still hits it. Report to pyreqwest
  (or mimalloc) if the user wants. — *source: journal 2026-11-21, wheel tests*
- [ ] **darwin in GitHub Actions:** jobs `darwin` (macos-26, macos-26-intel; gil, ft)
  and `darwin-glue` added 2026-11-18 via `ci/darwin-gha.sh`; unverified until a push
  (`darwin-glue`'s SDK comes from Xcode, the prebuilt's from the Command Line Tools).
  — *source: journal 2026-11-18, darwin tiers get GitHub Actions jobs*
- [ ] **AOT-translated small extensions** (only if pursued): ask the bao agent for .so delinking with per-component namespaces, deferred constructors and C++ unwinding. — *source: "Spike conclusions" / Dependencies on bao*
- [ ] **`py` API growth:** iteration, contexts, types (buffers are done); possibly stale. — *source: "Spike conclusions" / Next milestones*
- [ ] **Embedded wheels extracted to a cache directory** as an extension site source; deferred in PLAN M3 until there is demand. — *source: "Spike conclusions" / Decided*
- [ ] **`py` thread start+join gap:** about 25–45 µs over stock on 3.13, noisy on 3.14t with blocked threads; explicitly not chased further. Low priority. — *source: "`py` thread start/join handshake moved to C, plus spare threads"*
- [ ] **Only if VTs are revived**, in addition to the list above: SimpleQueue's single oldest-first wake to a pinned getter; finalizers in pool-mode `tstate_free` running carrier-blocking; "fast" pinned versus "safe" forwarded Go callbacks. — *source: "PyMutex handoff audit", "deferred-VT lock residual"*
