# Long-Term Memory Index

Durable project knowledge, by topic. `../JOURNAL.md` is the chronological log; the
`good-sleep` skill consolidates its entries into documents here. Open work is in
`../TODO.md`, not here.

## Topic documents

Consolidated from the journal's Work summary (2026-11-14 to 2026-11-17).

| Document | Summary |
|----------|---------|
| [feasibility-and-threading-decision.md](feasibility-and-threading-decision.md) | The spike verdict (feasible on linux arm64/amd64, 3.13 and 3.14t): what S1–S5 proved, the decisions, spike-to-product mapping, bao dependencies, accepted risks, and why threads are goroutine-backed 1:1 with VTs frozen |
| [virtual-threads-research.md](virtual-threads-research.md) | The frozen M:N VT design: the BRC refcount fix (`carrier_brc_owner`), PyMutex handoff audit, pinned VTs with nested runs, the unfixable deferred-VT lock cycle, svt reproducers and revival conditions |
| [py-runtime-and-threads.md](py-runtime-and-threads.md) | Package `py` on goroutine-backed threads: `Do`/`Release` and admission, one thread state per OS thread, the C start/join handshake, spare threads, and the M1 runtime hardening (`Run`, `OnMain`, Environment, multiprocessing, signals) |
| [go-c-ffi.md](go-c-ffi.md) | The Go↔C boundary as built: cgocall + asm thunk (`call0`…`call8`, `cframe`), crosscall2 callbacks with C-decoded arguments, the `uintptrescapes` and no-stack-memory rules, `pythago_py_build`/`pythago_py_call`, the glue ABI check, immortal singleton results, zero-copy buffers, call counts and costs |
| [image-build-and-toolchain.md](image-build-and-toolchain.md) | `pythago-dist`: shared mode (default, translates PBS's `libpython.so`) vs LTO mode, prebuilt reproducible glue, bao in process and LLVM provisioning, the `fs.FS` stdlib importer, bao pin history; no prebuilt `.syso`, consumers need only Go |
| [glibc-floor-and-amd64.md](glibc-floor-and-amd64.md) | The glibc 2.18 floor and symbol-version cap, allowed above-cap weak imports, LTO needing 2.30+, the amd64 port under qemu and its bao gaps (#18–#20), three bao bugs below glibc 2.34 (#34–#36), the qemu maps shim |
| [ci-and-test-image.md](ci-and-test-image.md) | The CI tiers (`ci/lib.sh`, `unit.sh`, `floor.sh`, `gpu.sh` with its stock-Python control, `glue.sh`, sysroots, old-glibc runs), the workflows, and the `.work/testimage` module joined by `.work/go.work` |
| [environments-and-ml-stacks.md](environments-and-ml-stacks.md) | Environments via a stock PBS Python (`pythago-env`, `Config.Environment`), PyTorch+CUDA and vLLM results, the `py` fixes they needed (atfork, `sys.executable`/`IsPythonCommand`/`Main`, signal restore, UTF-8 mode) |
| [aio-event-loop.md](aio-event-loop.md) | The Go-backed asyncio loop (`aio`/`GoEventLoop`): design on `py.Release`, the port's three bugs (lost wakeup, blocking pidfds, DNS errors), BufferedProtocol, buffer reuse that changed nothing, inline reads that did |
| [bao-tls-and-uvicorn-parity.md](bao-tls-and-uvicorn-parity.md) | Why uvicorn on pythago was 29% behind stock (bao's TLS thunk), how it was profiled, bao #32/#33 reaching stock speed, opt-in `-static-tls` and its fallback |
| [asgi-server-protocol.md](asgi-server-protocol.md) | asgi as an http.Handler: workers, response actions and profiling; trailers and field rules (the NUL-packing injection hole); WebSockets same-origin by default; RFC 8441 via a stand-in hijack; pathsend/sendfile proven with strace |
| [asgi-inline-and-flat-combining.md](asgi-inline-and-flat-combining.md) | Why a per-request thread handoff loses to uvicorn on one core; flat combining vs GIL handoff and a C-resident combiner; inline requests as eager tasks under a worker lock (`aio.Hooks`), the release leak, fidelity limits, 58.5k vs 72k; inline is the default, `LoopThread` opts out |
| [wsgi-server.md](wsgi-server.md) | `wsgi` as an http.Handler on `py.Do` (slots are the thread pool): the latin-1 packed environ and its NUL hole, Go-side head validation, flushing, file_wrapper/sendfile, wsgiref and Flask comparisons, 21 mutation controls, 2.7× gunicorn gthread on one core |
| [asgi-worker-processes.md](asgi-worker-processes.md) | Package `workers` (moved from asgi): `workers.Spawn` for any handler, spawn not fork, listener as fd 3 plus `PYTHAGO_WORKER`, restart backoff and `MaxEarlyExits`, SIGTERM/SIGKILL, Pdeathsig from a locked thread, per-worker GOMAXPROCS; 234k vs uvicorn's 268k on 4 cores |
| [project-status-and-agent-process.md](project-status-and-agent-process.md) | Milestone status (M1–M4 done, M5 partly), journal conventions, spikes archived in `.agents/attic/spike`, and the agent harness (AGENTS.md, `.agents/docs`, skills) |

## Lessons

Moved verbatim from the journal's former "Durable lessons" section (2026-11-17).

| Document | Summary |
|----------|---------|
| [building-cpython-for-bao.md](building-cpython-for-bao.md) | Turning PBS archives into a bao image: LTO lowering of bitcode members, which objects to take, glue and link pitfalls |
| [embedding-cpython.md](embedding-cpython.md) | The Go↔C boundary: no variadics, the glue symtab, uintptrescapes and stack rules, versioned shared structures, ownership across two memory managers |
| [free-threaded-cpython-under-mn.md](free-threaded-cpython-under-mn.md) | Per-OS-thread ownership in free-threaded CPython (ob_tid, mimalloc, BRC) and what it forbids for M:N |
| [performance.md](performance.md) | Localize before optimizing, fixed per-call costs, avoiding round trips, Go scheduler slots held in C, benchmarking setup |
| [hardening-network-facing-code.md](hardening-network-facing-code.md) | Rules for asgi and aio: reject loudly, mutation-test every defence |
| [waker-and-lifecycle-safety.md](waker-and-lifecycle-safety.md) | Wakers, generations and type-stable structs: avoiding use-after-free on asynchronous wakeups |
| [isolation-verdict.md](isolation-verdict.md) | Why all VTs on a carrier are one trust domain |
| [debugging-techniques.md](debugging-techniques.md) | Reproducers, bisection by mechanism, reading the exact CPython source, diagnostics that worked |
| [design-lessons.md](design-lessons.md) | Design rules: match the embedded runtime's assumptions, admission, baselines and controls, sys.executable, cross-arch ports, compatibility floors, batching without losing semantics |
| [process-lessons.md](process-lessons.md) | Working rules: the bao agent, reverting failed experiments, reviews, shell pitfalls |
