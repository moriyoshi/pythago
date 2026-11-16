# Feasibility Verdict and the Threading Decision

## Summary
The 2026-11-14/15 spikes proved that CPython embeds in a `CGO_ENABLED=0` Go binary through bao
and python-build-standalone (PBS), on linux/arm64 and linux/amd64, for the GIL (3.13) and
free-threaded (3.14t) builds. The verdict (concluded 2026-11-15) was "feasible, build the
product". The same day the threading model was fixed: each Python thread is a goroutine
locked to its own OS thread (1:1). Virtual threads (VTs, M:N) are frozen as a documented
experiment.

## Key Facts
- **Verdict: feasible.** Static embedding, FFI, stdlib from `fs.FS`, unmodified manylinux
  wheels and free-threaded parallelism all work on both architectures and both ABIs.
- **Mechanism:** bao address mode plus purego. One bootstrap; C glue for PyStatus/PyConfig,
  the symbol table (`pythago_symtab`), thread hooks and the trampoline. Go never reads
  CPython object layouts.
- **Threading:** goroutine-backed 1:1 threads in package `py`. `pthread_create` inside
  libpython is routed (`--wrap=pthread_*`) to a hook that starts a goroutine locked to a
  fresh OS thread. Details: [py-runtime-and-threads.md](py-runtime-and-threads.md).
- **VTs are frozen.** They worked mechanically but caused a chain of structural deadlocks.
  The last one (deferred-lock) is unfixable without forwarding Go callbacks off the carrier
  stack. The gain over OS threads was small. Details:
  [virtual-threads-research.md](virtual-threads-research.md).
- **Invariant: at most one Python thread state per OS thread.** Free-threaded 3.14 merges a
  detached owner's refcounts on its behalf, so a second thread state on one OS thread
  corrupts refcounts.
- **M:N benefits come from elsewhere:** blocking work done in Go (the `aio` loops over the
  netpoller), admission slots for goroutines entering Python (`Config.Slots`), and a thread
  cap that fails clearly (`Config.MaxThreads`).
- **Allocator stays native.** Extensions load by `dlopen` through the SHN_ABS shim.
  UTF-8 mode is on. `sys.executable` is the program itself. GIL and free-threaded builds
  ship as separate variants.

## Details

### What the spikes proved
The spikes live in `.agents/attic/spike/` (see
[project-status-and-agent-process.md](project-status-and-agent-process.md) for how to run
them).

| Spike | Result |
|---|---|
| S1 static embedding | PBS CPython 3.13 `pgo+lto-full`, LTO-lowered into one object (about 31k sections) and translated by bao into `.syso` objects. Runs in a 26 MB `CGO_ENABLED=0` binary whose only dynamic libraries are libc-family. sqlite, ssl, ctypes and libffi closures, asyncio, subprocess, decimal, zstd (3.14) and threads work. Built with `.agents/attic/spike/s1/build.sh`. |
| S2/S3 FFI | One glue `pythago_symtab` gives Go every C API function and data address (1748 symbols). A single `purego.NewCallback` dispatcher serves as a METH_FASTCALL method, with no C trampolines; errors and panics become Python exceptions. Called from 16 Python pthreads and 8 goroutines. |
| S4 stdlib from `fs.FS` | Two-phase init plus a meta-path finder imports the whole stdlib from a Go `fs.FS`. Tracebacks, `inspect`, `importlib.resources` and `pkgutil.get_data` work. |
| S5 unmodified wheels | A code-free in-memory ELF of SHN_ABS symbols, dlopen'ed from a memfd, lets manylinux wheels load: numpy with threaded OpenBLAS, cffi with callbacks, markupsafe. Works on both architectures and both ABIs (cp313, cp314t). Only `Py*` symbols are exported, so vendored OpenSSL and friends never collide. |
| Free-threaded | 3.13t and 3.14t pass all spikes. The GIL stays off. Parallel speedup 7x on 8 cores (arm64) and 4x on 4 (amd64, emulated). Go callbacks run in parallel. |
| Threading | Each Python thread on its own goroutine-locked OS thread. Start/join handshake in C; spare threads make thread start roughly as fast as stock on 3.14t. Lock behaviour matches stock. |
| linux/amd64 | Works. The TLS-model risk did not materialise (local-dynamic only). |

