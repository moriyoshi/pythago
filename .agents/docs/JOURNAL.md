# pythago journal

Work log. The product plan is `PLAN.md` (repository root); durable lessons are in
`LTM/` (index: `LTM/INDEX.md`) and open items in `TODO.md`. The spike-era design notes and
full measurements are in `~/.claude/plans/i-m-thinking-of-creating-lexical-charm.md`.

## Conventions

- **Append-only, from 2026-11-16 on.**
  - New entries go at the end of this file (the end of "Work summary"), under
    `### YYYY-MM-DD — <the finding, as a sentence>`.
  - Existing entries are not edited. A correction or superseding finding is a new entry
    that names the one it corrects.
  - The curated "Durable lessons" and "Open items" sections moved out on 2026-11-17, to
    `LTM/` and `TODO.md`, which are edited as lessons are learned and items close. The
    `good-sleep` skill consolidates entries into `LTM/`; `reconcile-journal-ltm` may
    remove entries already consolidated there.
- **What belongs in an entry:**
  - the context (who asked for what, and the constraint);
  - the design decision and the alternatives rejected, with the reason;
  - each bug, with the exact error text in a code block;
  - **why the existing tests or checks did not catch it**, and which layer did;
  - the fix, and the **control** that holds it (a sabotage or mutation run showing the
    test fails without the fix);
  - measurements before and after, with the setup (versions, cores, commands) needed to
    repeat them;
  - commits, versions and digests of what was used (bao commits, PBS release, Go
    toolchain, library versions, glue `SHA256SUMS`);
  - what to carry: the rule a future agent should apply.
- **What does not belong:** routine "the gate passed" notes. Scratch artifacts (logs,
  profiles) stay in `.agents-workspace/tmp` (before 2026-11-17, the session scratchpad);
  their names here identify past runs, not permanent files.
- **Peer work:** findings from the bao agent (bao-77) are recorded with its PR numbers and
  commits; pythago never edits bao.

## LTM Consolidation Record

The journal was audited against `.agents/docs/LTM/` and `.agents/docs/TODO.md` on 2026-11-17 (reconcile-journal-ltm): each entry below was checked for its decisions, invariants, file paths, bugs with their error texts, pitfalls, tests and controls, measurements and open work, and gaps were filled in LTM before the entry was removed. The full pre-removal journal is kept locally in `.agents-workspace/tmp/JOURNAL.before-reconcile.md` (git-ignored); git history has only the entries committed before 2026-11-17.

