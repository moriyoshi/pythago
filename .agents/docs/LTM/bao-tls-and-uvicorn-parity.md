# bao TLS and uvicorn Parity

## Summary
uvicorn run on pythago was 29% slower than the same CPython build run stock. Half of
all instructions were in bao's thread-local-storage thunk, which ran on every
`_Py_tss_tstate` read. bao PR #32 (a fast path) and #33 (opt-in glibc static TLS) brought
pythago to stock speed and slightly past it. Static TLS also exposed a dlopen name
collision that pythago's extension shim now guards against.

## Key Facts
- **Symptom:** equal function-call speed, but allocation-heavy code 2–3× slower than stock
  (`str(i)` churn 35.4 → 102.9 ms). That pattern points at a per-access runtime cost, not
  the eval loop or the allocator.
- **Cause:** callgrind showed 50.2% of instructions in TLS lookup:
  `bao_elf_*_tls_thunk` 20.9%, `tls_lookup_offset` 10.4%, `current_thread` 8.5%,
  `pthread_getspecific` 6.3%, `tls_lookup` 4.1%.
- **The old thunk** (bao `linuxelf/native/thunk.S`) saved and restored q0–q31 and x1–x18
  (672 bytes) per access, then called `bao_tls_lookup_offset` (atomic load,
  `pthread_getspecific`, bounds check). glibc's static-TLS descriptor is
  `ldr x0, [x0, #8]; ret`.
- **bao #32** (`linux-tls-fast-path`, `3b1d95e`): published per-thread records; on a hit
  the thunk spills only x1–x3. Default. About 32 instructions per access.
- **bao #33** (`linux-static-tls`, `d209874`): glibc static TLS through a memfd stub DSO.
  Opt-in: `pythago-dist -static-tls` (bao `-linux-static-tls`). The generated package's
  `TLSStatic()` reports whether it took effect. Falls back to bao's per-thread blocks
  when glibc has too little static TLS left.