Python 3.14 specifics found by the spikes:
- 3.14 removed `_Py_InitializeMain`. Init calls `Py_InitializeFromConfig` a second time with
  `_init_main=1` (public API).
- 3.14 adds a zstd dependency.

Other early experiments, with their outcome:
- **pymalloc arenas from the Go heap:** they work on GIL builds and the Go pacer sees them.
  Not adopted: free-threaded builds allocate from per-thread-state mimalloc heaps, out of
  Go's reach. The allocator stays native.
- **Go-backed asyncio loops** (`.agents/attic/spike/goaio`): `GoEventLoop` is proactor-style
  over `net.Conn`; `GoSelector` is readiness-based over the netpoller. These became package
  `aio`.
- **Extensions by AOT translation through bao**, with an extracted temp-file fallback, was
  the first plan. It was replaced by `dlopen` through the SHN_ABS shim (below).

### Decisions
- **bao address mode plus purego.** Go reaches every C symbol through the symtab; C glue
  handles PyStatus/PyConfig, thread hooks and the trampoline.
- **Goroutine-backed 1:1 threads; VTs frozen** (agreed with the user, 2026-11-15). See the
  next section.
- **Allocator stays native** (see above).
- **Extensions load by `dlopen` through the SHN_ABS shim:**
  - from an external site directory, needed for large stacks: PyTorch + CUDA works
    unchanged on 3.13 and 3.14t, including DataLoader workers and `torch.compile`; vLLM
    (generation, embeddings, the OpenAI server) works the same way on the GIL build;
  - the site directory must be a full environment: console scripts and tools (ninja) on
    PATH.
  - Ahead-of-time translation through bao is only an optimisation for small extensions. It
    needs bao features: per-component namespaces, deferred constructors, C++ unwinding.
- **UTF-8 mode** is on (PEP 540). An isolated configuration would otherwise leave text I/O
  in the C locale's ASCII.
- **Processes:** `sys.executable` is the program itself. A program starts with
  `if py.IsPythonCommand() { os.Exit(py.Main(cfg, os.Args)) }` (or uses `py.Run`, which
  does this), so multiprocessing spawn/forkserver and `subprocess.run([sys.executable, ...])`
  work. Forked children run without Go: threads use libc, and Go functions raise.
  `signal.signal(sig, None)` restores Go's handler.
- **GIL and free-threaded builds ship as separate dist variants.** Wheels must match the ABI.

### Why goroutine-backed 1:1 threads
The original intent was goroutine-backed threading. VTs came from trying to also multiplex
Python threads onto fewer OS threads. Goroutines cannot do that:
- A Python thread lives inside C (the eval loop). Go deschedules a goroutine only in Go code.
  During a C call the goroutine keeps its OS thread, and Go cannot suspend a C stack.
- Free-threaded CPython keys object ownership (`ob_tid`, biased refcounts) and mimalloc heaps
  on the OS thread, so a Python thread cannot migrate either.

Reasons 1:1 is the better design:
- **It matches CPython's assumptions.** Locks, free-threaded ownership, heaps, TLS and C
  extensions all assume one OS thread per thread state. Nothing is emulated.
- **One scheduler.** VTs put a second scheduler under Go's. Every CPython blocking primitive
  became a park-or-block decision. That produced handoff-to-parked-VT deadlocks, pinning,
  nesting, deferral, stop-the-world parking rules, stale wakers and errno clobbering. The
  hazards were structural, so each fix exposed the next. Loom works because the JVM owns the
  whole stack; pythago owns neither CPython nor Go's runtime.
- **Go callbacks are ordinary.** They run on the thread's own stack: no pinning, and the
  deferred-lock deadlock class cannot occur.
- **Isolation.** Per-OS-thread state (the OpenSSL error queue, glibc mutex ownership,
  extension TLS) stays per Python thread. VTs on a carrier formed one trust domain
  ([isolation-verdict.md](isolation-verdict.md)).