| Section | LTM Document |
|---------|-------------|
| Spike conclusions (concluded 2026-11-15), all subsections | feasibility-and-threading-decision.md |
| 2026-11-14: feasibility spikes (linux/arm64) | feasibility-and-threading-decision.md, virtual-threads-research.md |
| 2026-11-15: refcount corruption on VTs, fixed | virtual-threads-research.md |
| 2026-11-15: PyMutex handoff audit | virtual-threads-research.md |
| 2026-11-15: pinned VTs fixed with nested runs | virtual-threads-research.md |
| 2026-11-15: deferred-VT lock residual, investigated, not fixable as designed | virtual-threads-research.md |
| 2026-11-15: threading model decided: goroutine-backed 1:1; VTs frozen | feasibility-and-threading-decision.md |
| 2026-11-15: package `py` started on goroutine-backed threads | py-runtime-and-threads.md |
| 2026-11-15: `py` thread start/join handshake moved to C, plus spare threads | py-runtime-and-threads.md |
| 2026-11-15: linux/amd64 check under qemu: PASS with three bao workarounds | glibc-floor-and-amd64.md |
| 2026-11-15: PyTorch + CUDA check: PASS after three `py` fixes | environments-and-ml-stacks.md |
| 2026-11-15: vLLM check: PASS after one more `py` fix (UTF-8 mode) | environments-and-ml-stacks.md |
| 2026-11-15: PLAN.md written; M1 (runtime hardening) done | py-runtime-and-threads.md, project-status-and-agent-process.md |
| 2026-11-15: M2 (product layout, `pythago-dist`, stdlib from `fs.FS`) done | image-build-and-toolchain.md |
| 2026-11-15: M3 (environments) done, via the stock PBS Python | environments-and-ml-stacks.md |
| 2026-11-15: bao main adopted (#18–#20); amd64 workarounds removed | glibc-floor-and-amd64.md, image-build-and-toolchain.md |
| 2026-11-15: glibc < 2.18 dropped | glibc-floor-and-amd64.md |
| 2026-11-15: M4 (CI and verification tiers) done | ci-and-test-image.md |
| 2026-11-16: no prebuilt native images | image-build-and-toolchain.md |
| 2026-11-16: consumers need only Go: pythago-dist provisions its toolchain | image-build-and-toolchain.md |
| 2026-11-16 — LLVM leaves the consumer's build: bao translates the shipped libpython.so, in process | image-build-and-toolchain.md |
| 2026-11-16 — `[42, '']`: a Go wrapper without `//go:uintptrescapes` lost an out-parameter | go-c-ffi.md |
| 2026-11-16 — Go-backed asyncio (`aio`) ported onto `py`: three bugs the spike never ran into | aio-event-loop.md |
| 2026-11-16 — uvicorn on pythago was 29% behind stock, and half the instructions were bao's TLS thunk | bao-tls-and-uvicorn-parity.md |
| 2026-11-16 — bao's TLS fixes (#32, #33): pythago reaches stock speed | bao-tls-and-uvicorn-parity.md |
| 2026-11-16 — No pythago binary had ever started below glibc 2.34; three bao bugs in a row | glibc-floor-and-amd64.md, ci-and-test-image.md |
| 2026-11-16 — Zero-copy arrays both ways through the buffer protocol | go-c-ffi.md |
| 2026-11-16 — GoEventLoop reads: BufferedProtocol, buffer reuse that changed nothing, and inline reads that did | aio-event-loop.md, go-c-ffi.md |
| 2026-11-16 — An ASGI server in Go: correct at once, 38% slower at first, near uvicorn after profiling | asgi-server-protocol.md |
| 2026-11-16 — ASGI trailers, and the header-injection hole my own optimization had opened | asgi-server-protocol.md |
| 2026-11-16 — ASGI WebSockets: same-origin by default, and two tests that passed for the wrong reason | asgi-server-protocol.md |
| 2026-11-16 — WebSockets over HTTP/2 through a stand-in hijack, and sendfile proven with strace | asgi-server-protocol.md |
| 2026-11-16 — Work summary: where pythago stands at the end of the day | project-status-and-agent-process.md |
| 2026-11-16 — Journal style: detailed entries, and conventions written down | project-status-and-agent-process.md |
| 2026-11-17 — Go↔C calls halved and Python→Go calls 5× cheaper, without a JIT; a stack buffer handed to C was a latent bug | go-c-ffi.md |
| 2026-11-17 — Refcounts were 5 of 58 Go→C calls per ASGI request; building containers in one C call removed 42 | go-c-ffi.md |
| 2026-11-17 — Go functions read their arguments without calling C; a stale glue image now fails at Init | go-c-ffi.md |
| 2026-11-17 — On one core, net/http plus a thread handoff per request cannot beat uvicorn; the gap is architectural, not FFI | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Running the ASGI app on the net/http handler's own thread reaches uvicorn on one core; the GIL caps it on more | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Flat combining instead of GIL handoff: the GIL build scales to 167k on 4 cores, past free-threaded | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Batching the combiner: the limit was the combiner waiting for a Go P inside callbacks, not the work it did | asgi-inline-and-flat-combining.md |
| 2026-11-17 — A C-resident combiner is slower than combining from Go: a dedicated combiner reintroduces the handoff that flat combining avoids | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Inline requests in asgi: eager tasks under a worker lock; a release path that waited for the loop thread leaked every response | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Returning responses in the batch result was rejected for fidelity; the per-request Python setup was already near its floor | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Argument and callback costs: cached constants, copied small bodies, and shared immortal results; inline asgi at 58.5k against uvicorn's 72k on one core | asgi-inline-and-flat-combining.md, go-c-ffi.md |
| 2026-11-17 — Spawned worker processes for asgi: 4 workers reach 234k req/s (87% of uvicorn's 4 workers), once each worker's GOMAXPROCS is its share | asgi-worker-processes.md |
| 2026-11-17 — Inline requests are asgi's default; `Options.LoopThread` opts out | asgi-inline-and-flat-combining.md |
| 2026-11-17 — Before publishing: spikes archived in `.agents/attic/spike`, the test image moved to `.work/testimage` | project-status-and-agent-process.md, ci-and-test-image.md |
| 2026-11-17 — The agent harness from cornus: `AGENTS.md`, `.agents/docs` with LTM and TODO, and the memory skills | project-status-and-agent-process.md |

No synthesis documents exist yet (`deep-sleep` has not been run).

Standalone documents, moved verbatim from the journal's former "Durable lessons" section (its former "Open items" section became `../TODO.md`):

- [building-cpython-for-bao.md](LTM/building-cpython-for-bao.md)
- [embedding-cpython.md](LTM/embedding-cpython.md)
- [free-threaded-cpython-under-mn.md](LTM/free-threaded-cpython-under-mn.md)
- [performance.md](LTM/performance.md)
- [hardening-network-facing-code.md](LTM/hardening-network-facing-code.md)
- [waker-and-lifecycle-safety.md](LTM/waker-and-lifecycle-safety.md)
- [isolation-verdict.md](LTM/isolation-verdict.md)
- [debugging-techniques.md](LTM/debugging-techniques.md)
- [design-lessons.md](LTM/design-lessons.md)
- [process-lessons.md](LTM/process-lessons.md)

See `.agents/docs/LTM/INDEX.md` for the full index.

## Work summary

Entries not yet consolidated into LTM. Append new ones at the end of the file.

### 2026-11-17 — aio.Buffer and aio.String moved to py (OwnedBuffer, String), and Object's Unicode accessors read a str natively

- **Context:** the user asked to move `aio.Buffer` and `aio.String` under `py`, then
  asked that `py/unicode.go` use the same native fast path as the string lease. Both
  lived in files Codex wrote (`aio/channel_*`, `py/native_unicode.go`); the user had
  this session take them over.
- **Names:** `py.Buffer` already is the borrowed PEP 3118 export, so the user chose
  `py.OwnedBuffer` for the owned lease; the string lease is `py.String`.
  - `NewOwnedBuffer(data, readOnly)`, `(*Object).OwnedBuffer()` (export a Python
    exporter into a lease; must be contiguous), `TakeObject()` (consume into a flat
    byte memoryview).
  - `NewString(text)`, `(*Object).Unicode()` (retain a str), `TakeObject()` (consume
    into the original str), plus the old `GoString/Len/Rune/Runes/Slice/Clone/Close`.
  - `TakeObject` replaces aio's unexported `python()`; aio's `channelLease` interface
    now names it. aio keeps only the `None` → nil decoding (`stringFromObject`,
    `bufferFromObject` in `channel_types.go`).
  - `py.NativeUnicode` and `(*Object).UnicodeInto` were exported only so aio could
    embed the view; they are unexported now (`nativeUnicode`, `unicodeInto`).
  - Files moved with `git mv` (`aio/channel_string.go` → `py/string.go`,
    `aio/channel_buffer.go` → `py/owned_buffer.go`); error texts say `py:`.
    `TestChannelStringRuneAccess` moved to `py/string_test.go` as `TestStringRuneAccess`.
- **Fast path in `py/unicode.go`:** `UnicodeLen`, `UnicodeRune`, `UnicodeRunes` read a
  str through the same layout readers as `String` (now functions on `*Object`:
  `unicodeLength`, `unicodeData`, `unicodeRuneAt`, `unicodeRunesNative`).
  - The str check (`IsString`, one C call, or none for decoded arguments) is cached
    per Object in `Object.str` (`atomic.Uint32`, fits the padding after `borrowed`, so
    `Object` stays 40 bytes). Being a str is immutable: `__class__` assignment cannot
    leave str's layout.
  - Non-str objects, out-of-range `UnicodeRune` indexes and builds without a verified
    layout go to the C API, so TypeError and IndexError are unchanged.
  - **Trap avoided:** `initializeNativeUnicode` took the subclass probe's expected
    values from `o.UnicodeRunes()`. With the fast path that would compare native reads
    with themselves; it now calls `unicodeRunesC`.
- **Test and control:** `TestUnicodeAccessorsReadStrNatively` swaps the C-API seams
  (`unicodeLenAPI`, `unicodeRuneAPI`, `unicodeRunesAPI`) for recorders. ASCII, Latin-1,
  BMP, non-BMP with a surrogate, empty, and a noncompact subclass overriding
  `__len__`/`__getitem__` must give C-API-equal results with no recorded call;
  out-of-range must give `IndexError`; bytes must reach the C API.
  - Control: forcing `nativeStr` false fails it with
    `"ASCII": str reads called the C API: [len runes rune rune rune rune rune]`.
- **Measured:** `BenchmarkUnicodeAccessors` (py, internal), CPython 3.14.10t test image,
  Go 1.26.11, `taskset -c 5 py.test -test.bench BenchmarkUnicodeAccessors -test.cpu 1
  -test.count 3`, host load about 10:

  | accessor | C API | native |
  |---|---|---|
  | `UnicodeLen` | 42.7 ns, 1 alloc | 2.27 ns, 0 |
  | `UnicodeRune` | 47.8 ns, 1 alloc | 4.13 ns, 0 |
  | `UnicodeRunes` (7 chars) | 112 ns, 3 allocs | 19.2 ns, 1 (the result) |

  The 3.13 image gave the same numbers within 3%.
- **Gate:** `ci/unit.sh gil` and `ci/unit.sh ft` passed on arm64 and amd64.
- **Carry:** a native reader added to `py` must not be its own oracle; keep a C-API
  path for the layout check and for tests.

### 2026-11-17 — macOS port: pure Python passes on darwin arm64 and amd64 (3.13, 3.14t) with bao -darwin-dylib

- **Context:** the user asked to port pythago to macOS, with two hosts: `dengaku.local`
  (M1 Max, macOS 26.5.2, CommandLineTools SDK 26.5, Apple clang 21) and
  `macbook-pro-2.local` (Intel i9-9880H, macOS 26.6.2, Xcode SDK 26.2, Apple clang 17).
  Go 1.26.11 sits in `~/pythago-work/go` on dengaku; macbook-pro-2 fetches it through
  `GOTOOLCHAIN`. Everything on the hosts lives under `~/pythago-work`.
- **Image build (decision):** shared mode, as on Linux: PBS 20261110 `install_only_stripped`
  `libpython3.13.dylib` / `libpython3.14t.dylib` (arm64 not arm64e, `LC_DYLD_INFO_ONLY`,
  TLV descriptors, deps libz, libedit, ncurses, panel, CoreFoundation, SystemConfiguration)
  plus the glue, with libpython's `pthread_create/join/detach/exit` binds wrapped to it.
  - bao v0.0.7 rejected "relocatable code combined with a dylib" and had no wrap for dylib
    binds. Asked bao-77; it built `-darwin-dylib` (`build.DarwinOptions.Dylib`, PR #41,
    commit f125b4b): the glue object plus the dylib, `-wrap` on the dylib's binds, glue TLS
    lowered into the same Darwin TLS runtime (the glue's block first), `-symbol` resolving
    glue exports then dylib exports. It also dropped a `dyld_stub_binder` bind that macOS 26's
    dlsym does not find, which broke plain PBS dylib input too.
  - pythago passes `-darwin-dylib` through `build.Options.Args` (`cmd/pythago-dist/darwin.go`)
    so go.mod stays at bao v0.0.7; runs use a bao built from a `git archive` of f125b4b
    (`PYTHAGO_DARWIN_BAO=$HOME/pythago-work/bin/bao`).
  - Glue as **one translation unit**: bao takes a single object and there is no Mach-O
    partial link here (Apple `ld -r` only on a Mac, ld64.lld has no `-r`).
    `compileDarwinGlue` compiles a unity file, reads the `pythago_*` functions with
    `debug/macho`, appends `pythago_symtab` and compiles again: 43 symbols, as on Linux.
    Clang: Xcode's (`xcrun --find clang`) unless `-llvm`/`PYTHAGO_LLVM`; SDK: xcrun's unless
    `-sdk`. No darwin prebuilt glue yet (TODO).
  - bao output: arm64 17,186,816-byte image, 75,166 rebases, 804 binds, 1,755 exports,
    17,593 FDEs, 4 TLS descriptors, 32 template bytes.
- **Code changes:**
  - glue: futex → `__ulock_wait`/`__ulock_wake` (`UL_COMPARE_AND_WAIT`, in the SDK's
    `libSystem.tbd`, macOS 10.12+; libc++'s atomic wait uses them); `pthread_atfork`
    instead of glibc's `__register_atfork`.
  - py: `osThreadID` per OS (`thread_selfid` on darwin); the ELF extension shim is
    Linux-only (`py/extshim_linux.go`, `//go:build linux` on `py/internal/extshim`), a no-op
    on darwin.
  - aio: ppoll/eventfd/raw fcntl → `golang.org/x/sys/unix` v0.49.0 (`Poll`, `FcntlInt`;
    eventfd waker on Linux, pipe on darwin, `aio/waker_*.go`, `aio/poll_*.go`).
  - asgi: no Pdeathsig on darwin; `InheritedListener` starts `watchParent`, a kqueue
    `EVFILT_PROC`/`NOTE_EXIT` watch on the parent that sends the worker SIGTERM.
  - internal/layout: the generator run on the darwin arm64 PBS headers (3.13.18, 3.14.10t)
    gives byte-identical definitions, so the tags are `(linux || darwin) && <arch>`.
  - pythago-dist: `pbs.Triple(goos, goarch)` with the apple-darwin triples,
    `glue.Key(pyver, goos, goarch)`, `-sdk`, darwin needs `-mode shared`; manifests omit
    `glibc_floor` on darwin.
- **Bugs found on the Macs (same on both arches), with the run that showed each:**
  1. `TestPythonThreads: 0 goroutine-backed threads started, want >= 33` (and
     `TestThreadLimit`): darwin's `pyconfig.h` defines `THREAD_STACK_SIZE 0x1000000`, so
     CPython gives every thread an explicit 16 MiB stack attribute, and the glue sends
     non-default stack sizes to libc. Fix: on darwin `default_stack` also accepts exactly
     `THREAD_STACK_SIZE`. Go's threads inherit the main thread's 8 MiB (fakecgo uses
     `pthread_get_stacksize_np(pthread_self())` of the creator).
     - Checked against stock with `.agents-workspace/tmp/stack.py` in a `threading.Thread`
       (pyrun vs PBS `python3.1x` on dengaku): stack 8.03 vs 16.01 MiB. 3.13: identical
       results (nested-list `json.dumps`/`repr` OK at 5,000, RecursionError at 20,000;
       Python recursion 150,000 OK). 3.14t: only `json.dumps` at depth 100,000 differs (OK
       on stock, RecursionError on pythago); no crash anywhere.
  2. `TestThreadStateFreedAtThreadExit: timed out waiting for the exited thread's thread
     state to be deleted`: Go does end the thread on darwin (mexit returns from mstart), but
     `owned_dtor` ignored its argument and read the `__thread` `owned`, which on darwin
     reads NULL by then: another key destructor releases the TLV storage, in no fixed order.
     Fix: `owned_dtor` deletes the thread state passed as the key's value. The failing run
     before the fix is the control.
  3. `TestErrors`: `[Errno 48] address already in use` (the test hardcoded Linux's 98; now
     `errno.EADDRINUSE`), then `[Errno -2] lookup bad!host` (aio sends Linux's EAI codes;
     darwin's are positive, EAI_NONAME 8). goaio.py now maps -2/-3/-4 to this platform's
     `socket.EAI_*`.
  4. 3.14t `TestMultiprocessingStartMethod`: py skipped setting forkserver on 3.14 assuming
     that is the default, which holds only on Linux (macOS defaults to spawn). Now the skip
     is Linux-only.
  - `TestExtensionShim` skips on darwin (no shim). `py/py_test.go` (Codex's) got per-OS
    `threadID`/`osThreads` (`py/os_*_test.go`; darwin counts threads with `proc_pidinfo`
    `PROC_PIDTASKINFO` through purego), with the user's approval.
- **Why Linux CI could not catch these:** each depends on a darwin difference (pyconfig,
  TLV teardown order, errno and EAI values, the multiprocessing default).
- **Result:** `ci/darwin.sh gil` and `ci/darwin.sh ft` pass on darwin/arm64 and
  darwin/amd64: py, aio, asgi inline and `PYTHAGO_ASGI_LOOP_THREAD=1`, including the
  worker-process tests on the kqueue parent watch. Linux: both `ci/unit.sh` tiers and
  `ci/glue.sh` (prebuilt glue regenerated twice for the glue changes).
- **Open:** C extensions on darwin (runtime dylib with absolute exports); darwin prebuilt
  glue and where to build it (SDK licence); the bao release with PR #41; examples/hello and
  GitHub Actions on darwin. See TODO.md.
- **Carry:** on darwin, never read `__thread` state from a pthread key destructor; pass
  what the destructor needs as the key's value.

### 2026-11-17 — bao v0.0.8: darwin builds on the pinned bao, no external bao
- **What:** bao-77 tagged v0.0.8 (`fcb42a6`, git tag only, no release page) with #41
  `-darwin-dylib`, #42 `-windows-dll` and #43 (Darwin TLS-at-thread-exit docs). `go.mod`
  now pins v0.0.8 (`go mod tidy -e` also made `golang.org/x/sys` a direct requirement,
  which aio and asgi import). `cmd/pythago-dist/shared.go` sets `bo.Darwin.Dylib`, the
  same as `bo.Linux.SharedObject`; the `darwinDylib` helper that passed the flag through
  `build.Options.Args` is gone. README no longer asks for `-bao`/`PYTHAGO_BAO` on macOS.
- **#43 matches what the glue does:** a pthread key destructor that runs after bao's
  per-package key sees glue `__thread` at its initial value, as under native dyld; passing
  the value through `pthread_setspecific` is the documented workaround (`owned_dtor`).
- **Result:** with `PYTHAGO_DARWIN_BAO` unset (both hosts have no `PYTHAGO_BAO`, so the
  log shows `bao build -target darwin/<arch> (v0.0.8)` from the in-process bao),
  `ci/darwin.sh gil` and `ci/darwin.sh ft` pass on arm64 (dengaku) and amd64
  (macbook-pro-2). Linux: `ci/unit.sh gil`, `ci/unit.sh ft` and `ci/glue.sh` pass. The
  `RuntimeError: content-length 5 does not match` lines in the asgi logs are
  `TestPathSend`'s negative case and appear in the earlier runs too.
- **Open:** Windows can now try `build.Options.Windows.DLL` (#42, not run on Windows by
  bao-77). See TODO.md.

### 2026-11-17 — darwin prebuilt glue, pinned to the macOS 26.5 SDK
- **Decision (user):** ship darwin glue objects like the Linux ones, pinned to one macOS
  SDK version. The pin is for byte reproducibility only: the objects target macOS 11
  (`--target=<arch>-apple-macos11`), so they need not follow SDK releases. Regenerate
  only when `glue/*.c` or the Python headers change.
- **What:**
  - `prebuiltTargets` gained `{cp313,cp314t}-darwin_{arm64,amd64}`; `darwinGlueSDK =
    "26.5"` (`cmd/pythago-dist/prebuild.go`). `pinnedSDK` takes `-sdk`, else `xcrun --sdk
    macosx26.5`, else the CLT's `MacOSX26.5.sdk`, and checks `SDKSettings.json` Version.
  - `-prebuild-glue` builds only the host OS's targets (the SDK is licensed for Apple
    hardware, so darwin objects are never built on Linux) and keeps the other OS's
    `SHA256SUMS` lines (`otherSums`). `-mode prebuild` is allowed for darwin.
  - `internal/toolchain` fetches `LLVM-23.1.5-macOS-ARM64.tar.xz` on darwin/arm64 (clang
    and `lib/clang` only; bsdtar reads .xz natively but its `-I` is not a compress
    program). LLVM publishes no macOS x86_64 build, so the darwin objects are built on
    the arm64 Mac, cross-compiling amd64.
  - `ci/darwin.sh glue` (check, arm64 host) and `glue-update` (rebuild there, rsync the
    darwin objects and SHA256SUMS back); `ci/darwin-host.sh glue|glue-update`.
  - Both glue checks seed the rebuild directory with the committed SHA256SUMS, so a
    prebuild that built nothing would still diff clean. Each now also checks the rebuilt
    files against the committed lines (`grep -- -<os>_ SHA256SUMS | sha256sum -c -`).
    **Control** on dengaku: a seeded directory with no objects passes the diff (the hole)
    and fails the new check with 4 "No such file"; an appended byte to one rebuilt object
    fails it with "1 computed checksum did NOT match".
- **Objects:** 35952 / 33248 / 37296 / 34504 bytes (cp313 arm64/amd64, cp314t
  arm64/amd64); LC_BUILD_VERSION platform 1, minos 11.0, sdk 26.5; no host paths
  (`strings` finds no /Users, /Library or cache paths).
- **Result:** `ci/darwin.sh glue` reproduces them (twice, fresh rebuilds); `ci/darwin.sh
  gil` and `ft` pass on arm64 and amd64 with `glue: prebuilt cp3xx-darwin_<arch>` in the
  log (no clang run). Linux: `ci/glue.sh` (hardened), `ci/unit.sh gil` and `ft` pass.
- **Not removed by this:** bao still reads the macOS SDK for darwin targets (xcrun or
  `-sdk`), so a Mac build still needs the Command Line Tools; the prebuilt glue removes
  only the clang step. Reproduction is checked on one host (dengaku); macbook-pro-2 has
  the 26.5 SDK too but no fetched LLVM (Intel).

### 2026-11-17 — package wsgi: PEP 3333 behind net/http
- **What (user: "Do the WSGI work", the TODO's "a WSGI counterpart"):**
  - `wsgi.New("module:app", Options{ScriptName, MultiProcess, Prefetch})` returns an
    `http.Handler`; `Close` releases the app. No Configure: `_pythago_wsgi` is a
    `py.NewModule`, `pythago_wsgi` is executed from the embedded source at the first New.
  - Each request runs on its net/http goroutine through `py.Do`: `Config.Slots` is the
    thread pool. Body reads and response writes detach (`py.Release`); small unflushed
    writes (≤ 2 KiB) stay attached.
  - `internal/httpfield` (`ValidName`, `ValidValue`) moved out of `asgi/fields.go`;
    asgi's messages are unchanged.
  - `internal/tools/wsgiserve`; `bench/wsgi` (`run.sh` interleaves configurations by
    round); `internal/tools/httpload` redials on `Connection: close` (gunicorn sync).
  - CI: `ci/unit.sh` and `ci/darwin-host.sh` run `./wsgi`; vet and gofmt cover it.
- **Found while building it:**
  - **NUL in the decoded path** (`/env%00HTTP_X_ADMIN%00yes`) would shift the packed
    environ and set keys: every packed value is checked, 400.
  - Python marked the response sent before Go validated it, and sendfile's length
    check ran after `WriteHeader`: both would have turned a clean 500 into an abort.
    Heads are now parsed (`parseHead`) before anything is written, and Go's
    `q.started` alone decides 500 vs abort.
  - Stock wsgiref passes `X_Underscore` headers on (my comment first claimed it drops
    them); we drop them, as nginx and gunicorn do. Recorded as the one intended
    difference in `TestEnvironMatchesWsgiref`.
  - Test-side: the app read `wsgi.input` to EOF, which hangs on wsgiref's socket
    input; `ExecIn` namespaces need `__builtins__`; 3.14 warns on `return` in
    `finally`.
- **Controls:** 21 mutants, one per defence, all caught by the intended test (list in
  `.agents/docs/LTM/wsgi-server.md`). `status-space` first passed for the wrong reason
  (the mutant panicked on `"200"`; `"200XOK"` added). The no-body mutant first failed
  to compile; redone as `len(b) < 0`. Three survivors were redundant defences and were
  removed: our short-body abort (net/http closes HTTP/1.1 and resets HTTP/2 itself),
  Python's 500-vs-abort choice, and with it the meaning of Python's `sent` ordering.
- **Stock comparisons:** wsgiref (same raw requests, same environ for PEP 3333/CGI keys
  and all HTTP_*), `wsgiref.validate` with WSGIWarning as error, Flask 3.1.3 / Werkzeug
  3.1.9 against gunicorn 27.0.1 gthread (identical bodies, hashes and headers on 10
  routes; differences: net/http's reason phrase `302 Found` vs `302 FOUND`, canonical
  `Etag`). strace: `sendfile(5, 9, NULL, 1999611)` for `send_file` of 2,000,123 bytes.
- **Bench** (CPython 3.13.18, gunicorn 27.0.1 stock, `bench/wsgi/run.sh`: server on
  core 5, 4 threads, httpload on 15-19, 8 s, 2 interleaved rounds; req/s r1/r2):
  `/` c=1: gunicorn gthread 21.2k/21.8k, wsgi 44.7k/44.8k; `/` c=64: 25.5k/26.4k vs
  69.9k/71.2k; `/big` 64 KiB c=64: 21.6k/21.8k vs 42.7k/42.2k; `/sleep` 10 ms c=64:
  381/380 vs 386/386 (4 threads × 100/s). gunicorn sync: 15.4k/8.5k, 20.3k/10.6k,
  16.7k/9.6k, 95/97 (connection churn; round 2 noisy).
- **Result:** `go test ./wsgi` passes; `ci/unit.sh gil` and `ft` pass (wsgi natively
  and under qemu); `ci/darwin.sh gil` and `ft` pass wsgi on arm64 and amd64.
- **Seen once, not from this change:** the darwin ft tier's asgi LoopThread run on
  amd64 crashed in `Py_FinalizeEx` (SIGSEGV, rip in bao's runtime-mapped dylib region,
  rax=rdi=0) after printing PASS. 0 of 16 direct reruns (8 per mode) and a tier rerun
  failed. In TODO.md; it needs a way to symbolize PCs in the mapped dylib.

### 2026-11-17 — package workers (Spawn moved out of asgi); security review of wsgi, asgi, workers
- **Move (user: "Move Spawn into a neutral package"):** `asgi/process*.go` → `workers/`
  (`git mv`). `asgi.Spawn` → `workers.Spawn`, `SpawnOptions` → `workers.Options`,
  `WorkerIndex` → `workers.Index`, `InheritedListener` unchanged; marker
  `PYTHAGO_ASGI_WORKER` → `PYTHAGO_WORKER`; messages say `workers:`. No asgi aliases
  (nothing is released). asgiserve's `-workers` variable was renamed (`loops`) because it
  shadowed the package. The tests no longer need Python: `workers_test.go`'s TestMain
  serves a plain Go handler as a worker, and reports pid, index and the marker as seen
  after `InheritedListener` (new checks: indexes 0 and 1, same index for a replacement,
  marker gone). `TestSpawnErrors` added. `go test ./workers` takes 0.4 s.
- **Controls for workers:** no restart, no SIGTERM, no give-up, marker kept, index 0 for
  all, `n < 1` unchecked: all caught. Two first attempts were wrong: the no-restart
  mutant did not compile (redone as `_, _ = i, d`); the no-SIGTERM mutant survived, as
  Spawn then returned after Grace by SIGKILL inside the test's 30 s allowance. TestSpawn
  now uses Grace 60 s and requires a return within Grace/4 (`Spawn took 1m0.003s` under
  the mutant). The `n < 1` mutant's hung test binary had to be killed by pid.
- **Security review (user: "Look for any other security concerns"):** a read-only review
  agent plus my own list. Confirmed and fixed, each with a test and a control on the GIL
  image (`.agents-workspace/tmp/mutate_sec.py`):
  1. wsgi wrote small unflushed chunks attached: a client that stops reading (or an
     HTTP/2 window of 0) blocked the write and, with the GIL, all of Python. Small
     chunks now gather in `pending` (16 KiB) and are written detached or after `py.Do`.
     `TestSlowReaderDoesNotStall` (16 MiB list to a non-reading client; another request
     must finish): control `a request stalled behind a client that does not read`.
  2. wsgi held `q.mu` across `py.Release` and waited for it attached: a helper thread
     reading the body plus the app thread writing deadlocked with the GIL.
     `TryLock`, else wait in `py.Release`. `TestTwoThreadsOneRequest`: control hangs
     (test timeout; httptest's Close waits for the stuck handler).
  3. HTTP/2 `host` ≠ `:authority`: Go's h2 server keeps the field in `r.Header`; wsgi's
     app saw `HTTP_HOST=evil.example` while Go routed by `good.example`; asgi sent two
     host headers. Now 400 in both, Host skipped in the header loops.
     `TestHTTP2RequestFields`, asgi `TestHTTP2HostField`, through the new raw client
     `internal/h2raw` (x/net/http2 Framer + hpack). Controls: 200 with the evil host.
  4. Allocation by declared length: wsgi's prefetch made `Content-Length` bytes before
     any arrived; asgi's `readChunk` made 64 KiB for every body chunk. Both grow with
     the data. `TestPrefetchGrows`, `TestReadChunkGrows` (first Read buffer ≤ 4 KiB):
     controls `first read into 65536 bytes`.
  5. CONTENT_LENGTH was the raw header (HTTP/2 `05`); now `r.ContentLength`. Control:
     `CONTENT_LENGTH "05" for a 5-byte body` (first mutant did not compile; redone).
  6. ContextVars carried across requests on a reused thread state: each request now
     runs in a fresh `contextvars.Context()`. `TestContextPerRequest`: control `saw
     current_user "user0" from an earlier request`.
  7. `done` was set after the final unlock (a stale `write()` could slip in): now set
     inside the final critical section. No deterministic test.
  8. asgiserve/wsgiserve had no ReadHeaderTimeout (slowloris): 10 s header, 2 min idle;
     no read/write timeouts (streaming, WebSockets). Package docs now show `http.Server`
     with ReadHeaderTimeout.
  - Documented, not changed: file_wrapper's sendfile shares the file offset with Python's
    file object (one file object per response, as with read()); `InheritedListener`
    must run before `py.Init` or the marker stays in Python's `os.environ`;
    `threading.local` persists per thread, as in gunicorn gthread.
  - Checked fine by the review: Go's request framing (CL with chunked, duplicate CL),
    environ key collisions, `UnsafeBytes` only on exact bytes while detached, the
    readinto export, sendfile basics, never-reused handles, generic error bodies,
    ScriptName boundaries, fd 3 close-on-exec in workers, Pdeathsig thread, darwin ppid.
- **Result:** `ci/unit.sh gil` and `ft` pass (workers natively and under qemu);
  `ci/darwin.sh gil` and `ft` pass on arm64 and amd64 with workers in the loop.

### 2026-11-17 — darwin finalize crash: asgi Close now joins its worker threads
- **Symptom:** once in the darwin ft tier (amd64), after the asgi suite printed PASS:
  SIGSEGV in `Py_FinalizeEx`, `PC=0x5a957db addr=0x0`, rax=rcx=rdi=0, rbx a heap pointer;
  then 0 failures in 16 + 80 + 60 + 400 reruns (and 1,200 parallel runs that only
  exhausted ephemeral ports).
- **Symbolizing:** the PC is in the dylib image bao loads into the binary's
  `__noptrbss`; no symbol table covers it. `_bao_dylib_<hash>_image` (nm) is the image
  base, the PIE slide comes from a Go frame of the same crash
  (`runtime.cgocall+0x4b pc=0x55041ab`), and PBS's unstripped `install_only` dylib has the
  same layout as the stripped one (`__text` 0xb00, size 0xb947d0). The crashing binary
  was gone, so a rebuild with the old spawn test restored stood in; it put the PC at
  `PyThreadState_Clear+0x3bb` = 0x17f7db, and the disassembly confirmed it exactly: an
  instruction starts there, `movq %rax, (%rcx)` with rax/rcx loaded from the thread
  state at 0x4a78/0x4a80, i.e. `llist_remove(&brc->bucket_node)` in the inlined
  `_Py_brc_remove_thread`, on a node already removed (prev = next = NULL).
- **Cause:** a thread state cleared twice. A glue diagnostic in a scratch tree (thread
  states listed just before `Py_FinalizeEx`) showed it in most runs: two asgi worker
  threads `state=1 cleared=0 finalizing=1` with their BRC node already NULL, i.e. inside
  their own `PyThreadState_Clear` past the BRC removal. The workers are daemon threads;
  `Close` returned after their `detach` callbacks, before CPython's teardown of their
  thread states. The rest of `PyThreadState_Clear` takes PyMutexes (TLBC index pool,
  delayed frees) that detach when contended (16 workers exit together); finalization's
  stop-the-world then counts the thread as stopped, `_PyThreadState_RemoveExcept` takes
  its still-listed state and `_PyThreadState_DeleteList` clears it again.
  - Ruled out on the way: the `alive`/`deleting` handshake (Dekker-correct), the cached
    thread state's key (`owned_drop`'s caller clears it), and a teardown that detaches
    *before* the BRC removal (stock 3.14t and pythago on Linux survive a ContextVar whose
    `__del__` sleeps in the daemon thread's Clear: 0/40).
- **Fix:** `pythago_asgi.start` keeps each server's worker threads (named
  `asgi-<server>-worker-<i>` now); `Server.Close`, after the detach callbacks, calls
  `pythago_asgi.join(server, timeout)` (ctx deadline, or none), and `Thread.join` returns
  only after the thread state is deleted. Close returns the ctx error or
  `context.DeadlineExceeded` if the threads do not exit in time.
- **Checks:**
  - diagnostic build, 20 runs each: thread states mid-teardown at finalization 24 before
    the fix, 0 after (of 272 listed);
  - `TestCloseJoinsWorkers` (both modes, 8 workers, each worker's teardown slowed by a
    0.2 s `__del__` in a thread-local set at lifespan startup): passes; control (`join`
    returns at once) `8 worker threads still running when Close returned`, 10/10. Without
    the slowdown the control was caught 1/10;
  - `ci/unit.sh gil` and `ft`, `ci/darwin.sh gil` and `ft` pass.
- **Open (TODO):** the race is CPython's; any application daemon thread that ends as the
  program finalizes can hit it, under stock CPython too. Needs a stock reproducer and an
  upstream report.

### 2026-11-17 — correction: TestCloseJoinsWorkers no longer finds workers by name
- The previous entry renamed the asgi workers `asgi-<server>-worker-<i>` so that the test
  could pick out the new server's threads by name. User review: application code can
  create threads with any name, so a name identifies nothing. `join` never used names
  (it joins the `Thread` objects `start` recorded in `_threads[server]`); only the test
  did. The test now takes the entry `New` adds to `pythago_asgi._threads` (keys before
  and after `New`, copied before `Close` pops it), and the names are back to
  `asgi-worker-<i>`. Control (no join): `8 worker threads still running`, 5/5.

### 2026-11-17 — tackle-todos sweep: GitHub CI, py calls, I/O workload tests, stock 3.14t race
- **Scope (user-selected):** the GitHub `ci` failures; fixed-arity Go→C calls; stale
  `py_test.go` comments; re-measured keyword calls; examples/hello on darwin; real I/O
  workloads in ASGI/WSGI handlers (user-added); a stock-CPython reproducer of the 3.14t
  daemon-thread double clear. Source had no TODO/FIXME comments. Closed as stale: bao
  PRs #12–#15 (all merged). Four agents in parallel on disjoint files; darwin and the
  tiers run by me afterwards (ci/darwin.sh syncs the whole tree).
- **GitHub CI** (run 44022388732 on c85fb11, every job failed): amd64 jobs got
  `403 Forbidden` from api.github.com (anonymous rate limit on shared runners); arm64
  jobs stopped at "vet, gofmt" because `go: downloading` stderr lines counted as vet
  findings. `internal/ghrelease` now sends `GITHUB_TOKEN`/`GH_TOKEN` as a bearer token to
  the API host only (a `CheckRedirect` strips it on any redirect off that host; Go itself
  keeps it for same-host-different-port, which the test exploits); `TestToken` with
  controls (`other /redirected: Authorization "Bearer ghtok"`). Workflows pass
  `GITHUB_TOKEN` and set `permissions: contents: read`. `ci/unit.sh` runs
  `go mod download` before vet and filters `^go: downloading`; reproduced with an empty
  GOMODCACHE (old fails on the six download lines, new passes). Unverified on GitHub until
  a push. `gpu-nightly` waits for a `[self-hosted, linux, gpu]` runner.
- **py:** the variadic `call(...)` sites in `py/unicode.go`, `py/native_unicode.go` and
  `callStr` became fixed-arity (callStr builds the cframe itself, as `call` does).
  `TestCCallSitesAllocateOnlyTheirResults` (AllocsPerRun; control via `go test -overlay`
  with the old files: every row failed at the old counts). `BenchmarkUnicodeAccessors`
  CAPI paths, 3.14t arm64 core 5: Len 41.5→34.5 ns, Rune 47.0→34.8 ns, Runes 109→85.2 ns.
  `py_test.go` comments now name `.work/testimage`. Keyword calls
  (`BenchmarkPythonGoCall`, core 5, 3 runs): `echo(x)` 128.7–128.9 ns, `echo(x, k=x)`
  236.5–239.4 ns (was 312): names decode in C now, and about 35% of the time is building
  the `map[string]*Object` the `Func` API hands over; left for an API decision.
- **I/O workload tests** (`asgi/io_test.go`, `wsgi/io_test.go`, each with an `ioapp`
  module): proxying a Go upstream (0 B to 4 MiB, chunked, 404, 1 MiB POST, two
  Set-Cookies; asyncio streams, `to_thread` urllib, `run_in_executor` in ASGI; http.client
  and urllib in WSGI), concurrency against a barrier upstream (ASGI 16 meet; WSGI exactly
  8 = Slots, 16 to a 250 ms upstream in two waves), client disconnect mid-proxy (upstream
  connection closed within 5 s, relays back to 0), SQLite WAL with 16 writers and 16
  readers (no torn reads, 800/800, rollback on error, busy timeouts), files, DNS,
  subprocess. Every check has a control (list in the agent report: dropped byte, dict
  headers, leaked connection, torn read, committed crash, serialized calls, ...).
  - Documented behaviour confirmed: inline asgi requests can move threads across an
    await, so a default sqlite3 connection fails exactly when they do (LoopThread: never);
    `check_same_thread=False` works in both.
  - **Fixed:** an ASGI app that returns after `http.disconnect` without finishing its
    response logged "ASGI callable returned without completing the response" and
    aborted; uvicorn is silent there (`not self.disconnected`). `_Worker.run` now returns
    quietly when the request is disconnected; `TestIOProxyDisconnectQuiet` unskipped,
    control logs the error.
  - **Open (TODO):** `GoEventLoop` has no subprocess transport and no TLS
    `create_connection`; `TestIOSubprocessAsyncio` skipped with the evidence.
- **Stock CPython 3.14.10t race:** reproduced. The detach after `_Py_brc_remove_thread`
  is `_PyMem_AbandonDelayed` (contended `interp->mem_free_queue.mutex`, or a delayed decref
  running `__del__`), not the TLBC index pool (`_Py_LOCK_DONT_DETACH`, index_pool.c:148).
  `delayed_del.py 1 0.3 130`: SIGSEGV 50/50 (agent) and 10/10 (my rerun); control
  `ADV=0` 0/50 and 0/10. Without `__del__`: 64 daemon threads × 2000 `__dict__` swaps,
  64/100 SIGSEGV. gdb: `llist_remove ← _Py_brc_remove_thread brc.c:220 ←
  PyThreadState_Clear pystate.c:1853 ← _PyThreadState_DeleteList pystate.c:2065 ←
  _Py_Finalize pylifecycle.c:2103`; the victim parked in `_PyMutex_LockTimed ←
  _PyMem_AbandonDelayed obmalloc.c:1453`. Debug build: `finalizing=1 cleared=0
  state=SHUTTING_DOWN bucket_node={0,0}`. Upstream issue drafted (title "Free-threaded:
  segfault in Py_FinalizeEx when a daemon thread is detached inside its own
  PyThreadState_Clear (double _Py_brc_remove_thread)"), not filed: the user decides.
- **examples/hello on darwin:** `ci/darwin-host.sh` (gil) go-generates and runs it from an
  empty directory with an empty environment; passes on arm64 and amd64.
- **Result:** `ci/unit.sh gil` and `ft` pass; `ci/darwin.sh gil` and `ft` pass.

### 2026-11-17 — GoEventLoop: pipes, subprocesses, and TLS in Go when the SSLContext maps
- **Subprocesses and pipes** (the sweep's open gap): `_goaio.pipe` (a duplicated fd as a
  `pipeConn` on the netpoller, through the usual `aioConn`), `watch_hangup` (write pipes
  notice the reader closing), `wait_pid` (`wait4` in a goroutine, `EV_EXITED`), and
  `_make_read_pipe_transport`, `_make_write_pipe_transport`, `_make_subprocess_transport`
  in `goaio.py`. `write_eof` now closes any `CloseWrite` connection (a pipe), not only TCP.
  `TestSubprocessAndPipes`: the same six scenarios (2 MiB cat round trip, communicate with
  exit 3, SIGKILL, stdin left open, raw pipes, reader goes away) on the stock Unix loop,
  GoEventLoop and GoSelectorEventLoop, compared with stock. Controls: no hang-up watch
  (timeout), signal code sign (`'killed': 9`), pipe fd left open (timeout).
  - darwin: `poll()` reports no POLLHUP on a pipe's write end, so the first watcher
    timed out there (`stdin_left_open`, `reader_goes_away`); `hangup_darwin.go` waits on
    kqueue `EVFILT_WRITE` for `EV_EOF`.
  - A nil-connection panic in about 1 tier run in 6 (Linux ft arm64, darwin): asyncio
    calls `pause_reading`/`resume_reading` after `close()`, and the Go functions
    dereferenced the unregistered handle. All connection functions are no-ops then;
    `TestTransportAfterClose`, control reproduces the tier's exact panic.
  - asgi `TestIOSubprocessAsyncio` unskipped; passes inline and with LoopThread.
- **TLS** (user decision: hybrid; user: patch `ssl` to record the configuration): see
  `.agents/docs/LTM/aio-event-loop.md` "TLS: crypto/tls when the context maps". Checked
  that PBS's libpython exports no OpenSSL symbols (0 `SSL_`/`X509_`/`EVP_` in its dynamic
  symbols), so recording at the Python level is the only way to read a context's
  configuration. Python's `hostname_checks_common_name` defaults to True; treating it as
  unmappable sent every default context to Python, so tls.go implements OpenSSL's
  common-name fallback instead. `TestTLS` passes; seven controls caught (no cafile
  record, PEM cadata ignored, no hostname check, no CN fallback, CN fallback ignoring the
  flag, wrong verify code, never Go). certifi (user request): bundle in certifi's format
  in the test (three forms), and the real certifi 2026.10.23 against three public sites,
  identical to the stock loop.
- Filed (user): a `cryptography` shim on Go's crypto (later); Go-built read-only
  containers (measure first).
- **Result:** `ci/unit.sh gil` and `ft`, `ci/darwin.sh gil` and `ft` pass.

### 2026-11-17 — benchmark: Go's crypto/tls against Python's ssl on GoEventLoop
- **Harness (new):** `bench/tls/run.sh` (peer `bench/tls/peer`, client
  `bench/tls/client.py` under pyrun, `bench/tls/summarize.py`); client pinned (core 5, or
  5-6), peer on separate cores (6-9, or 15-18); 5 s runs, 3 interleaved rounds. Every run
  records version, cipher and transport class: all configurations negotiated TLS 1.3
  TLS_AES_128_GCM_SHA256; `go` ran on `_GoTLSTransport`, `py` and `stock` on
  `_SSLProtocolTransport`. Raw JSONL in `$CACHE/bench-tls-{ft,ft-2core,gil,gil-2core}.jsonl`.
- **Results** (medians; full tables in `.agents/docs/LTM/aio-event-loop.md`, "TLS
  throughput"):
  - On GoEventLoop, crypto/tls beats Python's ssl except for 1-connection bulk
    (3.14t one core: rr 64 conns 78.1k vs 52.1k req/s; handshakes 1,369 vs 1,175/s).
  - Against asyncio's own loop with Python's ssl: a little behind on one core (rr 64:
    78.1k vs 85.4k on 3.14t, 79.1k vs 95.3k on 3.13), ahead with a second core, since
    crypto/tls runs in goroutines beside the Python thread (rr 64: 109.1k vs 69.3k on
    3.14t, 113.3k vs 80.4k on 3.13; 64 handshakes: 3,026 vs 1,815 and 2,965 vs 1,865).
  - Bulk on the Go path is limited by GoEventLoop's per-read event for a `Protocol`, not by
    TLS (790 MB/s with TLS, 846 without, on one core, 3.14t; stock 2,687). TODO filed.

### 2026-11-17 — coalesced reads; workers' listener flipped to blocking mode; a SYN stall
- **Coalesced reads (user request):** `aio/coalesce.go`. After a read of at least 16 KiB
  the reader goroutines read what is already ready (non-blocking `read(2)` through
  `RawConn`; for TLS, `nbConn` under `crypto/tls` returns a temporary `errWouldBlock`)
  into the same buffer, so one event carries it. `newConn` now sets the `RawConn` once
  (the read_into fast path set it lazily on the loop thread, which the reader goroutine
  would now race). Interleaved against the same build without coalescing (3.13):
  Go-TLS bulk +31% / +37% on two client cores, no change on one core (+0.8%, 8 rounds);
  coalescing after every read cost 3% on 1 KiB rr with 64 connections, so the 16 KiB
  threshold (rr now within +-2%). `TestCoalescedReads` (control: 263 chunks of 16,384).
- **workers: Spawn made the shared listener blocking.** Found by the tiers (TestSpawn:
  "Spawn took 1m0s to stop its workers"): a worker logged that it got SIGTERM and never
  exited. Cause, read in Go 1.26.11's source: `l.File()` returns an `*os.File` that Go put
  into non-blocking mode, and `Fd()` (called by `os.StartProcess` for ExtraFiles)
  switches the shared open file description to blocking mode, so a worker could block in
  `accept(2)` past `Close`. `listenerFile` passes an `os.NewFile` of a raw
  `F_DUPFD_CLOEXEC` duplicate. `TestSpawnKeepsListenerNonblocking` (control: the old
  `File()` → "the listener is in blocking mode after a worker started"). The
  "still running" log now names the workers and pids.
- **Not fixed: a SYN stall after a worker exits.** TestSpawn still fails in about 5% of
  runs on this host in a different way: requests after the victim's exit time out with the
  client in SYN-SENT; listener queue empty, workers idle; `nstat`: TcpExtTCPSynRetrans 96 =
  TcpExtTCPRetransFail 96, no listen drops. Same with SIGTERM (29/200) and with the kill
  300 ms later (7/200); never with two workers and no exit (798,358 requests) or plain
  loopback churn (1,568,818); not on darwin. Kernel 7.0.0-1019-nvidia; dmesg shows
  nothing. In TODO.
- **Result:** `ci/unit.sh gil` and `ft`, `ci/darwin.sh gil` and `ft` pass.

### 2026-11-17 — single-core Go-TLS bulk profiled: an unbounded reader; read credits
- **Request (user):** profile single-core Go TLS bulk (1.2 GB/s against 1.85 for the
  stock loop, 3.13).
- **Tooling:** pyrun gets `PYRUN_CPUPROFILE` and `PYRUN_TRACE`; at the user's request all of
  pyrun's environment (`PYRUN_ENV`, `PYRUN_GO`, the two new ones) is read once by
  `configure()` at the start of `main` into the `settings` global. `PYRUN_GO` that is not
  a count is now an error instead of ignored. perf is not allowed on this host
  (`perf_event_paranoid` = 4, no sudo).
- **Finding:** `GOMAXPROCS=2` on the same one core gave 2.7 GB/s. At 1, CPU per GB doubled,
  all in the Python thread, with equal chunk sizes; 2.1M minor faults in 5 s (one per
  page received) in the Python thread, glibc trimming 26-43 MB and re-creating 64 MB
  heaps (strace). Cause: the copy-mode reader read until `pause_reading` arrived, and with
  one P it queued about 100 buffers per time slice that the StreamReader took at once.
- **Fix:** read credits, `dataInFlight` = 1 (`aio/aio.go`: `aioConn.credits`,
  `completion.held`, handed back in `poll` after the copy). 3.13, interleaved, 3 rounds:
  1-core Go-TLS bulk 1.14-1.25 → 3.49-3.52 GB/s (faults per GB 350k → 350), 2 cores
  3.21-3.79 → 3.82-4.30; plain bulk 2,385 → 7,875 MB/s; rr, plain rr and handshakes within
  -0.5 to -1.6%. Limits 2 and 4 compared (fault episodes; 4 worse on one core). Table in
  LTM aio-event-loop.md, "Read credits".
- **Also measured:** with the fix, GOMAXPROCS=2 on one core gives rr 64 +37% (107.6k
  against 78.9k req/s) and bulk -3 to -5%. Filed as a user decision in TODO.
- **Test:** `TestReaderWaitsForPoll` (aio, internal). Control (the reader posts without a
  credit): "3502 EV_DATA events queued while the loop did not poll, want 1".
- **Result:** `ci/unit.sh gil` and `ft`, `ci/darwin.sh gil` and `ft` pass.

### 2026-11-17 — HTTP server benchmark: asgi and wsgi against uvicorn, plain and TLS
- **Request (user):** benchmark ASGI and WSGI, plain and TLS, in several scenarios,
  against uvicorn; put the current timings in README.md; a dedicated skill allowed.
- **New:** `bench/http/run.sh` (harness), `bench/http/summarize.py` (tables; `--readme`
  rewrites README.md between `<!-- bench-http:begin -->` and `<!-- bench-http:end -->`),
  the `bench-http` skill (`.agents/skills/bench-http/SKILL.md`, listed in CLAUDE.md),
  README "Server benchmarks". httpload: https URLs (`-ca`, ALPN http/1.1 only), `-body N`
  (POST), `-close` (a connection per request, full TLS handshakes), and in its output the
  TLS version and suite, `max_connect_ms` and `slow_connects` (over 1 s). Both bench apps
  got `/upload` (reads the body, answers its length).
- **Configurations** (one process on one core, CPython 3.13.18; stock servers in
  `$CACHE/venv-http`: uvicorn 0.54.0, uvloop 0.23.0, httptools 0.9.0, a2wsgi 1.10.10,
  gunicorn 27.0.1, OpenSSL 3.5.9): ASGI uvicorn uvloop+httptools, uvicorn asyncio+h11,
  `asgi` (asgiserve), `asgi` GOMAXPROCS=2; WSGI with 10 threads each (a2wsgi's pool):
  uvicorn a2wsgi, gunicorn gthread, `wsgi` (wsgiserve), `wsgi` GOMAXPROCS=2. Workloads:
  hello c1 and c64, 64 KiB response c64, 16 KiB upload c64, sleep c64 (1 ms async /
  10 ms blocking), new connection c16; each plain and TLS (ECDSA P-256).
- **Published run:** `BENCH_IMAGE=keep BENCH_WARMUP=1 BENCH_SERVER_CPU=7
  BENCH_LOAD_CPUS=15-18 BENCH_OUT=$CACHE/bench-http.jsonl bench/http/run.sh`, 3
  interleaved rounds of 5 s, 21:41-22:11 JST; image `shared` mode regenerated earlier
  this session from the same tree. Commit c85fb11 plus this session's uncommitted
  changes (aio read credits, coalesced reads, Go TLS, pipes and subprocesses; asgi, wsgi,
  py, workers, glue as in `git status`). Tables in README.md.
  - `asgi` against uvicorn uvloop+httptools: 0.79-0.97× plain keep-alive, 0.79-1.09× TLS
    (ahead on TLS hello and sleep), new connections 1.83× plain and 1.32× TLS; weakest
    on 64 KiB responses (0.79×). Against uvicorn asyncio+h11: 1.4-2.6×.
  - `wsgi`: 2.2-4.9× uvicorn's a2wsgi and 1.6-2.8× gunicorn gthread (sleep capped by
    the 10 threads at about 960 req/s for all).
  - GOMAXPROCS=2 is slower on both servers, worst for `wsgi` 64 KiB responses (41.2k →
    9.4k plain): in TODO, and against making it a global default.
  - Python's servers negotiate TLS_AES_256_GCM_SHA384 (OpenSSL server preference), Go's
    TLS_AES_128_GCM_SHA256; recorded per run.
- **Shared host:** the nyanjit session's pinned test campaign used cores 5-9 and 15-19.
  The first full run (server on core 5) had a disturbed round 3 (27 of 96 cells under 0.9
  of their median; round 1: 1, round 2: 0); the second (core 7) was stopped when other
  processes took 10-96% of the server core. Coordinated with nyanjit-31, which kept 7
  and 15-18 free for 35 minutes from 21:41; the published run fits that window.
  `run.sh` now records `foreign_cpu_pct` (`/proc/schedstat` rq_cpu_time on the server
  core minus the server threads' schedstat run time) and `host_syn_retransmits` (nstat).
  First version (`/proc/stat` ticks minus process utime+stime) read -8 to -28% on an
  idle core: rejected. Controls: 0 for all 24 runs on an idle core; 16-97% with a
  spinner pinned to the core (summarize then flags all 24 of 24, 0 of 24 without). In the
  published run 13 of 288 runs had over 5%, never more than one round of a cell except
  the GOMAXPROCS=2 wsgi sleep plain cell (†); the sleep runs were unaffected (948-968
  req/s), new-connection runs lost up to 41%. The summary marks a cell only when half its
  rounds or more are suspect. 86 SYN retransmissions on the host during the run, no
  connect over a second.
- **Stalls:** in the first smoke run (1 s runs), `pythago wsgi` over TLS completed no
  request and no error in 3 of 6 loads; a reproduction gave 1 in 12. None since in about
  620 wsgiserve TLS runs, including 120 under `strace -f --seccomp-bpf -e
  trace=accept4,close,shutdown` (user's suggestion). Not explained; in TODO.
- **Mistakes on the way:** the first strace catcher signalled strace instead of its
  tracee and waited forever; a `pkill -f` matched my own shell (CLAUDE.md's pitfall), no
  harm done.
- **Checks:** `go vet ./internal/tools/...` and `gofmt -l internal/tools` clean (the
  tiers only vet and format-check internal/tools; no product code changed since the last
  tier pass).

### 2026-11-17 — The loopback "SYN stall" is netfilter's connection tracking table filling up; TestSpawn loops fill it themselves

- **Context:** the user asked to look into the `wsgi` over TLS stalls (TODO: 0 requests,
  0 errors, 4 times in the first 108 runs, never since). No product code changed.
- **What the stalls were, from the transcript:** all four fell between 19:25 and 19:30
  JST; each hit every connection of one httpload process (keep-alive and new-connection
  loads alike), lasted 1.5-3 s and ended without an error; the server logged nothing.
  httpload counts a request only when it completes inside the window and an error ends
  its connection, so "0 and 0" means every connection sat in connect or handshake.
  Nothing in `py.Do`/`Release`, the wsgi handler or the Go scheduler explains a stall of
  seconds that ends by itself, and neither httpload nor wsgi changed before the 620
  stall-free runs.
- **The same symptom on demand:** `workers` TestSpawn, the other SYN stall in TODO. A
  loop of the test binary passed 115 times and then failed 10 times in a row:

  ```
  run 116 failed (started 22:28:55, ended 22:29:05): --- FAIL: TestSpawn (10.06s)
  ```

  During those failures a probe (`.agents-workspace/tmp/tlsstall/ctprobe.py`: a listener
  and a connect to it, 127.0.0.1) printed

  ```
  22:30:33 conntrack 262144/262144 FULL connect 20004.1 ms error timed out
  ```

  `nf_conntrack` is loaded on this host (NAT rules) and tracks loopback;
  `nf_conntrack_max` is 262144 and a closed connection's entry lives 120 s. With the loop
  stopped, the count fell to 124k, stayed there, and dropped to 48 two minutes later.
- **Why the loop fills it:** one TestSpawn run leaves about 1,760 entries (70 → 1,830; it
  uses `DisableKeepAlives` and a new listener port per run), a run takes about a second,
  so about 150 runs inside 120 s reach the limit. A full table drops the SYN in the
  output hook, which is what the earlier entry measured as TcpExtTCPSynRetrans =
  TcpExtTCPRetransFail with an empty listen queue. It also explains that entry's
  controls: keep-alive requests (798k) create no entries, and churn against one port
  (1.57M connections) is capped at about 14k entries (measured:
  `.agents-workspace/tmp/tlsstall/ctfill`, 151k connections → 14,158 entries).
- **Correction to "Not fixed: a SYN stall after a worker exits" (spawned workers entry):**
  the 5%, 29/200 and 7/200 failure rates came from looping the test, not from the worker's
  exit and not from the kernel; "dmesg shows nothing" was wrong, since `dmesg` is not
  readable without root here. A single run, as in `ci/unit.sh`, cannot fill the table.
  The TODO item is removed.
- **Why the stuck-backlog idea was dropped:** a 10 ms sampler of `/proc/net/softnet_stat`
  (`qwatch.py`) showed no stuck backlog and no new drops through the failing runs.
- **The wsgi TLS stalls: same signature, not proven.** Nothing recorded the table at
  19:25, and the benchmark alone cannot fill it: a run of the harness on `pythago wsgi`
  ends at 6% (one server port). So another workload on the host must have filled it then
  (the other session's jobs were running). The kernel log would settle it (root:
  `dmesg -T | grep 'nf_conntrack: table full'`). TODO entry rewritten.
- **Change:** `bench/http/run.sh` records `conntrack_pct` per run (the fuller end of the
  run) and `summarize.py` treats over 90% as suspect; the `bench-http` skill explains it.
  - Check: a harness run on `pythago wsgi` (1 s runs) recorded 0 for the first loads and
    6 after "new connection c16".
  - Control for the summarizer: the same results with one run's `conntrack_pct` set to
    100 mark its cell † and list it ("connection tracking table 100% full"); set to 50,
    nothing is flagged.
- **Not done:** filling the table on purpose under a wsgiserve TLS run, which would show
  "0 requests, 0 errors" directly. It stalls every new connection on the host for about
  two minutes (the other session's tests, new ssh logins), so it waits for the user.
- **Mistake on the way:** my own 200-run loop filled the table and broke new loopback
  connections host-wide from about 22:28:55 to 22:33.
- **Lesson** (in `LTM/debugging-techniques.md`): for loopback connects stuck in SYN-SENT,
  read `nf_conntrack_count` against `nf_conntrack_max` first; don't loop tests that open
  thousands of connections to fresh ports faster than the table drains.

### 2026-11-17 — The kernel log confirms the full conntrack table during the TestSpawn loop failures

- **Context:** follow-up to the previous entry; the user ran `dmesg -T` as root.
- **Result:** `nf_conntrack: nf_conntrack: table full, dropping packet` lines from
  22:30:24 to 22:30:42, inside the loop's failing runs (22:28:55-22:30:51) and matching the
  probe's `262144/262144 FULL` at 22:30:33. That confirms the TestSpawn cause.
- **Still open:** the lines shown cover only 22:30. Whether the kernel logged the same
  between 19:25 and 19:30 (the wsgi TLS stalls) is not yet known; the ring buffer may also
  not reach back that far.

### 2026-11-17 — The wsgi TLS stalls were not the conntrack table

- **Context:** follow-up to the two previous entries; the user grepped `dmesg -T` for
  `table full` as root.
- **Result:** lines at 17:46:56-17:46:58 and from 22:28:56 on, nothing in between. 17:46
  JST is during the earlier TestSpawn loops (08:44-08:47Z, the "SYN stall after a worker
  exits" measurements), and 22:28:56 is one second after my loop's first failing run
  started (22:28:55), so the log's clock is accurate to the second. The buffer reaches
  back before 19:25, and the kernel rate-limits this message rather than suppressing it, so
  a full table during the wsgi stalls (19:25-19:30) would have logged.
- **Conclusion:** the TestSpawn failures were the conntrack table, made by the test loops
  themselves (now confirmed for both loops). The wsgi TLS stalls had another cause, still
  unknown; the previous entry's "same signature" was a coincidence of symptoms. The
  TODO entry says so. `conntrack_pct` stays in the bench harness: it rules this cause in
  or out at once if a stall recurs.

### 2026-11-17 — gdb hung attaching to any pythago program: the extension shim was named /proc/self/fd/N in the link map

- **Context:** to catch the wsgi TLS stall in the act, the user set `ptrace_scope` to 0 so
  a trap could attach gdb to the server when it stops making progress. gdb 15.1 attached
  to `httpload` in a second but never finished attaching to `wsgiserve`, and left it
  stopped; with `--readnever`, `set auto-solib-add off`, `maint set worker-threads 0` or
  no libthread_db it still hung.
- **Cause:** a second gdb attached to the hung one showed it in `fread(…, 64, fp)` (an ELF
  header) on fd 23, a pipe; strace showed the open just before:

  ```
  openat(AT_FDCWD, "/proc/self/fd/5", O_RDONLY|O_CLOEXEC) = 23
  read(23,
  ```

  `extshim.Load` wrote the shim to a memfd, dlopened it as `/proc/self/fd/N` and closed
  the descriptor; the link map keeps that name. gdb opens every loaded object by name, in
  its own process, where fd 5 is one of its pipes. Any Linux pythago program with the
  shim (the default) hung gdb this way. Darwin has no shim yet, so it never did.
- **Why nothing caught it:** no test attaches a debugger, and `TestExtensionShim` checks
  only that the symbols resolve.
- **Fix (provisional, user's call):** load the shim from `os.MkdirTemp` + the soname,
  removed right after dlopen (the mapping outlives it), so the link map names a deleted
  path; gdb prints "Could not load shared library symbols" and goes on. The memfd path
  stays as the fallback when no temporary directory is writable. Alternatives in TODO.
- **Test:** `TestExtensionShimLinkMapName` reads the names through `dl_iterate_phdr`
  (the link map's `l_name`, which gdb reads): none may start with `/proc/self/`, and
  exactly one must end in `/libpython-pythago.so`, so a missing shim fails too.
  - Control: `loadFile` forced to fail (memfd path) →
    `AssertionError: objects loaded under /proc/self paths: ['/proc/self/fd/4']`.
- **Checks:** plain gdb (no workaround) attached to a fixed `wsgiserve` in 0.24 s, listed
  the shim as `/tmp/pythago-shim-3510272022/libpython-pythago.so`, backtraced all 11
  threads, and the server answered after detaching; no `/tmp/pythago-shim-*` is left.
  Workaround for older binaries: `set sysroot DIR`, DIR holding only `lib`, `usr` and
  `home` symlinks, so `/proc/self/fd/5` is not found (0.2 s).

### 2026-11-17 — Tiers for the shim fix: gil passes; ft fails on TestIOProxyConcurrent with or without it

- **Context:** gate for the previous entry's extshim change. Another session ran
  `ci/unit.sh` back to back in the same tree; its image regeneration removed `.work/go.work`
  under my runs twice. So `ft` ran in a private copy (`rsync` of the tree without `.git`,
  `.work`, `.agents-workspace`) with `PYTHAGO_CI_CACHE` pointing at a directory that
  symlinks the real cache's entries except `bin`, `*.test` and `hello-*`.
- **Mistake on the way:** my wrapper printed `exit $?` inside `"$(date +%T) … $?"`; the
  command substitution resets `$?`, so every run reported 0. Read the tier's "passed"
  line, not a wrapper's exit status.
- **Result:**
  - `ci/unit.sh gil`: passed (arm64, amd64), before the other session's run started.
  - `ft`, isolated, with the fix: arm64 all pass (including
    `TestExtensionShimLinkMapName`); twice `--- FAIL: TestIOProxyConcurrent` in the amd64
    asgi run under qemu: `/proxy?via=streams: 3 of 16 requests failed to meet at the
    upstream: 500 Internal Server Error` (once `/proxy?via=streams /slow: 500`).
  - `ft`, isolated, with HEAD's `extshim.go` and without the new test: the same failure,
    this time natively on arm64 in the loop-thread asgi run. So it does not come from the
    shim change.
  - The test alone passes 20/20 natively and 10/10 under qemu amd64 with either version,
    and the whole amd64 asgi package passes 3/3 with either; it fails only inside the
    full tier. `asgi/io_test.go` is the other session's uncommitted file; left alone, in
    TODO.

### 2026-11-17 — A gdb-armed trap ran 1,800 wsgi TLS loads without a stall

- **Context:** the wsgi TLS stall (TODO) never recurred in benchmarks; the user enabled
  ptrace (`ptrace_scope` 0) so a trap could capture the process state during one.
- **Trap** (`.agents-workspace/tmp/tlsstall/trap.py`): per cycle a fresh `wsgiserve
  -threads 10` over TLS on core 5 (the cached 3.13 build, gdb with the sysroot workaround),
  then the smoke run's six loads with httpload on 15-19 (1 s + 0.5 s warm-up). A watcher
  samples the server's `/proc/PID/io` rchar + wchar every 20 ms; 0.3 s without change
  while httpload runs triggers: `ss -tanpie` for the port, every thread's state, wchan and
  syscall in both processes, nstat, conntrack count, softnet_stat and top threads, `thread
  apply all bt` of both under gdb, `ss` again, then SIGQUIT both (GOTRACEBACK=all).
  - Control: SIGSTOP of the server 0.8 s into a load → captured at 1.13 s with all 12
    server threads backtraced and the server's goroutine dump.
- **Result:** 300 cycles, 1,800 runs, 23:07-23:53 JST, load average 8-17 (the other
  session's tiers and jobs): 0 captures, 0 runs without requests, 0 errors, slowest
  connect 210 ms (sleep c64). The stall stays unexplained and unreproduced; the trap is
  kept for the next attempt.

### 2026-11-18 — The extension shim's load source is configurable: Config.ExtensionShimDir and Config.ExtensionShimMemfd

- **Context:** the user asked for the dlopen source of the extension shim to be
  configurable, after the provisional gdb fix (entry "gdb hung attaching to any pythago
  program") made a temporary file the default with a memfd fallback.
- **Design:** `extshim.Load(soname, syms, Source{Dir, Memfd})`; `py.Config` passes
  `ExtensionShimDir` and `ExtensionShimMemfd` through (Linux; darwin ignores both, as it
  loads no shim yet).
  - Zero value: a new `pythago-shim-*` directory under `os.TempDir` ($TMPDIR), removed
    after loading; a memfd if that fails.
  - `Dir` set: a new directory under it; a failure is an error, not a fallback, since a
    directory that was asked for and silently not used would hide a misconfiguration.
  - `Memfd`: a memfd only (`/proc/self/fd/N` in the link map; gdb hangs attaching).
  - Both set: an error.
  - Rejected: one string field with a "memfd" sentinel (a relative directory of that name
    is a valid path), and an environment variable (package py takes its settings from Config only; a
    program can map one to the field itself).
- **Test:** `TestExtensionShimSource` (`py/extshim_linux_test.go`) runs one child from
  sys.executable per case, configured through `PYTHAGO_TEST_EXTSHIM_DIR` and
  `PYTHAGO_TEST_EXTSHIM_MEMFD` in `testConfig`. Each child checks that `PyLong_FromLong`
  resolves through `dlsym(RTLD_DEFAULT)` (only the shim provides it) and prints the
  shim's link-map name; the parent checks the name's pattern, that the directory is gone,
  and the errors for a missing directory and for both options.
- **Controls** (each mutation of `extshim.go` made the test fail as intended, file
  restored from a copy and compared):
  - `MkdirTemp("", …)` instead of the directory: `AssertionError: /tmp/pythago-shim-…`;
  - memfd branch disabled: the same, for the memfd case;
  - a set directory falling back: `(0, '/proc/self/fd/4\n', '')` for the missing one;
  - both options accepted: `(0, '/proc/self/fd/4\n', '')`;
  - `RemoveAll` dropped: `…'s directory was left behind`.
- **Gate:** gofmt clean, vet clean (apart from the usual unsafe.Pointer notes), `go test
  ./py/` passes, darwin builds; `ci/unit.sh gil` and `ci/unit.sh ft` pass on arm64 and
  amd64 (logs `.agents-workspace/tmp/tlsstall/unit-{gil,ft}-2.log`). `TestIOProxyConcurrent`
  passed in this ft run, one run, so its TODO entry stays.
- **Carry:** a configured path must fail loudly; only the default may fall back.

### 2026-11-18 — asgi request tasks were held only weakly; a garbage collection mid-request answered 500 (TestIOProxyConcurrent on 3.14t)

- **Context:** tackle-todos round, TODO item "asgi `TestIOProxyConcurrent` fails in the ft
  tier": `/proxy?via=streams` answered some of 16 concurrent requests with 500 in 3 of 3
  isolated ft tiers (2026-11-17), never alone. Found by a sweep agent; checked here.
- **Error** (visible only after the test kept tracebacks: ioapp's own `_Capture` handler on
  the `pythago.asgi` logger kept logging's stderr fallback from firing):
  ```
  Task was destroyed but it is pending!
  task: <Task pending name='Task-1078' coro=<_Worker.run() running at <pythago>/pythago_asgi.py:231> wait_for=<Future pending cb=[Task.task_wakeup()]>>
  io_test.go:672: the server logged: Exception in ASGI application
      File "<pythago>/pythago_asgi.py", line 231, in run
        await self.app(scope, req.receive, req.send)
    GeneratorExit
  ```
  The 2026-11-17 tier logs (`.agents-workspace/tmp/tlsstall/iso/unit-ft-*.log`) have the
  same "Task was destroyed" lines before each failure.
- **Cause:** `_Worker.start_request` (`asgi/pythago_asgi.py`) created each request's task
  (`asyncio.Task(..., eager_start=True)` or `loop.create_task`) and kept no reference;
  `on_websocket` likewise. asyncio holds tasks weakly, and `StreamReaderProtocol` holds
  its `StreamReader` by a weakref, so a request waiting in `reader.readline()` is
  reachable only through the cycle task → coroutine → reader → waiter future → callback
  → task. Any collection in that window frees it; the coroutine gets `GeneratorExit`,
  `_Worker.run`'s `except BaseException` answers 500. Not free-threading specific: 3.14t
  just collected inside the window under load.
- **Stock check:** `.agents-workspace/tmp/ioflake/stock/stock_gc.py` (an unreferenced
  `create_task` in `open_connection` + `readline()`, then `gc.collect()`): "Task was
  destroyed" and `GeneratorExit` on stock 3.12.3 (rerun here), 3.13.18 and 3.14.10t;
  kept in a set, as uvicorn keeps its tasks, it completes (`b'pong\n'`).
- **Fix:** `_Request.task` and `_WebSocket.task` slots hold the task; `self.requests[h]`
  holds the request until `req.close()` / `run_websocket`'s `finally`. One slot store per
  request. The lifespan task was already kept (`self.task`).
- **Why the tests missed it:** only a collection inside a stream wait triggers it, and
  ioapp swallowed the traceback. Now:
  - `TestTaskKeptAlive` (`asgi/asgi_test.go`, testapp `/gc-wait`: 4 requests await a
    future held only by a weakref'ing resolver; `gc.collect()` at 20 ms, resolve at 60 ms);
  - `TestWebSocketTaskKeptAlive` (`asgi/websocket_test.go`, `/ws/gc-wait`);
  - `TestIOProxyGC` (`asgi/io_test.go`: `/proxy?via=streams&gc=0.05` to a 200 ms
    upstream; also fails if the server logged anything);
  - `TestIOProxyConcurrent` reports the server's log after each phase (`dumpLogs`), and a
    logged exception fails it.
- **Controls:** `req.task = task` → `pass`: `TestTaskKeptAlive` (4× `500 "Internal Server
  Error"`) and `TestIOProxyGC` fail, inline and LoopThread. `ws.task =` removed: only
  `TestWebSocketTaskKeptAlive` fails (`received close frame: status =
  StatusInternalError`). Restored from a copy, compared with `cmp`.
- **Rates** (ft 3.14t, `taskset -c 0-4,10-14`, 5-6 copies of the asgi test binary in
  parallel, unfixed and fixed alternated, `.agents-workspace/tmp/ioflake/ab.sh`, `qab.sh`):
  native LoopThread 5/60 → 0/60, native inline 1/60 → 0/60, amd64 qemu LoopThread 8/20 →
  0/20.
- **Left open:** `TestInline` (`inline_test.go:159/167 ... ran at {LoopThread:true ...},
  want inline`) flaked 1/1200 unfixed and 3/1200 fixed under heavy qemu load: noise, not
  this change.
- **Carry:** keep a strong reference to every task you create (`create_task` results are
  weak in asyncio); a flaky 500 from an asyncio app under load: look for "Task was
  destroyed but it is pending!" first.

### 2026-11-18 — callStr split into fixed-arity callStr0/callStr1: Object.Attr 5 → 4 allocations

- **Context:** tackle-todos round, TODO "`callStr` is still variadic". Done by a sweep
  agent; reviewed here (added the missing `//go:uintptrescapes` on `callStr1`, which
  `callStr0` and `call0`…`call8` carry).
- **Change:** `py/capi.go` `callStr0(fn, s)` and `callStr1(fn, a0, s)` (Go parameter order
  = C argument order), cframe literal, same thunk, `cString` + `runtime.KeepAlive`.
  Callers: `Object.Attr`, `Import`, `mainDict`, `NewModule`, the six `fetchError` sites.
  Only one-argument calls paid the variadic array; no-argument calls passed nil.
- **Numbers** (3.14t, arm64, core 2, `BenchmarkStrCall`, 6 alternating rounds, load 2.7-2.9):
  Attr 618.6 ns, 96 B, 5 allocs → 591.7 ns, 85 B, 4 allocs (time within noise); Import of a
  loaded module 1191.5 → 1190 ns, 4 allocs both.
- **Test:** `TestCCallSitesAllocateOnlyTheirResults` gains Attr and Import (owned + 1: the
  result and the C string). Controls: Attr via a variadic helper → `Attr: 5 allocations
  per call, want 4`; `//go:noinline` removed from `cString` → `Attr: 3 ... want 4`,
  `Import: 3 ... want 4`, so the test also guards that the C string stays on the heap.
- **Left:** Attr still copies the name per call (an interned-name entry point would avoid
  it); not pursued.

### 2026-11-18 — wsgi at GOMAXPROCS=2 on one core is a GIL/P convoy, set off by Release for responses over 16 KiB; it can also start at GOMAXPROCS=1

- **Context:** tackle-todos round, TODO "`wsgi` with GOMAXPROCS=2 on one core: 64 KiB
  responses 4.4× slower". Measurement only, by a sweep agent; no tracked file changed.
- **Setup:** Cortex-X925, kernel 7.0.0-1019-nvidia, Go 1.26.11, CPython 3.13.18 (PBS
  20261110, a private image in the scratch dir), `wsgiserve -threads 10 bench/wsgi app:app`
  with the bench venv, server on core 7, httpload c64 (2 s warm-up, 5 s), interleaved ABAB,
  conntrack < 90k, load average 2.8-8.2. perf is not allowed; counters from schedstat and
  /proc. Data, traces and tools: `.agents-workspace/tmp/wsgigmp/`.

  | scenario | GMP=1 | GMP=2 | ratio | GMP=2 CPU | voluntary switches/s | threads |
  |---|---|---|---|---|---|---|
  | hello c64 | 65.8k | 59.4k | 0.90 | 98% | 129 → 5.9k | 12 → 26 |
  | 64 KiB c64 | 41.7k | 9.6k | 0.23 | 38% | 112 → 72k | 11 → 73 |
  | 64 KiB TLS c64 | 21.5k | 7.4k | 0.35 | 45% | 118 → 56k | 12 → 73 |
  | 16 KiB upload c64 | 37.0k | 19.3k | 0.52 | 97% | 203 → 12k | 12 → 25 |

- **Mechanism (execution traces):** a goroutine waiting for the GIL inside C
  (`PyEval_RestoreThread` in a cgocall) keeps its P until sysmon retakes it (two ticks of
  20 µs plus timer slack). The thread that gets the GIL returns from C without a P and
  sleeps in `stoplockedm` *holding the GIL* until one is retaken; the core idles (CPU
  38-45%). 64 KiB at GMP=2: 23,287 Ps retaken per 2 s (about 1.3 per request); GIL holders
  waited a mean 60 µs (p90 155 µs) for a P, 1.5 s of every 2 s, serially. At GMP=1: 0
  retakes, one thread runs 44% of the Go code, CPU 99%.
  - Responses over 16 KiB (`maxPending`) go through `py.Release`: a second GIL
    acquisition per request plus a FIFO re-queue for the admission slot (about 27
    goroutines in Release's re-entry, 27 in Do's select), which sustains the convoy.
  - hello and upload at GMP=2 are a milder effect (about 1k retakes per 2 s, CPU 97%): two
    Ms sharing one core (`goschedImpl` 18.6%, `bgsweep` 11%, `unlock2` 10.9%).
- **Experiments that confirmed it:**
  - `-threads` 10/4/2/1 at GMP=2: 9.6k/15.2k/35.2k/33.4k (CPU 42/53/99/99%); GMP=1 54k.
  - Response size at GMP=2: 16384 B 32.0k (0.62×), **16385 B 10.0k (0.17×)**, 39% CPU:
    the cliff is exactly `maxPending`.
  - `maxPending` 1 MiB (scratch copy): GMP=2 9.3k → 16.2k, but the copy costs GMP=1
    53.4k → 34.0k; not a fix.
  - Timer slack 1 µs (prctl) so sysmon ticks on time: GMP=2 9.3k → 17.5k; GMP=1 unchanged.
  - **Hysteresis:** GMP 1 → 2 → 1 at runtime: back at 1 it stayed in the convoy (20% CPU,
    73 threads, 45k switches/s). One plain GMP=1 run fell into it on its own (10.3k, 72
    threads); six 15 s GMP=1 timelines did not.
  - **Long handlers start it at GMP=1:** 200 µs of Python + 64 KiB: 8,450 retakes per 2 s,
    65 threads, 2.6k req/s (the same with 1 KiB: 4.6k); 50 µs: 0 retakes.
- **Conclusions:** don't default GOMAXPROCS above the affinity mask (Go's default already
  doesn't); the bench's GMP=2 rows measure this convoy, not parallelism. The convoy is a
  `py` admission/GIL problem that also reaches GMP=1 servers with handlers of 100 µs or
  more, so it matters beyond the GOMAXPROCS question.
- **Mitigations (not implemented):** (a) for list/tuple responses keep the last chunk's
  bytes and write it after Python returns, zero-copy, so no mid-request Release
  (predicted: 64 KiB at GMP=2 like 16384 B, about 0.6×; the 200 µs case near 4.6k);
  (b) a Release'd request regains its slot ahead of new ones; (c) don't block on the GIL
  while holding a P: try first, then wait in Go or hand the P off before blocking.
- **Carry:** a server stuck at 40% CPU with many threads and tens of thousands of
  switches/s is this convoy; check the trace for Ps retaken from cgocalls.

### 2026-11-18 — Windows port, first steps: Wine 11.22 in a container runs windows/arm64 Go and CPython on this host; bao cannot yet translate python313.dll

- **Context:** the user asked to try the Windows port with QEMU, then said to use Wine,
  then to use a container image as bao does. This host is arm64 (Cortex-X925, KVM), so
  windows/arm64 is the target that runs at native speed.
- **Wine on a pointer-authentication CPU:**
  - Ubuntu 24.04's Wine 9.0 (unpacked locally from the debs) and Debian trixie's Wine
    10.0 (bao's local aid, bao JOURNAL 2026-11-08) both spin at 100% CPU in `wineboot
    --init`, logging
    ```
    fixme:seh:execute_cfa_instructions e72de128a664: unknown CFA opcode 2d
    ```
    0x2d is `DW_CFA_AARCH64_negate_ra_state`: their arm64 unix-side DWARF unwinder ignores
    it and follows return addresses signed with PAuth. bao's run worked on Colima, whose
    guest likely has no PAuth.
  - Wine 11.22 no longer DWARF-unwinds unix code on arm64 (only x86_64 keeps
    `dwarf_virtual_unwind`). Built from source in a container (Debian trixie at snapshot
    20261108, `./configure --prefix=/opt/wine --enable-archs=aarch64 --without-x ...`,
    Debian clang/lld for the PE side; image `pythago-wine:11.22`, Dockerfile in
    `.agents-workspace/tmp/win/docker/`), `wineboot` completes.
  - Under it: a Go 1.26.11 windows/arm64 binary (100 goroutines) runs, and PBS's own
    `python.exe` 3.13.18 ARM64 (PBS 20261110 install_only, sha256 f050f4df…050ca) runs
    ssl (OpenSSL 3.5.9), sqlite3 3.53.1, threads and asyncio.
- **bao gap:** `bao build -target windows/arm64 -symbol Py_InitializeEx -o ./native
  python313.dll` (bao v0.0.8) fails:
  ```
  bao: unsupported DLL TLS array register x21 at 0xc200
  ```
  The instruction is `LDR X21, [X18,#0x58]`, MSVC's TEB TLS-array load into a callee-saved
  register kept across a call. Of 1179 such loads in python313.dll, 45 target x19-x28 (X8
  1023, X9 104). Requested from bao-77 with the repro; no pythago-side workaround.
- **Spike without glue or bao** (`.agents-workspace/tmp/win/spike2/`): a CGO_ENABLED=0
  windows/arm64 Go program loads python313.dll with `syscall.LoadDLL`, calls
  `Py_InitializeEx(0)`, exposes a Go function as a `METH_FASTCALL` builtin whose
  `ml_meth` is `syscall.NewCallback(goAdd)`, and calls it from the main thread, 8
  Python `threading` threads (8000 calls) and 8 locked goroutines through
  `PyGILState_Ensure` (1600 calls): 9601 Go calls from 17 OS threads, `Py_FinalizeEx`
  returns 0, at GOMAXPROCS 1 and 4. So on Windows the Python→Go path needs no
  `crosscall2`: one `syscall.NewCallback` dispatcher serves foreign threads too.
- **Carry:** run windows/arm64 checks with `docker run --rm -v $PWD:/w -w /w
  pythago-wine:11.22 wine prog.exe` (PYTHONHOME as a `Z:\` path); not Debian's Wine on
  this host.

### 2026-11-18 — bao PR #44 embeds python313.dll on windows/arm64; the stdlib's .pyd extensions cannot bind to the embedded copy

- **bao:** bao-77 fixed both gaps in PR #44 (branch windows-arm64-tls-registers, e9d42d1,
  not merged): TLS-array loads into any register x0-x30 except x18/xzr, and loads far
  from their `_tls_index` reference inside a `.pdata` function. 1178 TLS loads patched.
- **Checked here:** `bao build -target windows/arm64 -o ./native python313.dll` with that
  bao; the spike (`.agents-workspace/tmp/win/spike1/`, spike2 with
  `native.SharedObjectExport` instead of `syscall.LoadDLL`) under `pythago-wine:11.22`,
  with only the exe and vcruntime140*.dll in its directory: 9601 Go callbacks from 13-17
  OS threads (main, 8 Python threads, 8 goroutines), `Py_FinalizeEx` 0, GOMAXPROCS 1 and 4.
- **Blocker:** extension modules import `python313.dll` by name, and `LoadLibrary` cannot
  bind them to bao's in-memory copy:
  ```
  _sqlite3 ImportError: DLL load failed while importing _sqlite3: Success.
  _socket ImportError: DLL load failed while importing _socket: Success.
  ```
  PBS's Windows build has 32 such `.pyd` (`_socket`, `_ssl`, `_asyncio`, `select`,
  `_ctypes`, `_hashlib`, `pyexpat`, `_decimal`, `_sqlite3`, `unicodedata`, ...), so
  without a fix nothing networked works. If a `python313.dll` were on the search path, a
  `.pyd` would load a second interpreter instead: worse than failing.
- **Options (for the user):** (A) wrap python313.dll's `LoadLibraryExW` import (bao
  `-wrap`) and load `.pyd` files through bao's in-memory PE loader with an import resolver
  binding `python313.dll`/`python3.dll` to the embedded copy; covers third-party wheels
  too, needs a bao runtime API. (B) a stub `python313.dll` on disk with jump thunks plus
  IAT patching for data imports (PLAN's earlier sketch; fragile). (C) translate the
  stdlib `.pyd` into the image at build time and register their `PyInit_*` (needs several
  DLLs per bao image; leaves wheels uncovered).

### 2026-11-18 — Windows decision: extract-and-load, not an in-memory interpreter

- **Decision (user, 2026-11-18):** on Windows, write `python313.dll` and the glue (a small
  DLL linked against it) to a cache directory at `Init` and load them with the Windows
  loader. Told bao-77; no in-memory `.pyd` loader request.
- **Why not the alternatives:**
  - *Stub `python313.dll` on disk with IAT patching for data:* PE exports must point
    inside the exporting DLL, so data imports (`PyLong_Type`, `_Py_NoneStruct`,
    `PyExc_*`) bind to the stub's copies; patching each extension's IAT afterwards comes
    after its `DllMain` and C++ static initializers have already captured them, and misses
    helper DLLs bound inside the extension's own `LoadLibrary`.
  - *In-memory `.pyd` loader on bao's PE loader:* covers only what it loads. Any
    descendant DLL importing `python313.dll`/`python3.dll` (a wheel's helper DLL, run-time
    `LoadLibrary`, `ctypes.CDLL`) goes through the system loader and fails or loads a
    second interpreter; patching that means owning the whole dependency closure and hooking
    `LoadLibrary*`/`GetModuleHandle*`/`GetProcAddress`, and `GetModuleFileName`,
    resources and loads on behalf of system DLLs still escape. (The user raised the
    descendant-DLL case.)
  - The shared premise was the problem: an interpreter the loader doesn't know as
    `python313.dll`. A real loaded module of that name is reused for every later import by
    name, data symbols included; `GetModuleHandle`, `sys.dllhandle` and
    `ctypes.pythonapi` work.
- **Cost:** a file on disk at run time (as the Linux extension shim's default since
  2026-11-17); bao does not carry the interpreter on Windows. Embedding (PR #44) remains
  possible for programs that load no extension DLLs.
- **Carry:** on Windows, anything that must be found by DLL name has to be a module the
  system loader loaded.

### 2026-11-18 — Windows port of py: extract-and-load images, glue DLL, all py tests pass on windows/arm64 under Wine (3.13, 3.14, 3.14t)

- **Context:** the user approved the port after the extract-and-load decision (entry
  above). Scope: the glue's Windows sections, py's Windows files, a pythago-dist windows
  target, py's tests under Wine. aio, asgi, wsgi and workers are not ported (they use
  poll, dup, fcntl, parent watching).
- **Glue (`glue/pythago_win32.h`, `#ifdef _WIN32` in `pythago_py.c`, `pythago_glue.c`):**
  pthread keys/once/mutexes on FLS, InitOnce and SRW locks; futex waits on
  WaitOnAddress; no fork hooks; `restore_signal` a no-op; `pythago_py_fastcall` calls the
  Go dispatcher directly (a `syscall.NewCallback`, accepted from any thread) instead of
  `crosscall2`; the TLS directory (`_tls_used`, `_tls_index`) the DLL needs without a CRT
  (in the header, the one file including it). Counters Go reads as 64-bit use
  `pythago_long` (`long long` on Windows, `long` elsewhere). The pthread wraps are
  compiled out: CPython starts its own threads on Windows.
- **Pitfalls found:**
  - **Prebuilt objects changed although Linux code did not:** the Python headers' macros
    contain `assert`, the glue is built without NDEBUG, so `__LINE__` is in the objects
    (`__assert_fail("PyTuple_Check(kwnames)", ..., 585)`): any line inserted above an
    assert site changes them. Bisected by compiling with the prebuild's flags and diffing
    `clang -E`. Regenerated `glue/prebuilt` (Linux here, darwin with `ci/darwin.sh
    glue-update` on dengaku); `ci/glue.sh` and `ci/darwin.sh glue` pass.
  - `long`→`long long` alone also changes LP64 code (distinct TBAA types), hence the
    typedef.
  - rpcndr.h's `#define small char` broke the glue's `small` arrays: `#undef small`.
  - Windows' `pyconfig.h` leaves `Py_GIL_DISABLED` to the compiler (sysconfig passes it
    to extension builds): without `-DPy_GIL_DISABLED=1` the 3.14t glue compiles against
    the GIL structs and links `python314.lib`.
  - **3.14t PyASCIIObject bitfields differ on Windows:** `state` begins with an
    `unsigned char interned`, then `unsigned int` bitfields; GCC/Itanium packs them at
    byte 49, MSVC at byte 52. `internal/tools/nativelayout` now reads COFF probes
    (`-cflags`, `-build`); `internal/layout` has windows/arm64 definitions for 3.13,
    3.14 (equal to Linux) and 3.14t (bytes 52). Control: the 3.14t definition with
    Linux's 49 makes `TestStringRuneAccess` and `TestNativeReadsNeedNoPythonAdmission`
    fail with `py: native Unicode data layout mismatch`. The ELF path regenerates the
    checked-in Linux arm64 3.13 file byte for byte.
- **Glue DLL build:** clang (pinned LLVM 23) `--target=aarch64-pc-windows-msvc
  -nostdinc` against Wine's `msvcrt/` and `windows/` headers (`-D_UCRT -D__WINE_PE_BUILD
  -fms-extensions`), `lld -flavor link /dll /noentry /brepro` with PBS's
  `python3XY.lib` and Wine's ucrtbase/kernel32/kernelbase/vcruntime140 import libs; a
  generated `pythago_symtab` (43 entries) is the one export. Imports: python313.dll,
  ucrtbase, kernel32, kernelbase. Prebuilt `glue/prebuilt/cp313-windows_arm64.dll` and
  `cp314t-windows_arm64.dll` (`-prebuild-glue` with `-windows-sdk`), byte-identical
  across two builds; consumers need no SDK.
- **py (Go):** `ccall_windows.go` calls through `syscall.SyscallN` (its frame is in the
  M, so callbacks that move the goroutine stack are harmless; `_cgo_topofstack` does not
  exist without cgo on Windows); `setCallback` passes one `syscall.NewCallback`.
  `image_windows.go`: `RegisterImageFiles(id, fs)` → at Init, extract to
  `%LOCALAPPDATA%\pythago\image-<id>` (temp dir + rename, first rename wins),
  `LoadLibraryExW(pythago_glue.dll, LOAD_WITH_ALTERED_SEARCH_PATH)`, symtab via
  GetProcAddress, the C API looked up in python3XY.dll (`RegisterExports(nil, ...)`,
  `lookupSymbol`), `DLLs\` on sys.path. Also: no spares, `spawn` default, venv layout
  `Lib\site-packages` and `Scripts\`.
- **Windows behaviours fixed, each with its failing run before the fix as control:**
  - site.py's `abspath` turns `/pythago-fs/x` into `Z:\pythago-fs\x`: the path hook and
    `GoFSLoader.get_data` normalize (`_norm`), and after initialization py restores the
    virtual entries in sys.path. Before: `ModuleNotFoundError: No module named
    'signal'`, then `FileNotFoundError: ... '/pythago-fs/app/appmod\\data.txt'`
    (pkgutil.get_data joins with `os.path.join`), then `sys.path[0] ==
    'Z:\\pythago-fs\\app'`.
  - **Ctrl+C would never reach Go:** importing `signal` installs Python's SIGINT handler
    when the C runtime's SIGINT is SIG_DFL. On Linux Go's sigaction handler prevents
    that; on Windows Go uses a console control handler the CRT cannot see, and the UCRT's
    own console handler (registered later, called first) would consume Ctrl+C. With
    `Config.PythonSignals` off, py now sets SIGINT back to SIG_DFL after importing
    `signal`. Before: `TestPythonSignals` saw `default_int_handler` with
    PYTHAGO_TEST_PYSIGNALS=0. Not yet checked: an actual console Ctrl+C reaching
    os/signal.
- **pythago-dist `-target windows/arm64`** (`cmd/pythago-dist/windows.go`): PBS
  install_only_stripped; image files `python3XY.dll`, `python3.dll`/`python3t.dll`,
  `vcruntime140*.dll`, `DLLs\` without `_test*`, `_tkinter`, tcl/tk; the glue DLL
  (prebuilt, else compiled with `-windows-sdk`); stdlib zip from `Lib\` with `.pyc`
  from the host's PBS Python (bytecode is OS-independent: `packStdlib` now also uses
  the host Python for another GOOS); the registration file embeds `image_<goos>_<goarch>/`.
- **Tests (`py`, windows/arm64, `pythago-wine:11.22`):** 48 pass, 4 skip (forkserver,
  fork child, signal restore, thread limit) on 3.13, 3.14 and 3.14t. `TestExtensionShim`
  on Windows checks the design: `_socket`, `_ssl`, `_sqlite3`, `_ctypes`, `select`,
  `_hashlib`, `pyexpat`, `unicodedata` load from the image's `DLLs`, `sys.dllhandle` is
  the image's python3XY.dll beside `pythago_glue.dll`, and `ctypes.pythonapi` sees the
  same `PyExc_SystemError`. `TestPythonThreads` keeps its Python checks (native ids
  match Go's view of the thread). `TestModulesInChildren` (spawn) passes.
- **CI:** `ci/windows-wine.Dockerfile` (Wine 11.22 source, sha256 f172e9c1…63c9ee,
  Debian trixie snapshot 20261108), `ci/windows-wine.sh image|sdk|gil|ft` (test image in
  `$CACHE/windows-<v>`, not `.work`), `ci/glue.sh` checks the Windows DLLs when the SDK
  exists.
- **Gate:** ci/unit.sh ft passed; gil failed once in wsgi `TestHTTP2RequestFields`
  (`unexpected end of JSON input`, after an unrelated sendfile error from another wsgi
  test); the test passes 20/20 alone; wsgi is the other session's in-progress area
  (files edited 13:50-15:26). Rerun below. ci/darwin.sh glue, gil arm64 and ft arm64
  passed (amd64 Mac skipped: a crash-loop experiment runs there).
- **Gate, continued:** the gil tier rerun passed (no failures), so the wsgi
  `TestHTTP2RequestFields` failure was intermittent (TODO-worthy for wsgi's owner, not
  this change). `ci/windows-wine.sh sdk`, `gil` and `ft` passed (py under Wine through
  the tracked script, test images in `~/.cache/pythago-ci/windows-<v>`); `ci/glue.sh`
  passed with the Windows DLLs included (SDK in `~/.cache/pythago-ci/windows-sdk`).

### 2026-11-18 — aio, asgi, wsgi and workers on Windows: overlapped pipes and sockets on Go's I/O completion port; all tests pass under Wine

- **Context:** the user asked to port the remaining packages; during the work, "If
  overlapped I/O is a better fit, go use it", then "IOCP?" about the workers listener.
  Convention fixed on the user's remark: Python-side platform checks use
  `sys.platform == "win32"` (as the existing `"linux"`/`"darwin"` checks), never
  `os.name`.
- **aio:**
  - POSIX selector code moved to `selector_unix.go`; `selector_windows.go` watches each
    registered socket with WSAPoll in 50 ms slices (no dup into Go's poller on Windows),
    with the same re-arm protocol.
  - Non-waiting reads (`readnow_unix.go`/`_windows.go`): on Windows FIONREAD, then one
    WSARecv of what is buffered (Go's sockets are overlapped: a plain recv would wait).
    `TestCoalescedReads` (largest chunk) failed with the first no-coalescing stub
    (`('largest chunk', 39530, 'chunks', 138)`) and passes with it.
  - Pipes: subprocesses use `asyncio.windows_utils.Popen` on Windows (as asyncio's
    proactor), whose parent-side pipe ends are overlapped (stdin duplex); Go gets
    duplicated Win32 handles, and since Go 1.24 `os.NewFile` puts overlapped handles on
    its I/O completion port (`windows.IsNonblock`). `os.pipe()` pipes stay synchronous.
  - Hangup (`hangup_windows.go`): a pending read on the duplex write end ends with EOF
    when the child closes its end; a pipe that refuses reads falls back to a null-write
    probe (ERROR_NO_DATA / ERROR_BROKEN_PIPE) every 50 ms inside `RawConn.Control`.
  - `waitChild`: OpenProcess + WaitForSingleObject + GetExitCodeProcess.
  - Tests: the subprocess scenarios use `sys.executable` one-liners on Windows; the stock
    loop there is the proactor (killed → exit code 1, TerminateProcess); asyncio's
    selector loop has no subprocesses on Windows; `os.pipe` scenarios run on GoEventLoop
    alone (the proactor cannot serve anonymous pipes). 44/44 pass.
- **asgi:** `TestMain`'s re-exec (GODEBUG=http2xconnect=1) runs a child on Windows
  (`reexec_windows_test.go`); pathsend fixtures have no FIFO on Windows; ioapp's
  subprocesses use Python on Windows; the SQLite affinity app collects the connection it
  could not close (Windows cannot remove an open file). Two product fixes:
  - `Server.Close` never closed the `PathSendRoots` `os.Root`s (harmless on Linux, but the
    directory stays locked on Windows): `closeRoots`.
  - `openInRoots` checked `"../"`, while `filepath.Rel` gives `..\` on Windows: now
    `filepath.Separator` (os.Root still refused the escape; the check decides which root
    to try).
  - **Wine limitation:** `os.Root` follows a symlink out of the root under Wine (Unix
    symlinks; `Lstat` sees the symlink, `Root.Open` returns the target; Linux:
    `openat escape: path escapes from parent`). The test checks os.Root first and skips
    that sub-case only where os.Root itself follows the escape. 41/41 pass, inline and
    LoopThread.
- **wsgi:** `file_wrapper` passes the Win32 handle (`msvcrt.get_osfhandle`) on Windows;
  `file_windows.go` checks FILE_TYPE_DISK and not a directory, sizes it, duplicates the
  handle (sharing the file position, as dup does). Control: forcing `regularSize` false
  makes `TestFileWrapper` fail (`length -1, want 3145851`). SQLite tests use a
  best-effort temp dir on Windows (connections in `threading.local` outlive the test).
  28/28 pass.
- **workers:** Go has no `net.FileListener` or `ExtraFiles` on Windows and no SIGTERM.
  - New API `workers.NotifyContext(ctx)`: done on SIGINT/SIGTERM and, in a Windows
    worker, when the stop pipe closes. Worker mains (the test's, `asgiserve`, `wsgiserve`)
    use it; on POSIX it equals `signal.NotifyContext(ctx, os.Interrupt, SIGTERM)`.
  - Windows (`workers_windows.go`): Spawn passes an inheritable duplicate of the listening
    socket and a stop pipe's read end via `SysProcAttr.AdditionalInheritedHandles`
    (values in `PYTHAGO_WORKER_HANDLES`); the supervisor closes the write end to stop a
    worker, and it closes anyway when the supervisor exits (parent death).
  - The listener cannot use a completion port: one socket object takes one port, and the
    supervisor's Go runtime associated the shared socket with its own. Accept uses an
    overlapped AcceptEx whose OVERLAPPED event has its low bit set (completion not
    queued to any port), waits for it and Close's event together, and Close cancels it
    (CancelIoEx); SO_UPDATE_ACCEPT_CONTEXT, then the accepted socket (overlapped)
    becomes an `os.File` on the worker's I/O completion port (`socketConn`, with
    CloseWrite via shutdown).
  - Found on the way, under Wine: a blocking `accept()` is not woken by `closesocket`
    from another thread (workers then ran out the 60 s grace), and `WSAPoll` on an
    inherited socket returns POLLNVAL with WSAENOTSOCK (10038); AcceptEx works.
  - Controls: `stop` not closing the pipe → `TestSpawn`: `Spawn took 1m0.003s to stop
    its workers`; Close not setting its event → `1m0.002s`. With both: `TestSpawn` 0.43 s.
  - `TestSpawnKeepsListenerNonblocking` (O_NONBLOCK) is POSIX-only; `TestSpawnGivesUp`
    uses this binary exiting at once instead of `sh`.
- **CI:** `ci/windows-wine.sh gil|ft` runs py, aio, asgi (inline and loop thread), wsgi
  and workers. The tracked `ci/windows-wine.Dockerfile` builds from scratch (rc 0).
- **Carry:** on Windows, prefer overlapped handles that Go's `os.NewFile` puts on its I/O
  completion port; a handle shared across processes keeps the first process's port, so
  complete overlapped operations on it with an event (low bit set) instead.
- **Gate:** ci/unit.sh gil and ft (arm64, amd64 under qemu), ci/windows-wine.sh gil and
  ft (all five packages, asgi in both modes, under Wine), ci/darwin.sh gil and ft on the
  arm64 Mac: all passed (logs `.agents-workspace/tmp/win/gate2/`). The amd64 Mac was
  skipped: the crash-loop experiment runs there.

### 2026-11-18 — Windows CI jobs: test binaries cross-built on Linux, run on windows-11-arm, windows-2025 and Windows Server Core; windows/amd64 glue and layouts

- **Context:** the user asked whether "Windows core" could be used for testing (meaning
  Windows Server Core), then to add GitHub Actions Windows jobs; asked "Do we need Wine on
  CI?": no.
- **Server Core here:** its containers need a Windows kernel (not Docker on Linux), and
  Windows Server is publicly x64 only, so a VM on this arm64 host would be full emulation.
  The CI runs it instead: on windows-2025, in `mcr.microsoft.com/windows/servercore:ltsc2025`
  with process isolation (image build = host build).
- **Jobs (`.github/workflows/ci.yml`):** `windows-build` (ubuntu-24.04, arch × variant)
  runs `ci/windows-build.sh gil|ft arm64|amd64 DIR`: the windows test image and the py,
  aio, asgi, wsgi and workers test binaries, uploaded as an artifact (each binary embeds
  the image and is its own sys.executable). `windows` runs them with
  `ci/windows-run.ps1` (PowerShell: Server Core has no bash; asgi twice) on windows-11-arm,
  windows-2025, and Server Core on windows-2025. Actions pinned to the current releases
  (upload-artifact v7.0.2, download-artifact v8.0.2). No Wine in CI: the glue DLLs come
  from `glue/prebuilt`; they are checked where Wine's SDK exists (`ci/glue.sh`), as the
  darwin objects only on a Mac. Not yet run: it needs a push. actionlint could not be
  installed here (the tool install was denied); the workflow parses as YAML.
- **windows/amd64:**
  - `ci/windows-wine.Dockerfile` builds Wine's PE side for aarch64 and x86_64 (image
    `pythago-wine:11.22-2`); `ci/windows-wine.sh sdk` copies both architectures' import
    libraries (`lib/arm64`, `lib/amd64`).
  - x64 MSVC-ABI code that uses floating point references `_fltused`, which CRT startup
    objects define: `lld: error: undefined symbol: _fltused`. `pythago_win32.h` defines
    it for x86/x64 only; the arm64 DLLs stay byte-identical (b7f6de6d…, 8be9d415…).
  - `glue/prebuilt/cp313-windows_amd64.dll` (5d27c249…), `cp314t-windows_amd64.dll`
    (222f7d11…); `ci/glue.sh` reproduces all of glue/prebuilt with the SDK.
  - Layouts for windows/amd64 (3.13, 3.14, 3.14t): as on arm64, the GIL ones equal Linux
    and 3.14t has the bitfields at byte 52 (Linux 49).
  - `ci/windows-build.sh gil amd64` cross-builds the five binaries (PE32+ x86-64, 11-56
    MB). They have not run anywhere: amd64 is first exercised by the CI job.
- **Refactor:** `ci/windows-wine.sh gil|ft` now builds with `ci/windows-build.sh` (with the
  prebuilt glue, as users get it) and runs under Wine.
- **bao v0.0.9** (6bfae04, the merge of PR #44: MSVC DLL TLS loads in any register and far
  from `_tls_index`, windows/arm64 and amd64) is tagged. pythago stays on v0.0.8: the
  Windows images load python3XY.dll from disk, so nothing needs it; pin v0.0.9 if
  embedding python3XY.dll (programs without extension modules) is ever wanted.
- **Gate:** ci/windows-wine.sh gil and ft (through ci/windows-build.sh, image
  pythago-wine:11.22-2) and ci/unit.sh gil and ft passed (logs
  `.agents-workspace/tmp/win/gate3/`); ci/glue.sh passed with the Windows SDK.

### 2026-11-18 — Windows image cache: Config.ImageCacheDir, and stale images removed under an in-use marker

- **Context:** tackle-todos round (user picked it). Done by a sweep agent; reviewed here.
- **Design (`py/image_windows.go`):** layout under `Config.ImageCacheDir` (default
  `os.UserCacheDir()/pythago`, else `os.TempDir()`): `image-<id>`, the marker
  `image-<id>.lock` beside it, `tmp-<id>-<rand>`, `trash-<rand>`.
  - A process opens its marker (GENERIC_READ, share read|write, not delete) for its
    lifetime *before* checking that `image-<id>` exists; busy (cleanup holds it, or a
    delete is pending) → retry with backoff up to 30 s, then Init fails.
  - Cleanup (after the image is loaded, best effort, errors ignored): for each other id,
    open its marker with DELETE, share 0, FILE_FLAG_DELETE_ON_CLOSE; it fails while any
    process holds the marker → skip. Holding it, rename `image-<id>` to `trash-*`, close
    (deleting the marker), RemoveAll the trash: a starting process of that version sees a
    complete image or none. `tmp-*` older than 1 h are removed (another id's only under
    its exclusive marker); `trash-*` always.
  - **Why a marker, not "the delete fails":** control 1 (cleanup ignoring a refused
    exclusive open) removed a running process's image under Wine: `image inuse0 changed
    while a process used it: 0 of 32 files left`, then `No module named '_sqlite3'` in
    that process. Wine lets a directory with loaded DLLs be renamed and deleted.
- **Tests (`py/imagecache_windows_test.go`, children of `sys.executable` with
  `ImageCacheDir`/id overrides via env):** `TestImageCacheDir`, `TestImageCacheRemovesStale`,
  `TestImageCacheKeepsImageInUse` (a holder keeps its 32 files and imports four modules
  not loaded before, after another process's cleanup; removed once it exits),
  `TestImageCacheConcurrentStarts` (4 × (3 + 3) simultaneous starts). Controls: no
  exclusive-open check, holders not keeping the marker, ImageCacheDir ignored, no
  cleanup: each fails with its diagnostic. Under Wine (arm64, gil and ft): full py suite
  passes (52 pass, 4 skip on gil), `TestImageCache -count 25` passes.
- **Only Windows can confirm:** NTFS share-mode behaviour of the marker
  (DELETE/share 0 refused against a reader without FILE_SHARE_DELETE; DELETE_ON_CLOSE;
  ERROR_ACCESS_DENIED while a delete is pending); antivirus/indexer holding files (the
  rename then fails safely and is retried at the next start).

### 2026-11-18 — wsgi: known-length bodies are written after the application returns; the GIL/P convoy at 64 KiB is gone (9.2k → 37.1k req/s at GMP=2 on one core)

- **Context:** tackle-todos round, TODO "GIL/P convoy in `py`", mitigation (a) from the
  diagnosis entry above. Done by a sweep agent; reviewed here.
- **Before:** a known-length (exact list/tuple) chunk was copied into `pending` up to
  `maxPending` (16 KiB); past that, `py.Release` wrote `pending` and the chunk detached:
  one Release per 64 KiB response, a second GIL acquisition and an admission-slot re-queue.
- **Change (`wsgi/wsgi.go`):** known-length chunks of 4 KiB or more (`minHeld`) that do
  not fit the copy budget are held by reference (`NewRef`; a repeated object takes one)
  as segments between copied bytes, and `ServeHTTP` writes them after `py.Do` returns.
  Small chunks past the 16 KiB copy budget still go through a detached write (bounded Go
  memory; `TestSlowReaderDoesNotStall`). Held references are queued
  (`Server.releaseWritten`) and closed by the next request's `py.Do` or `Server.Close`,
  the aio/asgi pattern. A body longer than its Content-Length still raises in Python.
  Differences: a client that disconnects during a large list body now ends the request as
  DONE rather than ABORT; an idle server keeps the last responses' bytes until the next
  request or Close.
- **Test:** `TestKnownLengthBodyAfterReturn` (`wsgi/knownlength_test.go`, hook
  `writeDetached`/`CountDetachedWrites`): bodies whole and ordered over HTTP/1.1 and
  HTTP/2, Content-Length and HEAD, wrong Content-Length never complete, 320 concurrent
  distinct responses, zero detached writes (positive controls: streamed bodies counted,
  one small chunk past 16 KiB counted once), a shared object's refcount stable over 50
  responses. Controls: old Release path → `978 body writes happened while Python's call
  was in progress`; no reference → `302 of 320 concurrent responses were not their
  chunks`; never released → `SHARED's reference count went from 2 to 52`.
- **Numbers** (CPython 3.13.18, server on core 7, httpload 15-17,19, c64, 2 s + 5 s, 6
  interleaved pairs per cell, 12:19-12:37, conntrack ≤ 3.4k; `.agents-workspace/tmp/convoy/`):

  | scenario | GMP | before | after | ratio | CPU % | vol. switches/s |
  |---|---|---|---|---|---|---|
  | 64 KiB | 1 | 38.7k | 39.7k | 1.03 | 100 → 100 | 136 → 132 |
  | 64 KiB | 2 | 9.2k | 37.1k | 4.05 | 42 → 98 | 67.8k → 4.5k |
  | 64 KiB TLS | 2 | 7.2k | 19.5k | 2.69 | 49 → 99 | 53.0k → 3.4k |
  | 16384 B | 2 | 27.6k | 27.8k | 1.01 | 97 → 97 | 14.9k → 14.9k |
  | 1 KiB | 2 | 51.0k | 51.5k | 1.01 | 98 → 98 | 6.4k → 6.5k |
  | 200 µs + 64 KiB | 1 | 2.5k | 4.4k | 1.77 | 63 → 99 | 23.0k → 4.4k |
  | 200 µs + 1 KiB | 1 | 4.5k | 4.5k | 1.00 | 99 → 99 | 4.5k → 4.4k |

  64 KiB at GMP=2 is now 0.93× of GMP=1 (was 0.24×); p99 rises with the throughput in
  the 200 µs cells, matching the 1 KiB control.
- **Left:** the mechanism (a GIL waiter holding its P) remains: streamed bodies,
  sendfile, readinto and many small chunks past 16 KiB still use Release; 16384 B at
  GMP=2 is 0.68× of GMP=1. Mitigations (b) and (c) remain.
- **Gate for both units (image cache, known-length bodies):** ci/unit.sh gil and ft,
  ci/windows-wine.sh gil and ft, ci/darwin.sh gil and ft on the arm64 Mac all passed
  (`.agents-workspace/tmp/win/gate4/`).

### 2026-11-18 — The cString corruption was a stack copy before the C call: morestack in the old callStr's append freed the 2 KiB stack, and another goroutine reused it

- **Context:** tackle-todos round 3 (user picked "Stack-buffer root cause"). The fix had
  been `//go:noinline` on `cString` with the cause unknown (`LTM/go-c-ffi.md`, "How the
  stack-resident `cString` buffer was isolated").
- **Calling convention, audited** (`py/ccall.go`, `ccall_arm64.s`, runtime go1.26.11):
  Go→C runs on g0 via `cgocall`/`asmcgocall`; while the goroutine is in C its stack
  cannot move (`copystack` throws with `syscallsp != 0`, `isShrinkStackSafe` is false).
  C→Go (`crosscall2 → cgocallback`) runs on `m.curg`, the same goroutine, below its
  `cgocall` frames, as an ordinary running goroutine: morestack may copy its whole stack,
  including the caller's frame. So the calling goroutine's stack can move under a pointer
  C holds in two windows: (A) a Go call between taking the uintptr and the C call, (B) a
  C→Go callback during the call.
- **Root cause (window A):** the old variadic `callStr` did `append([]uintptr{u},
  args...)` after converting the inlined, stack-resident buffer to a uintptr. Under 2000
  goroutines, `growslice → mallocgc` slow paths (`mspan.nextFreeIndex`, `uncacheSpan`,
  `gcParkAssist`, `gcMarkDone`) hit morestack on a 2 KiB initial stack: `newstack →
  copystack(2048 → 4096) → stackfree(old)` puts the old stack at the head of the P's
  stack cache, the next goroutine stack allocation takes it, and C reads that goroutine's
  frames. This explains every earlier observation: `_cgo_topofstack` equal before and
  after (the move precedes the thunk), Go's view intact (copystack relocated the buffer),
  timing dependence (a delay widened the reuse window). Stack shrinking plays no part.
- **Evidence** (copy of the tree with the pragma removed, `.agents-workspace/tmp/r3/stackbuf/`;
  an instrumented go1.26.11 `newstack` printing growths over a `callStrOld` frame):
  - old shape in `mainDict`, `TestGoroutines`, ft, 15 interleaved rounds: inlined 11/15
    runs fail (`UnicodeDecodeError ... byte 0x80..0xa4`, `NameError: name 'bump' is not
    defined`: `AddModuleRef` made a module under a garbage name), with
    `GODEBUG=gcshrinkstackoff=1` 12/15, `//go:noinline` restored 0/15; gil 3/10 vs 0/10;
  - `TestStackbufOldCallStr` (2000 goroutines × 5 Do, a callee that never calls Go):
    bad reads only on calls whose stack moved; pre-growing the stack
    (`STACKBUF_PREGROW=16`) gives 0 moves and 0 bad reads; the traced runtime showed
    each stale address inside a 2 KiB range a growth had just freed, and the garbage read
    (`"(\xdd%\x9a]E\x00\x00"` = 0x455d9a25dd28 inside [0x455d9a25d800, 0x455d9a25e000))
    is a frame pointer of the goroutine now running there;
  - deterministic window A (`TestStackbufGrowBeforeC`): with `GODEBUG=efence=1`, SIGSEGV
    "signal arrived during cgo execution" at the old address; with a goroutine filling
    its stack with 0xAB, C reads `"\xab"×8` 5/5 (ft and gil); control without the growth
    passes;
  - window B (`TestStackbufCallbackDuringC`: `PyObject_CallMethod` whose `__getattr__`
    calls Go, which grows the stack before C reads the argument): SIGSEGV with efence,
    0xAB reads in 2-4 of 5 runs otherwise; control without growth reads `"__main__"`.
- **Current tree:** no live bug. With `cString` inlined into today's `callStr0`/`callStr1`
  the buffer is stack-allocated (`-m`: `does not escape`) but no Go call sits in the
  window (0/40 `TestGoroutines`); window B stays possible for a type with a `tp_getattr`
  calling Go, so the pragma stays. Every other buffer handed to C in `py`, `aio`, `asgi`,
  `wsgi`, `cmd` is converted inside a `//go:uintptrescapes` call expression and escapes
  (checked with `-gcflags=-m`).
- **Why it was missed:** the earlier probe watched the call itself, where the stack never
  moves; the move happened just before it.
- **Changes:** the `cString` comment (`py/capi.go`) names both windows; `LTM/go-c-ffi.md`
  records the mechanism.
- **Carry:** never let a stack address reach C as a uintptr across any Go call or
  callback; convert in the call expression of a `//go:uintptrescapes` function, or
  allocate on the heap. `GODEBUG=efence=1` turns this class into a deterministic fault.

### 2026-11-18 — asgi TestInline expected inline dispatch while the loop thread could still hold the worker; the dispatch was right

- **Context:** tackle-todos round 3 (user picked the flaky tests). TODO: `inline_test.go:159/167
  ... ran at {LoopThread:true ...}, want inline`, 4 in 2400 under load.
- **Audit:** `worker.submit` (asgi.go) appends to `w.pending`; a request that finds the
  worker free runs it inline, otherwise the holder runs it. The loop thread holds the
  worker from `attach` until its first wait, and every time it wakes (Lock after
  `py.Release`, Serve at each poll, Unlock before `aio.batch.wait`). A request arriving
  then runs on the loop thread by design (`serve_inline`'s docstring says so). The test
  assumed the loop thread idle between sequential requests; it is not right after startup
  (line 159, `/`) or right after a request that continued on the loop thread (167 `/task`
  after `/sleep`, 181 `/body` after `/task`).
- **Fix (test only):** `asgi/export_test.go` `WaitIdle(s, timeout)` polls each worker
  under `w.mu` until `!w.busy && len(w.pending) == 0`; `TestInline` waits for it before
  each request (`fetchIdle`) and keeps its strict assertions.
- **Numbers** (10 parallel copies, `-test.run '^TestInline$'`, cores 0-4,10-14, original
  and fixed alternated; logs `.agents-workspace/tmp/r3/asgi-flake/logs/`):

  | configuration | original | fixed |
  |---|---|---|
  | native ft, 3 rounds | 38 / 9000 | 0 / 9000 |
  | native gil, 3 rounds | 22 / 9000 | 0 / 9000 |
  | qemu amd64 ft, 3 rounds | 361 / 3000 | 0 / 3000 |

- **Controls:** inline dispatch disabled → all four checks fail (`inline_test.go:177: /:
  ran at {LoopThread:true ...}, want inline`); `WaitIdle` a no-op → the flake returns
  (12/3000 native, 109/1000 qemu); worker never released → `/ worker 0 still held after
  10s`.
- **Carry:** inline is guaranteed only for a request that finds its worker free; a test
  that checks where a request ran must wait for the worker first.

### 2026-11-18 — wsgi TestHTTP2RequestFields: a reset stream can carry a 200 with no body; the test treated it as a response

- **Context:** tackle-todos round 3. The gil tier failed once with
  ```
  wsgi_test.go:676: unexpected end of JSON input
  ```
- **Cause (test race, no product bug):** the malformed `content-length` cases (`"5,
  100000"`, `"+5"`) with a 5-byte DATA frame. net/http's HTTP/2 server fails `ParseUint`,
  sets `ContentLength = 0` (CONTENT_LENGTH "0"), and resets the stream when the DATA
  frame exceeds it (PROTOCOL_ERROR). Usually the reset wins (status 0, Reset); on a busy
  host the 200 HEADERS go out first and RST_STREAM drops the body: `h2raw.Do` returns
  `Status=200, Reset=true, Body=""`. The test checked only the status.
- **Fix:** `if resp.Status != 200 || resp.Reset { continue }` (a complete response ends
  with END_STREAM, which `h2raw` returns on before any later RST, so Reset means no
  complete response).
- **Not related:** the preceding `file_wrapper: Content-Length 3145852 is longer than the
  3145851 bytes left in the file` is `TestFileWrapper`'s deliberate `len(data)+1` case.
- **Controls:** without the fix, 4 copies × 150 under load on cores 0-2: 11/600 fail with
  the original message; with it 600/600 (and 600/600 on ft). Putting the raw header in
  CONTENT_LENGTH (`wsgi/environ.go:92`) fails the test: `content-length "05":
  CONTENT_LENGTH "05" for a 5-byte body`.
- **Seen along the way:** when the host ran short of loopback ports (TIME-WAIT about
  10.8k with parallel stress), `TestEnviron` (`REMOTE_ADDR/SERVER_NAME = "::1", want
  "127.0.0.1"`: httptest fell back to `[::1]`), `TestEnvironMatchesWsgiref` (`OSError:
  [Errno 98] Address already in use`) and `TestIODNS` failed for 40 s, then recovered.

### 2026-11-18 — GIL/P convoy mitigation (b), slot priority for Release, is in but does not change throughput; a GIL gate (c1) does

- **Context:** tackle-todos round 3 (user picked "Convoy (b)"); follows "wsgi at
  GOMAXPROCS=2 on one core is a GIL/P convoy..." and "wsgi: known-length bodies are
  written after the application returns".
- **Before:** Release gave its slot to the first goroutine blocked in Do (FIFO channel),
  then re-took one with a blocking send at the back of the queue, then a second blocking
  GIL acquisition (`cPyReacquire`).
- **(b) in `py/thread.go`:** Do's exit and Release call `putSlot()`, Release's re-entry
  `getSlotBack()`; a returning goroutine that finds no slot queues in `admission.queue`
  and `putSlot` hands it the slot (the channel token stays, the owner changes). After
  `returnRun = 4` handoffs in a row a slot goes to the channel, so arrivals get at least
  one in five. A lock-free fast path checks `returning` before and after its receive.
  Finalize, nested Do and Python threads (`slot_held == 0`) are unchanged.
- **Tests** (`py/admission_internal_test.go`): `TestReleaseRegainsSlotBeforeArrivals`,
  `TestArrivalsNotShutOutByReturns`, `TestReturnQueuedInPutSlotWindow` (hook
  `putSlotWindow`), `TestAdmissionUnderLoad` (64 × 500 with nesting). Controls:
  `returnRun = 0` → `the freed slot went to "arrival", not to the Do returning from
  Release (queued behind 3 new arrivals)`; `returnRun = 1000` → `the arrival got slot 11 of
  11 ([return ×10 arrival]); it must get one of the first 5`; fast-path re-check removed →
  `the return that queued in putSlot's window was not admitted (3 slots taken of 4, 1
  returns waiting)`; no decrement → `... 38248 returns wait`.
- **Numbers** (CPython 3.13.18, Cortex-X925, `wsgiserve -threads 10`, server core 5,
  httpload 7-9, c64, 2 s + 5 s, 6 interleaved pairs, 15:03-15:45; `/g/n` generator body
  (Release per chunk), `/c/n` 1 KiB chunks; `.agents-workspace/tmp/r3/convoy/`):

  | scenario | GMP | before | (b) | (b) + gate (c1) | CPU % (b → gate) |
  |---|---|---|---|---|---|
  | 1 KiB list | 2 | 65.9k | 64.6k | 59.5k | 98 → 99 |
  | 16 KiB gen | 2 | 10.1k | 10.3k | 34.8k | 32 → 100 |
  | 64 KiB gen | 2 | 9.6k | 9.8k | 31.9k | 34 → 100 |
  | 64 KiB in 1 KiB chunks | 2 | 3.3k | 3.6k | 6.0k | 56 → 98 |
  | 200 µs + 64 KiB gen | 1 | 2.6k | 2.6k | 4.4k | 62 → 100 |
  | GMP=1 controls (1 KiB, 16/64 KiB gen) | 1 | | unchanged | unchanged | |

  (b) only cuts OS threads (about 75 → 26). It cannot help: the convoy depends on how many
  goroutines wait in C for the GIL holding a P, and slots bound that number the same way.
- **(c1), scratch only** (`.agents-workspace/tmp/r3/convoy/gate/`, `go build -overlay`):
  on GIL builds a 1-token channel is taken before `cPyEnter` in outer Do and before
  `cPyReacquire`, released after the call returns, so at most one goroutine waits in C;
  others park in Go without a P. Costs: 1 KiB at GMP=2 −8%, 1 KiB-chunk p99 at GMP=1
  60 → 138 ms, about 2.5× voluntary switches. Nested Do inside Release and mainCalls are
  not gated; a waiter whose P was retaken still holds the GIL until it gets one back.
- **Carry:** the convoy lever is the number of GIL waiters inside C, not slot order.

### 2026-11-18 — 3.14t inline asgi scales 2.8× on 4 cores: Go GC, combiners waiting for a P, and shared refcounts on Go-made constants, not the combiner

- **Context:** tackle-todos round 3 (user picked "3.14t inline scaling"); investigation
  only.
- **Path:** on 3.14t `asgi.New` sets `Workers = GOMAXPROCS`, each with its own Python
  thread, loop, `worker.busy` and `serve_inline`; dispatch is round-robin; `Slots` also
  defaults to GOMAXPROCS. So there are already several combiners, one thread state each.
  Python's GC never ran under load.
- **Scaling** (CPython 3.14.10t PBS 20261110, Go 1.26.11, Cortex-X925; server on cores
  15..15+N-1 with GOMAXPROCS=N, `httpload -c 256 -d 5s -w 2s` on 19,10-14; 3 interleaved
  rounds, medians; uvicorn 0.54.0 + uvloop 0.23.0 + httptools 0.9.0 on stock 3.14t;
  `.agents-workspace/tmp/r3/ftscale/scale.jsonl`):

  | hello | 1 | 2 | 4 |
  |---|---|---|---|
  | inline | 62.7k | 103.1k | 177.3k (2.83×) |
  | inline `-workers 1` | 64.2k | 97.5k | 171.9k |
  | `-loop-thread` | 52.2k | 64.1k | 116.2k |
  | uvicorn `--workers N` | 80.8k | 166.7k | 344.1k (4.26×) |
  | Go net/http hello | 137.3k | 243.0k | 458.8k (3.34×) |

  Small JSON: inline 58.1k → 157.4k (2.71×), uvicorn 70.1k → 278.7k. At 4 cores CPU per
  request rises 14.95 → 19.5 µs and 13% of the CPU idles.
- **Findings:**
  - combiner count makes no difference (`-workers` 1/4/8/16 all 174-178k); 98% of
    `serve_inline` calls carry one request; a stalled worker collects up to 254 (round-robin
    convoy);
  - Go GC runs 220 cycles/s (0.24 core); `gcMarkDone` is 35% of sync blocking; `GOGC=800`
    → 25 cycles/s, +8-13% (RSS 95 → 134 MB), and most `worker.mu` contention (81% of
    mutex delay, `append` under the lock in `submit`) goes with it;
  - a combiner returning from C into `respond` often waits for a P (19.6% of scheduling
    latency under `py.Do → cgocallbackg1`; gdb samples show threads in `stoplockedm` and
    `startlockedm`); GOMAXPROCS=8 on 4 cores +5%, with GOGC=800 +20% (2 cores +11%, 1 core
    −4%);
  - scope constants Go creates once (method, http_version, scheme, server host and port)
    are shared non-immortal objects; on 3.14t every touch is a cross-core atomic
    refcount. A Python replica of `serve_inline` on stock 3.14t: 7.4 µs per request shared
    vs 4.35 interned at 4 threads (3.66 vs 3.27 at 1); unshared Python scales perfectly
    (control). Interning them (`sys.intern`, immortal on ft) and a per-worker `_ASGI` dict
    in a scratch copy: +5% at 4 cores (hello 183k → 191k; with GOGC=800 and GMP=8 213k →
    224k), +1% at 1.
  - perf is unavailable (`perf_event_paranoid=4`); gdb stack sampling stood in.
- **Options (not implemented):** intern the constants and copy `_ASGI` per worker (+5%,
  cheap); fewer per-request Go allocations (`request`, the `kick` chan, the scope tuple,
  15 boxed values, `append` under `w.mu`) or document GOGC (+8-13%); keep a P free for
  combiners (GOMAXPROCS above cores, or a C-side respond queue); dispatch to an idle
  worker instead of round-robin. `-processes 4` with GOGC=800 reaches 241k.
- **Carry:** "several combiners, one thread state each" is already how 3.14t runs; look
  at Go allocation rate and P availability first.

### 2026-11-18 — C extensions load on macOS: a runtime-generated, ad-hoc-signed dylib of absolute exports; each process pays about 0.36 s for macOS's first-load check

- **Context:** tackle-todos round 3 (user picked "macOS extension shim"; M5 darwin).
- **Premise checks** (macOS 26.5.2 on the M1 Max, 26.6.2 on the i9):
  - dyld never sees the image's libpython: bao's loader maps it inside the executable;
  - wheel and stdlib extensions bind `<flat-namespace>/_Py*`;
  - the executable cannot export them: Go's internal linker writes an empty export trie,
    and `//go:cgo_export_dynamic` is allowed only in `_cgo_*` files;
  - dyld honors `EXPORT_SYMBOL_FLAGS_KIND_ABSOLUTE` for dlsym, lazy and GOT binds
    (functions and data), and flat lookups find an RTLD_GLOBAL image;
  - dyld refuses a non-r-x `__TEXT` even with no code (`__TEXT segment permissions is not
    'r-x'`); arm64 refuses an unsigned dylib (`code signature invalid`); amd64 loads one
    without `LC_CODE_SIGNATURE` but refuses an invalid one.
- **Implementation:** `py/internal/extshim/macho_darwin.go` builds the dylib (an r-x
  `__TEXT` page of header and load commands; `__LINKEDIT` with an export trie, an N_ABS
  symtab for nm, an ad-hoc linker-signed signature as cmd/internal/codesign builds it;
  0.9 ms for 1,754 symbols). `extshim_darwin.go` writes it to a new temporary directory
  under `Dir`, dlopens it RTLD_NOW|RTLD_GLOBAL, removes both, and checks dlsym of the first
  symbol; `Memfd` is an error. `py/extshim_darwin.go` exports `^_?Py` as on Linux, install
  name `libpython-pythago.dylib`. `TestExtensionShim` no longer skips on darwin.
- **Tests:** `TestExportTrie`, `TestLoad` (codesign --verify, the image list, dlsym, a cc-built
  `-bundle -undefined dynamic_lookup` calling a function and reading data), `TestLoadSource`,
  `TestExtensionShimSource`, `TestExtensionShimExports`, `TestExtensionShimFlatNamespace`.
  py, aio, asgi (both modes), wsgi and workers pass on dengaku for gil and ft; py on
  macbook-pro-2 for gil and ft.
- **Wheels** (pip download, `.agents-workspace/tmp/r3/macos-shim/wheels.py`): markupsafe
  3.0.4 `_speedups`, numpy 2.5.5 (inv, det, eigvals, solve and svd from 8 threads), cffi
  2.1.1 (qsort callbacks, one from a thread), cp313 and cp314t on arm64 and x86_64; output
  equals stock PBS python3.13/3.14t.
- **Controls:** omit `PyExc_SystemError` → `symbol not found in flat namespace
  '_PyExc_SystemError'`; skip the signature → Init `code signature invalid`; export
  everything → `pythago_py_abi is in the global scope`; flat trie → `an export trie node
  has 1754 children`; export kind 0 → `dyld resolved PyShimTest_GetPID to 0x28e42a178, not
  its absolute address 0x18b03a178`; memfd accepted and temp dir kept both fail their
  tests. A first control (no edge split) passed rightly: insertion and dyld share the
  first-match rule.
- **Cost:** macOS checks every code file it has not seen; ASLR makes every shim new. dlopen
  of a new file is about 300-320 ms on arm64, 390-490 ms on amd64 (same cdhash at a new
  path about 120 ms, a known file 0.3 ms); a cc-built dylib costs the same. pyrun of an
  empty script: 0.41 s vs 0.045 s with `NoExtensions` (stock 0.02 s), paid by every worker
  process too. Options: load on the first extension import; a constant-content shim with
  resolver exports that can be cached; exports from the executable (Go linker/bao).
- **Left:** `PY_TIMEOUT_MAX` is libpython's only export outside `^_?Py` (Linux too); the
  image has no lib-dynload (`_dbm`, `_tkinter`).

### 2026-11-18 — darwin tiers get GitHub Actions jobs (macos-26, macos-26-intel); unverified until a push

- **Context:** tackle-todos round 3 (user picked "darwin in GitHub Actions").
- **Change:** `ci/darwin-gha.sh gil|ft|glue ARCH` runs `ci/darwin-host.sh` from a runner
  checkout (cache in `$RUNNER_TEMP/pythago-ci` via the new `PYTHAGO_DARWIN_CACHE`; for
  `glue` it selects an Xcode with the SDK pinned in `cmd/pythago-dist/prebuild.go`).
  `ci.yml` jobs `darwin` (gil, ft × macos-26 arm64, macos-26-intel amd64) and
  `darwin-glue` (macos-26). ssh path unchanged. Labels from docs.github.com on 2026-11-18
  (macOS 14 retired 2026-11-02; `macos-26-intel` is the last Intel image).
- **Checked:** on macbook-pro-2 with `env -i`, a fresh HOME and `RUNNER_TEMP`: gil and ft
  pass; ft through the unchanged ssh path in a new directory passes.
- **Risk:** the runner's macosx26.5 SDK comes from Xcode 26.5/26.6, the prebuilt darwin
  glue from the Command Line Tools' 26.5: the first suspect if `darwin-glue` mismatches.

### 2026-11-18 — A 3.14t asgi test binary on darwin/amd64 hung 25 h in Finalize after its tests passed; the test timeout does not cover TestMain

- **Context:** the Intel Mac crash loop of 2026-11-17 (`ploop.sh`, `asgi-spawn-ft.repro`,
  a binary built 2026-11-17 from a since-deleted diagnostic tree `repro-src`) still had
  one process running at 15:47 on 2026-11-18, launched 14:10 the day before, with
  `-test.timeout 600s`.
- **State** (`sample`, then stderr redirected with lldb `dup2` and SIGQUIT;
  `.agents-workspace/tmp/r3/maclhang/{sample734.txt,pythago-hang734.txt}`):
  - goroutine 1: `py.Finalize` (runtime.go:459) waiting on a channel for the main thread,
    called from `asgi_test.TestMain` (asgi_test.go:258), so every test had finished;
  - the main thread (`py.mainThread`, runtime.go:269, `call0`) inside libpython's
    finalization, in `_pthread_cond_wait`;
  - `asgi-worker-2` in `pythago_py_thread_main` → libpython → `pause()` (3.14 hangs a
    daemon thread that tries to re-attach after finalization starts);
  - `asgi-worker-0` returned from its Python function, waiting in
    `pythago_py_thread_main` (`__ulock_wait`: the join/detach handshake).
- **Why nothing stopped it:** `-test.timeout` covers only `m.Run`; a hang in `TestMain`
  after it (here `py.Finalize`) runs forever.
- **Not established:** whether the current tree can still do this (the binary may predate
  `Server.Close` joining its workers), and which libpython wait the main thread is in
  (bao-local symbols in the sample).
- **Carry:** give test binaries' `TestMain` a Finalize watchdog (panic with all goroutines
  after a bound) so a finalization hang fails the tier instead of hanging it.
- **Gate for round 3** (convoy (b), test flakes, darwin shim, darwin jobs, doc changes):
  ci/unit.sh gil and ft, ci/windows-wine.sh gil and ft, ci/darwin.sh gil and ft on the
  arm64 Mac all passed (`.agents-workspace/tmp/r3/gate/`).

### 2026-11-18 — macOS extension shim loads lazily: an empty-script start falls from 0.40 s to 0.043 s

- **Context:** user decision (round 3): "Lazy load (Recommended)" for the macOS shim's
  0.36 s first-load cost (entry "C extensions load on macOS").
- **Design** (`py/extshim_darwin.go`, `py/runtime.go`):
  - `start()` loads the shim eagerly only when `lazyExtensionShim` is nil (Linux).
    On darwin `initialize()` calls `extensionShimHook` right after `cInitializeCore`,
    so the hook is in place before the main phase (site, `.pth`, sitecustomize).
  - The hook is Python source wrapping the attributes CPython looks up at each call:
    - `_imp.create_dynamic`, used by every extension module;
    - `_ctypes.dlopen`, patched through an `_imp.exec_builtin` wrapper when `_ctypes`
      executes, before ctypes copies it as `_dlopen`;
    - `posix.register_at_fork(before=)`, which loads the shim in the parent, since a
      forked child cannot run Go.
  - The wrappers call a Go `NewFunc` around `sync.OnceValue(loadExtensionShim)`, which
    waits in `Release` (no GIL held, no stop-the-world blocked). A Python flag
    short-circuits later calls, and the originals are put back after the load.
  - A failed load is cached and not retried. It raises `ImportError(msg, name=, path=)`
    or `OSError(msg)`, where msg is
    `py: extension shim (C extensions need it; Config.NoExtensions disables it): <err>`.
  - `ExtensionShimMemfd` still fails at Init. A bad `ExtensionShimDir` fails at the
    first extension load.
- **Rejected:** a C dlopen hook. It would mean interposing inside the bao-translated
  libpython, and it would miss C-level dlsym on `dlopen(NULL)` just the same.
- **Known gaps** (TODO):
  - `import ctypes` pays the load (`pythonapi = PyDLL(None)`);
  - subinterpreters have no hook;
  - `CDLL(handle=)` is not covered;
  - a process's first fork pays the load;
  - the first import's traceback shows one extra `"<pythago>"` frame.
- **Tests** (`py/extshim_darwin_test.go`; each runs `py.Main` in a child of the test
  binary, `PYTHAGO_TEST_LAZYSHIM`; dyld's image list is read through purego; probe
  bundles `-bundle -undefined dynamic_lookup` raise from `PyInit`):
  - `TestExtensionShimLazy`, `...Site`, `...Threads` (8 at a barrier);
  - `...Ctypes`, `...Fork`, `...NoExtensions`, `...Error`.
- **Controls** (`controls.sh`, gil and ft on dengaku, all failing as intended):

  | Control | Failing test |
  |---|---|
  | eager load restored | Lazy: `('loaded at Init', [...])` |
  | `create_dynamic` not wrapped | Threads: `symbol not found in flat namespace '_PyExc_RuntimeError'` |
  | hook after `cInitializeMain` | Site |
  | racy flag instead of the Once | Threads |
  | no `_ctypes` hook | Ctypes |
  | `dlopen(None)` skipped | Ctypes |
  | no at-fork hook | Fork: `Go functions are not available in a forked child process` |
  | `NoExtensions` ignored | NoExtensions |
  | error swallowed | Error |
  | load retried | Error |

- **Measurements** (dengaku: M1 Max, macOS 26.5.2; PBS 20261110 3.13.18 and 3.14.10t;
  go1.26.11; cores not pinned; interleaved; `bench.sh`, `wtime.sh`):
  - pyrun with an empty script, 20 rounds:
    - gil 0.403 s → 0.043 s;
    - ft 0.406 s → 0.044 s.
  - `wheels.py` (markupsafe, numpy, cffi; `OPENBLAS_NUM_THREADS=1`), 5 rounds:
    - gil 1.217 → 1.218 s;
    - ft 0.666 → 0.673 s;
    - the output is identical to stock CPython's.
  - Aside: with default BLAS threading, ft numpy takes minutes under stock python3.14t
    too (247 s): OpenBLAS oversubscription, not pythago.
- **Also passed:** macbook-pro-2 (amd64, macOS 26.6.2) py gil and ft; Linux `./py` gil
  and ft; vet for darwin/{arm64,amd64} and windows/{amd64,arm64}.
- **Carry:** on macOS, anything new that loads native code binding Py* must go through
  `ensure()`. Wrapping a CPython entry point works only if CPython looks it up as an
  attribute at each call.

### 2026-11-18 — 3.14t inline asgi: interned constants, per-worker scope values and recycled requests give +17% at 4 cores

- **Context:** user decision (round 3): "Cut Go allocations, Intern constants", from the
  profile in "3.14t inline asgi scales 2.8×" (GC at 220 cycles/s; mortal shared
  constants contended across cores).
- **Changes** (`asgi/asgi.go`, `asgi/fields.go`, `asgi/pythago_asgi.py`):
  - **Constants:** method, version, scheme and the server host go through
    `sys.intern` (immortal on 3.14t). The posted (LoopThread) scope uses the same
    objects.
  - **Server address per worker** (`worker.addrs`): keyed on the `*net.TCPAddr` fields,
    with no per-request formatting, capped at 64 entries. The port int belongs to the
    worker, since it can't be made immortal.
  - **Python side:** each `_Worker` has `self.asgi = dict(_ASGI)`, and `root_path` is
    interned.
  - **Recycled requests:** `request.refs` counts ServeHTTP until `finish`, a queued
    inline batch until `serve_inline` took its values, and each `_pythago_asgi` call
    (`reqOf` → `Server.acquire` under `s.mu`).
    - The last `unref` drains `kick`, zeroes the request and puts it in a per-Server
      `sync.Pool`.
    - It keeps `kick`, a `[2]action` queue array, `[8][2]string` head pairs and a
      first-body-chunk buffer (claimed by CAS), and `buf` up to 16 KiB.
    - WebSocket requests are not pooled, since the reader can outlive the handler.
  - **Batching:** `worker.pending` is `[]*request`, and the holder builds the arguments
    outside the lock (`appendInline`).
  - **`parseFields`:** `bytes.Count`, then `strings.Cut`.
- **Semantics:** values are unchanged. Identity changed: the `asgi` dict and the port
  are now per worker, where they were process-wide. A framework mutating `scope["asgi"]`
  still affects that worker's later requests. uvicorn makes one dict per request; not
  adopted (one dict per request), noted in TODO.
- **Tests:**
  - `TestInlineAllocs`: at most 9 allocations per inline request;
    `BenchmarkInlineRequest` goes from 37 to 9 allocations and 2584 to 751 B.
  - `TestSharedScopeValues`: immortality on ft, per-worker isolation, both modes.
  - `TestRecycleNoCrossTalk`: 32 clients × 60 requests over `/fast`, `/stream`, `/slow`
    and `/block`, with a third abandoned; every answer must carry its own id.
- **Controls:**

  | # | Change | Result |
  |---|---|---|
  | C1 | new `kick` per request | `an inline request allocates 10 times, want at most 9` |
  | C2 | no `sys.intern` | `scope method ("GET") is not immortal` on ft (gil passes by design) |
  | C3 | one address cache for all workers | `both workers' requests share one server port object` |
  | C4 | shared `_ASGI`, no root_path intern | `share one scope asgi dict`, `worker N sees worker M's change` |
  | C5 | recycle regardless of refs | other requests' ids in bodies (`/fast?r82: x-id "r89"`), sometimes `fatal error: sync: unlock of unlocked mutex` or a hang |
  | C6 | no ref for the queued batch | stream bodies doubled |

  - C5 is **not** caught in LoopThread mode, where the window is microseconds.
- **Measurements** (CPython 3.14.10t and 3.13.18, Go 1.26.11):
  - Setup: server on cores 15..15+N-1 with GOMAXPROCS=N; `httpload -c 256` on 19,10-14
    with GOMAXPROCS=6; 2 s warm-up, 5 s measured; 3 interleaved rounds, medians.
  - Foreign CPU on the server cores was ≤1.4%, from `/proc/schedstat`. A first ft run
    with another agent's qemu jobs on 15-18 was discarded.
  - The before and after binaries were built together against the same py/aio state.

  | app | cores | before req/s | after req/s | ratio | GC/s b/a | allocs/req b/a |
  |---|---|---|---|---|---|---|
  | hello | 1 | 65.2k | 70.7k | 1.085 | 85/55 | 53.0/27.5 |
  | hello | 2 | 102.0k | 112.6k | 1.103 | 130/83 | 53.3/27.9 |
  | hello | 4 | 174.4k | 203.6k | 1.168 | 214/139 | 53.2/27.9 |
  | json | 4 | 154.2k | 181.5k | 1.177 | 187/123 | 53.2/27.9 |
  | gil hello | 1 | 68.9k | 76.3k | 1.108 | 92/63 | 53.0/27.5 |
  | gil json | 1 | 59.7k | 63.9k | 1.070 | 80/53 | 53.0/27.5 |

  - Scaling from 1 to 4 cores goes from 2.67× to 2.88×.
  - CPU per request at 4 cores: 19.66 → 17.04 µs; p99 6.97 → 5.81 ms.
  - Per-worker copies of `_g.respond`, `_g.body` and `_chain` made no difference
    (203.9k vs 203.6k), so they were not adopted.
  - Scratch: `.agents-workspace/tmp/r3/ft-asgi/`.
- **Left:**
  - `py.dispatch` makes 2 allocations per Python→Go call (`slots`, `pos`; about 576 B
    for `respond`), the largest remaining share on the asgi path. A per-thread arena
    would remove it; that is a py change.
  - net/http accounts for about 18 of the 27.9 allocations per request.
  - GOGC and keeping a P free for combiners are untested.

### 2026-11-18 — A darwin "TestConcurrent hang" was a tree copy taken while a break-and-restore control was in the shared tree

- **What happened:** the lazy-shim agent rsynced the tree to the M1 at 18:53, while the
  ft-asgi agent had control C5 written into `asgi/asgi.go`. Its asgi `TestConcurrent`
  then hung on darwin with eager and with lazy load, while the older asgi tree passed.
  It reported this as an asgi regression.
- **Found by** diffing the copy against the tree. The one difference:
  ```
  916: 	q.refs.Store(1) // CONTROL C5: recycle now, whoever still holds it
  ```
- **Checked:** with the real tree on dengaku, TestConcurrent `-count=20` and the full
  asgi suite pass for gil and ft, inline and LoopThread. With the copied file, inline
  times out 2 runs in 3.
- **Why no check caught it:** controls are restored before the agent reports, so the
  tree it reports on is clean. The window is visible only to someone copying the tree
  meanwhile (rsync to the Macs, `ci/darwin.sh`).
- **Carry:** when agents run in parallel, run break-and-restore controls in a scratch
  copy of the package, not the shared tree. An agent that syncs the tree elsewhere
  greps for `CONTROL` markers first.
- **Gate for the lazy shim and the 3.14t asgi changes:** ci/unit.sh gil and ft,
  ci/windows-wine.sh gil and ft, and ci/darwin.sh gil and ft on arm64 and on amd64 all
  passed (`.agents-workspace/tmp/r3/gate2/`). That includes the darwin/amd64 3.14t asgi
  tier, where the old binary hung in Finalize.

### 2026-11-18 — Lazy event tuples would save at most 0.4% on the asgi path: Python reads every item pythago passes

- **Context:** tackle-todos round 4, "Go-built read-only containers" (user idea
  2026-11-17), "measure first". The work was done in a scratch snapshot
  (`.agents-workspace/tmp/r4/lazytuple/base`, digest `2129e39e…41f1`), with no tree
  change.
- **Audit:**
  - **Inline:** `appendInline` passes 15 values, and `serve_inline` unpacks all of them
    into the scope or `_Request`. Six are made per request: h, raw_path, path, headers,
    client host and port.
  - **LoopThread:** 4 tuple builds: the `"r"` tuple, client and server `(host, port)`,
    and the `(token, kind, x, y)` wrapper. Every item is read; `y` is None.
  - No item goes unread, so a lazy container would materialize everything at once, since
    the scope must be a real dict.
- **Bound 1, end to end:**
  - Setup: variant `pre/asgi/asgi.go` creates those items once per worker. Control: a
    stale `"path"` in responses, and allocations 27.5 → 24.5 per request. asgiserve
    `GOMAXPROCS=1 taskset -c 7`; `httpload -c 256 -d 5s` on 8-9; 3 interleaved rounds.
  - Medians of pre/base CPU per request:

    | image | hello inline | hello LoopThread | json inline | json LoopThread |
    |---|---|---|---|---|
    | ft | 0.987 | 0.995 | 1.012 | 0.996 |
    | gil | 0.979 | 0.991 | 0.984 | 0.990 |

    Round-to-round spread is ±2-4%.
- **Bound 2, microbenchmark:**
  - Setup: `tool/callbench`, core 7; serve_inline-shaped calls of 15 values; Go values
    against prebuilt borrowed objects.
  - The difference is 56-65 ns per call on ft and gil (ranges don't overlap), about 0.4%
    of a 15 µs hello request.
  - The whole call costs about 1 µs (6-7%). Most of it is the vectorcall, the `*args`
    tuple, the dict and `_unpack`, none of which a lazy container changes.
- **Decision:** no prototype, nothing adopted.
  - A non-tuple sequence loses UNPACK_SEQUENCE's fast paths.
  - Holding Go memory until release needs a Go call on dealloc (about 100 ns), more than
    it saves.
  - Adoption would also cost a glue ABI bump and 12 rebuilt prebuilt artifacts.
- **Carry:** the idea pays only where items go unread: Go channel values, or a borrowed
  keyword view for `Func` (about 80 ns per keyword call). The asgi path makes no keyword
  calls into Go.

### 2026-11-18 — macOS shim: `import ctypes` and subinterpreters no longer load it at import (0.38 → 0.043 s); subinterpreters lack the fs.FS importer

- **Context:** tackle-todos round 4, the gaps left by "macOS extension shim loads lazily".
- **Design** (`extensionShimHookSrc` in `py/extshim_darwin.go`; the Go code is unchanged):
  - **`dlopen(None)` from `CDLL._load_library`** (identified by checking the calling
    frame's code against that CDLL class's `_load_library`) no longer loads the shim.
    - Instead a `property` replaces the class attribute `CDLL._handle = 0`. Its setter
      stores into the instance `__dict__`.
    - Every C-level lookup reads `dll._handle` by attribute: `__getattr__`,
      `__getitem__`, `_FuncPtr((name, dll))`, prototypes, `in_dll`,
      `_ctypes.dlsym(dll._handle)`. So the getter loads the shim before dlsym runs.
    - It raises `OSError`, because an `AttributeError` would recurse through
      `CDLL.__getattr__`. The class attribute is restored after the load.
    - `CDLL(handle=)` is now covered too.
  - **`dlopen(path)`** tries `RTLD_NOLOAD` first. An already-loaded image runs no
    initializer, and its handle never sees the shim (checked for libc and the main
    executable), so its handle is exempt. 3.14's `ctypes.util` opens libc at import, which
    is why this was needed. A library not loaded yet still loads the shim at dlopen.
  - **Subinterpreters:** `_interpreters.create` and `_xxsubinterpreters.create` are
    wrapped through the existing `exec_builtin` hook, so the shim loads in the main
    interpreter before the first subinterpreter.
  - **Rejected:** a `ctypes.dlsym` audit hook. It is permanent, can't be removed, and a
    user's audit hook can veto it.
- **Tests** (`py/extshim_darwin_test.go`):
  - `...LazyCtypes` (rewritten);
  - `...CtypesImport`: about 20 steps none of which may load the shim, including
    `ctypes.util`, `find_library("c")`, `in_dll` of libSystem and `dllist()`;
  - `...CtypesLookup`: 11 lookup paths, each in a fresh process;
  - `...CtypesThreads`: 8 threads at a barrier;
  - `...Subinterpreter`: `concurrent.interpreters` on 3.14, `_interpreters` on 3.13;
  - `...Error` (updated).
- **Controls:** 12 in a scratch copy on dengaku (gil and ft), all failing as intended.
  Examples:
  - lazy path disabled → `('loaded by import ctypes', ...)`;
  - `dlopen(None)` sent down the NOLOAD path → `dlsym(RTLD_DEFAULT, Py_IsInitialized):
    symbol not found`;
  - no interpreters hook → `symbol not found in flat namespace` inside the subinterpreter;
  - no NOLOAD → `loaded by import ctypes.util`.

  The first version of the `dlopen-none-eager` control passed. That showed NOLOAD on
  NULL succeeds, so the `None` branch was made exclusive, and the `none-as-image`
  control now guards it.
- **Numbers** (pyrun, 20 interleaved rounds, medians; PBS 20261110, go1.26.11; cores not
  pinnable on macOS; `.agents-workspace/tmp/r4/shim2/bench.sh`):

  | script | dengaku gil | dengaku ft | macbook-pro-2 gil | macbook-pro-2 ft |
  |---|---|---|---|---|
  | empty | 0.038 → 0.039 | 0.040 → 0.041 | 0.088 → 0.089 | 0.095 → 0.097 |
  | `import ctypes` | 0.377 → 0.043 | 0.377 → 0.043 | 0.555 → 0.090 | 0.532 → 0.104 |
  | `pythonapi.Py_IsInitialized()` | 0.376 → 0.376 | 0.384 → 0.382 | 0.521 → 0.515 | 0.528 → 0.518 |

- **Finding (all platforms):** `concurrent.interpreters.create()` fails with the
  embedded stdlib:
  ```
  InterpreterError('interpreter creation failed')
  ModuleNotFoundError: No module named 'encodings'
  ```
  The fs.FS importer is installed only in the main interpreter. The test uses a disk
  stdlib (`diskStdlib`). Added to TODO.
- **Carry:** the hook depends on ctypes reading `dll._handle` by attribute and on
  `CDLL._load_library`. Re-check `_ctypes` and `ctypes/__init__.py` on each CPython bump.

### 2026-11-18 — Python→Go calls with up to 8 arguments allocate nothing for them (an 8-argument call 261 → 122 ns)

- **Context:** tackle-todos round 4. The 3.14t asgi entry named `py.dispatch`'s two
  allocations per call (`slots`, `pos`; 576 B for `respond`) as the largest remaining Go
  allocation on the asgi path.
- **Design** (`py/func.go`):
  - An `argFrame` holds `[8]argSlot` and `[8]*Object`, where 8 is
    `len(callFrame{}.decoded)`. Frames come from a `sync.Pool`. Larger calls fall back to
    `make`.
  - Each active call gets its own frame, so nested calls on one thread and concurrent
    calls on many never share one.
  - `pos` is `f.pos[:nargs:nargs]`, so a Func's append copies.
  - Each slot's `Object` is written whole, so a reused slot carries no `pre` or str cache.
  - One defer both recovers and Puts. It runs after `returnValue`, which may read a
    borrowed argument as the result. That defer also covers panics in kw-name decoding
    now.
  - Two separate defers made a panicking call allocate once more than a failing one
    (5 vs 4). The runtime cause was not investigated.
  - **Rejected:** a stack array (`-gcflags=-m`: `moved to heap: fr`, since `pos` escapes
    through `fn(pos, kw)`); a glue-side arena (needs an ABI bump).
- **Contract:** the `Func` doc already said args are "valid only during the call". It now
  says the slice and every `*Object` are reused after return. An audit of every Func in
  py, aio, asgi, wsgi, workers and examples found none keeping them past return; they keep
  `NewRef`s or copies.
- **Tests** (`py/dispatch_internal_test.go`):
  - `TestDispatchAllocs`: 0 per call at 1 and 8 arguments, 2 at 9, 2 for the kwargs map.
  - `TestDispatchPanicAllocs`: a panic allocates as much as an error.
  - `TestDispatchNestedConcurrent`: 16 goroutines × 30 rounds of 7-deep Go→Python→Go
    recursion. Each level checks its arguments on entry, after its nested call, and after
    `Release(runtime.Gosched)`.
- **Controls** (scratch copy, ft and gil):

  | Control | Result |
  |---|---|
  | old `make` | `allocates 2.00 times, want 0` |
  | one shared arena | `call (id 450, depth 0) after Release: its id is 90: it shares its arguments with another active call` |
  | frame put back before the Func | `after its nested call: its depth is 0` |
  | an arena per depth | caught `after Release` (the concurrency half catches sharing on its own) |
  | Put only on a normal return | `panicking ... 5.00 times per call, one that returns the same error 4.00` |

- **Numbers** (go1.26.11; 3.14.10t and 3.13.18 test images; `taskset -c 5`, `-benchtime=2s`,
  5 interleaved rounds, medians; foreign CPU ≤0.25%):

  | Benchmark | ft before → after | gil before → after | allocs |
  |---|---|---|---|
  | PythonGoCall/Positional | 131.3 → 92.1 ns | 130.7 → 93.5 ns | 2 → 0 |
  | PythonGoCall/Keyword | 240.1 → 210.2 ns | 240.8 → 216.4 ns | 4 → 2 |
  | PythonGoCall8 | 261.5 → 123.3 ns | 260.5 → 120.7 ns | 2 → 0 (576 B) |

- **Left:** the kwargs map (2 allocations, 256 B per keyword call) needs a `Func` API
  change. asgi end-to-end req/s was not measured with this change alone.
- **Carry:** a Func that keeps an argument past return now silently aliases a later
  call's argument, where before it held a stale borrowed pointer. The doc forbids it, and
  `NewRef` is the way to keep one.

### 2026-11-18 — A held-call test catches early request recycling in LoopThread mode; idle-worker dispatch gains nothing; a spare P buys 5-8% at 2-4 cores for p99

- **Context:** tackle-todos round 4. Control C5 (recycle in `finish` regardless of refs)
  passed `TestRecycleNoCrossTalk` in LoopThread mode. Also untested: the levers
  "dispatch to an idle worker" and "keep a P free".
- **Audit of the LoopThread path:**
  - Posted tuples carry only `h` and copied data.
  - `written()` and `Post` run before `finish`.
  - `kick` is touched only by holders and ServeHTTP.
  - Queued actions that alias `q.pairs` and `q.body` are cleared by `finish`.
  - The only holder after `finish` is a `_pythago_asgi` call that ran `acquire` before
    the delete, for microseconds.
- **Why answers alone can't show C5** (found with debug prints in a scratch copy):
  1. The held call pushes A's response into B's recycled struct.
  2. Its deferred `unref` then recycles B's struct too, zeroing the queue and draining
     the kick.
  3. B's response still arrives intact, and aliased pairs and body are overwritten in
     place.
- **Test:** `TestRecycleHeldRequest` (`asgi/recycle_test.go`), with test hooks
  `Server.onCall` (`reqOf`, nil in production) and `HoldCall`/`Requests`/`Queued` in
  `export_test.go`.
  - `call`: 1 worker and `GOMAXPROCS(1)`, so `sync.Pool.Get` takes the per-P slot A's
    `finish` filled. It holds A's `respond`, cancels A, sends B, releases, and checks that
    the held call's handle is unchanged.
  - `batch` (inline only): queues a request behind a held call, abandons it, then
    queues a stream.
- **Controls** (scratch copy `.agents-workspace/tmp/r4/asgi/ctl/`, `-count=10`, ft and gil):

  | Control | Mode | Result |
  |---|---|---|
  | C5 | LoopThread | `call` 10/10 (was 0): `the request a _pythago_asgi call held (handle 1) was recycled under it: it went on with request 2` |
  | C5 | inline | `call` 10/10 plus `request B: no response within 5s (lost)`; `batch` 10/10: `body "B;B;B;B;B."; want ... "B;B;B;B."` |
  | C6 | inline | `batch` 10/10: `body "B;B;B;B;B;B;B."` |
  | C6 | LoopThread | passes (no `submit` in that mode) |

- **Dispatch levers** (3.14.10t and 3.13.18, Go 1.26.11):
  - Setup: server on cores 15..15+N-1; `httpload -c 256` on 19,10-14; 3+ interleaved
    rounds, medians of per-round ratios. Rounds with ≥5% foreign CPU were dropped.
  - The rr and idle binaries were built together. Source digest `a9c883f8072ed55a`,
    which includes the dispatch-arena py change: 25.8 allocs/req.
  - Variant: `tool/asgiidle`, where `pick()` scans for a worker no one holds.

  | img | cores | rr:n req/s | idle:n | rr:n+1 | rr:2n | p99 rr:n → rr:2n |
  |---|---|---|---|---|---|---|
  | ft | 1 | 64.9k | 0.996 | 0.918 | 0.921 | 7.48 → 13.50 ms |
  | ft | 2 | 112.4k | 1.002 | 1.067 | 1.079 | 7.44 → 10.31 ms |
  | ft | 4 | 212.5k | 0.999 | 1.014 | 1.051 | 5.45 → 7.65 ms |
  | gil | 1 | 79.2k | 0.996 | 0.936 | 0.941 | 6.47 → 9.94 ms |

  - With GOMAXPROCS 2N but `-workers N`: 1.051 at 4 cores with p99 6.22 ms, and 1.073 at
    2 cores with p99 9.07 ms.
  - The gain tracks CPU use (351 → 378% at 4 cores): combiners waiting for a P.
- **Decision:** idle-worker dispatch is not adopted. It gives no gain, is worse with 2N
  (1.035), and raises GC from 119 to 130 cycles/s. GOMAXPROCS and `Workers` defaults are
  unchanged; that's the user's call (TODO).
- **Carry:** a recycle test must observe the holder itself (handle identity at the
  holder), not just the answers: a recycled struct can deliver a correct response.

### 2026-11-18 — Test binaries' TestMain now fails a hang in Init or Finalize after a bound; 890 darwin/amd64 3.14t asgi runs did not reproduce the 25 h hang

- **Context:** tackle-todos round 4, from "A 3.14t asgi test binary on darwin/amd64 hung
  25 h in Finalize". `-test.timeout` covers only `m.Run`.
- **Change:** a new package, `internal/testwatch`.
  - `Arm(what) (disarm func())` starts a `time.AfterFunc` that prints:
    - `testwatch: <what> did not return within <d> (pid N)`;
    - the native threads (Linux: `/proc/self/task/*` comm, state, wchan and syscall;
      macOS: a 3 s `sample`, capped at 2 min);
    - all goroutines;
    - `FAIL\ttestwatch: <what> hung`.

    It then exits 3. A 10 s self-kill backs up `os.Exit`, because on darwin that is
    libc `exit`, which runs atexit handlers.
  - The bound is `PYTHAGO_TEST_WATCHDOG` (a duration or seconds, `0` disables), else
    `-test.timeout/4` clamped to [3m, 10m], or 10m without a timeout.
  - The TestMain functions of py, aio, asgi and wsgi arm Init (plus `asgi.New` and
    `wsgi.New`) and disarm it before `m.Run`. They arm Close plus Finalize through
    `os.Exit`.
  - Child paths (`IsPythonCommand`, asgi's re-exec) return before any `Arm`.
  - workers never calls Finalize, so it is unchanged.
  - `ci/unit.sh` vets and gofmts the package and runs its tests.
- **Tests:** `internal/testwatch/testwatch_test.go`, whose TestMain doubles as the child:
  `TestFires`, `TestDisabled`, `TestDisarm`, `TestBound`.
- **Controls:**
  - A real Finalize hang in a scratch aio: a non-daemon Python thread
    (`daemon=False`; threads started from pythago's Dummy thread are daemons by default).
    - With `PYTHAGO_TEST_WATCHDOG=5s` it exits 3 after 5.04 s, showing the thread in
      `hrtimer_nanosleep` and goroutine 1 in `py.Finalize`.
    - With `0` it hangs until `timeout 30` (exit 124).
    - darwin/amd64: exit 3 after 12.6 s, the main thread in `_pthread_cond_wait`.
  - Breaking the package in a scratch copy:

    | Break | Failure |
    |---|---|
    | fire made a no-op | `exit status -1 after 3m0s, want 3` |
    | no goroutine dump | `output lacks "goroutine 1 ["` |
    | no thread list | `output lacks the native threads` |
    | `0` doesn't disable | `exit status 3 after 1.0s, want a hang until killed` |
    | disarm made a no-op | `disarm: exit status 3 after 1.0s` |

- **Reproduction loop:**
  - Setup: macbook-pro-2 (i9, macOS 26.6.2), CPython 3.14.10t, PBS 20261110, the current
    tree. Inline and LoopThread alternated, each run bounded at 1200 s from outside,
    20:45 to 22:15 JST.
  - Result: **890 runs, all clean**, 6-7 s each. That does not show the hang is fixed:
    it was seen once, on an old binary.
- **Reading of the r3 evidence** (hypothesis; frames are only `bao_<hash>_local_N+off`):
  - `asgi-worker-2` in `pause()` under a thread-state re-attach matches 3.14's
    `PyThread_hang_thread`, which parks a daemon thread re-attaching during
    finalization.
  - The main thread was in a PyMutex/PyEvent-shaped `_pthread_cond_wait` from Python
    code inside Finalize. Likely, finalization waited on a lock or event that the parked
    thread would never release. The scratch control ends in the same frame family.
- **wsgi under loopback port shortage:** the failures were the tests assuming IPv4.
  `httptest` falls back to `[::1]`; SERVER_NAME comes from Host and REMOTE_ADDR from the
  peer.
  - TestEnviron expects the listener's IP.
  - TestEnvironMatchesWsgiref runs wsgiref on httptest's IP, and `run_wsgiref` uses
    `AF_INET6` for an IPv6 host.
  - TestIODNS skips only when the upstream fell back to `::1` and localhost doesn't
    resolve to it.
  - Control: every httptest server routed to `[::1]` in a scratch copy. Before the fix,
    3 failures, the TODO's: `SERVER_NAME = "::1", want "127.0.0.1"`; `REMOTE_ADDR =
    "::1", wsgiref "127.0.0.1"`; `ConnectionRefusedError [Errno 111]`. After it, 28 pass
    and TestIODNS skips.
  - A wsgiref bind failure on 127.0.0.1 after httptest got it, or EADDRNOTAVAIL on dial,
    is genuine exhaustion and is left.
- **Carry:**
  - Arm `testwatch` around anything after `m.Run` that can block.
  - bao-local symbol names make darwin `sample`s of libpython unreadable. A symbol map
    from bao would make hangs analysable; ask the bao agent if that is needed.

### 2026-11-18 — Once under Wine (3.14t, LoopThread), a streamed asgi response lost one middle chunk; not reproduced in about 650 runs since

- **Seen in:** the round-4 gate, `ci/windows-wine.sh ft`, asgi loop-thread pass
  (`.agents-workspace/tmp/r4/gate/win-ft.log`):
  ```
  --- FAIL: TestRecycleNoCrossTalk (0.12s)
      recycle_test.go:138: 220 answered, 118 abandoned
      recycle_test.go:144: /stream?r265: status 200, x-id "r265", body "r265;r265;r265."; want 200, "r265", "r265;r265;r265;r265."
  ```
  The id is the request's own, and the final chunk arrived. One of the three `more_body`
  chunks is missing, with chunked framing intact. That is a lost write or action, not
  cross-talk.
- **Not reproduced:**
  - 20 + 20 runs of the test alone under Wine (both modes);
  - the full asgi suite 5 times under Wine in loop-thread mode;
  - 300 + 300 native linux/arm64 ft runs on cores 15-18;
  - a full rerun of the Wine ft tier, which passed.
- **Audited and ruled out by reading:**
  - ServeHTTP's queue handoff: the array is handed back only after it is processed and
    cleared, and `push` appends under the same lock.
  - The first-chunk buffer is claimed once per request by CAS.
  - The dispatch-arena change: `pyValue.payload` points into the Python object, not the
    slot, and asgi Funcs copy small bodies during the call.
  - The client: a cancel during `ReadAll` returns an error and is counted as abandoned.
- **Suspects:**
  - Wine's overlapped socket send: one Flush per chunk is one WSASend, so a dropped one
    would lose exactly one framed chunk;
  - an asgi ordering race that only Wine's timing exposes.
- **Carry:** if it recurs, capture the server's writes (log action kinds and lengths per
  request in a scratch build) to tell a lost action from a lost send. The Windows CI jobs
  on real Windows will show whether it is Wine's.
- **Gate for round 4** (watchdog, wsgi IPv6, asgi held-call test, dispatch arena, macOS
  shim follow-ups):
  - passed: ci/unit.sh gil and ft (testwatch included), ci/windows-wine.sh gil,
    ci/darwin.sh gil and ft on arm64 and amd64;
  - ci/windows-wine.sh ft failed once with the test above, then passed on a full rerun;
  - logs: `.agents-workspace/tmp/r4/gate/`.

### 2026-11-19 — Subinterpreters start from the embedded stdlib: a frozen `encodings` stub installs the fs.FS importer in every interpreter

- **Problem:** `concurrent.interpreters.create()` failed with `No module named 'encodings'`.
  The fs.FS importer existed only in the main interpreter, which gets it through two-phase
  init, and a new interpreter imports `encodings` during its own creation.
- **CPython facts relied on (3.13, 3.14):**
  - `new_interpreter` copies the creating interpreter's PyConfig.
  - `init_interp_main` runs `_PyImport_InitExternal`, then `_PyUnicode_InitEncodings`, whose
    `import encodings` is the first import that is not a bootstrap module.
  - `look_up_frozen` searches the bootstrap table, then `PyImport_FrozenModules` (public
    `PyAPI_DATA`, exported by python3XY.dll too), then the frozen stdlib. Only the frozen
    stdlib is gated by `use_frozen()`, so a custom entry is found even under
    `-X frozen_modules=off`.
  - `_imp.find_frozen(name, withdata=True)` returns a frozen entry's bytes as a zero-copy
    memoryview.
  - `struct _frozen` is `{name, code, int size, int is_package}` in the public 3.11+
    headers.
- **Mechanism (`py/fsimport.go`; no glue change, no ABI bump, no Go at runtime in the new
  interpreter):**
  - In the core phase, `snapshot` reads the mounts into blobs:
    - a mount's zip, whose stored entries stay in place;
    - or arenas of copied files, each at most `MaxInt32` bytes, with a first byte of 0x01
      because frozen data must not start with NUL.
  - The C `_pythago_fs` index points into the blobs.
  - `pyIndex` writes a marshal-format index in Go.
  - `installFrozen` sets `PyImport_FrozenModules` to a Go-held table, keeping any table set
    earlier after it. The table holds:
    - `_pythago_fsimport`, the existing finder source;
    - `_pythago_fs`, a pure-Python version of the C module over the index and blobs;
    - an `encodings` package stub;
    - the index and blobs as data entries whose names can't appear in an import statement.
  - The stub imports `_pythago_fsimport`, puts the app mount first on sys.path if missing
    (the disk-stdlib case; `runtime.go`'s post-init insert is gone), and loads the real
    `encodings` through `PathFinder`. The main interpreter takes the same path.
- **Rejected:**
  - An inittab or multi-phase `_pythago_fs`: needs a C init function, so a glue change, or
    Go, which crashes in forked children.
  - An audit hook: fires on every event and needs Go.
  - zipimport or `open_code`: they stat the real filesystem.
  - Wrapping `create`: runs too late.
- **Tests (`py/subinterp_test.go`):**
  - ImportS: both configs plus `concurrent.interpreters`; covers json, codecs, the app
    module, resources, pkgutil, namespace packages, a thread inside the subinterpreter, and
    `__file__` and loader parity.
  - FSModule: the Python `_pythago_fs` must match the C module on all 1242 paths plus edge
    cases. It found the C module's `listdir("")` returning `['pythago-fs']`; the Python
    module now does the same.
  - Concurrent (Do goroutines and Python threads), Recreate (12 cycles), ForkChild,
    DiskStdlib.
  - `TestExtensionShimLazySubinterpreter` (darwin) now runs without PYTHONHOME.
- **Controls** (scratch copy, `.agents-workspace/tmp/r5/subinterp/ctl/`):

  | Control | Result |
  |---|---|
  | no stub | every test fails with `interpreter creation failed` / `No module named 'encodings'` (Linux gil and ft, Wine ft, dengaku) |
  | reversed listdir | FSModule fails, 1231 of 1242 entries differ |
  | `stat("")` reported as a directory | FSModule fails, 1 entry differs |
  | app mount inserted in the main interpreter only | DiskStdlib fails, `No module named 'appmod'` |
- **Results:** py passes on linux/arm64, dengaku, macbook-pro-2 and Windows/arm64 under
  Wine, gil and ft.
- **Cost:**
  - Subinterpreter creation (median of 20, core 5) is the same as stock with a disk stdlib:

    | | isolated | legacy |
    |---|---|---|
    | 3.14t pythago | 10.8 ms | 8.1 ms |
    | 3.14t stock | 10.3 ms | 9.1 ms |
    | 3.13 pythago | 7.5 ms | 6.3 ms |
    | 3.13 stock | 7.3 ms | 6.3 ms |
  - Main startup is about +1 ms: empty `pyrun` 33.7 → 34.8 ms ft, 38.1 → 39.0 ms gil,
    30 interleaved runs, noisy core.
- **Audit of Go-side assumptions:** subinterpreters cannot reach Go today; Funcs and
  `Config.Modules` live only in the main interpreter. If that changes, `Do` needs an
  interpreter check, because `pythago_py_enter` nests on whatever thread state is attached.
- **Visible to users:**
  - `encodings`, `_pythago_fs` and `_pythago_fsimport` are frozen names.
  - GoFSLoader's `__module__` is `_pythago_fsimport`.
- **Open:**
  - On Windows, a subinterpreter's sys.path entries are made absolute (`Z:\pythago-fs\...`)
    and not restored. Imports still work.
  - Files of 2 GiB or more and paths that aren't valid UTF-8 are left out of the
    subinterpreter index.
  - The arena path for deflated zip entries and `Config.Stdlib` mounts is untested.

### 2026-11-19 — macOS shim: a failed load looks like stock ctypes; the first-load cost is dyld's library validation, not ours

- **Failed load, now (`py/extshim_darwin.go` hook source only):**
  - The `_handle` getter no longer raises. After a failed load (not retried, cached in
    Python), every C API lookup raises stock's exception for a missing name:
    - `hasattr(pythonapi, "Py_X")` is False;
    - attribute and item lookups raise AttributeError;
    - `in_dll` raises ValueError;
    - `dlsym` raises OSError.
  - A `CDLL.__getitem__` wrapper sets `__cause__ = OSError(<load error>)` on the attribute
    and item paths.
  - dlopen of a file calls through: a library that doesn't bind the C API loads, and one
    that does fails in dyld (`symbol not found in flat namespace`) with the cause.
  - Extension imports still raise `ImportError(<load error>)`.
- **Fixed alongside:**
  - Names outside the C API looked up by attribute or item through `CDLL(None)` no longer
    load the shim. The wrapper sets a thread-local flag that the getter honours; this is
    safe because the shim exports only `^_?Py`.
  - A CDLL subclass that shadows `_handle` with a class attribute is detected in the
    `dlopen(None)` wrapper, through the MRO of `self`, and loads the shim at once.
- **Tests:**
  - `TestExtensionShimLazyError` has 3 subtests, one per first-attempt site, covering stock
    exception types on every path, causes, a plain cc-built dylib, `CDLL(None).getpid`,
    and that no shim was loaded.
  - CtypesImport and CtypesLookup gained the non-Py, `hasattr`, other-first and shadowed
    cases.
  - 7 controls on dengaku gil and ft, all failing as intended: the getter raising again,
    no cause, dlopen raising early, the non-Py skip removed, the skip flag never reset,
    shadowing ignored, `__getitem__` not restored. Script:
    `.agents-workspace/tmp/r5/shim3/controls.sh`.
  - py passes on both Macs, gil and ft, with the subinterp change in the tree.
- **First-load cost:** all of it is `fcntl(F_CHECK_LV)`, the library-validation check, while
  XprotectService runs. Generation, writing, `F_ADDFILESIGS_RETURN`, mmap and dyld take
  under 2 ms together, and the export count doesn't matter (1 symbol costs the same as
  1754). Measured with `lat.c`:

  | Case | M1 Max | i9 |
  |---|---|---|
  | new content, new file | 325-335 ms | 390-510 ms |
  | same content, new file | 136-140 ms | 228-313 ms |
  | hard link to a file already loaded | 3-4.7 ms | 3.3-3.9 ms |
  | same path, loaded before by another process | 0.28-0.37 ms | 0.29-0.38 ms |
- **Resolver exports rejected:** dyld binds an extension's GOT and data references to the
  stub, not to the resolver's result, so the first `PyExc_*` access crashed on the stub's
  instruction bytes.
- **Prototype that works (scratch, `exp/proto/patched_darwin.go`), the patched-trie
  constant shim:**
  - The absolute exports carry 10-byte ULEB128 placeholders, so the file depends only on the
    names and the arch.
  - At load:
    1. dlopen RTLD_LOCAL;
    2. mprotect the export trie in `__LINKEDIT` (a private copy-on-write mapping) and write
       the live addresses;
    3. protect it again;
    4. dlopen RTLD_GLOBAL;
    5. check with `dlsym`.
  - Data identity holds. py, wheels.py (numpy, cffi, markupsafe) pass on both Macs.
  - `pyrun` of `import ctypes; ctypes.pythonapi.Py_IsInitialized()`, medians of 20
    interleaved runs, in seconds:

    | Mode | dengaku gil | dengaku ft | mbp gil | mbp ft |
    |---|---|---|---|---|
    | current shim | 0.424 | 0.428 | 0.491 | 0.542 |
    | constant content, new file each time | 0.241 | 0.245 | 0.333 | 0.344 |
    | constant content, cached file | 0.061 | 0.064 | 0.097 | 0.112 |
    | empty script | 0.046 | 0.049 | 0.085 | 0.099 |
- **Decision for the user, not landed:**
  - A: constant content in a new file each time, about −45%.
  - B: a persistent cache file that dyld loads, about 3.5 ms. B would let the shim load
    eagerly again and retire the lazy hook, but it needs a location, a config, and a trust
    rule (owner-only directory and file, temp file plus rename, content compared before
    dlopen, fall back to A).
- **Executable exports:** libpython sits at a constant offset from the executable header,
  but Go's internal linker writes an empty export trie, and `//go:cgo_export_dynamic` is
  refused outside cgo-generated files. That needs Go linker or bao work.
- **Open:**
  - Non-Py names through `in_dll`, `_FuncPtr`, prototypes and `dlsym` still load the shim.
  - The cause is attached only on the attribute and item paths.
  - Re-check `CDLL.__getitem__`, `_handle` and `_load_library` on each CPython bump.

### 2026-11-19 — Keyword calls into Go: a borrowed `Kwargs` view halves `echo(x, k=x)`; prototypes only, the API is the user's call

- **Question:** TODO "Python→Go keyword calls". The rest of a keyword call's cost was
  building the `map[string]*Object` that `Func` receives.
- **Prototyped in scratch (`.agents-workspace/tmp/r5/kwcall/`); the shared tree is
  unchanged:**
  - A: the map borrowed from the dispatch arena, cleared per call. A2 adds a name cache.
  - B: a `Kwargs` view over the arena's parallel slices. Variants:
    - B2 caches whole keyword-name tuples;
    - B5 caches names as Go strings. A slot comes from the address of the name's UTF-8
      bytes but is used only if the contents are equal; there are two entries per slot,
      and no Python object is held.
  - C: Funcs that declare parameter names.
- **Benchmark (`py/kwcall_bench_test.go` in the copies, `kwcall/bench.sh`):**
  - go1.26.11, linux/arm64; 3.14t on core 5, 3.13 on core 6.
  - 5 rotated rounds, medians, foreign CPU checked through `/proc/schedstat`.
  - 3.14t, ns per call / allocations per call:

  | shape | today | A | A2 | B2 | B5 | floor (keywords dropped) |
  |---|---|---|---|---|---|---|
  | echo(x) | 96.1/0 | 95.2/0 | 96.7/0 | 96.8/0 | 96.5/0 | 95.7/0 |
  | 1 keyword | 218.1/2 | 127.1/0 | 127.8/0 | 108.8/0 | 112.1/0 | 107.0/0 |
  | 4 long names | 332.8/6 | 232.8/4 | 180.4/0 | 132.9/0 | 145.8/0 | 131.6/0 |
  | 7 long names | 683.3/12 | 566.1/10 | 335.7/0 | 141.2/0 | 385.9/0 | 141.1/0 |

  3.13 shows the same pattern: 1 keyword 223 → 113 ns.
- **Findings:**
  - A borrowed map that a Func retains is a fatal "concurrent map read and map write" on
    3.14t.
  - B2's 64-slot tuple cache shuts late call sites out once it collides (3.13, 7 keywords:
    283 ns against 141), and it holds Python objects keyed by address.
  - C would save only the name lookups: 13 ns for 4 names.
  - Names 5 to 8 cost about 80 ns each because the glue decodes only 4 (`PYTHAGO_NAMES 4`).
    A simulation of decoding 8 brings 7 long names from 386 to 171 ns.
  - In Go 1.26, ranging over an `iter.Seq2` method value inside a func literal allocates 5
    times per call. `range kw.Each` and index loops don't allocate.
- **Recommended (B5):** `type Func func(args []*Object, kw Kwargs) (any, error)`, with `Len`,
  `Name`, `Value`, `Get`, `Each` and `Map`. Values are borrowed like args; names may be
  kept.
  - Migration: 16 files, about 70 lines. Every production Func ignores keywords. External
    code stops compiling unless `NewModule`/`From` keep the old type through an adapter.
  - Tests, each with a control that failed as intended:
    - `TestKwargsMatchesPython`: Python-equal items, 600 built name pairs.
    - `TestKwargsNamesStayTheirs`: 8 threads × 30000 calls with dying names.
    - `TestKwNameRereadsChangedContents`.
    - `TestKwNameSetHoldsTwo`.
    - `TestDispatchAllocs`: 0 allocations up to 8 arguments.
  - The controls broke: length-only match, one entry per slot, no cache, a wrong `Get`.
- **Open:**
  - Raise `PYTHAGO_NAMES` to 8 (ABI bump, glue rebuild) or read names through
    `internal/layout`.
  - Merge B5's two extra allocations past 8 arguments.
  - B5's copies of `py/extshim_darwin*.go` predate the round-5 shim change; merge only the
    Func signature when landing.

### 2026-11-19 — B5 landed: `Func` takes a borrowed `Kwargs` view (user decision)

- **Change:**
  - `py.Func` is `func(args []*Object, kw Kwargs) (any, error)`.
  - `Kwargs` has `Len`, `Name`, `Value`, `Get`, `Each` and `Map`. Values are borrowed like
    args; names are Go strings the Func may keep.
  - `py/kwnames.go` adds the name cache.
  - Applied from `.agents-workspace/tmp/r5/kwcall/B5/` as a patch against its base copy;
    the base matched the tree except the darwin shim files, which merged cleanly.
- **Edits at landing:**
  - The `iter.Seq2` `All` was dropped in favour of `Each`, because ranging over `All` inside
    a closure allocates 5 times per call in Go 1.26. Tests now use `range kw.Each`.
  - The `Func` doc was re-wrapped.
  - The scratch-only `kwvariant_test.go` and `kwcall_bench_test.go` were not landed.
- **Breaking change:** external Funcs must change `map[string]*py.Object` to `py.Kwargs`.
  `.agents/attic/spike/sinline` keeps the old type; it doesn't build in place anyway.
- **Checks:**
  - gofmt and vet are clean.
  - darwin/arm64 and windows/arm64 builds pass.
  - py, aio, wsgi, workers and asgi (inline and loop thread) pass on 3.14t and 3.13, on cores
    0-4 and 10-14.
  - The controls are in the prototype entry above.

### 2026-11-19 — The lost middle chunk under Wine is Wine arm64 zeroing vector registers on SetThreadContext, not asgi

- **Context:** TODO "asgi streamed response lost a middle chunk once under Wine". No asgi bug.
- **Audit** of the LoopThread path for an interleaving that drops exactly one middle action
  while later ones survive. Each was ruled out:
  - `disconnected`, a nil `reqOf` and a dead `push` are all permanent, so later chunks would
    drop too.
  - ServeHTTP's queue hand-off can't alias: the array comes back only after it is cleared,
    and only if still nil.
  - A lost kick would stall the response, not drop one chunk.
  - Refs are balanced: every `refs`/`unref`/`pool` use was checked.
  - The first-chunk buffer is claimed by CAS.
  - net/http write errors are sticky.
  - Func args are per call.

  Only a whole frame lost in transport remained.
- **Reproducing it:**
  - 0 failures in about 20,000 runs across many configs:
    - under Wine: LoopThread and inline, 1 to 3 cores, GOMAXPROCS 1 to 20, a stream-heavy
      mix, random yields at hand-off points, full suites, the round-4 gate's own binary;
    - natively;
    - a Go-only HTTP model.
  - `-race` can't build: it needs cgo.
  - It reproduced only under contention: 3 to 6 Wine containers sharing cores 7-9 and 17-19,
    `TestRecycleNoCrossTalk` alone, LoopThread.
  - A per-request trace in a scratch copy showed one action's own bytes zeroed, always in a
    32-byte-aligned quarter of the 144-byte `action`:
    - its `kind` (taken for a start, nothing written: the original symptom);
    - `data.len`;
    - `more`;
    - header pairs.
- **Cause:**
  - Go's windows/arm64 `preemptM` injects `asyncPreempt` with `SetThreadContext` using
    CONTEXT_CONTROL|INTEGER.
  - Wine 11.22 arm64 then zeroes V0-V31. It writes zeros even when the context buffer's
    vector area holds a pattern.
  - Go copies structs with `FLDPQ`/`FSTPQ` through F16 and F17, so a goroutine preempted
    between the load and the store writes 32 zero bytes.
  - Real Windows leaves the FP registers alone for a CONTROL set, and Linux preempts through
    a signal frame, so neither is affected.
  - Every Go binary under Wine arm64 is exposed, not only asgi.
- **Evidence (`.agents-workspace/tmp/r5/winechunk/`):**
  - A/B with the same binary under contention:

    | Setting | Runs | Failing runs |
    |---|---|---|
    | default | 4500 | 6 |
    | `GODEBUG=asyncpreemptoff=1` | 4500 | 0 |
  - `uafrepro` (Go only, patterned stack frames plus GC) finds 16-72 corrupted words per
    60 s under Wine, even with no socket I/O. It finds 0 with asyncpreemptoff, 0 natively,
    and 0 with a Go copy whose preemptM adds CONTEXT_FLOATING_POINT.
  - `ctxrepro` (asm: a thread spins with patterns in V0-V31 while another suspends it, gets
    and sets its context, and resumes it), rounds where the V registers changed:

    | Set? | Flags | Rounds with V registers changed |
    |---|---|---|
    | no | — | 0 |
    | yes | CONTROL | 2117 of 2117 |
    | yes | CONTROL\|INTEGER (Go's) | 1261 of 1289 |
    | yes | CONTROL\|FP | 0 |
  - Ruled out: a toolchain without `FILE_SKIP_COMPLETION_PORT_ON_SUCCESS` still failed.
- **Controls:**
  - `WC_DROP` skips a write; the dump fired with the right diagnostic.
  - ctxrepro's no-set and FP cases.
  - uafrepro natively and with asyncpreemptoff.
- **Changed:** `ci/windows-wine.sh` runs every test binary with
  `GODEBUG=asyncpreemptoff=1`.
- **Upstream, for the user:**
  - Wine: arm64 `NtSetContextThread` without CONTEXT_FLOATING_POINT zeroes V0-V31;
    `ctxrepro` is the test case.
  - Optionally Go: a defensive CONTEXT_FLOATING_POINT in windows/arm64 preemptM.
- **Found along the way:** asgi `TestIOProxyConcurrent` failed on 3 CPUs or fewer.
  - Its thread routes need 16 executor threads, but two workers' default executors hold
    2 × (cpu+4).
  - The thread routes' barrier is now `min(16, 2*min(32, NumCPU+4))`. It passes on 2, 3 and
    10 CPUs.
  - Control (scratch copy, barrier one above capacity on 3 CPUs): `14 of 15 requests
    arrived`.
  - GitHub's 3-vCPU macos-26 runners would have hit this.

### 2026-11-19 — Gate for round 5

- **Scope:**
  - subinterpreters from the embedded stdlib;
  - the macOS shim failed-load behaviour;
  - B5 `Kwargs`;
  - `TestIOProxyConcurrent` on small machines;
  - the Wine tier's `GODEBUG=asyncpreemptoff=1`.
- **Result:** every tier passed on the first run.
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu);
  - ci/windows-wine.sh gil and ft;
  - ci/darwin.sh gil and ft on arm64 (dengaku) and amd64 (macbook-pro-2).
- **How:** run from a scratch copy of the tree with its own .work, so agents using the
  shared test image were not disturbed. Script and logs: `.agents-workspace/tmp/r5/gate/`.

### 2026-11-19 — A generic borrow arena (`py.Scope`) designed and prototyped; waiting for the user's decisions

- **Context:** the user asked for "a more generic solution for the allocation
  reconciliation", choosing one borrow arena for py, aio, asgi and wsgi over ad-hoc pools.
- **Design:** `.agents/docs/arena-design.md` covers the inventory, abstraction, C side,
  numbers, migration plan and decisions. In outline:
  - **Call scopes:** private to a goroutine and pooled, with inline `[8]` chunks.
  - **Request scopes:** `ScopePool[T]` with a root value, an atomic count, serialized takes
    and `Detach`.
  - **Lanes:** typed lanes that grow to fit and trim past 64 KiB.
  - **Misuse:** released borrowed Objects are poisoned and every accessor checks
    `o.live()`. `PYTHAGO_CHECK_BORROWS=1` stops reuse altogether.
  - **ABI 4:** C decodes every argument and name into a 24-value stack array, or PyMem past
    that, passed as pointers.
  - **Kept separate:** the caches (kwNameCache, interned constants), aio `readBufs` and
    channel values, `py.Buffer` reuse.
- **Prototype:** `.agents-workspace/tmp/r5/arena/proto/`, patch `arena-proto.patch`. The py
  call path and asgi are ported; wsgi is designed only.
- **Numbers:** go1.26.11, linux/arm64, 3.14t on core 5 and 3.13 on core 6, 5 rotated rounds.
  8 of 80 runs were dropped for more than 2% foreign CPU, while the gate was running.

  | shape | 3.14t before → after | 3.13 before → after | allocations |
  |---|---|---|---|
  | 1 positional | 94.5 → 94.5 ns | 94.3 → 94.0 ns | 0 → 0 |
  | 8 positional | 127.7 → 128.7 ns | 124.4 → 131.2 ns | 0 → 0 |
  | 9 positional | 291 → 136 ns | 297 → 143 ns | 2 → 0 |
  | 16 positional | 403 → 174 ns | 395 → 177 ns | 2 → 0 |
  | 8 keywords | 664 → 206 ns | 685 → 212 ns | 4 → 0 |

  - Builds, Go→Python calls and aio channels are unchanged (ratios 0.97 to 1.03).
  - The asgi inline micro-benchmark is +2%: three mutex-guarded takes.
  - An asgi response with 12 headers: 18 → 17 allocations, 825 → 440 B, and the 8-pair cap
    is gone.
  - hello req/s shows no measurable difference: ft ratio 1.008, gil 1.021, rounds spread
    0.945 to 1.148.
- **Tests:** py, aio, wsgi, workers and asgi (both modes) pass on both images with scratch
  images built from the prototype's glue. Nine controls failed as intended, C1 to C7 plus
  A1 and A2 (listed in the design doc, section 4.4).
- **Not run:** qemu, darwin and Wine, because they need the rebuilt prebuilt glue.
- **Decisions D1-D7:** in the design doc, section 6.

### 2026-11-19 — The Wine register bug is a Wine 11.22 regression (5484caec5e), reproduced with plain Go; only V16-V31 are zeroed

- **Corrects the entry above:** only V16-V31 are zeroed. V0-V15, FPCR and FPSR survive. Go
  copies structs through F16 and F17, which is why it bites Go.
- **Cause:** Wine commit `5484caec5e` (2026-11-05, MR !12587 "ntdll: Add support for
  partial-FP context flags"), "server: Store upper half of ARM64 floating point registers
  as vector registers". It is in wine-11.22 and still at master `e2aa6cd75ae6`
  (2026-11-18). The path:
  1. A thread suspended in PE code saves V0-V31 in `usr1_handler` →
     `save_context` (`dlls/ntdll/unix/signal_arm64.c:1430`, `:319`).
  2. The server's select handler (`server/thread.c:1966`) stores the FP state without
     adding its flags. The reply then carries only what `set_thread_context` set:
     CONTROL|INTEGER.
  3. `context_from_server` (`dlls/ntdll/unix/thread.c:1060-1063`), ARM64 only, has a new
     `else if (to_flags & CONTEXT_ARM64_FLOATING_POINT) memset(&to->V[16], 0, ...)`.
     `restore_context` writes those zeros back into the thread.
- **Plain Go reproducer** (`.agents-workspace/tmp/r5/winechunk/plainrepro/`):
  - 74 lines, no dependencies, no assembly, no unsafe.
  - Goroutines copy a 144-byte array in a busy loop and check each copy; `runtime.GC()`
    runs every millisecond.
  - 3 cores, 30 s per run:

    | Wine | Corrupted copies |
    |---|---|
    | 11.22 (CI image), go1.26.11 | 2360 in 45e9 |
    | 11.22 (CI image), go1.27.4 (newest Go; same `CONTEXT_CONTROL` in preemptM) | 2158 in 37.5e9 |
    | 11.22 plain `./configure --enable-archs=aarch64` | 3111 in 34.9e9 |
    | 11.22 with `asyncpreemptoff=1` | 0 |
    | native linux/arm64 | 0 |
    | 11.21 | 0 |
    | 11.22 with the one hunk reverted (`winebuild/no-v16-memset.patch`) | 0 |
  - ctxrepro agrees: 11.22 clobbers V16-V31 in 2327 of 2327 rounds and V0-V15 in 0.
    11.21 and the patched build show 0.
- **Latest stock Wine:**
  - The newest releases are development 11.22 (2026-11-13) and stable 11.0.
  - Tarball sha256 values are in the agent's notes; sha512 matches WineHQ's list, and the
    GPG signatures were not checked.
  - No arm64 Linux binary package of 11.x exists: WineHQ apt has amd64 and i386 only,
    Debian has at most 10.0, and Fedora rawhide's 11.21 has no aarch64 build.
- **Issue databases:** nothing identical. Related, but different bugs:
  - Wine 51996 (fixed in 6.23: aarch64 cross-thread get/set ignored FPU state);
  - Wine 60453 (unconfirmed: FPCR overwritten in user-mode callbacks);
  - Wine 60405 (fixed: SSBS);
  - Go #49759 (windows/arm64 async preemption, closed);
  - Go #36494 (preemption vs debuggers, open).

  Nothing found in Wine GitLab issues, dotnet/runtime or openjdk. Bugzilla's REST API
  answered 403, so its HTML search was used instead.
- **Draft Wine bug:** `.agents-workspace/tmp/r5/winechunk/WINE-BUG-DRAFT.txt`, not filed. A
  Go report isn't warranted: Go is correct on real Windows. At most an FYI suggesting a
  defensive CONTEXT_FLOATING_POINT in preemptM.
- **Wine tier options:** pin 11.21, apply the revert patch in the Dockerfile, or keep
  `GODEBUG=asyncpreemptoff=1` (current). The first two keep async preemption under test.

### 2026-11-19 — Wine Bugzilla checked across all statuses: no report of the 11.22 register bug; the Wine tier moves to 11.21

- **Bugzilla search** (fetched 2026-11-19 14:03 UTC; curl from this host gets the anti-bot
  page, a fetch agent got through):
  - Open-only quicksearches found nothing for SetThreadContext arm64, ARM64 context
    floating, golang, or bugs filed against ARM64 since 2026-11-01. The only NtSetContextThread
    hit was 59838, an x86-64 game crash.
  - Repeated with `ALL` (every status) for SetThreadContext arm64, NtSetContextThread,
    arm64 floating point, aarch64 context, golang and arm64 NEON.
  - No report describes vector registers zeroed by SetThreadContext on ARM64, or Go under
    Wine on ARM64. The closest:
    - 51996, CLOSED FIXED in 6.23: aarch64 NtGet/SetContextThread on another thread ignored
      all FP state, because `save_context`/`restore_context` never called
      `save_fpu`/`restore_fpu`. Regression `07f37f21377`, fix `9f0df41a6cc5`.
    - 60453, UNCONFIRMED: ARM64 user-mode callbacks overwrite FPCR with FPSR.
    - 60405, FIXED: SSBS cleared after RtlRestoreContext.
  - The golang hits (25014, 27680, 38272, 38636) are 2015-2021 x86 issues.
- **Wine tier now on 11.21:**
  - `ci/windows-wine.Dockerfile` builds 11.21 (sha256 `5a40f593…c54e3d`) as image
    `pythago-wine:11.21-1`.
  - `GODEBUG=asyncpreemptoff=1` is removed, so async preemption is under test again.
  - 11.22 was only the newest release when it was chosen. Wine 11 itself was needed because
    Debian's 10.0 hangs in wineboot on PAuth CPUs, and 11.21 boots too (agent's plain
    build).
- **11.21 image and SDK:**
  - `pythago-wine:11.21-1` is image `sha256:a1b280a7ce77…db9b9`.
  - `ci/windows-wine.sh sdk` regenerated `~/.cache/pythago-ci/windows-sdk`; 371 files differ
    from the 11.22 SDK, the import libraries among them. The 11.22 copy is in
    `.agents-workspace/tmp/r5/sdk-11.22`.
  - The Windows glue DLLs in `glue/prebuilt` are rebuilt against the 11.21 SDK, together
    with ABI 4.

### 2026-11-19 — The borrow arena lands for calls: py call scopes on `internal/arena`, glue ABI 4 decodes every argument

- **Context:** user decisions on `.agents/docs/arena-design.md`:
  - steps 1 and 2 together, with one glue rebuild;
  - poisoning always on, and no-reuse as a `Config` field;
  - the generic part in `internal/arena`;
  - no Func access to its call scope;
  - decode every argument;
  - lock-free takes for step 3;
  - aio `readBufs` stays separate.

  The design doc's section 7 ("As landed") is the reference.
- **Change:**
  - `internal/arena.Lane[T]` is a chunked bump allocator. It keeps up to 64 KiB for the
    next lifetime and trims the rest to its home chunk.
  - `py/callscope.go` adds call scopes with inline `[8]` arrays, used directly by calls
    that fit. They replace argFrame (capped at 8) and buildPool in dispatch, build and
    callValues.
  - Glue ABI 4 (`PYTHAGO_PY_ABI` = `glueABI` = 4): `struct pythago_call` passes `decoded`
    and `names` as pointers. C decodes every argument and name into a 24-value stack
    array, or PyMem past that, and `pre` points into it.
  - A released borrowed Object gets `p=0` and a released `pre`. Every accessor checks
    `o.live()` and panics: "py: a borrowed *Object was used after the call that lent it
    returned (keep a NewRef instead)".
  - `Config.CheckBorrows` never reuses released call scopes. It is the only new export.
- **glue/prebuilt:**
  - All 12 files are rebuilt. Linux and Windows came from `pythago-dist -prebuild-glue`,
    darwin from `ci/darwin.sh glue-update` on dengaku.
  - The Windows DLLs built against the Wine 11.21 SDK are byte-identical to the 11.22
    build.
  - `ci/glue.sh` reproduces them. The ABI 3 sums are in
    `.agents-workspace/tmp/r5/arena/SHA256SUMS.abi3`.
  - Images must be regenerated; stale ones fail at Init.
- **Numbers:** go1.26.11, linux/arm64, 3.14t on core 5 and 3.13 on core 6. Before (ABI 3)
  and after were rotated over 5 rounds; 11 of 100 runs were dropped for foreign CPU.
  Data: `.agents-workspace/tmp/r5/arena/results2/`.

  | shape | ft before → after | gil before → after | allocations |
  |---|---|---|---|
  | 1 positional | 94.0 → 95.6 ns | 97.2 → 95.0 ns | 0 → 0 |
  | 8 positional | 127.3 → 126.2 ns | 127.3 → 125.4 ns | 0 → 0 |
  | 9 positional | 345 → 140 ns | 344 → 145 ns | 2 → 0 |
  | 16 positional | 477 → 178 ns | 477 → 172 ns | 2 → 0 |
  | 4 keywords | 149.5 → 154.9 ns | 158.5 → 153.6 ns | 0 → 0 |
  | 8 keywords | 740 → 213 ns | 746 → 208 ns | 4 → 0 |

  - The asgi respond shape, Go→Python calls, builds, asgi/wsgi requests and aio give
    after/before ratios of 0.973 to 1.024, with unchanged allocations.
  - `f(*t)` with 10000 unread arguments: 119 → 96 µs and 729 → 483 KB per call.
  - hello req/s, inline, GOMAXPROCS=1 server on core 5, 6 rounds:
    - ft: ratio 0.982, range 0.948 to 1.017;
    - gil: 1.005;
    - p99 unchanged.
- **asgi:** `TestInlineAllocs` is tightened from 9 to 7, since py's dispatch of respond no
  longer allocates (measured 7 on 3.14t).
- **ci/unit.sh:** now vets, gofmts and tests `internal/arena`.
- **Tests:**
  - `internal/arena`: distinct and zeroed takes, keep and trim, Rest/Adopt, NoZero and
    ResetWith.
  - py: TestBorrowedArgumentPoisoned (inside and past the inline arrays),
    TestCheckBorrowsNeverReuses, TestCallScopeTrims, and TestConfigCheckBorrows (child
    processes with and without the field; the default child must show aliasing).
  - TestDispatchAllocs covers 9, 16 and 40 positional, 8 long keywords and 16 keywords, all
    at 0 allocations.
  - TestDispatchNestedConcurrent has a wide variant that goes past C's stack array.
- **Controls:** 14 mutated copies, all failing as intended:
  - lane always trims, or never trims;
  - no poison (SIGSEGV);
  - reuse regardless of CheckBorrows, and the field not applied in `start`;
  - one scope for every call;
  - scope released before the Func runs;
  - C names overlapping the arguments, caught only by the wide nested test;
  - Adopt always in place;
  - Reset never zeroing;
  - grow reusing its chunk;
  - C stack sizing ignoring names.

  Scripts: `.agents-workspace/tmp/r5/arena/controls2.sh`.
- **Left:**
  - Step 3, asgi request scopes with lock-free takes.
  - Step 4, wsgi, an estimated 5 to 7 of its 20 allocations per request.
  - `.agents-workspace/tmp/r3/gilwork`'s 3.13 test image is ABI 3 now; regenerate it before
    reusing it.

### 2026-11-19 — Gate after arena steps 1-2 (ABI 4) and Wine 11.21

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu), including the new `internal/arena`
    step;
  - ci/windows-wine.sh gil and ft on `pythago-wine:11.21-1`, with async preemption on (no
    GODEBUG);
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from a scratch copy, `.agents-workspace/tmp/r5/gate2/`.

### 2026-11-20 — The Wine 11.22 ARM64 register regression is reported upstream

- **Report:** the user filed it in WineHQ Bugzilla under Product Wine, Component ntdll. The
  memset is in `dlls/ntdll/unix/thread.c` `context_from_server()`; commit `5484caec5e`'s
  `server/` changes only split the registers into `q[16]` plus `q16[16]`.
- **Contents:** built from `.agents-workspace/tmp/r5/winechunk/WINE-BUG-DRAFT.txt`:
  - the Go reproducer inline;
  - `ctxrepro.tar.gz` attached;
  - a note that an AI assistant helped.
- **What was left out:** no patch. Wine's Clean Room Guidelines reportedly forbid
  LLM-generated code; we know the rule only from secondary sources, because the wiki is
  behind Anubis.

### 2026-11-20 — asgi requests on `internal/arena` scopes with lock-free takes (borrow arena step 3); the 8-pair head cap is gone

- **Context:** step 3 of `.agents/docs/arena-design.md`, approved by the user with D6
  (lock-free takes). The design doc's section 7 is updated.
- **Change:**
  - `internal/arena/shared.go`:
    - `SharedLane[T]`: a take is `cur.Load()` plus one `used.Add(n)`. Only starting a new
      chunk locks: the taker retries the current chunk once, retires it to `full`, then
      publishes a chunk of `max(n, 2×len)` with `used = n`.
    - `Pool[T]`/`Scope[T]`: a reference-counted root with `Get`, `Retain`, `Release` and
      `Detach`. Releasing past zero panics.
  - asgi requests are scope roots (package-level `requests` pool). Replaced: `refs`, the
    per-Server `sync.Pool`, `buf`, `body`, the `[8][2]string` pairs and their CAS. In their
    place, `q.sc` and `q.mem` with a bytes lane (`SetNoZero`; inline values and the first
    body chunk) and a pairs lane (every head and trailer, uncapped).
  - The reset drains `kick`, resets the lanes and zeroes the rest. WebSocket requests
    `Detach`. The retain rule is unchanged: `Retain` under `s.mu` only while the request is
    in `s.reqs`.
- **Memory ordering:**
  - A chunk's buf and counter are set before `cur.Store` publishes it.
  - Atomic adds give disjoint intervals; an overflow only wastes the tail.
  - Retired chunks stay in `full` until Reset, which runs after the last Release.
- **Numbers:** go1.26.11, linux/arm64, 3.14t on core 5, 3.13 on core 6, 7 rotated rounds,
  before = steps 1-2:

  | benchmark | ft ratio | gil ratio | allocations |
  |---|---|---|---|
  | InlineRequest | 1.003 | 1.007 | 7 → 7 |
  | 12-header request | 0.966 | 0.966 | 19 → 17, 824 → 440 B |
  | LoopThreadRequest | 1.002 | 1.000 | 32 → 32 |

  hello req/s, with the median ratio and its range:

  | mode | ft | gil |
  |---|---|---|
  | inline | 0.999 (0.934-1.036) | 0.997 (0.911-1.090) |
  | LoopThread | 0.994 (0.982-1.016) | 0.989 (0.978-1.001) |

  On gil, LoopThread hello was below 1 in 7 of 8 rounds (p99 2.05 → 2.12 ms) while its
  micro-benchmark is at parity. That is unexplained.
- **Tests:**
  - internal/arena: SharedLaneConcurrentTakes, SharedLaneGrowthRace (2000 fresh lanes
    behind a barrier), SharedLaneKeepsAndTrims, ScopeReferences.
  - asgi: FieldsFromLane, RequestLanesReset.
  - The recycle tests and TestInlineAllocs (7) are unchanged and pass. ci/unit.sh gil and
    ft pass.
- **Controls** (`.agents-workspace/tmp/r5/arena/controls3.sh`):

  | # | mutation | result |
  |---|---|---|
  | S1 | take without one atomic add | overlaps and unzeroed takes, 5 of 5 runs |
  | S2 | chunk published before its counter is set | **not caught** in 5 × 2000 lifetimes; the window is nanoseconds, and `-race` needs cgo |
  | S3 | Reset doesn't zero | fails |
  | S4 | no trim | fails |
  | S5 | reset at every release | fails |
  | S6 | Detach ignored | fails |
  | F1 | pairs always made | fails |
  | A1 | recycle with references left | the over-release panic inline; "recycled under it" in LoopThread |
  | A2 | no reference for the queued batch | the over-release panic |
  | A3 | lanes not reset | `TestInlineAllocs` still passed; `TestRequestLanesReset` caught it |

  S2's ordering rests on the code and its comment.
- **Reviewed by the coordinator:** `shared.go` and the asgi retain, release and reset paths.

### 2026-11-20 — Gate after arena step 3 (asgi request scopes)

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu), with asgi in both modes;
  - ci/windows-wine.sh gil and ft on Wine 11.21;
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from `.agents-workspace/tmp/r5/gate3/`.

### 2026-11-20 — wsgi requests on arena scopes; handles are lifetime IDs checked by TryRetain (borrow arena step 4)

- **Context:** user-approved step 4. Until now wsgi's `with` relied on requests never being
  reused: a stale handle could find a request just before it was unregistered and lock it
  after ServeHTTP had returned.
- **Audit of the paths that touch a request after its handler returns:**
  - Late `write()`, late `wsgi.input` reads and `respond` from threads all go through
    `with`.
  - sendfile and the result's `close()` run inside `serve`.
  - Deferred known-length writes and the disconnect abort happen in ServeHTTP before it
    returns.
  - A thread writing while `serve` returns is waited for on `q.mu`.
- **Change:**
  - `arena.Scope` adds `ID()`, a process-unique ID per lifetime stored before the count, and
    `TryRetain(id)`: CAS-increment unless zero, then check the ID, and release on a
    mismatch.
  - wsgi handles are scope IDs. `acquire` is `sync.Map.Load` followed by `TryRetain`, and
    `with` holds the reference until after its unlock. ServeHTTP gets the scope first and
    releases it last, after `reqs.Delete`, the deferred writes and `releaseWritten`.
  - `requestMem` lanes take the environ (built in place), the first pending buffer
    (`Take(maxPending)`) and the head's pairs (`bytes.Count` and `strings.Cut` instead of
    `strings.Split`).
  - Header names are lower-cased in a stack buffer, and `HTTP_` names are built without a
    string copy.
- **Numbers:** go1.26.11, linux/arm64; before is the tree after step 3.
  - In process, `BenchmarkWSGIHello`, 7 rotated rounds, 3.14t on core 5 and 3.13 on core 6:

    | image | before → after | allocations |
    |---|---|---|
    | ft | 5264 → 4893 ns (0.930) | 21 (1152 B) → 12 (296 B) |
    | gil | 4859 → 4443 ns (0.914) | 21 (1152 B) → 12 (296 B) |
  - req/s: `wsgiserve -threads 10` with GOMAXPROCS=1 on core 5, and `httpload -c 64` with
    GOMAXPROCS=2 on cores 6 and 16, 6 rounds; median ratios:

    | route | ft | gil |
    |---|---|---|
    | hello | 0.977 (0.934-1.067; noisy, an 8-round rerun was dropped for 4-13% foreign CPU) | 1.047 |
    | 64 KiB | 1.038 | 1.049 |

    p99 was unchanged or lower.
- **Tests:**
  - wsgi `TestRecycleHeldRequest`: a thread's late `write()` is held with its reference
    across A's end and B's run, and must report "gone A".
  - wsgi `TestRecycleNoCrossTalk`: 16 × 40 `/late` requests.
  - wsgi `TestRequestAllocs` (at most 12) and arena `TestTryRetainLifetime`.
  - The existing wsgi tests are unchanged and pass. ci/unit.sh gil and ft pass.
- **Controls:**
  - W1: `with` without a reference, "recycled under it ... lifetime 3" and "late write() of
    request A: []".
  - W2: TryRetain without its ID check.
  - W3: recycling with references left, the over-release panic.
  - W4: pending taken from the heap, 13 allocations.
- **Flake fixed along the way:** step 1's `TestConfigCheckBorrows` failed once in the ft tier.
  Its default-mode child expects reuse, which a P change or a GC can prevent. The children now
  run with GOMAXPROCS=1 and GOGC=off; it passed 30 of 30 on both images, and its controls
  still fail.
- **Reviewed by the coordinator:** `acquire`, `with` and ServeHTTP's order of release.
- **Left:**
  - The 12 remaining allocations per request: py result Objects, net/http header maps, the
    `sync.Map` entry, Do's closure, the boxing of serve's arguments, the packed-headers copy.
  - One allocation in `write`'s `UnsafeBytes` that C decoding should have avoided.
  - `held []segment` allocations.

### 2026-11-20 — Gate after arena step 4 (wsgi request scopes)

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu);
  - ci/windows-wine.sh gil and ft on Wine 11.21;
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from `.agents-workspace/tmp/r5/gate4/`.

### 2026-11-20 — OwnedBuffer leases: 9 to 1 and 20 to 5 allocations; no memoryview request body; inline first body chunks from the request's lane

- **Who:** the arena agent, on the user's request (2026-11-20) to (a) profile the
  OwnedBuffer aio benchmarks and fix them without the arena, then (b) design a
  scope-backed lease and land it only if sound and a win. Details and the design notes
  are in `.agents/docs/arena-design.md` section 8.
- **Profile** (aio `BenchmarkChannelBuffer`, Workers1, 4 KiB, 3.14t):
  - **Go→Python, 3.2 µs and 25 allocations:**
    - five owned Objects, with `AddCleanup` alone at 17% of CPU;
    - `Import("builtins")` and the `memoryview` call through Objects, 37%;
    - `NewBuffer`'s Pinner, shape, format C string and format map literals.
  - **Python→Go, 2.5 µs and 11 allocations:**
    - a new `Buffer` with its cleanup, 23%;
    - a `NewRef` of the exporter, 10%;
    - `Close` through `Do`, 18%, kept: it is the documented prompt release.
- **Fixes** (py `owned_buffer.go`, `buffer.go`, `capi.go`; API and semantics unchanged):
  - export Buffers pooled (`leaseExports`), put back closed;
  - no `NewRef`: the export's `Py_buffer` holds the exporter;
  - `TakeObject` calls `PyMemoryView_FromObject` on the exporter, or on a GoBuffer from
    the new `newGoBuffer`, with no Object, the pin and shape in the lease, and a static
    "B" format;
  - `formatOf` is a switch;
  - `drainGoBuffers` uses a static token array under `goBufMu`.
- **Numbers** (go1.26.11, linux/arm64, 3.14.10t on core 5, 3.13.18 on core 6; 5
  interleaved rounds, `-benchtime=1s`, 1 of 60 runs dropped for foreign CPU), ratios
  now/before:

  | case | 3.14t | 3.13 | allocations |
  |---|---|---|---|
  | Go→Python, Workers1 | 0.75-0.76 (3.2 → 2.4 µs) | 0.81-0.82 | 25 → 10 |
  | Go→Python, Workers32 | 0.88-0.94 | 0.92-0.98 | 31 → 16 |
  | Python→Go, Workers1 | 0.49-0.52 (2.5 → 1.3 µs) | 0.50-0.51 | 11 → 3 |
  | Python→Go, Workers32 | 0.69 | 0.70 | 17 → 9 |
  | NativeViewAccess, BufferExportNative | 0.98-1.00 | 0.98-1.00 | |

- **(b) not landed:**
  - **Not sound:** ASGI's `http.request` `body` is `bytes`, and applications call
    `body.decode()` or `json.loads(body)`, which fail on a memoryview.
  - **No win:** the scratch prototype (`proto6`) was slower at 100 B (7374 vs 7240 ns, 4
    more allocations) and equal at 16 KiB.
  - **Hazards, had it landed:** a kept view holds the scope until GC.
- **Landed instead, inline mode only** (asgi `readFirstChunk`):
  - When the declared length fits one chunk, the first chunk is read to that length, into
    the request's bytes lane. Before, it came from the heap and was read past the end to
    see EOF, which cost a last doubling.
  - The read starts at 4 KiB and doubles as data arrives.
  - A one-byte probe still reaches EOF, which starts net/http's background read.
  - In process, 16 KiB upload: 0.61 (ft) and 0.60 (gil), 34294 → 240 B per request.
    100 B: 0.83 and 0.82.
  - Network (asgiserve GOMAXPROCS=1 on core 5, httpload -c 64 on cores 6 and 16,
    `bench/uvicorn/app.py` `/upload`):
    - 16 KiB: 1.38 (ft) and 1.37 (gil) req/s, p99 4.64 → 3.03 ms on ft;
    - 100 B: 1.09 and 1.08.
  - LoopThread keeps `readChunk` and measured parity. Reading to the declared length
    there doubled p99, unexplained.
- **Tests:**
  - py: `TestOwnedBufferAllocs` (at most 1 and 5), `TestOwnedBufferExportReuse` (a reused
    Buffer is not seen by a closed lease; a memoryview holds its own export, so the
    exporter can't be resized), `TestFormatOfEveryElem` (against `struct.calcsize`).
  - asgi: `TestFirstChunkSizes` (sizes around 4 KiB and 64 KiB, declared and chunked,
    sha256, both modes), `TestUploadBodyNotAllocated` (under 40 KiB per 16 KiB upload;
    measured 24 KB, was 58 KB), `TestFirstChunkFollowsData`.
- **Controls** (scratch copies, agent workspace `r5/arena/ctl/`):
  - O1: a new Buffer per lease, "allocates 6 times, want at most 1".
  - O2: memoryview through builtins, "allocates 19 times, want at most 5".
  - O3: export put back still open, "BufferInto needs a closed Buffer".
  - O3b: `PyMemoryView_FromBuffer` of the lease's Py_buffer, "holds no export of its
    exporter". This control first passed; the test gained the resize check.
  - O4: complex format by full size, "complex64: format Zd, want Zf".
  - B1: no lane inline, 61285 bytes per request.
  - B2: one byte short, requests rejected.
  - B3: never read more, sha mismatches at 65537 and 200000 bytes.
  - B4: declared length allocated up front, "10 bytes of a declared 64 KiB took 65536".
- **Gate (agent):** gofmt and vet clean; py, aio, asgi (both modes), wsgi on both images;
  ci/unit.sh gil and ft pass.
- **Reviewed by the coordinator:** the pooled exports' cleanup on a lost lease, the
  lease's pin kept alive through `goBufs`, and `readFirstChunk`'s bounds and probe.
- **Left:** see TODO, "OwnedBuffer and body-path leftovers".

### 2026-11-20 — Gate after the OwnedBuffer leases and the inline first chunk

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu);
  - ci/windows-wine.sh gil and ft on Wine 11.21;
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from `.agents-workspace/tmp/r5/gate5/`. The log noise (the workers
  crash-loop test's "giving up" line, qemu's nomaps preload warning) matches gate 4.

### 2026-11-20 — Go-owned `bytes`: not landed (3.14t QSBR makes the lane design unsound; at most 3-6% in process, nothing over the network; a free hook costs up to 2%)

- **Who:** the arena agent, on the user's request for exact-type `bytes` objects with
  header and data in Go memory, freed back to Go, first for asgi request bodies. Design in
  `.agents/docs/arena-design.md` section 9.
- **Source findings (3.13.18, 3.14.10):**
  - **Construction:** `PyBytes_FromStringAndSize` is `PyObject_Malloc` +
    `_PyObject_InitVar` + `ob_shash = -1` + the NUL. Lengths 0 and 1 are immortal
    singletons.
  - **Free:** `object_dealloc`, then `tp_free` (`PyObject_Del` on 3.13, `PyObject_Free` on
    3.14), then `_PyObject.free`. There is no bytes freelist.
  - **Resize:** `_PyBytes_Resize` calls `PyObject_Realloc`.
  - **3.14t, the deciding fact:**
    - bytes live in the object heap, which uses QSBR, and mimalloc preserves a freed
      block's first `debug_offset` bytes;
    - lock-free list and dict readers (`_Py_TryIncrefCompare`) CAS `ob_ref_shared` of
      objects that may be freed;
    - QSBR is internal and not exported.
  - **Consequence:** reusing variable-size chunks at live count 0 is unsound on 3.14t;
    fixed, never-repartitioned slots with header-preserving writes would be sound.
  - **Checked by the coordinator** in the 3.14t headers: `pycore_object.h`
    `_Py_TryIncrefCompare`, and the mimalloc `types.h` fields `debug_offset` and
    `page_use_qsbr`.
- **Upper bound** (`proto9`: the body passed as an existing bytes object of its length,
  so no copy, allocation or free; go1.26.11, linux/arm64, 3.14.10t on core 5, 3.13.18 on
  core 6; 5 interleaved rounds, 0 of 20 runs dropped):
  - in-process inline `BenchmarkUpload`:
    - 100 B: 1.00 (ft and gil);
    - 16 KiB: 0.972 ft, 0.944 gil;
    - 64 KiB: 0.952 ft, 0.968 gil;
    - 16 KiB chunked: 0.968 ft, 0.946 gil.
  - Network (asgiserve inline, `httpload -c 64 -body N`, 5 rounds): 1.008 and 0.999 (ft),
    1.020 and 1.027 (gil). Every round range spans 1.0.
- **Hook cost** (`proto10`: a glue hook on object free and realloc with one range check;
  the same binaries with `PYTHAGO_FUSION_HOOK` 0 or 1, 6 rounds):

  | workload | ft | gil |
  |---|---|---|
  | `bytes(64)` loop | 1.007 | 1.011 |
  | str concatenation loop | 1.017 | 1.023 |
  | class instance loop | 1.009 | 1.010 |
  | asgi InlineRequest | 0.998 | 1.003 |

  - That is 0.4 to 0.7 ns per free. A scratch test confirmed that the hook takes frees in
    its range. Its control, with no hook, aborted with "free(): invalid pointer".
- **Decision:** not landed; the decision on the fixed-slot design is the user's.
  - The fixed-slot design would add a second allocator, an ABI bump, a tax on every object
    free and new conversion API for LoopThread.
  - It could save at most 3 to 6% of a 16 to 64 KiB in-process upload: the copy costs
    about 0.2 to 0.3 µs per 16 KiB.
- **Scratch:**
  - prototypes: `.agents-workspace/tmp/r5/arena/{proto9,proto10}`;
  - results: `results9`, `results10`;
  - scripts: `bench9.sh`, `bench10.sh`, `load9.sh`.

### 2026-11-20 — Go-owned `bytes` with a GC grace: working prototype, not landed; most of the large gains are the read path's churn, fixable without it

- **Who:** the arena agent. Context: the user asked to try Python's stop-the-world as
  the grace period that section 9 found missing on 3.14t. Design and results:
  `.agents/docs/arena-design.md` section 10.
- **Source checks (3.14.10, `Python/gc_free_threading.c`, `Objects/funcobject.c`):**
  - **GC callbacks bracket a stop-the-world:**
    - `gc_collect_main` calls "start", then `gc_collect_internal`, then "stop", with no
      early exit between them (skipped only at shutdown);
    - `gc_collect_internal` begins with `_PyEval_StopTheWorld`;
    - `stop_the_world` waits for every thread state to park;
    - list and dict lock-free reads have no safepoint between the pointer load and
      `_Py_TryIncrefCompare`.
  - **A forced grace:** `PyFunction_SetDefaults` stops and restarts the world. That is an
    implementation detail, to re-check on every release.
  - **Re-checked by the coordinator in the source tree:** the callbacks' order around
    `gc_collect_internal`, and `PyFunction_SetDefaults`.
- **Prototype** (`.agents-workspace/tmp/r5/arena/proto11`):
  - **glue:**
    - a chained free/realloc hook (one range check);
    - per-chunk live counts and epoch stamps, updated atomically by C with no Go
      callback;
    - `pythago_bytes_adopt`.
  - **py:**
    - `FusedBytes` from 256 KiB chunks of an mmap'd 64 MiB region, adopted as exact
      `bytes` by `py.From` (singletons below 2 bytes);
    - chunk reuse at live 0: immediate with the GIL, after a grace on 3.14t;
    - graces come from `gc.callbacks` plus a forced STW when a quarter or less of the
      region is free;
    - without memory, a body falls back to a copy.
  - **asgi:** first and `receive()` chunks in both modes.
- **Findings:**
  - 3.14t ran 0 collections in 60,000 requests, so forced graces are required. With them,
    89% of 16 KiB bodies and 100% of 100 B bodies were fused (4-5 forced graces per
    20,000 requests).
  - **Soundness on 3.14t is argued, not tested.** The lock-free stress test's no-grace
    control did not fail in 16M takes over 2 minutes.
  - **Other controls failed by behaviour:**
    - adopt without `ob_shash = -1`: `KeyError`;
    - no grace: "no chunk waits for a collection";
    - stamp ignored: "handed out again before any collection".
- **Numbers** (go1.26.11, linux/arm64, 3.14.10t on core 5, 3.13.18 on core 6, against the
  current tree; interleaved, no runs dropped):
  - In process, `BenchmarkUpload` time ratio (ft / gil):
    - inline: 100 B 1.017 / 1.030; 16 KiB 1.022 / 0.947; 64 KiB 0.488 / 0.183;
      chunked 1.026 / 0.903;
    - LoopThread: 100 B 0.936 / 0.960; 16 KiB 0.991 / 0.705; 64 KiB 0.980 / 0.294;
      chunked 0.754 / 0.695.
  - Network req/s ratio (ft / gil):
    - inline: 100 B 1.017 / 1.023; 16 KiB 1.033 / 1.118; 64 KiB 2.507 / 2.873;
    - LoopThread: 100 B 1.049 / 1.038; 16 KiB 1.390 / 1.480; 64 KiB 2.048 / 2.420.
  - Hook tax on non-body workloads: 1.005-1.013 (ft) and 1.010-1.019 (gil).
- **Where the large gains come from:**
  - **Inline 64 KiB:** the first chunk's growth steps (124 KiB) exceed the bytes lane's
    64 KiB keep bound, so every request reallocates (about 258 KB per request).
  - **LoopThread:** reads into doubling heap buffers (35 KB per 16 KiB body).
  - **Without the fusion:** a 256 KiB keep bound alone (`proto12`, 4 rounds) takes inline
    64 KiB from 34.6 to 8.3 µs on ft, and from 30.8 to 6.9 µs on gil. The fusion's
    figures are 17.0 µs (ft) and 5.7 µs (gil).
- **Decision:** not landed. First fix the read paths, then re-measure the fusion against
  that.

### 2026-11-20 — Read-path fixes: the inline 64 KiB lane cliff (2.5-2.7x req/s) and LoopThread's first-chunk read; the fusion re-measured against them

- **Who:** the arena agent. Context: proto11 showed that most of the fusion's large gains
  came from read-path churn. Design notes: `.agents/docs/arena-design.md` section 11.
- **Cause of the cliff:**
  - A 64 KiB first chunk grows through 4, 8, 16, 32 and 64 KiB takes.
  - The bytes lane's current chunk then exceeded the 64 KiB keep bound, so `Reset` dropped
    it and the next request made it again: about 258 KB per upload.
- **Change:**
  - `internal/arena/shared.go`:
    - `SharedLane.Extend` grows the last take in place with one CAS on the chunk's `used`.
      It refuses when another take followed, the chunk has no room, or the slice is not
      in the current chunk.
    - `SharedLane.SetKeep(n)` keeps a chunk up to n past `Keep` while lifetimes use more
      than `Keep`, and drops it after 16 lifetimes in a row that don't (`keepIdle`).
  - `asgi/asgi.go`:
    - each request calls `SetKeep(2 × BodyChunk)` on its bytes lane;
    - `readFirstChunk` tries `Extend` before taking;
    - LoopThread first chunks use `readFirstChunk` on the heap (declared length, EOF
      probe) instead of `readChunk`.
- **The earlier LoopThread "doubled p99" did not reproduce:**
  - Over 2 × 6 alternating rounds, the new read is 5% faster for 100 B, with p99 3.35 →
    2.79 ms (ft) and 3.08 → 2.76 ms (gil).
  - With `GOGC=off` both reads give about 1.55 ms, so the tail is the Go GC, which the new
    read halves (about 600 vs 1150 collections per run).
  - The earlier result was most likely load on core 5's sibling, core 15, which the old
    foreign-CPU check didn't watch. The load scripts now record core 15.
- **Retained memory per pooled request:** 128 KiB while 64 KiB uploads continue, back to
  300 B after 20 small requests. Worst case: pooled requests × 2 × BodyChunk.
- **Numbers:**
  - Setup: go1.26.11, linux/arm64, 3.14.10t on core 5, 3.13.18 on core 6, 5 interleaved
    rounds. Network: asgiserve GOMAXPROCS=1 on core 5, `httpload -c 64` on cores 6 and 16.
  - Ratios after/before (in process: time; network: req/s), for ft / gil:

    | case | in process | network |
    |---|---|---|
    | inline 64 KiB | 0.223 / 0.202 | 2.520 / 2.668 (p99 6.01 → 3.18 and 5.76 → 2.95 ms) |
    | inline 16 KiB | 0.977 / 0.969 | 1.004 / 0.991 |
    | inline 100 B | about 1.00 | 0.982 / 1.007 |
    | LoopThread 100 B | 0.950 / 0.936 | 1.067 / 1.034 |
    | LoopThread 64 KiB | 0.810 / 0.803 | 0.988 / 1.007 |
    | hello | about 1.00 | 0.983-0.999, every range spanning 1.0 |

- **Tests and controls** (the controls ran in scratch copies):
  - `TestSharedLaneExtend`. Control: Extend never extends; fails with "Extend of the last
    take: false".
  - `TestSharedLaneKeepDecay`. Controls: SetKeep ignored, fails; no decay, fails with
    "still keeps 66536".
  - asgi `TestInlineUpload64KiBNotAllocated` (154 B per request). Control: no SetKeep;
    fails with "allocates 258942 bytes".
  - asgi `TestLoopThreadSmallUploadSized` (1420 B per posted 100 B upload). Control: the old
    read; fails with "allocates 5389 bytes".
- **The fusion (`proto15`, not landed) against the fixed tree**, ft / gil:
  - **Inline:** in process 1.02-1.06 (slower) on ft and 0.90-0.97 on gil; network
    0.93-1.03 on ft and 1.03-1.08 on gil.
  - **LoopThread 16 and 64 KiB:** network 1.36 and 2.08 (ft), 1.46 and 2.45 (gil). These
    are against a LoopThread path that still reads into doubling heap buffers, so they
    don't isolate the fusion.
- **Gate (agent):**
  - gofmt and vet clean;
  - asgi in both modes, internal/arena, wsgi and workers on both images;
  - ci/unit.sh gil and ft pass.
- **Reviewed by the coordinator:** `Extend`'s range, alignment and CAS checks; Reset's
  keep and decay; `SetKeep` called before the request is shared.
- **Open:** a LoopThread lane path (posted values holding a request reference), the
  remaining non-fusion comparison. The fusion decision is the user's.

### 2026-11-20 — Gate after the read-path fixes (lane Extend/SetKeep, LoopThread first chunk)

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu);
  - ci/windows-wine.sh gil and ft on Wine 11.21;
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from `.agents-workspace/tmp/r5/gate6/`. The log noise matches gates 4 and 5.

### 2026-11-20 — LoopThread body leases (landed); the fusion against every non-fusion fix (not landed)

- **Who:** the arena agent. Context: section 11's fusion gains for LoopThread were
  measured against heap read buffers; this is the non-fusion fix for them. Design:
  `.agents/docs/arena-design.md` section 12.
- **Change:**
  - `internal/lease.Bytes` (new, internal): `py.From` copies it into a bytes object
    (`PyBytes_FromStringAndSize`), then calls `ReleaseLease` exactly once. py's public API
    is unchanged.
  - **asgi `bodyLease`:** holds a request-scope reference from the post until the loop
    thread's copy. Each request has two, `firstLease` and `recvLease`, so a post
    allocates nothing.
  - **LoopThread first chunk:** read into the bytes lane, as inline mode does.
  - **`receive()` chunks:** read into a per-request receive buffer (`readPosted`,
    `readChunkInto`).
    - It is kept up to BodyChunk across recycling and dropped after 16 lifetimes without
      use.
    - While it is lent (a second `receive()` before the first was converted), a chunk gets
      heap memory.
  - **Lifetimes:**
    - A client gone before the loop thread runs drops only ServeHTTP's reference.
    - A post never converted (the loop closed), or a tuple whose earlier element failed,
      keeps its reference: the request is never recycled and the GC reclaims it.
    - WebSocket requests are detached and unaffected.
  - `ci/unit.sh` vets and gofmts `internal/lease` (added by the coordinator).
- **Tests and controls** (controls in scratch copies):
  - `TestPostedBodyHeldUntilConverted` recycles a request while its posted body is
    unconverted. Control, no reference: "request B's application saw 1000 bytes starting
    C", 3 of 3 runs, both images.
  - `TestLeaseCopiedThenReleased`. Control, released before the copy: the bytes hold
    "XXXXXXXXXXXXXXX".
  - `TestReceiveBufferBusy`. Control, busy flag ignored: "a second read while the first
    chunk was lent overwrote it".
  - `TestLoopThreadUploadNotAllocated` (at most 8 KB; measured 1.3-3.2 KB). Control, heap
    chunks: "a posted 204800-byte upload allocates 348155 bytes".
  - The new tests passed 15 runs in a row on each image.
- **Numbers** (5 interleaved rounds, ft on core 5, gil on core 6, load on cores 6 and 16;
  core 15 busy up to 99.7% in most gil network rounds, from another user's process):

  | case | in process ft / gil (time) | network ft / gil (req/s) | p99 ms ft / gil |
  |---|---|---|---|
  | LoopThread 16 KiB | 0.714 / 0.710 | 1.364 / 1.371 | 4.45 → 3.65 / 4.14 → 3.39 |
  | LoopThread 64 KiB | 0.390 / 0.385 | 2.171 / 2.217 | 8.23 → 6.27 / 8.23 → 5.92 |
  | LoopThread chunked 16 KiB | 0.712 / 0.716 | not run | |
  | inline, 100 B, hello | 0.994-1.003 | 0.975-1.029, ranges span 1.0 | |

- **The fusion (proto17, rebased onto this tree) against the leases**, as ratios
  fusion/leases:
  - **ft:** no throughput gain; 1.7-4.7% slower in process on uploads.
  - **gil:** 3-10% faster in process; network +6-13% for 16 and 64 KiB bodies.
  - **p99, LoopThread 64 KiB:** 5.89 → 3.90 ms (ft), 5.54 → 3.73 ms (gil).
  - **Conclusion:** its earlier 1.36-2.45x LoopThread gains were the heap read buffers.
    Not landed; the decision is the user's.
- **Gate (agent):**
  - gofmt and vet;
  - py, asgi, aio, wsgi and internal tests on both images, asgi in both loop modes;
  - ci/unit.sh gil and ft.
- **Reviewed by the coordinator:**
  - ReleaseLease reads q before clearing the busy flag;
  - `request.mem` is a pointer;
  - the `lease.Bytes` case is not shadowed in From's type switch;
  - unconverted posts leak to the GC rather than being recycled.

### 2026-11-20 — Gate after the LoopThread body leases

- **Result:** every tier passed on the first run:
  - ci/unit.sh gil and ft, arm64 and amd64 (qemu), now vetting `internal/lease`;
  - ci/windows-wine.sh gil and ft on Wine 11.21;
  - ci/darwin.sh gil and ft on arm64 and amd64.
- **How:** run from `.agents-workspace/tmp/r5/gate7/`. The log noise matches gates 4-6.

### 2026-11-20 — Slotted Go-owned bytes reuse freed slots at once on 3.14t, and a deterministic test with CPython's own readers holds it

- **Context:**
  - The user asked for a prototype of "option 5": fused bytes reused like mimalloc
    reuses blocks, at once but only within a size class, so that no grace is needed for
    reuse, only for class changes.
  - Prototype only, in `.agents-workspace/tmp/r5/arena/proto18` (proto17 plus this; the
    shared tree's code is unchanged). Details are in arena-design.md section 13.
- **Source facts verified (3.14.10):**
  - mimalloc keeps bytes up to `ob_type` of a freed object (pystate.c:3222-3243). Its
    free-list link sits at offset 0 (`ob_tid`).
  - All-free pages change size class only after QSBR (obmalloc.c:161).
  - A dead header is local 0 with shared 0 or `_Py_REF_MERGED` (object.c:436).
  - `_Py_TryIncRefShared` refuses those without writing (pycore_object.h:570).
  - `_Py_TryIncrefCompareStackRef` trusts `ob_gc_bits`' deferred bit with no compare
    (pycore_stackref.h:792).
  - `PyGC_Collect` does nothing when gc is disabled or already collecting
    (gc_free_threading.c:2660, 2332).
- **Design:**
  - 1 MiB chunks of one size class each: 44 capacity classes, from 16 B up to 64 KiB.
  - Go writes only from `ob_sval` on.
  - Per-chunk Treiber stacks (tag | index) with their links in Go memory. The C free hook
    pushes lock-free; Go pops under its mutex.
  - A class change needs live 0 plus a stop-the-world after the chunk's last Python free.
    When the region is exhausted, a background `PyGC_Collect` provides one.
  - The forced `PyFunction_SetDefaults` grace is removed.
  - asgi grows unknown-length bodies 4 → 16 → 64 KiB by moving them (a copy).
- **Rejected alternatives:**
  - A global per-class stack: reclaiming one chunk would mean filtering the stack.
  - A per-chunk bitmap: Go would have to scan for free bits.
  - Links in the slot: control c1c below shows why.
- **Why the old checks could not catch a bug here:** proto11's stress test cannot fail
  (a nanosecond window). Here it again passed under the fill-header control (16 million
  takes) and the no-grace control.
- **The new layer that does catch it: `TestFusedBytesStaleReader`.**
  - It runs the real `_Py_TryIncrefCompare` and `_Py_TryIncrefCompareStackRef`, compiled
    with `Py_BUILD_CORE` against the PBS internal headers in a test-only glue file.
  - The reader runs on a thread that neither freed nor adopted the object (asserted).
    The stages are: freed; reused and adopted, with `ob_ref_shared` 0 and 0x5; taken with
    the payload written. Sources are the real list slot, and one still holding p.
  - It compares the whole chunk across the calls. With the fix: all 0, no byte changed.
- **Controls** (scratch copies, glue controls with their own images):

  ```
  c1 payload from slot start: (c) ... a source still holding p: the stale reader's incref succeeded (_Py_TryIncrefCompare 1, _Py_TryIncrefCompareStackRef 1)
                              ... changed the chunk at slot offset +16 (a5 a5 a5 a5 a5 a5 a5 a5 -> a9 a5 a5 a5 a5 a5 a5 a5)
  c1c link over ob_ref_shared: (c) ... changed the chunk at slot offset +16 (00 00 00 00 02 00 00 00 -> 04 00 00 00 02 00 00 00)
  c2 0xdd fill on free:       (a) freed, the list slot: the stale reader's incref succeeded (_Py_TryIncrefCompare 0, _Py_TryIncrefCompareStackRef 1)
  c3 class change, no grace:  a chunk whose objects Python freed changed class before any collection
                              a stale reader of a freed object in it now: _Py_TryIncrefCompare 1, the old header's bytes (class B payload) ... -> ... a9 a5 ...
  c1b link at offset 0 (as mimalloc), c2b zeroed header: pass, as predicted (harmless)
  ```

- **Suites (fusion on, both images):** py, aio, wsgi, and asgi inline and LoopThread
  pass.
- **Numbers:**
  - Setup: go1.26.11, PBS 20261110, linux/arm64. In process: ft core 5, gil core 6.
    Network: server on core 5, load on cores 6 and 16.
  - 5 rounds with tree16, p17 and p18 rotating. Core 15 was 82-100% busy from other
    users' processes throughout.
  - Ratios against the tree with leases:

  | case | in process ft / gil (time) | network ft / gil (req/s) | p99 ms ft (tree → p18) |
  |---|---|---|---|
  | inline 16 KiB | 0.958 / 0.951 | 1.093 / 1.055 | 3.13 → 2.87 |
  | inline 64 KiB | 0.869 / 0.878 | 1.172 / 1.197 | 3.99 → 3.30 |
  | LoopThread 16 KiB | 0.983 / 0.975 | 1.087 / 1.058 | 4.59 → 3.08 |
  | LoopThread 64 KiB | 0.918 / 0.932 | 1.173 / 1.167 | 8.43 → 3.57 |
  | 100 B, hello, chunked | 0.979-1.009 | 1.011-1.041 | |

  - proto17 in the same runs: ft 1.04-1.07 slower in process and 0.95-1.02 on the
    network; gil about like p18.
  - Hook tax (fusion off against on, same binary): 0.6-1.3%.
  - Memory at 64 MiB: no class changes, collections or fallbacks. Resident: 4-200 KiB
    inline, 2-4.9 MiB LoopThread. Class waste is 1.12 for 100 B and exact at 16 and
    64 KiB.
  - With a 2 MiB region and mixed sizes, ft ran 3922 `PyGC_Collect`s in 10 s and lost
    about two thirds of its throughput: a cliff.
- **Not landed;** the user decides.
- **What to carry:**
  - A soundness argument about concurrent C internals can be tested deterministically
    by compiling CPython's own static inline readers against the release's internal
    headers, and running them on the states a racing thread can meet.
  - Use a source that still holds the pointer, so that a restored CAS cannot hide a
    write.
  - Pair the unsound controls with harmless variants that must pass.

### 2026-11-20 — Landing plan for the slotted fusion (opt-in)

- **Who:** the coordinator, at the user's request ("Plan the port and landing as opt-in").
  The design is `.agents/docs/arena-design.md` section 13 (proto18); the plan is section 14.
- **Decision (user):** land the slotted fusion as an opt-in, ported to darwin and Windows.
  The GC-grace fusion (proto17) and its `PyFunction_SetDefaults` forced grace are dropped.
- **Findings made while planning, checked in the 3.14.10 source:**
  - **tracemalloc can remove the hook.** `tracemalloc.stop()` restores the allocators it
    saved at start (`Python/tracemalloc.c:841, 873`), and `-X tracemalloc` starts it in
    the main phase of init (`Python/pylifecycle.c:1233`).
    - A hook installed after that would be removed by `stop()`, and fused frees would then
      reach mimalloc.
    - The plan installs the hook between `cInitializeCore` and `cInitializeMain`
      (`py/runtime.go`), and adds a test plus a control for this.
  - **The darwin glue is compiled as one unity file** (`cmd/pythago-dist/darwin.go`), so
    the `Py_BUILD_CORE` check helper must be its own object there.
  - **The glue compiles with `-I include/internal` on every OS**
    (`cmd/pythago-dist/build.go:204`, `darwin.go:48`, `windows.go:148`), so the helper
    needs no new include path.
- **Status:** planned, not started. No code changed.

### 2026-11-20 — Fusion region as a Go heap slice (user decision)

- **Decision (user):**
  - The proposed defaults: `Config.FusedBytes` (the region size in bytes), no public type
    in v1, asgi follows py, and `Init` fails on unverified versions.
  - The region is an ordinary Go heap slice, pinned for the life of the process, instead
    of an OS mapping.
  - This removes the per-OS region code. Plan: `.agents/docs/arena-design.md` section 14,
    step 2.
- **Measured** (`.agents-workspace/tmp/heapregion`, go1.26.11, linux/arm64; a 64 MiB
  `make([]byte)` against a control with none):
  - **GC pacing:** the heap goal went from 4 to 128 MiB, and 125 MiB of garbage ran 1 GC,
    against 30 in the control.
  - **Residency:** +0.9 MiB of RSS after `make`, and +4.1 MiB after touching 4 MiB.
  - **Reused pages:** a region allocated after an 80 MiB buffer was dropped reused those
    pages; RSS stayed at 75 MiB.
- **Consequence:** the region counts as live heap and against `GOMEMLIMIT`. The README will
  suggest 8-16 MiB.

### 2026-11-20 — The slotted fusion landed as an opt-in (`py.Config.FusedBytes`), with per-chunk Go memory and a sweeper

- **Context:**
  - The user approved landing section 13's design (proto18) per
    `.agents/docs/arena-design.md` section 14, steps 1 to 9. Step 10, the Wine and darwin
    tiers, is left to the coordinator.
  - During the work the user replaced the pinned region slice with per-chunk Go
    allocations, released when empty and past their grace (section 14 step 2, rewritten).
  - Full record: arena-design.md section 15. Scratch: `.agents-workspace/tmp/r5/land`.
- **What landed:**
  - `py.Config.FusedBytes` (a cap in bytes; 0 installs nothing).
  - `internal/fusion`: chunks, classes, free stacks, a two-level window table for the C
    hook, reclaim, release and the sweeper.
  - `py/fusion.go`: support check, install between the init phases, gc callback,
    startup check, `From`.
  - Glue ABI 5: the hook, `pythago_bytes_adopt`, `glue/pythago_fusioncheck.c` with
    CPython's readers.
  - asgi bodies; `asgiserve -fused-bytes`; fusion passes in `ci/unit.sh`,
    `ci/windows-wine.sh` and `ci/darwin-host.sh`.
  - All 12 `glue/prebuilt` objects rebuilt. Linux and Windows here; darwin with
    `ci/darwin.sh glue-update` on dengaku.local (macOS 26.5.2). `ci/glue.sh` and
    `ci/darwin.sh glue` reproduce them.
  - The new `SHA256SUMS` starts `ae8ae24c… cp313-darwin_amd64.o`,
    `133024a7… cp313-linux_arm64.o`, `570b19dc… cp314t-linux_arm64.o`.
- **Decisions beyond the plan, with the reason:**
  - **The prefilter bounds live in C static storage**
    (`pythago_fusion_bounds`): through a pointer to Go memory they were a dependent load
    on every free.
  - **Sweeps run once a second, from a goroutine, not at gc callbacks.**
    - The first version swept at each collection's end. The pacer run showed a server
      reaching none: 0 collections in 3 s of `GET /`, so the chunks of a finished load
      stayed held (heap goal 13.7 MiB against 4.2 MiB off).
    - Cause: gen0 counts allocations minus frees, and a server's balance.
    - Free-threaded, the sweeper runs `PyGC_Collect` when more than 2 empty chunks wait
      for a grace and no collection ran for 10 s.
    - A 2-sweep decay churned chunks under steady GIL load (13 made, 8 released in one
      run); 4 sweeps, with the extra idle chunks also waiting 4, removed it.
  - **darwin stays one translation unit** (bao takes one object; ld64.lld has no `-r`).
    The check file comes last, with `PYTHAGO_GLUE_UNITY`. Its readers compile to the same
    instructions as alone (objdump, 3.14t aarch64).
  - **`Py_BUILD_CORE_MODULE`** for the check file: no libpython TLS reference, and
    dllimport on Windows.
  - **The subinterpreter guard is in C** (adopt returns NULL outside the main
    interpreter). No Go function is reachable from a subinterpreter today; the test uses
    `Py_NewInterpreter`.
- **Findings:**
  - **`-X tracemalloc` and `PYTHONTRACEMALLOC` never start tracemalloc in pythago, fusion
    or not.** `PyConfig_InitIsolatedConfig` sets `tracemalloc = 0`, and both are read
    only when it is -1 (Python/initconfig.c:1152, 2336, 3.14.10). Seen as:

    ```
    sys._xoptions == {'tracemalloc': True}, tracemalloc.is_tracing() == False
    ```

    So the main-phase start the plan guards against cannot happen today. The test drives
    `config->tracemalloc` through a test-only glue setter. `PYTHONMALLOC=debug` is moot
    the same way: the isolated pre-config ignores it.
  - **Bug, found with the fusion on (GIL py suite, forkserver test):** a forked child
    calls the Go gc callback.

    ```
    Exception ignored in: <built-in method pythago_fusion_gc of int object at 0x12f4168>
    RuntimeError: Go functions are not available in a forked child process
    ```

    Fixed: the callback is dropped in `os.register_at_fork(after_in_child=...)`. No test
    failed: the message is printed as an unraisable exception, not raised. It was caught
    by reading the suite's output.
- **Controls:** each in its own copy, with the diagnostics in section 15's table. Every
  unsound variant fails; c3 and crel pass with the GIL, as expected.
  - c1, c1c, c2: caught by the startup check at `Init` (free-threaded), and by the
    stale-reader test with the check off.
  - c3 (a class change without a grace), crel (release before the grace).
  - cafter (hook after the main phase), coff (hook with the fusion off).
  - csweep (no sweeper), cwin (a chunk registered in one window).
- **Measured** (go1.26.11, linux/arm64, PBS 20261110, bao v0.0.8; interleaved; ft core 5
  and gil core 6 in process; network on core 5 with the load on 6 and 16; no run had
  more than 2% foreign CPU; core 15 median 2%, up to 100%):
  - fusion off against the pre-landing tree: 0.99 to 1.00 in process; network within the
    round ranges;
  - on against off, network: 16 KiB +5 to 10%, 64 KiB +12 to 16%; LoopThread 64 KiB p99
    6.1 → 2.4 ms (ft), 6.0 → 2.4 ms (gil). In process, 64 KiB -5 to -11% ns/op. 100 B is
    +2 to 4% in process and at parity on the network;
  - 16 and 64 MiB the same;
  - hook tax: 0.6 to 1.8% on free loops (GIL instances 2.8%, of which about 2 points
    are binary placement: off alone is 1.020), at most 1.5% on a request; the same as
    proto18's single range check except GIL instances;
  - pacer: idle +0 (GIL) and +0.5 MiB heap goal (ft, the startup check's chunk). Under
    LoopThread 64 KiB load the goal is lower with the fusion on (14-17 against 20-34
    MiB). 25 s after the load every chunk beyond the idle keep was back with Go.
- **Gate:** `ci/unit.sh gil` and `ft` passed, arm64 and amd64 (qemu), on the final tree.
- **Unresolved:**
  - the Wine and darwin tiers (coordinator);
  - 100 B in process about 90 ns behind proto18, unexplained (not the interpreter
    check, measured without it);
  - no `perf` access to settle the GIL instance-free gap;
  - a LoopThread post never converted keeps its slot.
- **Carry:**
  - **Re-verify per CPython minor release:** before widening
    `pythago_fusion_supported`, run `TestFusedBytesStaleReader` against the new headers
    and section 13's checklist.
  - **Run a control for anything sweeping or releasing:** a control that releases or
    demotes early must fail `TestFusedBytesRelease` or `ClassChangeGrace`.
  - **Don't trigger periodic work on Python collections in servers:** they are rare
    there.

### 2026-11-20 — The fusion's startup check deadlocked `py.Main` called from an `init` function on Windows free-threaded builds; its reader now runs on a goroutine's thread

- **Context:** the coordinator's gate 8 on the landed fusion (the entry above). Every tier
  passed except Wine ft, in the fusion-on pass of `py`.
- **Bug:** four tests hung until their 2-minute context killed the child:
  ```
  --- FAIL: TestImageCacheDir (120.01s)
      imagecache_windows_test.go:113: child (image id ""): exit status 1
  ```
  (also `TestImageCacheRemovesStale`, `KeepsImageInUse`, `ConcurrentStarts`). By hand, the
  same child prints nothing with `PYTHAGO_TEST_FUSION=1` and works with 0. Without
  `PYTHAGO_TEST_IMAGE_CACHE_DIR` it works with the fusion on too. That variable is what
  makes the child call `py.Main` from the test file's `init()`.
- **Cause:** `fusionSelfCheck` ran its stale reader as a Go function called from a
  `threading.Thread`.
  - On Windows no Python thread is goroutine-backed. Go makes a callback from a thread
    that runs no Go wait for package initialization (`<-main_init_done`,
    runtime/cgocall.go:422-428 in go1.26.11).
  - `Init` was itself running inside `init()` and waiting for that thread.
  - The GIL build skips the check, and on Linux Python threads are goroutine-backed, which
    is why only Windows ft showed it.
- **Why nothing caught it earlier:** the landing agent ran only the Linux tiers. No Linux
  test can see it, and the Windows image cache tests are the only ones that call `py.Main`
  from `init()`.
- **Fix** (`py/fusion.go`):
  - The reader runs on a goroutine locked to its own OS thread, with its own thread state
    attached through `enter(fn, 0)` (`pythago_py_enter`, no admission slot).
  - The caller waits in `Release`, detached, because creating a thread state may stop
    the world.
  - The reader still runs on a thread that neither freed nor adopted the object, and still
    checks `*owned`.
- **Controls:**
  - The gate 8 binaries (old check) hang 1/1 by hand; the fixed binaries pass all 22
    `TestImageCache|TestFusedBytes` tests under Wine ft, and the whole `py` suite with the
    fusion on and off.
  - The new check still refuses the c1c and c2 glues at `Init`, with
    `startup check: a freed object: a stale reader's incref succeeded
    (_Py_TryIncrefCompare 1, _Py_TryIncrefCompareStackRef 1)`. The runs are in
    `land/ctl-{c1c,c2}-fix`: the final Go code with each control's glue.
- **Carry:** a check that runs inside `Init` must not depend on a Go callback from a
  Python-created thread: `Init` can run before `main`, and on Windows those threads are
  foreign to Go. Use a goroutine's locked thread instead.

### 2026-11-20 — `py.Main` now honors `-X tracemalloc`; `-X dev`, `-X perf` and `-X int_max_str_digits` are still pinned by the isolated config

- **Context:** the user asked for `py.Main` to honor `-X tracemalloc`, after the fusion
  landing found that it never did, fusion or not (section 15, item 6).
- **Cause:** `PyConfig_InitIsolatedConfig` sets `config->tracemalloc = 0`. CPython reads
  `-X tracemalloc` (and `PYTHONTRACEMALLOC`) only while the field is -1
  (Python/initconfig.c:1152, 2336 in 3.14.10). Stock `python -I` doesn't hit this: its
  command-line path starts from a config with -1 and only then applies `-I`.
- **Fix** (`py/runtime.go`, `start`): after `pythago_config_set_argv`, which only `py.Main`
  reaches, py calls `pythago_config_set_tracemalloc(config, -1)`, so the command line
  decides.
  - tracemalloc starts in the main phase, after the fusion's hook (installed between the
    phases).
  - The test-only `initTracemalloc` / `PYTHAGO_TEST_TRACEMALLOC` hook is removed, and
    `TestFusedBytesTracemallocAtInit` uses the real `-X tracemalloc`.
  - `glue/pythago_glue.c`'s comment on the setter was rewritten at the same line count.
    `ci/glue.sh` and `ci/darwin.sh glue` still reproduce `glue/prebuilt/SHA256SUMS`, so
    no prebuilt object changed and no ABI bump is needed.
- **Stock comparison** (python 3.12.3, `-I`):
  - `-X tracemalloc` gives `True 1`, `=7` gives `True 7`, and no flag gives `False 1`;
  - `PYTHONTRACEMALLOC=5` gives `False 1`;
  - `=x` is `Fatal Python error: config_init_tracemalloc: -X tracemalloc=NFRAME: invalid
    number of frames`, exit 1.

  `TestSysExecutableTracemalloc` checks py.Main against all four.
- **Control** (`.agents-workspace/tmp/r5/tmctl`: the call replaced by `_ = nframe`), the
  fusion off and on:
  ```
  --- FAIL: TestFusedBytesTracemallocAtInit
          AssertionError: tracemalloc did not start at initialization
  --- FAIL: TestSysExecutableTracemalloc
      py_test.go:616: AssertionError: (['-X', 'tracemalloc'], 0, 'False 1', '', 'True 1')
  ```
- **Survey of the other `-X` options under `py.Main`** (3.14t test binary):
  - honored: `faulthandler` and `warn_default_encoding`;
  - ignored where stock `-I` honors them: `dev`, `perf` and `int_max_str_digits=640`
    (4300 stays), pinned by the isolated config the same way.

  Recorded in TODO. `-X dev` switches on the debug allocators at preinitialization, so it
  needs a check with the fusion's hook first.
- **Carry:** an option CPython reads only "if unset (-1)" is dead under
  `PyConfig_InitIsolatedConfig`. Reset the field to -1 on the `py.Main` path to let the
  command line decide, and compare with stock `python -I`.

### 2026-11-20 — `py.Main` honors `-X dev` (with the debug allocators), `-X faulthandler` on 3.13, `-X perf`/`perf_jit` and `-X int_max_str_digits`

- **Context:** the user asked for the options the previous entry left ignored. This
  supersedes that entry's "still pinned" survey, which was taken on 3.14t only: 3.13's
  `PyConfig_InitIsolatedConfig` also pins `faulthandler = 0`, so `-X faulthandler` was
  ignored there too.
- **Why the config alone wasn't enough:** dev mode is decided at preinitialization.
  - `core_read_precmdline` (Python/initconfig.c:3325) takes `dev_mode` from the runtime's
    preconfig, unless the config's own is not -1.
  - `_PyPreCmdline_Read` reads `-X dev` only while that value is -1.
  - Preinitialization also picks the allocators: dev mode means `PYMEM_ALLOCATOR_DEBUG`
    (Python/preconfig.c:738). pythago preinitialized before it had the command line.
  - So `-X dev` needs the config reset *and* a preinitialization that reads argv.
- **Fix:**
  - **Glue** (`glue/pythago_glue.c`):
    - `pythago_config_new(argc, argv)`: given a command line, it preinitializes with
      `Py_PreInitializeFromBytesArgs`, `parse_argv = 1` and `dev_mode = -1`; argc 0 keeps
      `Py_PreInitialize`.
    - `pythago_config_set_argv` resets `dev_mode`, `faulthandler`, `tracemalloc`,
      `perf_profiling` and `int_max_str_digits` to -1.
    - `pythago_config_set_tracemalloc` and the Go call to it (previous entry) are gone.
  - **Go:** `initialize` builds argv before the config and passes it to both.
  - **Prebuilt:** all 12 `glue/prebuilt` objects were rebuilt (Linux and Windows here, darwin
    with `ci/darwin.sh glue-update`) and reproduce under `ci/glue.sh` and `ci/darwin.sh
    glue`. The glue ABI stays 5: no shared structure changed, and ABI 5 has not left this
    uncommitted change set.
  - **A clang warning on the first build:** `Py_PreInitializeFromBytesArgs` takes
    `char **`, not `char *const *`, so the parameter now has that type.
- **Probing the debug hooks with the fusion on:** `_PyMem_GetCurrentAllocatorName`
  returns NULL once the fusion's hook replaces the OBJ domain, because it only names
  known combinations.
  - The tests probe the RAW domain instead (`py.DebugHooks`, test-only). In a debug block
    of n bytes, the API id `'r'` is at -8 and `0xfd` is at -1 and at n
    (Objects/obmalloc.c:2805). Under glibc, -1 is the top byte of the chunk size, 0.
- **Stock comparison** (python 3.12.3, `-I`):
  - `-X dev` gives dev_mode True, faulthandler True, `warnoptions ['default']`;
  - `-X int_max_str_digits=640` gives `640 640`;
  - `=10` gives `Fatal Python error: config_init_int_max_str_digits: -X
    int_max_str_digits: invalid limit; must be >= 640 or 0 for unlimited.`, exit 1;
  - `-X perf` activates the trampoline.
- **Tests:**
  - **`TestSysExecutableXOptions`** checks each case and lists every failure together:
    - dev mode;
    - `PYTHONDEVMODE` ignored;
    - `-X faulthandler`, with `PYTHONFAULTHANDLER` ignored;
    - int digits 640 and 0, `PYTHONINTMAXSTRDIGITS` ignored, the invalid limit;
    - `-X perf` and `perf_jit` against a runtime probe of trampoline support
      (`sys.activate_stack_trampoline`), with `PYTHONPERFSUPPORT` ignored.

    It removes the `/tmp/perf-PID.map`, `jit-PID.dump` and `jitted-PID-*.so` files that
    its children leave.
  - **`TestFusedBytesDevMode`:** `-X dev` with the fusion on. Init passes, including the
    free-threaded startup check over the debug hooks. A fused object's free reaches the
    hook, its slot is taken again, and 20k ordinary objects go through the debug hooks.
  - **The trampoline really works on the 3.14t image:** `-X perf` wrote a 236-line
    `/tmp/perf-PID.map` (`py::BufferedIncrementalDecoder.__init__:<frozen codecs>`).
    So `-X perf` isn't passing as False against an unsupported probe.
- **Control** (`.agents-workspace/tmp/r5/xctl`: preinitialization ignores argv, and the
  faulthandler, perf and digits resets are removed):
  - Both builds fail `TestFusedBytesDevMode` (`AssertionError: (False, False)`) and
    `TestSysExecutableXOptions` on `-X dev`, the three digit cases, `-X perf` and
    `-X perf_jit`.
  - `-X faulthandler` fails on 3.13 only, as expected.
  - `TestSysExecutableTracemalloc`, whose path the control leaves alone, still passes.
- **Carry:** an option CPython resolves in the preconfig (dev mode, the allocator) needs
  the command line at preinitialization; resetting the `PyConfig` field alone is
  overridden by the runtime preconfig.

### 2026-11-20 — CI run 44729569405 (e8f2503): three causes, one of them a flaky test; fixed

- **Context:** the user reported CI still failing. Every run since 2026-11-16 had failed.
  In this run 9 of 31 jobs failed: darwin (amd64, ft), unit (ubuntu-24.04, gil and ft,
  lto), and all six native Windows test jobs.
- **Windows (all six): no test ever ran.** `ci/windows-run.ps1` called
  `& ".\$pkg.test.exe" -test.timeout 15m`.
  - PowerShell passes a bare `-test.timeout` to a native command as two arguments, split at
    the dot, so each binary printed its usage:
    ```
    flag provided but not defined: -test
    == failed: py, aio, asgi, asgi:1, wsgi, workers
    ```
  - It now passes one token, `"-test.timeout=15m"`.
  - The Wine tier calls the binaries from bash, which is why it never showed.
- **Unit lto on the amd64 runner: no arm64 image.**
  - `runBao` (cmd/pythago-dist/build.go) took arm64's compiler support library only from
    the pinned LLVM's aarch64 compiler-rt, which `LLVM-23.1.5-Linux-X64` doesn't ship:
    ```
    pythago-dist: no compiler support library (compiler-rt builtins or libgcc.a) found
    ```
  - Shared mode doesn't reach this. The arm64 runner's LLVM has its own compiler-rt.
  - **Fix:** fall back to the sysroot's `usr/lib/gcc/aarch64-linux-gnu/*/libgcc.a`.
    `ci/sysroot.sh` installs `libgcc-13-dev`, and its libgcc has the outline atomics
    (`__aarch64_ldadd4_acq_rel`, `__aarch64_cas8_acq_rel`).
  - **Check** (`.agents-workspace/tmp/r5/ltofb`): with the compiler-rt glob made to miss,
    an arm64 lto 3.13 image built against the CI sysroot links
    `sysroot-arm64/usr/lib/gcc/aarch64-linux-gnu/13/libgcc.a` (bao-build json), and the
    `py` tests pass on it. The CI log is the before-fix control: the same condition
    failed the build.
- **darwin (amd64, ft): `TestIOSQLite` raced a slow runner (4 CPUs, macOS 26.6.1 x86_64).**
  ```
  io_test.go:698: a writer with a 100 ms busy timeout: <nil> &{200 OK ...} {} after 463.891607ms
  io_test.go:703: a writer with a 5 s busy timeout got the held lock after 9.745209ms
  io_test.go:708: after the busy writers: 804 rows, counter 804; want 803
  ```
  - The test slept 150 ms and assumed the `op=hold` request had the write lock by then.
    The holder had not started, so both writers got the lock.
  - **Fix:**
    - the app's hold sets `_holding` (a `threading.Event`) after `BEGIN IMMEDIATE`, and a
      new `op=held` reports it;
    - the test polls that before starting the writers, with a 10 s guard;
    - the hold is 2 s instead of 600 ms, because the 100 ms writer itself took 463 ms here.
  - **Controls** (`.agents-workspace/tmp/r5/sqlrace`):
    - a holder delayed 300 ms before `BEGIN IMMEDIATE` makes the *old* test fail exactly
      as CI did: 200 after 15 ms, then 804 rows against 803;
    - the new test passes with the same delay;
    - with `_holding.set()` removed, the new test stops at `the holder did not take the
      write lock within 10 s`.
- **Carry:**
  - pass a Go test flag from PowerShell as one quoted `-name=value` token;
  - a test that needs another request to be in a state must poll for that state, not
    sleep;
  - a cross build must not assume the host LLVM ships the target's runtime libraries.
- The native Windows jobs have never run the tests before, so their next run is the first
  real one, and it may find new things.

### 2026-11-21 — Real wheels (pyreqwest, pyzmq) under every loop: Do's OS threads could exit under a Python thread, and mimalloc's main heap was then shared (pyreqwest SIGSEGV); fixed in `py`

- **Context:** the user asked for tests of async Python awaiting async functions in native
  extensions, through every runtime path: aio's Go loops, the stock asyncio loop inside
  pythago, several Python threads with a loop each, and asgi apps under concurrent load
  (inline and `LoopThread`). Two PyPI packages, pinned, with wheels for every target:
  - **pyreqwest 0.15.1** (Rust, tokio): its awaitables are `loop.create_future()`
    futures that tokio's own threads complete through `loop.call_soon_threadsafe`, so on
    the Go loops the wake is a Go call from a thread neither Python nor Go created;
  - **pyzmq 27.3.0** (`zmq.asyncio`, libzmq 4.3.5 bundled): Python coroutines woken by
    the ZMQ fd's readiness through `loop.add_reader`.
- **Design:**
  - **`ci/test-wheels.txt`** pins both. **`internal/tools/fetchwheels`** (stdlib only, so
    it builds with `GOWORK=off` on the Macs) reads PyPI's JSON API and picks the target's
    wheel. The order is the ABI's own (cp313-cp313, cp314-cp314t), then abi3 (never for
    free-threaded), then py3-none. Platforms: manylinux up to glibc 2.28 (musllinux never),
    macOS up to 11.0 for the arch or universal2, win_amd64 or win_arm64. It checks
    PyPI's sha256, caches in `$CACHE/wheels/cache`, and writes the selection with a
    SHA256SUMS. pyzmq has no cp313 manylinux wheel, so 3.13 gets cp312-abi3.
  - **`internal/testwheels`** extracts every wheel in `PYTHAGO_TEST_WHEELS` into a new
    temporary site with archive/zip. It keeps modes and the `pyzmq.libs`/`.dylibs`
    layout, and rejects entries that leave the directory, symlinks and duplicates
    (wheels are untrusted). It needs no `*testing.T`, so TestMain calls it from
    `config()` before `py.Init`, and children reuse the site through
    `PYTHAGO_TEST_WHEELS_SITE`. It also holds the Go HTTP backend for pyreqwest
    (`/hello`, `/bytes`, `/echo`, `/slow` held until `/release` or the client goes,
    `/stats`). Unset: the tests skip. Set: a missing or unloadable package fails.
  - **`aio/wheels_test.go`:**
    - the pyreqwest suite, on `asyncio.EventLoop`, `GoEventLoop` and
      `GoSelectorEventLoop`, with tokio's current-thread and multi-threaded runtimes:
      300 concurrent requests, an 8 MiB GET and a 4 MiB POST echo, a cancelled held
      request, a request still held when the loop closes (released after it, then a new
      loop), the client's timeout (`RequestTimeoutError`) and a refused port
      (`ConnectError`);
    - the zmq suite on the loops with `add_reader`: REQ/REP and PUSH/PULL over tcp and
      inproc, 2000 and 4000 messages with backpressure, a poller, and a cancelled and a
      timed-out `recv`;
    - both suites again on 10 and 4 Python threads at once, a loop each;
    - `TestWheelsLoad` asserts the native modules' `__file__` is under the site and, on
      3.14t, that the GIL stays off after the imports.

    Every await has a 60 s bound, so a lost wake fails with a TimeoutError instead of
    hanging.
  - **`asgi/wheels_test.go`:** `wheelsapp`, whose workers each make pyreqwest clients in
    their lifespan, takes 400 requests, 100 at once: fan-outs of 5, 1 MiB bodies, client
    timeouts and cancellations.
  - **Stock comparison is built in:** with `PYTHAGO_TEST_STOCK_PYTHON` (a stock PBS
    python of the image's version and variant), `runWheels` runs the same script under it
    against the same Go server, on asyncio's loops only. A local check, not a CI tier.
  - **CI:**
    - `ci/lib.sh` `test_wheels`, used by `ci/unit.sh` per arch, for both arches;
    - `ci/windows-build.sh` copies the wheels into `run/wheels`, which the artifact
      carries;
    - `ci/windows-wine.sh` passes `PYTHAGO_TEST_WHEELS=wheels`;
    - `ci/windows-run.ps1` sets the absolute path and fails if there are no wheels;
    - `ci/darwin-host.sh` builds and runs fetchwheels on the Mac.
- **Bug 1 (pythago, fixed): pyreqwest crashed on Python threads, both variants.**
  - `TestPyreqwestThreads -test.count 25` crashed in 7 of 12 runs, with only the stock
    asyncio loop as with only the Go loops. The faulthandler stack is `ClientBuilder()`
    in a coroutine on a Python thread:
    ```
    SIGSEGV: segmentation violation
    PC=0xf7d5ffc3753c m=36 sigcode=1 addr=0x18
    signal arrived during cgo execution
      ... _pyreqwest.cpython-314t-aarch64-linux-gnu.so, at +0x41753c
    ```
  - Stock python3.14t ran the same script 200 times with no failure.
  - **Cause:**
    - +0x41753c reads a TLSDESC thread-local (glibc gave it static TLS: descriptor
      argument 0x140) and loads `*(*p + 24)`.
    - A core (apport, `GOTRACEBACK=crash`) shows the crashing thread's slot pointing at
      a `.data` static, `.so+0x7c3dc0`. Its relocated pointers were overwritten with
      zeros and a ChaCha state (`expand 32-byte k`). A routine at +0x421e10 re-zeroes
      these globals and stores `tpidr_el0` in one.
    - This is mimalloc, which pyreqwest bundles (its strings are in the .so).
      `_mi_heap_default` starts at `&_mi_heap_empty`, the main heap has a ChaCha random
      context, and the thread id is the thread pointer. mimalloc takes the thread it
      initialized on as its main thread.
    - **Stock reproduces it:** pyreqwest imported first on a `threading.Thread` that
      exits, then 8 threads building clients, segfaults in 10 of 10 runs; imported on the
      main thread, 0 of 10 (`.agents-workspace/tmp/wheels/scripts/mimalloc_main.py`).
      The exited thread's cached stack, and so its thread pointer, goes to a new thread,
      which then shares the main heap.
    - **In pythago:** the import ran in `py.Do` on a Go M, which Do unlocks afterwards.
      Go later started a Python thread's goroutine on that idle M. `threadStart` keeps
      its OS thread locked, so the thread exited with the Python thread. The glue already
      handled this placement (it drops the thread state Do cached there), and
      `TestMixedThreads` exercises it, but only for shared objects.
  - **Why no test caught it:** no test loaded native code that keeps a thread's identity.
    The exit is invisible to Python.
  - **Fix** (`py/thread.go`, Go only, no glue or ABI change): before running a Python
    thread, the goroutine checks whether its OS thread ran Python outside a Python
    thread: `PyGILState_GetThisThreadState()` is non-NULL (Do's cached state, dropped
    only when a Python thread starts there or at thread exit), or its OS thread ID is in
    `keptThreads`. If so, it unlocks after the Python thread instead of exiting the
    thread. Fresh threads still exit and run their TLS destructors.
  - **Regression test:** `py/threadkeep_linux_test.go` (`TestDoThreadsOutlivePythonThreads`).
    8 goroutines run Do and record their native ids, then 64 Python threads run. The OS
    threads that hosted both must still be in `/proc/self/task` a second later. Reuse
    happens in the first round (1-7 of 8 threads).
  - **Controls** (`.agents-workspace/tmp/r5/wheels/ctl-keep`, `keep := ranPythonBefore()
    && false`):
    - the new test fails 3 of 3:
      ```
      threadkeep_linux_test.go:91: OS thread 2532320 ran Python through Do, then a Python thread, and exited (7 such threads: [...])
      ```
    - `TestPyreqwestThreads` (count 25) crashes in 7 of 12 runs (`addr=0x18`); with the
      fix, 0 of 12 on ft and 0 of 12 on gil.
- **Gap (not fixed, design decision): `GoEventLoop` has no `add_reader`.** It is a
  proactor (`base_events.BaseEventLoop`), as asyncio's on Windows. On it, zmq.asyncio's
  `recv` raises `NotImplementedError` from `_init_io_state`, and so does `Socket.close()`
  from `remove_reader`. ASGI workers run `GoEventLoop`, so an asgi app gets
  `NotImplementedError` too. pyzmq falls back to a selector thread only for
  `asyncio.ProactorEventLoop`, with tornado. `TestZmqGoEventLoopNoAddReader` and asgi's
  `/zmq` check pin it; TODO.
- **Wine arm64 cannot run libzmq:** the first socket stops at `mrs x8, pmccntr_el0` in
  `_zmq.cp314t-win_arm64.pyd+0x3dbb0`, libzmq's clock. Windows lets user code read the
  cycle counter; Linux does not, so Wine reports `Unhandled illegal instruction` and
  starts winedbg, which held the test until its 15-minute timeout. Stock PBS
  `python.exe` under the same Wine (11.21) dies at the same instruction, exit 29. The zmq
  tests skip only under Wine on arm64 (`testwheels.ZmqUnsupported`, through ntdll's
  `wine_get_version`); native Windows runs them.
- **Test bugs the stock comparison caught** (each failed identically under stock):
  - pyreqwest's cancel leaves the request running in tokio: the server never sees the
    client go, not even at `client.close()`. The suite now releases the held request and
    checks that no error reaches the loop's exception handler.
  - The pythago and stock runs share one server, so their tags collided; tags are now
    unique per run.
  - An early probe's `socketserver` backlog of 5 made 50 concurrent connects take 7.6 s
    (SYN retries).
- **Stock comparison** (PBS 20261110, python3.13.18 and 3.14.10t, aarch64): every wheel
  script passes under stock on asyncio's loops, as in pythago. The GIL stays disabled
  after both imports under stock 3.14t and pythago 3.14t (`sys._is_gil_enabled()`
  False), on Linux and on both Macs.
- **Results.** The aio rows cover the three loops and the threads; asgi runs inline and
  with LoopThread.

  | tier | gil | ft |
  |---|---|---|
  | linux/arm64 native (`ci/unit.sh`) | pass | pass |
  | linux/amd64 qemu (`ci/unit.sh`) | pass | pass |
  | windows/arm64 Wine 11.21 (`ci/windows-wine.sh`) | pass, zmq skipped | pass, zmq skipped |
  | darwin/arm64 (dengaku, `ci/darwin.sh`) | pass | pass (verbose run: all six aio wheel tests, asgi both modes) |
  | darwin/amd64 (macbook-pro-2, `ci/darwin.sh`) | pass | pass (verbose run, as arm64) |
  | windows amd64/arm64 native, Server Core | not run locally | not run locally |

  Gate: `.agents-workspace/tmp/r5/wheels-gate`. The first `ci/unit.sh ft` ran alongside
  the five other tiers. It failed only in py's fusion pass:
  - `TestFusedBytesStraddle`: `no fused memory for 1000 bytes`;
  - `TestFusedBytesSweeper`: `chunks waiting for a grace were released without a
    collection`.

  The fusion has no per-thread state. The control copy, which lets threads exit as before,
  failed other fusion tests with `no fused memory` under the same load. Run alone,
  the ft tier passed.
- **Timings** (one ft run, linux/arm64, not a benchmark, host busy):
  - each pyreqwest suite takes 0.36-0.42 s on every loop;
  - `zmq_suite` takes 0.74-0.82 s on stock `SelectorEventLoop` and 1.01-1.33 s on
    `GoSelectorEventLoop`; on the Macs the two are equal (1.70/1.71 s and 3.89/3.92 s);
  - asgi's 400 requests take 0.36-0.50 s.
- **Versions:**
  - Go 1.26.11, bao v0.0.8, PBS 20261110;
  - wheel sha256 (PyPI's): pyreqwest cp313 manylinux aarch64 `f46714c2...`, x86_64
    `8e1f5a82...`, cp314t aarch64 `69297967...`, x86_64 `2ebb47f2...`, macOS arm64
    `b1a1bc20...`/`3c77f439...`, x86_64 `05a744a0...`/`845b00e9...`, win_arm64
    `d2ff95f8...`/`69b33c4f...`, win_amd64 `b254d6ee...`/`87ae0271...`;
  - pyzmq cp312-abi3 manylinux aarch64 `ad8b8284...`, x86_64 `858fe252...`, universal2
    `15155fd8...`, win_arm64 `2187a5ca...`, win_amd64 `3ea8e8fa...`; cp314t aarch64
    `924f8700...`, x86_64 `6dac496c...`, universal2 `d292cedc...`, win_arm64 `2b9f0877...`,
    win_amd64 `8a3d02fc...`. The full digests are fetchwheels' SHA256SUMS.
- **Carry:**
  - An OS thread that ran Python outside a Python thread must not exit while the program
    runs. Native libraries take a thread's identity (often the thread pointer) to mean
    "main thread".
  - Compare against stock with the same script and the same server: three of this
    round's four failures were the test's own.
  - A rare crash in a third-party .so can be traced without symbols. Take the
    faulthandler stack, a core, the TLS descriptor's argument and the static the slot
    points at, then the strings (`expand 32-byte k`, `mimalloc`).

### 2026-11-21 — GoEventLoop readers and writers (add_reader, add_writer); two GoSelector re-arm bugs fixed

- **Request:** the user asked for `add_reader` on `GoEventLoop`. This closes the gap that
  the wheel tests pinned: `zmq.asyncio` raised `NotImplementedError` on `GoEventLoop` and
  in ASGI apps.
- **Design:**
  - `GoEventLoop` lazily keeps a `GoSelector(loop_id)` that holds the selector-loop
    bookkeeping (key data `(reader, writer)` handles, the same logic as `selector_events`).
  - Its Go selector has `loop` set (`aio/readiness.go`, `sel_new(loop)`), so the watchers'
    reports go into the loop's completion queue as `evReady` (token 0, routed to
    `_on_ready`) instead of the selector's own batch. One poll waits for both, and
    other completions pay nothing.
  - The loop's `poll` re-arms the directions it handed out on the previous poll, after
    their callbacks ran, as `sel_poll` does. `close` closes the selector before
    `close_loop`.
  - `sock_*` are still `BaseEventLoop`'s `NotImplementedError`.
- **Two pre-existing GoSelector bugs, found by the new test and fixed for both loops:**
  1. **Double report after `modify`.** Adding a direction to an fd that already had one
     (`add_reader` beside `add_writer`) went through `sel_modify`, which starts the
     watcher and kicks it. The kick was left in the same channel the watcher waits on
     after a report, so it re-checked the level at once and reported again before Python
     consumed the data. The second callback got `BlockingIOError`.
     - Fix: a separate `rearm` channel per direction. Only poll kicks it, plus close.
       `kick` stays for want changes.
  2. **Stale fd after unregister.** A report posted before `remove_reader` came back
     from poll by fd number. If that number was registered again, the new reader was
     called for an unreadable fd.
     - Fix: poll (both `poll` and `sel_poll`) skips events of closed watchers. Unregister
       and poll both run on the loop thread, so the check is exact.
- **Tests:**
  - `aio`'s `TestReadersAndWriters` runs one script on stock `asyncio.SelectorEventLoop`,
    `GoEventLoop` and `GoSelectorEventLoop`. It covers:
    - level-triggered calls, and none before data;
    - file objects and args;
    - replacement, and remove returning True then False;
    - writers, and a reader and a writer on one fd;
    - non-blocking and blocking pipes;
    - a report for an fd unregistered while the loop was blocked, with `dup2` putting an
      unreadable socket at the same fd number. The first version relied on fd reuse and
      failed 1 run in 3 in the full package;
    - close with a reader registered, and `remove_reader` after close.

    asyncio only logs callback exceptions, so the script installs an exception handler
    and fails on any. The first run passed while printing the EAGAIN.
  - The wheel tests now run `zmq_suite` on `GoEventLoop` too (`TestZmqLoops`,
    `TestZmqThreads`). `TestZmqGoEventLoopNoAddReader` is removed.
  - asgi's `/zmq?n=200` (a PUSH/PULL exchange on the worker's loop) joins
    `TestWheelsUnderLoad`'s mix: every 20th request, inline and on the loop thread.
- **Controls** (scratch copies `.agents-workspace/tmp/r5/arctl/c1..c3`, 3 runs each, all
  fail as intended):
  - c1, re-arm back on the kick channel: `('GoEventLoop', [... BlockingIOError(11 ...)])`;
  - c2, no closed check in `poll`: `('GoEventLoop', 'called for an unreadable fd', [], [1])`;
  - c3, no closed check in `sel_poll`: the same for `GoSelectorEventLoop`.
- **Gate 14** (`.agents-workspace/tmp/r5/gate14`, parallel, 9m10s): all 8 tiers pass.
  - The tiers: unit gil and ft (linux/arm64 native, amd64 qemu), Wine gil and ft (zmq
    skipped as before), and darwin arm64 and amd64, gil and ft.
  - The usual noise only: the worker-restart "giving up" lines.
  - One ft run on linux/arm64, host busy: `zmq_suite` takes 0.92 s on `GoEventLoop`,
    0.8 s on stock and 0.9 s on `GoSelectorEventLoop`.
- **Carry:** a test that only calls loop callbacks must make their exceptions fail it.
  asyncio's default handler logs them, and the test passes.

### 2026-11-21 — Type annotations for the Python sources, and the types-pythago stubs (`typing/`)

- **Request:** the user asked for:
  - type annotations on the Python files that stand alone;
  - a set of `.pyi` stubs under `typing/`, to publish as `types-pythago`.
- **Annotated in place, with no behaviour change:**
  - `aio/goaio.py`, `aio/goaio_tls.py`, `aio/channel.py`, `asgi/pythago_asgi.py` and
    `wsgi/pythago_wsgi.py`. Each starts with `from __future__ import annotations`.
    Typing-only imports sit under `TYPE_CHECKING = False` / `if TYPE_CHECKING:`, so
    nothing new is imported at runtime and no typing construct is evaluated.
    - `channel.py` runs once per channel in an injected namespace, so its `_handle` and
      `_release` are declared in that block.
  - The bench, ci and example scripts.
  - Work split: two fork agents (asgi; wsgi and the scripts) and the parent (aio, the
    package, the checks).
- **The no-behaviour-change check** (`.agents-workspace/tmp/typing/samecode.py`):
  - Each file's AST is compared with the original's, with annotations, the `__future__`
    import and the `TYPE_CHECKING` blocks removed. All match except three intended
    rewrites:
    - `ci/gpu/torch_basic.py` and `torch_threads.py` replace list comprehensions over
      `t.start()`/`t.join()` with loops;
    - `bench/*` scripts gain runtime imports of `typing`/`collections.abc` (they run on
      stock CPython).
  - One visible side effect is accepted: `from __future__ import annotations` binds
    `annotations` (a `__future__._Feature`) in each module, and `TYPE_CHECKING` is a
    module name.
- **`typing/`, the stub-only distribution:**
  - PEP 561 `<module>-stubs` packages for `goaio`, `goaio_tls`, `_goaio`, `pythago_asgi`,
    `_pythago_asgi`, `pythago_wsgi` and `_pythago_wsgi`;
  - built with hatchling as `types_pythago-0.1.0` (wheel and sdist);
  - `README.md`, and `mypy.ini` for checking the repo's sources.
  - `goaio.Channel[T]` is `@type_check_only`, since each channel has its own class at
    runtime.
  - `_goaio` types its handles as ints, `_Failure` as `(None, errno, message)`, and the
    TLS spec as a TypedDict that `goaio_tls.go_config` returns.
  - `_pythago`/`_pythago_fs` are not covered (internal to py's bootstrap).
- **The stubs against the live modules:**
  - `typing/stubcheck.py`, embedded by the Go package `typing` (`typing/stubs.go`), is
    run by `TestStubs` in aio, asgi and wsgi, so prebuilt test binaries carry it too.
  - Every stub name must exist at runtime, and every public runtime name must be in a
    stub. `@type_check_only` classes must be absent. Python functions must match the
    stub's parameter names, kinds and defaults.
  - Control (scratch copy): renaming `run`'s `debug`, dropping `_goaio.sel_close` and
    removing `@type_check_only` from `Channel` gave three distinct failures.
  - Its first real run found the `annotations` binding above.
- **`ci/typecheck.sh`:**
  - uv venv, mypy 2.4.1, builds and installs the wheel;
  - checks the stubs, the sources (one run per script, as three share the name
    `summarize.py`) and `typing/tests/usage.py` with `--strict`.
  - Control: a consumer with a wrong return type, a misspelt keyword and a wrong
    callback arity in a fresh venv gave exactly those three errors.
- **`type: ignore`s:** each covers typeshed narrower than CPython or a private asyncio
  name:
  - `SSLProtocol` timeouts typed int, `futures._set_result_unless_cancelled`,
    `protocols._feed_data_to_buffered_proto`, `ssl._ssl._test_decode_cert`,
    `Transport._extra`;
  - `create_server` returning an AbstractServer;
  - `getaddrinfo`'s family and type as given;
  - `windows_utils` on non-Windows (with `unused-ignore`).
  - Private base-class methods typeshed omits (`_check_closed`, `_add_callback`,
    `_fd_to_key`, `_fileobj_lookup`) are declared in class-level `TYPE_CHECKING` blocks.
- **Fork notes:**
  - The wsgi fork deleted 8 gitignored `__pycache__` directories under bench/ci/examples
    that held some older `.pyc` files besides its own. They regenerate.
  - `examples/hello`'s `host` module is ignored in `mypy.ini`.
- **Gate 15** (`.agents-workspace/tmp/r5/gate15`, parallel, 9m08s): all 8 tiers pass.
  - The tiers: unit gil and ft (linux/arm64 native, amd64 qemu), Wine gil and ft, and
    darwin arm64 and amd64, gil and ft.
  - `TestStubs` runs in aio, asgi and wsgi on every tier.
  - Usual noise only.
- **Carry:** `from __future__ import annotations` is a module attribute. A check that
  compares a module's public names with anything must allow for it.

### 2026-11-21 — CI run 44732288299: the first native Windows test runs (connect bursts refused), a darwin timing bound, TLS diagnostics

- **Context:**
  - Run 44732288299 is commit e5922d9, which includes the fix for `-test.timeout` being
    split. So the native Windows jobs ran the tests for the first time. In run 44729569405
    every test binary had stopped at "flag provided but not defined: -test".
  - The unit jobs all pass now. Logs: `.agents-workspace/tmp/ci-44732288299/`.
- **The failures:**
  - **All six Windows jobs:** `ConnectionRefusedError [Errno 10061]` and Go's "actively
    refused" in aio `TestEcho` (`echo(300, ...)`) and asgi `TestInlineConcurrent` (400
    requests at once; 39 refused inline and 192 on the loop thread, of 400).
  - **Only on windows/arm64:** `TestTLS` hit its overall 30 s bound with a bare
    `TimeoutError` (gil and ft), and `TestThreadStateFreedAtThreadExit` timed out once
    (gil).
  - **darwin amd64 ft (GitHub's Intel Mac):** asgi `TestIOProxyConcurrent`,
    `/tproxy?mode=executor`: 16 requests to a 250 ms upstream took 1.08 s, over the
    4 × 250 ms bound.
- **Diagnosis (connect refusals): Windows refuses connects when the accept queue is full.**
  - Microsoft's `listen` documentation: a request that arrives with the queue full gets
    WSAECONNREFUSED.
  - Go's Windows `listen` asks for `SOMAXCONN`, for which the provider picks "a maximum
    reasonable value". A Microsoft SDK post puts that at a few hundred in Winsock 2, and
    only `SOMAXCONN_HINT(N)` sets it, which Go's `net` does not expose.
  - Linux drops the excess SYNs instead, and the client retries. Wine runs on Linux
    sockets, which is why the Wine tiers never saw it.
  - The bursts that failed were 300 and 400 dials. asgi's `TestWheelsUnderLoad` at 100
    at once passed on the same runners.
  - This is not pythago's bug: any Go server on Windows takes a burst the same way.
    Unverified natively, as there is no local Windows host; the next CI run tells.
- **Fixes (tests only):**
  - `TestEcho` runs 150 connections at once on Windows (300 elsewhere).
  - `TestInlineConcurrent` caps the client at 150 connections per host on Windows, so
    requests beyond them wait for a connection.
  - Control: forcing both caps on Linux, the tests still pass 3/3 (inline and loop thread)
    with no deadlock from requests that wait on each other.
  - asgi's `/tproxy` bound is now 6 × delay, as wsgi's is. Serialized, it would take
    n × delay = 4 s.
  - `TestTLS` bounds each scenario at 20 s and names it and the loop in the timeout
    (overall bound now 120 s). Control (scratch copy, a scenario that sleeps on
    GoEventLoop): `TimeoutError: TLS scenario 'hostname_mismatch' on GoEventLoop`.
- **Not fixed:**
  - `TestTLS` on windows/arm64 needs the next run's scenario name. Wine arm64 passes it.
  - `TestThreadStateFreedAtThreadExit` failed once, on arm64 gil only; amd64 and arm64 ft
    passed. On Windows the thread state is freed by a fiber-local-storage callback at
    thread exit (`glue/pythago_win32.h`), which takes the GIL. No cause found from here.
- **Gate 16** (`.agents-workspace/tmp/r5/gate16`): unit gil and ft, and Wine gil, on the
  final tree; see the next entry for the result.

### 2026-11-21 — Gate 16 result (CI 44732288299 test fixes)

- **Gate 16 passed** (9m08s, the final tree): unit gil and ft (linux/arm64 native, amd64
  qemu) and Wine gil, with no failures.
- **Native Windows is unverified:** the Windows-only caps take effect only there, so the
  next CI run is their first real test.

### 2026-11-21 — CI run 44784093416: Windows refusals fixed; a test deadlock pattern; Python threads and Do threads; a crash in bao-emulated TLS on reused thread pointers (open)

- **Run 44784093416 (commit f010b14, this tree):** the Windows amd64 jobs pass now, so the
  connect caps worked. Four jobs fail:
  - darwin amd64 ft: asgi `TestIOProxyConcurrent`;
  - windows arm64 gil and ft: `TestTLS`, now named: 'default' (gil) and 'client_cert'
    (ft), both on GoEventLoop;
  - unit ubuntu-24.04-arm ft shared: SIGSEGV in "py tests, fusion on, ft linux/amd64"
    (qemu), in `TestMixedThreads`.
- **`TestTLS`: the test's echo pattern deadlocks.** The client writes 1 MiB and drains it
  before reading, while the echo server waits for the client to read.
  - On plain TCP that deadlocks on every loop once the payload is bigger than the buffers.
  - Python's ssl layer buffers its way out; Go's crypto/tls applies TCP's flow control.
  - So GoEventLoop's Go-side TLS depended on kernel buffers, and Windows arm64's are
    smaller.
  - Measured on linux/arm64, 32 MiB, write-all-then-read:

    | loop | tcp | tls (Go-side on GoEventLoop) | start_tls (Python ssl) |
    |---|---|---|---|
    | stock | deadlock | ok | ok |
    | GoEventLoop | deadlock | deadlock | ok |
    | GoSelectorEventLoop | deadlock | ok | ok |

    GoEventLoop deadlocked from 8 MiB; stock passed up to 128 MiB.
  - **Fix (tests):** `roundtrip` and aio's `echo_client` read while they write. Control:
    the fixed `roundtrip` at 32 MiB passes on all three loops (the old one deadlocked
    GoEventLoop at 8).
- **`TestIOProxyConcurrent` (asgi): an even-split assumption, and a wall-clock bound.**
  - The executor routes' barrier wanted 16 requests on 2 × min(32, cpu+4) threads. That
    assumes the two workers split them 8/8; on the 4-CPU runner one got 9, and the barrier
    stayed one short.
  - The slow phase took 2.9-3.5 s against a 1.5 s bound.
  - **Fix:**
    - the barrier takes one executor's worth, min(32, cpu+4), which fits however the
      requests split;
    - the slow phase checks the upstream's peak in-flight count (≥ that number: a
      serialized server has 1) and only logs the time.
  - Control (scratch copy, the upstream's `/slow` behind a mutex): "at most 1 at the
    upstream at once, want 16 (took 4.0s)" on every route.
- **The SIGSEGV: reproduced, partly explained, not fixed.**
  - **Reproducer:** the amd64 py test binary under qemu-x86_64 on this host,
    `PYTHAGO_TEST_FUSION=1 -test.run TestMixedThreads -test.count=10`, ft image. Crashing
    runs out of N:

    | build | variant | fusion | crashing runs |
    |---|---|---|---|
    | HEAD (keep: a Do thread that ran a Python thread is unlocked) | ft | on | 3/6 |
    | the same, fusion off | ft | off | 0/6 |
    | the same, GIL (3.13) | gil | on | 0/6 |
    | the same, native arm64, ×40 | ft | on | 0/6 |
    | pre-keep (the thread exits after its Python thread) | ft | on | 0/8 |
    | keep, but the thread parked forever (never reused) | ft | on | 0/8 |
    | keep, the thread waiting in C as a spare for more Python threads | ft | on | 1/5 |
    | new: Python threads never run on Do threads (`runThread`) | ft | on | 1/8 |
  - **What the crashes show:** each one reads a per-thread variable that bao emulates. The
    glue's `__thread` variables and libpython's TLS go through
    `bao_elf_*_tls_lookup` / `__tls_get_addr`.
    - The thread got a garbage block (`current = t` faulting), or another thread's
      values:
      - `owned` read as 1 in CI;
      - `depth` read as nonzero on a fresh goroutine, so `Do` took the nested path without
        `LockOSThread` and left Python on another thread (`PyEval_SaveThread`, tstate
        NULL, addr 0xb0).
    - bao (`linuxelf/native/tls.c`, v0.0.8) keeps a 4096-entry cache keyed by the thread
      pointer, and records that `finalize_thread` releases in the last of glibc's 4
      destructor passes.
  - **Hypothesis, unproven:** something touches bao TLS on an exiting thread after its
    record was released. `current_thread()` then creates and publishes a new record that
    nothing releases. The next thread glibc starts on that cached TCB has the same thread
    pointer, matches the record, and inherits the dead thread's values; when a cache
    collision evicts it, the live thread switches blocks mid-life. That would explain why
    thread churn makes it worse.
  - **Next steps:**
    - read bao's counters (`tls_live_blocks` against the live thread count);
    - find which destructor touches TLS after bao's last pass;
    - take the evidence to the bao agent.
- **Python threads never run on Do threads now** (`py/thread.go` `runThread`). This
  replaces "keep", which unlocked a Do thread after a Python thread ran on it. Rules:
  - a Do thread must not exit (pyreqwest's mimalloc);
  - a thread that ran a Python thread must not be used again (the table: reuse 3/6 and
    1/5, exit or park 0/8).

  A goroutine that lands on a Do thread holds it until another goroutine gets a thread
  that never ran Python.
  - `TestPythonThreadsAvoidDoThreads` replaces `TestDoThreadsOutlivePythonThreads`. It
    checks that no Python thread runs on a Do thread, that Do threads stay alive, and that
    a joined Python thread's OS thread exits. Control: HEAD's `thread.go` fails "a Python
    thread ran on OS thread …, which had run Do".
  - pyreqwest: asgi `TestWheelsUnderLoad` ×25, 10 batches: 0/10 fail. The pre-keep code:
    9/10, all `addr=0x18`.
- **Gate 17:** see the next entry.

### 2026-11-21 — Gate 17 result (CI 44784093416 fixes, `runThread`)

- **Gate 17 passed:** all 8 tiers, with no failures and no SIGSEGV in any log (8m59s).
  - The tiers: unit gil and ft (linux/arm64 native, amd64 qemu), Wine gil and ft, and
    darwin arm64 and amd64, gil and ft.
- **This does not clear the open crash:** it shows in about 1 in 8 runs of the 10×
  reproducer, and a gate runs `TestMixedThreads` once per tier.

### 2026-11-21 — The TestMixedThreads SIGSEGV was the glue's TLS key, not bao: `pythago_py_enter` stored a thread state under key 0 before `owned_key` existed

Corrects "CI run 44784093416 (full matrix)" (its bao hypothesis) and the second rule in
"`runThread`" (a thread that ran a Python thread must not be reused).

- **Context:** the user asked to keep investigating the crash left open there
  (free-threaded 3.14, fusion on, amd64 under qemu-x86_64, `TestMixedThreads` ×10).
- **Bug:** `pythago_py_started` created `owned_key` (`pthread_once`), but Init runs code
  that enters Python on other OS threads before it: with the fusion on and the
  free-threaded build, `fusionSelfCheck` runs `enter` on a goroutine's locked OS thread
  and then unlocks it. `pythago_py_enter` then ran `pthread_setspecific(owned_key, ts)`
  with `owned_key` still 0. Key 0 is bao's TLS key (`bao_tls_configure` creates it first,
  at package init), so that thread's bao record pointer became a `PyThreadState *`. The
  thread went back to Go's pool as a long-lived M. Its TLS kept working while bao's
  lookup cache hit (keyed by thread pointer, 4096 slots); after an eviction, bao's slow
  path (`pthread_getspecific`) took the thread state as the record, and TLS reads and
  writes went to `ts->next` + offset:
  ```
  PC=0xb875f6 m=3 sigcode=1 addr=0x8ef32781607e02dc     (_PyThreadState_Attach, after __tls_get_addr)
  PC=0x18b04f7 m=12 sigcode=1 addr=0xa2aaabc2f3482ee1   (pythago_py_thread_main: current = t)
  Fatal Python error: _PyThreadState_Attach: non-NULL old thread state
  ```
  The victims are low-numbered Ms (m=3 to 12), never fresh threads.
- **Why it needed all of ft, fusion, qemu:** the self-check runs only free-threaded with
  the fusion on; the clobbered thread only fails after a cache eviction of its slot, which
  thread churn makes likelier (hence keep 3/6, `runThread` 1/8, exit 0/8: different churn,
  not different safety). Native arm64 never failed (0 in 240 iterations), probably
  because its thread pointers collide less in the cache (not measured). `-static-tls` images never read the key: 0/18.
- **How it was found** (scratch: `.agents-workspace/tmp/tlstrace`, `nocache`,
  `statictls`, `keepfix`):
  1. An LD_PRELOAD tracer (`tlstrace.c`) wrapped pthread key destructors,
     `__cxa_thread_atexit_impl`, `free`/`realloc`, `pthread_{get,set}specific`. It ruled
     out the earlier hypotheses: no bao record is created after bao's last destructor
     pass (0 in 120 traced iterations, 2 crashes among them), and none is freed.
  2. The crash registers showed `__tls_get_addr` (bao's resolver) returning the garbage
     through its slow path.
  3. `-static-tls` (pythago-dist, a scratch image only): 0/18 runs failing against 3/12
     dynamic, interleaved.
  4. A scratch copy of the test binary with bao's fast paths patched to always miss
     (`nocache/patch.py`: the `JE` after `CMPQ DX, CX` in `bao_tls_lookup` NOPed, the
     thunk's null-entry `JE` made a `JMP`) failed at once; the `getspecific` wrapper then
     showed bao's key holding a pointer bao never created, set by the thread itself from
     `pythago_py_enter` (`FOREIGN setspecific(bao key 0, ...) from 0x18aff86`).
- **Fix** (`glue/pythago_py.c`): `pythago_py_enter` runs `pthread_once(&owned_once,
  owned_key_init)` before it creates a thread state. Every glue object rebuilt
  (`pythago-dist -prebuild-glue`, darwin through `ci/darwin.sh glue-update`; `ci/glue.sh`
  reproduces them). No shared structure changed, so no ABI bump. New `SHA256SUMS` include
  `97baf6698a59…  cp314t-linux_amd64.o`, `01af73346771…  cp314t-linux_arm64.o`,
  `e3732a6b289d…  cp313-linux_amd64.o`.
- **Regression test:** `TestThreadStateKeyNotNativeTLS` (py, Linux; it runs when the
  fusion's check ran: ft with `PYTHAGO_TEST_FUSION=1`) locks 128 goroutines at once,
  which takes Go's idle OS threads, and fails if one has its cached thread state
  (`PyGILState_GetThisThreadState`) under pthread key 0. **Control:** against the
  unfixed glue it fails deterministically, "1 of 1 OS threads with a cached thread state
  hold it under pthread key 0, the native image's TLS key"; fixed, it passes and still
  observes that thread ("1 OS threads with a cached thread state; none under key 0").
- **Why nothing caught it:** no test entered Python before `pythago_py_started`, and a
  wrong key value is silent until bao's cache misses on that one thread. The fusion tier
  under qemu caught it, and only as rare crashes.
- **Measurements** (`TestMixedThreads -test.count=10`, amd64 ft under qemu-x86_64
  8.2.2 on the arm64 host, `PYTHAGO_TEST_FUSION=1`, 6 to 12 parallel runs, interleaved):

  | build | failing runs |
  |---|---|
  | HEAD (keep), unfixed glue | 3/12 dynamic TLS, 0/18 `-static-tls` |
  | HEAD (keep) + fix | 0/6 |
  | `runThread` + fix | 0/6 |
  | cache always missing, unfixed / fixed | 2/3 / 0/3 (count=3) |
- **Consequence for `runThread`:** its second rule, "a thread that ran a Python thread must
  not be used again", rested on this bug's crash rates. With the fix, the reusing (keep)
  design passes too. `runThread` stays (it also keeps Do threads alive for pyreqwest's
  mimalloc); its comment no longer claims reuse crashes. Whether to go back to reuse is in
  TODO.
- **Carry:** glue code reachable before `pythago_py_started` must not depend on state
  that function sets up: Init enters Python on other threads earlier (the fusion check).
  A pthread key variable that is still 0 aliases whatever key 0 is, and in a bao image
  that is the TLS key. When native TLS looks wrong under bao, compare a run with bao's
  cache disabled (binary patch) and a `-static-tls` image before suspecting bao.

### 2026-11-21 — Creating `owned_key` early made Windows children hang at exit: `owned_delete` no longer re-attaches while the runtime is finalizing or gone

Follows "The TestMixedThreads SIGSEGV was the glue's TLS key".

- **Bug (found by gate 18, win-ft, "py tests, fusion on", both the run and its rerun):**
  ```
  --- FAIL: TestSysExecutableXOptions (60.30s)
      py_test.go:645: subprocess.TimeoutExpired: Command '['Z:\\w\\py.test.exe', '-c', ...]' timed out after 60 seconds
  panic: test timed out after 15m0s
  	running tests:
  		TestFusedBytesTracemallocAtInit (13m11s)
  ```
  With the key created on first use, the fusion check's OS thread now really holds its
  thread state in the glue's slot (an FLS slot on Windows, with `owned_dtor` as its
  callback); before, `FlsSetValue(0, ...)` had stored it nowhere useful. A child started
  from `sys.executable` runs `py.Main`, and `Py_RunMain` finalizes without
  `pythago_py_stopping`, so `alive` stays 1. Windows runs the exiting thread's FLS
  callbacks at `ExitProcess`, and `owned_delete` then attached a thread state to a
  finalized runtime: the child hung.
- **Fix:** `owned_delete` also requires `Py_IsInitialized() && !Py_IsFinalizing()`.
  Glue rebuilt for every OS again (`ci/glue.sh` reproduces it), e.g.
  `81c47611e722…  cp314t-linux_amd64.o`, `8c820773d847…  cp314t-windows_arm64.dll`.
- **Control** (Wine, `PYTHAGO_TEST_FUSION=1 py.test.exe -test.run
  'TestFusedBytesTracemallocAtInit|TestSysExecutableXOptions'`):

  | glue | result |
  |---|---|
  | gate 17 (key created late) | pass, 0.95 s |
  | key created early, no guard | `TestSysExecutableXOptions` times out (60 s), 1/1 here and both gate 18 runs |
  | key created early, with the guard | pass, 1.13 s and 1.14 s |

  `TestSysExecutableXOptions` under Wine with the fusion on is the regression test.
- **Why the Linux tiers missed it:** glibc runs no key destructors at process exit.
- **Carry:** a thread-exit destructor that enters Python must check the runtime itself.
  `alive` covers only `py.Finalize`; `py.Main` (`Py_RunMain`) finalizes on its own.
  On Windows, FLS callbacks also run at process exit.

### 2026-11-21 — `runThread` stays: Python threads never reuse a Do thread, and their own OS thread exits at their end

- **Decision (the user's):** keep `py/thread.go` `runThread` rather than return to the
  keep design, which let an OS thread run a Python thread and then Do or another Python
  thread. Both pass since the glue's key fix; the rule that reuse crashes is withdrawn
  (see "The TestMixedThreads SIGSEGV was the glue's TLS key").
- **Why runThread:** Python threads get libc's lifetime (TLS destructors run, and nothing
  sees a thread that already ran a Python thread). The cost is one goroutine hop when Go
  starts a Python thread's goroutine on an idle Do thread.
- **Carry:** Do threads must still never exit (pyreqwest's mimalloc main heap, keyed by
  thread pointer). `TestPythonThreadsAvoidDoThreads` holds both properties.

### 2026-11-21 — CI 44853895032: `TestThreadStateFreedAtThreadExit` failed again on Windows; not reproduced, so the test now diagnoses itself and keeps off the main thread

- **Context:** the user reported job 136691788416 (windows amd64 Server Core, ft) of run
  44853895032 (commit 16330e0); every other job of the run passed.
  ```
  == py tests
  --- FAIL: TestThreadStateFreedAtThreadExit (5.00s)
      py_test.go:526: timed out waiting for the exited thread's thread state to be deleted
  ```
  The second occurrence, both on Windows (the first: windows/arm64 gil, run 44732288299).
  On Windows the thread state is deleted by the glue's fiber-local slot callback
  (`owned_dtor`) when the OS thread exits.
- **Not reproduced:**
  - 300 runs of the test alone under Wine (arm64 ft, fixed glue).
  - 800 runs natively (linux/arm64 ft, GOMAXPROCS 1 and 2) of a scratch copy recording
    whether the goroutine ran on the main thread; it never did.
- **One real hole:** Go wedges the main thread instead of exiting it (`runtime.mexit`,
  "This is the main thread. Just wedge it."). A goroutine that lands there never ends its
  thread, and the test then waits for nothing. Unproven as this failure's cause.
- **Change** (`py/py_test.go`, with the user's permission, since it is Codex's file;
  `py/threadalive_{linux,windows,darwin}_test.go`):
  - a goroutine on the main thread (`mainThreadID`, recorded at package init, which runs
    there) is held until the test ends, and the next attempt runs on another thread;
  - the wait is 30 s; a deletion slower than 5 s prints a line on stderr (go test hides
    `t.Log` of passing tests);
  - a failure reports the OS thread id, whether it still exists (Linux `/proc/self/task`,
    Windows Toolhelp, darwin unknown) and the created/deleted counters.
- **Control** (scratch copy, the goroutine unlocks its OS thread so it goes back to Go's
  pool instead of exiting):
  ```
  --- FAIL: TestThreadStateFreedAtThreadExit (30.00s)
      py_test.go:557: OS thread 606722's thread state was not deleted 30 s after its goroutine exited (thread still exists: yes; thread states created 0 -> 1, deleted 0 -> 0)
  ```
- **Carry:** the next Windows failure or stderr line tells the cases apart:
  - slow (deleted after 5 s);
  - the thread never exited, or its callback is stuck ("still exists: yes");
  - the callback never ran or skipped the deletion ("no").

### 2026-11-21 — CI: Linux jobs on Ubuntu 26.04 runners; Windows Server Core runs the GIL variant only

- **Context:** the user asked whether the Server Core jobs are needed. They run the
  windows-2025 jobs' binaries on the same host build, inside `servercore:ltsc2025`. In the
  journal, no failure is recorded that only they caught, and they take about 7 minutes
  against 1.7 (the image pull), so I recommended keeping one variant as a smoke check.
  The user chose to drop the ft one, and to move the Ubuntu runners to 26.04.
- **Changes** (`.github/workflows/ci.yml`, README):
  - the `unit`, `glue` and `windows-build` jobs run on `ubuntu-26.04` and
    `ubuntu-26.04-arm` (generally available since GitHub's 2026-09-17 changelog);
  - the unit job installs `qemu-user` as well as `qemu-user-binfmt`, which on 26.04 (QEMU
    10.2) carries only the binfmt registration;
  - the `windows` matrix excludes ft on Server Core.
- **Not verified locally:** the workflow parses as YAML, but there is no runner here
  (this host is 24.04). The cache keys include the runner name, so the first run starts
  cold.
- **Gate 20** (`TestThreadStateFreedAtThreadExit` diagnostics): 7 tiers passed. Wine ft
  failed aio `TestPyreqwestThreads` once, with GoSelectorEventLoop, multithreaded, thread 5:
  ```
  pyreqwest.exceptions.DecodeError: ('error decoding response', {'causes': [... {'message': 'invalid HTTP version parsed'}]})
  ```
  It passed 30 of 30 runs on the same build. Neither aio nor the glue changed since gate
  19, whose Wine ft tier passed: an existing rare flake, not chased further.

### 2026-11-21 — CI 44856520533: workers `TestSpawn` on windows/arm64 lost a request to the worker it had just killed; it now waits for the worker to be gone

- **Context:** the user reported job 136700221765 (windows arm64, ft) as flaky and
  recurring:
  ```
  workers: worker 0 exited (exit status 1) after 23ms; restarting
  --- FAIL: TestSpawn (0.04s)
      workers_test.go:114: request: Get "http://127.0.0.1:60938/": read tcp 127.0.0.1:60971->127.0.0.1:60938: wsarecv: An existing connection was forcibly closed by the remote host.
  ```
- **What "exit status 1" was:** the test's own kill. Windows `os.Process.Kill` is
  `TerminateProcess(h, 1)`; the victim had lived 23 ms. It was not a worker failing to
  start, which is why no message from the worker appears. The later "exit status 3" lines
  are `TestSpawnGivesUp`'s workers, which exit at once on purpose.
- **Cause (test):** `TerminateProcess` returns before the process is gone. Until then,
  the victim's pending AcceptEx can take a connection from the shared accept queue, and
  the process's exit resets it. The test began its "every request must succeed" phase
  right after `Kill`.
- **Fix:** after `Kill`, `TestSpawn` waits (up to 10 s) until `alive(victim)` is false.
  On Windows that means the process object is signaled, after its handles (and the
  pending AcceptEx) are closed; on Unix, that Spawn has reaped it.
- **Control: none that fails.** The race never showed under Wine: 40/40 `TestSpawn` runs
  before the change (the "exit status 1" lines there are the kills too), and 20/20 whole
  workers runs after it. The next native Windows run is the check.
- **Carry:**
  - on Windows, "exit status 1" from a worker the test or the supervisor killed is the
    kill itself;
  - a test that kills a process sharing a listener must wait for the process to be gone
    before it counts lost connections;
  - Go's Windows `Accept` also retries AcceptEx on `ERROR_NETNAME_DELETED` and
    `WSAECONNRESET` (`internal/poll` `acceptOne`), which `workers`' own `socketListener`
    does not; not seen failing, noted in TODO.

### 2026-11-21 — CI no longer runs the other architecture under qemu: it cross-builds it only (`PYTHAGO_OTHER_ARCH=build`)

- **Context:** the user asked why the Linux jobs use qemu. `ci/unit.sh` was written for
  the arm64-only development host and adds the other architecture whenever qemu-user is
  installed, so every CI job re-ran the other runner's tests under emulation. In run
  44853895032, `unit (ubuntu-24.04, ft, shared)` spent about 1.5 min on its native tests
  and about 5 min on arm64 under qemu. What the qemu leg adds:
  - cross-building, which building alone covers;
  - odd thread timing, which once exposed a real bug (the glue's TLS key, found only
    under qemu-x86_64).

  The user agreed to drop the runs and keep the cross-build.
- **Change:**
  - `ci/lib.sh` `other_arch_mode`: `PYTHAGO_OTHER_ARCH` is `run` (the old behaviour, and
    the default when qemu-user can run the other architecture), `build` or `none`. A bad
    value, or `run` without qemu, fails the tier.
  - `ci/unit.sh`, in `build` mode: generates the other architecture's image and builds
    its py, aio, asgi, wsgi and workers test binaries and examples/hello (gil), runs none.
  - `ci/floor.sh`: the symbol-version checks still cover both architectures; the
    old-glibc runs of the other one are skipped unless `run`.
  - The unit job sets `PYTHAGO_OTHER_ARCH: build` and no longer installs qemu-user.
    README says how to override it.
- **Checked** (scratch copy, linux/arm64 host): `PYTHAGO_OTHER_ARCH=build ci/unit.sh gil`
  passes, "unit tier (gil) passed: arm64 amd64 (linux/amd64: build)", with the amd64 image
  and binaries built and nothing run. `ci/floor.sh` passes too: amd64's symbol versions
  are checked, its old-glibc runs skipped. `PYTHAGO_OTHER_ARCH=bogus` stops with "must be
  run, build or none". Unset, the mode here is still `run`.
- **Carry:**
  - local gates keep the qemu runs (this host has no amd64);
  - a failure seen only in local qemu runs now has no CI counterpart: reproduce it here,
    as the TLS-key bug was.