- **Result (req/s, one core, 64 connections):** asyncio 39.7k → 52.5k (#32) → 56.1k (#33)
  vs stock 55.9k; uvloop 48.8k → 72.7k → 75.1k vs stock 71.8k.
- **Static TLS broke every C extension** until `py/internal/extshim` stopped reusing a
  `/proc/self/fd/N` path that glibc already had loaded under that name.

## Details

### Benchmark setup (`bench/uvicorn/run.sh`)
- **Server:** uvicorn 0.54.0, one worker, framework-free ASGI app `bench/uvicorn/app.py`
  (`/` small JSON, `/big` 64 KiB, `/sleep` 1 ms). Pinned to one Cortex-X925 core
  (`BENCH_SERVER_CPU=5`, or `5,6` for two).
- **Load:** `internal/tools/httpload`, a keep-alive HTTP/1.1 generator, on cores 15–19 with
  `GOMAXPROCS=5`. 2 s warm-up, 8 s measured.
- **Stock:** the venv's PBS python3.13 (3.13.18, release 20261110), the same build as the
  image. uvloop 0.23.0, httptools 0.9.0.
- **pythago:** `pyrun --python -m uvicorn` on the shared-mode 3.13 image.

### The gap before the fixes (req/s, 64 connections)

| | 1 core | 2 cores |
|---|---|---|
| stock asyncio + httptools | 55.6k | 55.3k |
| stock uvloop + httptools | 72.1k | 72.9k |
| pythago asyncio + httptools | 39.7k | 39.5k |
| pythago uvloop + httptools | 48.8k | 48.6k |
| pythago GoEventLoop + httptools | 28.1k | 51.5k |

A second core did nothing for pythago on stock loops, so the Go runtime competing for the
core was not the cause.

### How it was localized
1. **Microbenchmarks against the same stock interpreter** (one core, ms):

   | | stock | pythago |
   |---|---|---|
   | `fib(27)` | 15.2 | 14.2 |
   | `str(i)` churn, 1M | 35.4 | 102.9 |
   | `{}` churn | 15.3 | 43.9 |
   | tuple churn | 18.5 | 48.6 |
   | json 20k round trips | 231.6 | 462.8 |
   | socketpair round trip, 100k | 60.4 | 85.4 |

   Function calls were equal; allocation and syscalls were not.
2. **Ruled out:**
   - the allocator: both use pymalloc (`_testinternalcapi.pymem_getallocatorsname()`);
   - the GC: `gc.disable()` changed nothing;
   - shared mode: the LTO image was just as slow;
   - `PYTHONMALLOC=malloc` measured nothing, because the isolated config ignores the
     environment.
3. **Profile:** `valgrind --tool=callgrind ~/.cache/pythago-ci/bin/pyrun alloc.py` on
   `for i in range(200000): x = str(i)`, 675,014,041 instructions:

   ```text
   141,009,238 (20.89%)  bao_elf_c07b46de5584_tls_thunk
    70,043,094 (10.38%)  bao_elf_c07b46de5584_tls_lookup_offset
    57,307,977 ( 8.49%)  current_thread
    42,450,297 ( 6.29%)  pthread_getspecific@@GLIBC_2.34
    27,592,734 ( 4.09%)  bao_elf_c07b46de5584_tls_lookup
   ```

4. **Caller tree:** about 6.5 thunk calls per iteration, from the eval loop (673k), an
   inlined helper (413k) and `PyDict_SetItem` (203k). `_PyObject_Malloc` accounted for
   only 3.7k, so switching allocators could not help.

The finding went to the bao agent with two proposals: glibc static TLS through a stub DSO,
and a fast path that skips the register spill. Both landed. (Process: see
[process-lessons.md](process-lessons.md); bao is never edited from this repo.)

### bao #32: the fast path (default)
- Per-thread records are published; on a hit the thunk spills only x1–x3 and computes
  the address itself. The first access on a thread takes the old slow path.
- callgrind: 675M → 452M instructions (stock 321M). The thunk fell to 9.4%.
- amd64 under qemu (emulated timings): `str(i)` churn 496 → 398 ms.

### bao #33: glibc static TLS (opt-in)
- The generated package dlopens a memfd stub DSO whose PT_TLS is the image's TLS
  template. glibc allocates it in static TLS, and TLS descriptors then get the
  two-instruction static resolver.
- The 3.13 libpython template is 64 bytes with alignment 8; `TLSStatic()` is true for it.
- callgrind: 403M instructions; the thunk is gone from the profile.
- **Fallback:** when glibc has too little static TLS left for the template, bao falls
  back to its own per-thread blocks (the #32 path), and `TLSStatic()` is false. Check
  `TLSStatic()` before attributing a measurement to static TLS.
- Enable with `pythago-dist -static-tls` (`cmd/pythago-dist/main.go`, sets
  `bo.Linux.StaticTLS`). For test images, `ci/lib.sh` passes `PYTHAGO_DIST_ARGS`
  (e.g. `PYTHAGO_DIST_ARGS=-static-tls`). It stays opt-in by the user's decision.

### Microbenchmarks across the fixes (ms, one core)

| | before | #32 | #33 | stock |
|---|---|---|---|---|
| `str(i)` churn | 104.6 | 46.2 | 38.8 | 35.9 |
| `{}` churn | 44.4 | 19.7 | | 15.5 |
| json x20k | 470 | 272 | 246 | 232 |
| dict/str | | 163 | 139 | 143 |
| socketpair round trip | 85.3 | 74.8 | 69.7 | 60.1 |

### uvicorn after the fixes (req/s, 64 connections; stock in brackets)

| | before | #32 | #33 static TLS |
|---|---|---|---|
| asyncio, 1 core | 39.7k | 52.5k [55.9k] | **56.1k** [55.9k], p99 1.17 ms |
| uvloop, 1 core | 48.8k | 72.7k [72.8k] | **75.1k** [71.8k] |
| asyncio, 2 cores | 39.5k | 55.0k [55.3k] | 56.0k [55.2k] |
| GoEventLoop, 2 cores | 51.5k | 73.7k | 70.8k [uvloop 72.5k] |

- With #32 only, pythago on asyncio had p99 4.2 ms on one core; static TLS removed that
  tail.
- `GoEventLoop` on two cores serves 64 KiB responses at 4059–4112 MB/s against 3224 for
  uvloop (see [aio-event-loop.md](aio-event-loop.md)).

### The dlopen name collision static TLS exposed
- With static TLS, every C extension (httptools, uvloop) failed:

  ```text
  ImportError: .../httptools/parser/parser.cpython-313-aarch64-linux-gnu.so: undefined symbol: PyExc_SystemError
  ```

- Mechanism:
  - bao dlopened its stub as `/proc/self/fd/N` and closed N;
  - the extension shim's memfd got the same N and dlopened the same path string;
  - glibc matches loaded objects by **name** before opening the file, so it returned
    bao's stub, loaded `RTLD_NODELETE`.
- **pythago's fix** (`py/internal/extshim/extshim.go`):
  - probe with `RTLD_NOLOAD`; while the path names an already loaded object, move the
    memfd to the next fd number with `F_DUPFD_CLOEXEC`;
  - after the dlopen, `dlsym` of the first shim symbol must succeed.
- **bao's fix** (b991cc8): keep the memfd open, probe with `RTLD_NOLOAD`, and check the
  link map's `l_ld`.
- The unit tiers had missed it because no test loaded a C extension through the shim.

## Files
- `bench/uvicorn/run.sh`, `bench/uvicorn/app.py`: the uvicorn benchmark and its app.
- `internal/tools/httpload`: keep-alive HTTP/1.1 load generator.
- `internal/tools/pyrun`: runs Python (`pyrun --python -m uvicorn`) on an image.
- `cmd/pythago-dist/main.go`, `cmd/pythago-dist/build.go`: the `-static-tls` flag.
- `ci/lib.sh`: `PYTHAGO_DIST_ARGS` for test images.
- `py/internal/extshim/extshim.go`: the extension shim's memfd dlopen with the
  `RTLD_NOLOAD` probe and `F_DUPFD_CLOEXEC` move.
- bao (not in this repo): `linuxelf/native/thunk.S`, branches `linux-tls-fast-path`
  (#32), `linux-static-tls` (#33).

## Test Coverage
- `TestExtensionShim` (`py/py_test.go`) resolves `PyExc_SystemError` through
  `ctypes.CDLL(None)` (global scope, where an extension's undefined symbols are resolved).
  With the extshim fix reverted it fails with exactly the `undefined symbol` error.
- To exercise static TLS in the unit tiers:
  `PYTHAGO_DIST_ARGS=-static-tls ci/unit.sh gil` (and `ft`).
- Benchmarks: `bench/uvicorn/run.sh` (optionally with a filter, e.g.
  `bench/uvicorn/run.sh 'asgi/net-http|stock/uvloop/httptools'`).

## Pitfalls
- **Compare against the same build.** Stock here is the identical PBS 3.13.18 release.
  Anything else mixes compiler and configuration differences into the result.
- **callgrind on a Go binary** can die with
  `Assertion 'sigNum == vgCallgrind_current_state.sig' failed` in `runtime.sigtramp`.
  Run with `GODEBUG=asyncpreemptoff=1`.
- **Function-level callgrind comparisons against stock are unreliable.** The `.so` is
  stripped, so static functions are charged to the preceding export (the eval loop and
  `PyDict_SetItem` traded places between profiles). Compare totals.
- **`PYTHONMALLOC` and similar environment variables are ignored** by the isolated config.
- **Rerun benchmarks after any stack change** (bao version, static TLS). Numbers from
  before #32 are no longer representative.
- **Never reuse a `/proc/self/fd/N` path for dlopen without an `RTLD_NOLOAD` probe.**
  glibc dedups by name, and a closed-and-reused fd number aliases another object.
- **A new loading mechanism needs a test that loads a real C extension** and resolves an
  extension-visible symbol through global scope.