- **Portability.** 1:1 needs only the thread-creation hook and works on GIL builds. VTs
  needed a context switch per architecture and a wrapped sem/socket/sleep/pthread surface per
  OS.
- **The measured gain was small** (3.14t, 10k `threading.Thread`s):

  | Backing | Time | Memory |
  |---|---|---|
  | OS threads | about 1.3 s | 503 MB |
  | raw VTs | about 2 s | 315 MB |
  | pooled VTs (unsound) | 1.27 s | |

  In free-threaded builds the dominant per-thread cost is the thread state's mimalloc heaps
  (about 150 KiB), which VTs pay too.

How the 1:1 design gets M:N benefits:
1. **Do blocking in Go.** The Go-backed asyncio loops (`aio`, from the `goaio` spike) serve
   thousands of connections from one Python thread over Go's netpoller.
2. **Admission for goroutine-to-Python calls.** A slot semaphore taken before
   `LockOSThread` bounds OS threads while any number of goroutines call in (the `smn`
   spike; now `py.Do` with `Config.Slots`).
3. **A configurable thread cap that fails clearly** (`Config.MaxThreads`, default 4096),
   plus `debug.SetMaxThreads` tuning. Exceeding Go's limit is otherwise fatal.

### What moved from the spikes into the product
Spike code is reference, not a base. Current locations:

| Spike | Product |
|---|---|
| `py/` | package `py`, same API and tests |
| `.agents/attic/spike/glue/pythago_glue.c`, `pythago_py.c` | `glue/pythago_glue.c`, `glue/pythago_py.c` (plus `glue/pythago_static.c`), embedded in the generator as package `glue` |
| `.agents/attic/spike/glue/pythago_amd64.c` | removed once bao #18–#20 were adopted |
| `.agents/attic/spike/s1/build.sh` (fetch and verify PBS, LTO-lower with LLVM >= the PBS clang, regenerate the inittab, add glue and symtab, run bao, pin import versions) | `cmd/pythago-dist` |
| S4 finder | `py/fsimport.go` plus the C `_pythago_fs` in `glue/pythago_py.c` (stayed inside `py`, not a separate `importer/`) |
| S5 `elfshim` | `py/internal/extshim` |
| `goaio` | package `aio` |
| VT code (`glue/pythago_vt.c`, `pythago_vtio.c`, `svt`) | stays frozen in `.agents/attic/spike` |

The product glue keeps only the four pthread wraps (`__wrap_pthread_create`, `_join`,
`_detach`, `_exit` in `glue/pythago_glue.c`); the VT and sthreads hooks are gone from it.

### bao dependencies as of the verdict
- **Merged and used:** TLS-template and TLSDESC fixes (PRs #5, #6); text-alignment capping
  (`-linux-max-text-alignment`, PR #7); objcopy phase 1 (PR #11: `-wrap`,
  `-redefine-sym(s)`, scoped `-prefix-symbols`, `bao objcopy`), requested by pythago on
  2026-11-14.
- **amd64 workarounds, later replaced by bao PRs #18–#20** (adopted the same day, see the
  journal entry "bao main adopted (#18–#20); amd64 workarounds removed"):
  - **Unversioned imports (PR #18):** imports bind the default (@@) version.
    `-linux-max-symbol-version GLIBC_2.17` caps it. An import with no version under the cap
    stays unversioned and is listed in the manifest as `linux_imports_above_version_cap`.
    bao's own TLS runtime imports `__cxa_thread_atexit_impl` (2.18), which stays unversioned
    under a 2.17 cap. Single-version imports get versioned too, as ld records them. This
    replaced the pinning step in `build.sh` (`cmd/pythago-dist/pin.go`, since removed).
  - **SHN_COMMON (PR #19):** commons are allocated in `.bss.bao.common`. Replaced the
    `OPENSSL_ia32cap_P` definition.
  - **Missing-weak GOTPCREL (PR #20):** biases -4..-8 resolve to a zero cell, and the error
    names the symbol, section and offset. Replaced the zstd hook definitions and the
    `sem_clockwait` wrap. Until then amd64 spike images were built without `NO_VT=1`,
    because the VT I/O wrappers (`__wrap_sem_clockwait` in `pythago_vtio.c`) resolved the
    weak reference; arm64 images were already built with `NO_VT=1`.
- **In review at the verdict (stacked):** #12 localize, keep-global and strip; #13 Mach-O,
  plus a fix for `-wrap` combined with keep-global; #14 COFF; #15 ELF section edits. Their
  later state is unchecked (tracked in `.agents/docs/TODO.md`).
- **Needed for AOT extensions only:** .so delinking with per-component namespaces.

The bao in use has since moved on (in-process `build.Run`, pinned in `go.mod`); see
[building-cpython-for-bao.md](building-cpython-for-bao.md) and `PLAN.md`.

### Accepted risks (not spiked)
- **macOS:** the runtime-generated dylib with absolute exports and its ad-hoc code signature
  are untested (no host).
- **Windows:** pure Python first. Extension support needs export stubs plus IAT patching.
  bao's bigobj limit must be checked against the lowered `libpython.obj`.
- **amd64 performance:** amd64 has run only under qemu emulation.
- **libffi on 16/64 KiB-page arm64 kernels** under the text-alignment cap.
- **Private CPython API in use:** `_init_main`, internal headers in the glue (thread hooks,
  stats). Re-check on each new CPython minor (3.15).

### Milestones as planned at the verdict
1. `pythago-dist` generator and per-target dist packages (linux arm64/amd64, GIL and
   free-threaded), replacing the spike `pyembed`.
2. `py`: `fs.FS` importer and embedded stdlib with precompiled `.pyc` files in `Init`, then
   API growth (iteration, buffers, contexts, types).
3. Extension support in the product: the SHN_ABS shim. Site directory from disk or from
   extracted embedded wheels.
4. CI: native linux/amd64 and arm64.
5. darwin, then Windows (pure Python).

These became PLAN.md's M1–M5. Status: see
[project-status-and-agent-process.md](project-status-and-agent-process.md).

## Files
- `PLAN.md` — the product plan that folds in these conclusions (architecture, milestones,
  private-API register).
- `.agents/attic/spike/README.md` — how the archived spikes are laid out and run.
- `.agents/attic/spike/s1/build.sh` — the spike image builder (with `NO_VT=1` it leaves the
  VT glue out).
- `.agents/attic/spike/{s1,s23,s4,s5,sthreads,smn,salloc,saio,goaio,svt}` — the spikes.
- `glue/pythago_glue.c` — bootstrap, symtab, `__wrap_pthread_*`.
- `glue/pythago_py.c` — `py`'s C side (thread handshake, trampoline, fs importer).
- `cmd/pythago-dist/` — the image generator that replaced `s1/build.sh`.
- `py/thread.go`, `py/runtime.go` — the 1:1 threading and admission in the product.

## Test Coverage
- The product tiers (`ci/unit.sh gil|ft`, `ci/floor.sh`, `ci/gpu.sh`) carry the verdict
  forward; see `AGENTS.md` and `PLAN.md` section 13.
- The spikes do not build in place. Copy `.agents/attic/spike` back to `internal/spike` and
  run `s1/build.sh` into `s1/pyembed` first.

## Pitfalls
- Do not reintroduce a second thread state on an OS thread (pools, anchors, per-call thread
  states). On 3.14t it corrupts refcounts silently; see
  [free-threaded-cpython-under-mn.md](free-threaded-cpython-under-mn.md).
- Do not try to make goroutines multiplex Python threads. Go cannot preempt or move a
  goroutine that is inside C, and free-threaded ownership is per OS thread.
- An extension site directory without console scripts and tools on PATH breaks large stacks
  (torch.compile needs ninja).
- Forked children have no Go runtime: Go functions raise there. Default to forkserver
  (`Config.MultiprocessingStart`).
- Wheels must match the variant's ABI (cp313 vs cp314t).
