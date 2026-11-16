# The Go↔C Boundary (FFI)

## Summary
pythago calls CPython from Go with no cgo: Go→C calls go through `runtime.cgocall` and a
small assembly thunk, and Python→Go calls come back through `crosscall2` into one Go
dispatcher. Static C glue (`glue/pythago_py.c`) batches work on the C side: containers and
call arguments are built in one C call from tagged values, and a Go function's arguments
arrive already decoded. This document is the reference for that boundary, its rules
against stack memory, its ABI check, and its measured costs. The general rules are also
listed in [embedding-cpython.md](embedding-cpython.md); the profiling method is in
[performance.md](performance.md).

## Key Facts
- **Go→C:** `call0`…`call8` (`py/ccall.go`) build a `cframe {fn, args[8], ret}` on the
  goroutine stack and run `runtime.cgocall(pythago_cthunk, &frame)`. 61.4 → 32.7 ns per
  empty call, 0 allocations.
- **Python→Go:** `pythago_py_fastcall` (C) decodes the function index, keyword count and
  up to 8 arguments, then calls `crosscall2(dispatchFrame, &frame, …)`. No purego
  reflection. 561 → 112.7 ns per `echo(x)`.
- **Every Go wrapper that forwards `uintptr` arguments to C must be
  `//go:uintptrescapes`.** Without it an out-parameter stays on the stack, and C writes
  to the old copy after a stack move (`[42, '']`).
- **C must never read or write goroutine-stack memory.** The pragma covers only pointers
  converted in the call expression itself. Check with `go build -gcflags=-m` that every
  buffer handed to C escapes. `cString` is `//go:noinline` for this reason.
- **A variadic `//go:uintptrescapes` function heap-allocates its argument array.** Hot
  paths use the fixed-arity `call0`…`call8`, and C-string calls the fixed-arity
  `callStr0`/`callStr1` (one allocation: the C string); variadic `call` remains for cold
  code. `TestCCallSitesAllocateOnlyTheirResults` counts the allocations per call site; it
  also fails if `cString` is inlined (one allocation fewer), since its buffer must be on
  the heap.
- **One C call per container or call:** `pythago_py_build` (tuple, list, dict) and
  `pythago_py_call` (vectorcall) take arrays of `struct pythago_value` (`pyValue` in Go).
- **Glue ABI check:** `pythago_py_abi()` returns `PYTHAGO_PY_ABI`; `py` compares it with
  `glueABI` at Init. Both are currently **3**. Change them together whenever `callFrame`
  or `pyValue` changes layout.
- **None/True/False results** come back as three shared, borrowed, immortal `*Object`s
  (`singletons` in `py/object.go`): no allocation, cleanup or C call.
- **Zero-copy arrays:** Python→Go through `(*Object).Buffer`/`View[T]`/`WithView[T]`;
  Go→Python through `NewBuffer[T]` (a `pythago.GoBuffer` heap type, Go memory pinned).
- **Go→C calls per ASGI `GET /` (64 connections):** 58 → 16 (container builder) → 6
  (decoded arguments).

## Details

### Go→C calls: `runtime.cgocall` and an assembly thunk
Files: `py/ccall.go`, `py/ccall_amd64.s`, `py/ccall_arm64.s`.

- `runtime.cgocall` is pushed with `//go:linkname` (the linker's "hall of shame" list
  names purego), so any package may link to it. `py` declares it as `runtimeCgocall`.
- `cframe` is `{fn uintptr; args [maxCArgs]uintptr; ret uintptr}`, `maxCArgs = 8`.
- `pythago_cthunk` loads the arguments (all in registers on arm64; the last two on the
  stack on amd64, which has six integer argument registers) and calls `fn`.
- **The result is stored relative to the goroutine stack top after the call**
  (`_cgo_topofstack`, as cgo's generated wrappers do). A Go callback during the C call can
  grow and move the stack, and with it the frame.
- Compared with `purego.SyscallN`, this skips the pooled argument block, the
  floating-point register shuffling and the variadic marshalling.
- Call sites were rewritten mechanically with a `go/ast` tool (`call(fn, a, b)` →
  `call2(fn, a, b)`).
- **Rejected designs:**
  - a heap-allocated frame: 49 ns and one allocation per call;
  - calling C without `cgocall`: `runtime.asmcgocall` is not pushed, so the linker
    refuses it.
- purego remains for a few cold spots: `purego.RegisterFunc` for
  `PyFloat_FromDouble`/`PyFloat_AsDouble` (`py/capi.go`), `purego.NewCallback` for the
  thread-start callback (`py/runtime.go`), and `purego.Dlopen` in `py/internal/extshim`.

#### How the stack-resident `cString` buffer was isolated
The faster thunk made `TestGoroutines` (2000 goroutines × 5 `Do`+`Exec`) fail in 5 of 6
full runs; the failing call was `callStr(cImportAddModuleRef, "__main__")`. Go's view of
the string after the call was intact; Python had read different bytes. Stress loop,
`TestGoroutines` alone, 10–25 runs per variant:

| Variant | Failing runs |
|---|---|
| purego | 0/25 |
| thunk, stack frame, `_cgo_topofstack` | about 1/3 |
| thunk, stack frame, no `_cgo_topofstack`, no delay | 0/10 |
| thunk, stack frame, an 80-iteration delay loop instead | 3/12 |
| thunk, heap frame, same delay | 0/12 |
| thunk with `_cgo_topofstack`, `cString` on the heap | 0/15, full suite 5/5 |

Instrumentation ruled out the thunk's own writes: the relocated result address always
equalled the frame's, the argument words never changed during the delay window or the
call, and the stack never moved during any call in this test (`_cgo_topofstack` before =
after). The failure depends on frame layout and timing. `-gcflags=-m` showed
`capi.go:157: make([]byte, len(s) + 1) does not escape` before the fix.

Root cause (established 2026-11-18): the goroutine's stack grew *before* the C call,
after the buffer's address had been taken as a uintptr. The old variadic `callStr`'s
`append([]uintptr{u}, args...)` reached `growslice → mallocgc`, whose slow paths
(`mspan.nextFreeIndex`, `uncacheSpan`, GC assists) ran morestack on a 2 KiB initial
stack: `newstack → copystack(2 → 4 KiB) → stackfree(old)` put the old stack at the head
of the P's stack cache, the next goroutine took it, and C read that goroutine's frame
words (pointer bytes like `0xce`). That is why `_cgo_topofstack` never changed during
the call and Go's own view stayed intact. Stack shrinking plays no part
(`gcshrinkstackoff=1` changes nothing; pre-growing the stack removes it). A Go callback
during the C call is a second window with the same effect: the callback runs on the
calling goroutine's stack, which may then be copied. While the goroutine is in C its
stack cannot move (`copystack` throws with `syscallsp != 0`). Reproducers, made
deterministic with `GODEBUG=efence=1` or a goroutine scribbling 0xAB over freed
stacks: `.agents-workspace/tmp/r3/stackbuf/`. The current `callStr0`/`callStr1`
have no Go call in the window, but `cString` stays `//go:noinline`: it is the only
site whose safety depends on escape analysis.

### Python→Go calls: crosscall2 and a C-decoded frame
Files: `glue/pythago_py.c` (`pythago_py_fastcall`, `decode`, `struct pythago_call`),
`py/func.go` (`callFrame`, `argSlot`, `dispatchFrame`, `dispatch`, `setCallback`).

- Every Go-defined builtin has `ml_meth = pythago_py_fastcall` (METH_FASTCALL |
  METH_KEYWORDS). A PyLong index in `self` selects the Go function, so one dispatcher
  serves all of them.
- C decodes the index (`PyLong_AsSsize_t(self)`) and the keyword count, fills
  `struct pythago_call` (mirrored by Go's `callFrame`), and calls
  `crosscall2(dispatchFrame, &frame, …)`. `crosscall2` is the entry cgo-exported functions
  use; purego's fakecgo provides it.
- `dispatchFrame`'s ABIInternal entry is read from a func value (`dispatchFrameFn`), as
  purego does for `callbackWrap`, so no reflection is involved.
- `pythago_py_set_callback(crosscall2, entry)` installs it at Init. Its name replaced
  `pythago_py_set_dispatch`, so a pre-crosscall2 glue image fails with
  `pythago_py_set_callback is missing from pythago_symtab`.
- **Returned references:** Go returns `borrowed = 1` for None, booleans and returned
  arguments; C takes the new reference itself. A nil `*Object` result returns None
  (before crosscall2 it panicked inside `dispatch` and raised RuntimeError).

#### Decoded arguments
- `pythago_py_fastcall` describes the first `PYTHAGO_DECODED` (8) arguments (positional,
  then keyword) and the first `PYTHAGO_NAMES` (4) keyword names as `struct pythago_value`.
- It decodes only what reads without side effects and cannot fail:
  - None, True, False;
  - exact ints that fit in int64;
  - exact floats;
  - exact bytes (address and length);
  - exact ASCII str (its UTF-8 form is its storage).
- Everything else (subclasses, overflowing ints, non-ASCII str, more than 8 arguments)
  stays `PV_OBJECT`, and Go uses the C API as before.
- A call's borrowed arguments are one allocation of `argSlot{Object, pyValue}`;
  `Object.pre` points at the decoded value. `Int64`, `Float64`, `GoString`, `Bytes`,
  `UnsafeBytes`, `Truth`, `IsString` and `Go` answer from it without calling C.
- `NewRef` returns an Object without `pre`, so nothing decoded outlives the call.
- Decoded payloads point into the argument object. They are valid while it is, the same
  contract as `UnsafeBytes`.
- **Rejected:** a `pyValue` inside every `Object` (24 more bytes on every object, not just
  arguments).

### The container builder: `pythago_py_build`
Files: `glue/pythago_py.c` (`pythago_py_build`, `value_new`, `values_release`),
`py/convert.go` (`pyValue`, `build`, `setValue`, `buildPool`).

- **Why:** counting Go→C calls by C function showed refcounts were only 5 of 58 calls per
  ASGI request. 45 were item-by-item tuple building from `py.From`'s `sequence`:
  `PyTuple_SetItem` 22, `PyUnicode_FromStringAndSize` 7, `PyLong_FromLongLong` 5,
  `PyTuple_New` 4, `PyBytes_FromStringAndSize` 4, and `Py_IncRef` 4 for None/True/False.
  Inline refcount fast paths (writing the object layout from Go) would have saved about
  150 ns per request (0.7% of a one-core request) and were set aside. Most decrements in
  the `Do` cases are probably final releases of temporaries, which must still call
  `Py_DecRef` anyway.
- **Tagged values:** each item is a `pyValue` with tags `pvNone`, `pvTrue`, `pvFalse`,
  int, uint, float (its bits), str and bytes up to 256 bytes (copied into one arena), a
  borrowed `*Object`, or `PV_OBJECT` (an already converted new reference). Larger payloads
  and nested containers are converted first by their own calls.
- **One builder for three kinds:** C creates the tuple, list or dict (from pairs) and all
  items in one call. `dictSet` is gone; maps use the builder.
- **Ownership:**
  - C consumes every `PV_OBJECT` reference, also on failure (`values_release` drops the
    ones it never placed).
  - Go releases what it converted when a later item fails before C is called.
  - A borrowed `*Object` costs no call; C takes the new reference.
- **Stack rule:** both arrays are passed directly in the `call4` expression, so the
  pragma keeps them on the heap (`-gcflags=-m`: `make([]pyValue, n) escapes to heap`; the
  arena's `append escapes to heap`).
- **Pooled scratch:** `buildPool` (`sync.Pool`) of item and arena buffers, capped at
  `maxPooledItems = 4096` and `maxPooledArena = 64 << 10` so one huge container does not
  pin memory. Unpooled, arena growth cost about 4.5 allocations per build.
- `value_new` treats a NULL arena as "the payload is an address" (used by callers that
  pass Go memory directly) instead of computing `NULL + offset`, which is undefined
  behaviour in C. That layout change took the ABI to 3.

### `Object.Call` in one C call: `pythago_py_call`
- `CallKw` used to convert each argument with `From` (a C call and a GC cleanup each)
  and `Close` each afterwards: about 18 C calls, 2.3 µs for 9 arguments.
- `pythago_py_call(callable, n, items, …)` takes the arguments as tagged values, creates
  them on the C stack (heap past 8), makes a vectorcall and releases them. One C call
  (`call5(cPyCall, …)` in `py/convert.go`). Go-side conversion errors are returned
  unchanged.

### The glue ABI version check
- `pythago_py_abi()` returns `PYTHAGO_PY_ABI` (`glue/pythago_py.c`); `py/capi.go` checks
  it against `const glueABI` at Init:
  `py: the native image's glue has ABI %d, but this package needs %d: regenerate the image with the matching pythago-dist`.
- **Why it exists:** after the decoded-arguments change, a stale test image carried the
  old glue, whose call frame ended before `decoded`. Go read the C stack beyond it and got
  a pointer as a worker index:
  `RuntimeError: py: Go function panicked: runtime error: index out of range [261492238762000] with length 20`.
  Renaming a symbol catches a stale image; changing a struct layout renames nothing.
- An image older than the check fails with `pythago_py_abi is missing from pythago_symtab`.
- History: ABI 2 introduced the decoded frame; ABI 3 the NULL-arena `value_new`. The check
  caught a stale image at once: `glue has ABI 2, but this package needs 3`.

### Shared immortal singleton results
- `result(p)` (`py/object.go`) returns one of `singletons [3]*Object` when a C function's
  new reference is None, True or False. Each is borrowed and has a preset `pre` value.
- No allocation, no `runtime.AddCleanup`, no C call to read, `Close` or release. These
  objects are immortal in CPython 3.12+ (PEP 683), so skipping the release changes
  nothing.
- Every `py` call that returns None or a bool benefits.

### Zero-copy arrays through the buffer protocol
File: `py/buffer.go`; GoBuffer type in `glue/pythago_py.c`.

**Python → Go**
- `(*Object).Buffer(writable)` calls `PyObject_GetBuffer` (STRIDES|FORMAT, plus WRITABLE)
  into a `Py_buffer` in `PyMem_RawCalloc` memory, which never moves. Its layout is stable
  ABI since 3.11, so Go reads it directly.
- `Bytes()` and `View[T]` return slices after checking C-contiguity (from shape and
  strides), the format class and size against T, alignment, and no suboffsets. `l`/`L` is
  8 bytes native and 4 standard, so the item size decides. Big-endian formats are refused
  except for 1-byte items.
- `Close` releases the export. A Buffer the GC finds unclosed is released at the next entry
  into Python through the pending queue.
- `WithView[T](obj, writable, fn)` borrows for exactly fn's duration and releases on return
  or panic. It came out of a proposal to allocate Python's memory from the Go heap; why
  that would not remove the liveness rules is in [embedding-cpython.md](embedding-cpython.md)
  ("Ownership across two memory managers").
- **Reusable Buffer:** the C `Py_buffer` and GC cleanup belong to the Buffer through an
  anchor (`bufAnchor`), so a zero `py.Buffer` can live in a struct. `Close` releases only
  the export; `(*Object).BufferInto(b, writable)` exports again. Exporting into an open
  Buffer fails. Measured: 2 allocations per export+close reused against 9 fresh; the 2
  were the variadic argument arrays, gone with fixed-arity calls.

**Go → Python**
- `NewBuffer[T](data, readOnly, shape...)` returns a `pythago.GoBuffer`, a
  `PyType_FromSpec` heap type with `Py_bf_getbuffer`.
- Go pins the backing array with `runtime.Pinner` under a token.
- The deallocator pushes the token onto a lock-free list and bumps an atomic counter. Go
  reads the counter on each entry into Python and drains (`drainGoBuffers`) as the single
  consumer, so there is no ABA problem.
- **The deallocator never calls Go,** so it is safe on foreign threads and in forked
  children.
- `Stats.GoBuffers` counts pinned slices.

**Measured with numpy 2.5.5**
- A C-order array maps to `[]float64`, with writes seen both ways. `complex128` works both
  ways.
- Fortran order is refused as non-contiguous; `>i4` as big-endian.
- `np.asarray(GoBuffer)` is a writable view with `owndata False`; `a += 0.5` shows in the
  Go slice. A read-only export becomes a non-writable array.
- Networking does not benefit for uvicorn-style servers: httptools, h11 and asyncio streams
  use `data_received(bytes)`, and bytes writes were already zero-copy
  (`(*Object).UnsafeBytes`). See [aio-event-loop.md](aio-event-loop.md) for
  BufferedProtocol.

### Measurements
3.14t, arm64, one core pinned with `taskset -c 5`, Go 1.26.11:

| | purego | now |
|---|---|---|
| Go→C, empty function | 61.4 ns | 32.7 ns, 0 allocations |
| `incRef` + `decRef` | 135.8 ns | 67.8 ns, 0 allocations |
| Python→Go `echo(x)` (C builtin `abs`: 18 ns) | 561 ns | 112.7 ns |
| Python→Go `echo(x, k=x)` | n/a | 312 ns (keyword names still convert through C); 237 ns on 2026-11-17 against 129 ns for `echo(x)` (`BenchmarkPythonGoCall`), names decoded in C, the rest the kwargs map |
| `aio` `BenchmarkPythonGoCall` | n/a | 219 ns |
| `py` test suite, wall time | about 9.8 s | 4.1 s |

Before the change, a Python→Go call was about 30% purego's `callbackWrap` dispatching
through `reflect.Value.call`, and about 43% pythago's `dispatch`, two thirds of which were
two more Go→C calls (`Int64` for the index, `incRef` in `steal`).

Container builder, ASGI-like 11-item tuple, A/B interleaved in one process, core 3:

| | ns per build | allocations |
|---|---|---|
| item by item | 1428 | 0 |
| builder, unpooled | 840 | 5 |
| builder, pooled | **541** | 0 |

(The pooled benchmark reports 1 allocation: the benchmark boxing the tuple into `any`.)

Decoded arguments: a callback reading an int, bytes and str, A/B against the same objects
without decoded values, core 3: 784 → **493 ns**, 7 → 4 allocations.

Go→C calls per operation (counted with temporary counters on `incRef`/`decRef`, every
`callN` and the dispatcher; `bench/uvicorn/app.py` through `asgi.New` and `httptest`,
CPython 3.13.18, arm64, GC-deferred decrefs drained):

| Per operation | Refcount calls | All Go→C calls | Python→Go calls |
|---|---|---|---|
| ASGI `GET /`, 1 connection | 5 → 2 | 62 → 20 → **9** | 2 |
| ASGI `GET /`, 64 connections | 5 → 2 | 58 → 16 → **6** | 1.0 |
| `Do` + `Eval("1+1")` + `Go()` | 5 | 14 | 0 |
| `Do` + `f(1, 2)` + `Close` | 8 | 18 | 0 |
| `Do` + `From(3-entry map)` + `Close` | 9 | 25 → 6 (3 are `Do`'s own) | 0 |

The remaining 6 per request are 4 event-tuple builds plus one `Py_IncRef` and one
`Py_DecRef`.

End to end, loop-thread Go `asgi` against stock uvicorn 0.54.0 + uvloop 0.23.0 +
httptools 0.9.0 (`bench/uvicorn/run.sh 'asgi/net-http|stock/uvloop/httptools'`, CPython
3.13.18, raw app, 64 connections, req/s; two cores with `GOMAXPROCS=4`):

| | stock uvicorn + uvloop | Go `asgi` |
|---|---|---|
| 1 core, before the FFI work | 72.2k | 38.2k |
| 1 core, after cgocall/crosscall2 | 72.2k | 42.9k |
| 1 core, after builder + decoded arguments | 72.0k | **45.5k** |
| 1 connection, after builder + decoded arguments | 40.3k | 31.1k (was 29.5k) |
| 2 cores, before the FFI work | 72.8k | 69.9k |
| 2 cores, after cgocall/crosscall2 | 72.8k | **74.0k** |
| 2 cores, after builder + decoded arguments | 67.5k | **70.4k** |

On two cores the Go server matches or passes uvicorn + uvloop (the last row ran with host
load rising to 6.8, so compare only within the row). On one core net/http's own cost
(about 9.2 µs per request) bounds it; inline requests later took one core to 58.5k
([asgi-inline-and-flat-combining.md](asgi-inline-and-flat-combining.md)).

Argument and callback costs in inline asgi (one core, a no-op Python function, core 9):
no arguments 284 ns; an inline request's 15 values 515 ns; the same with constants as
cached borrowed objects 411 ns. The whole `respond` callback path (`py.dispatch` and
below) was only about 3.5% of the core (about 0.6 µs per request). The asgi-side changes
(cached constants, copied small bodies) belong to the asgi documents.

### Why no JIT
nyanjit (`../nyanjit`) was evaluated and rejected. Its Go→C gateway is `purego.SyscallN`
and its callbacks are `purego.NewCallback`, the same mechanisms. A JIT could only fuse
C-API calls under one transition or specialize converters per Go type. Static glue does
both without executable memory, without purego's 4096-callback cap and without an
unpublished dependency. It also could not inline refcount fast paths without the object
layouts pythago deliberately never hard-codes.

## Files
- `py/ccall.go`: `runtimeCgocall` (linkname), `cframe`, `call`, `call0`…`call8`.
- `py/ccall_amd64.s`, `py/ccall_arm64.s`: `pythago_cthunk` (`_cgo_topofstack` relocation),
  `crosscall2` address.
- `py/capi.go`: symtab binding, `glueABI`, `cString` (`//go:noinline`), `callStr0`, `callStr1`.
- `py/func.go`: `callFrame`, `argSlot`, `dispatchFrame`, `dispatch`, `setCallback`.
- `py/convert.go`: `pyValue` and tags, `build`, `buildPool`, `setValue`, `UnsafeBytes`,
  `pythago_py_call` call site.
- `py/object.go`: `result`, `singletons`, `Call`/`CallKw`.
- `py/buffer.go`: `Buffer`, `BufferInto`, `View`, `WithView`, `NewBuffer`,
  `drainGoBuffers`.
- `glue/pythago_py.c`: `struct pythago_value`, `struct pythago_call`, `PYTHAGO_PY_ABI`,
  `pythago_py_abi`, `decode`, `pythago_py_fastcall`, `value_new`, `values_release`,
  `pythago_py_build`, `pythago_py_call`, `pythago.GoBuffer`.
- `glue/prebuilt` and its `SHA256SUMS`: prebuilt glue embedded by `pythago-dist`
  (`//go:embed all:prebuilt`); `ci/glue.sh` rebuilds and verifies them.

## Test Coverage
Run with `GOWORK=$PWD/.work/go.work go test ./py`, and `ci/unit.sh gil && ci/unit.sh ft`
(arm64 natively, amd64 under qemu) before calling a change done. Each test below has a
mutation control that was shown to fail it.

- `py/ffi_test.go`:
  - `TestFuncResults`: every result path, keyword arguments, errors, panics, refcounts
    unchanged over 3000 calls. Control (borrowed argument reported as owned):
    `AssertionError: (0, -2000)`.
  - `TestCallbackMovesStack`: a Func called from `py.Eval` grows the stack by about 1.2 MB
    and makes a nested `py.Eval`. Control (relocation removed from the arm64 or amd64
    thunk): `got py: C API call failed without setting an exception, want 1042`.
  - `TestFromContainers`: every tag (int64/uint64 extremes, inline and large str/bytes,
    empty ones, borrowed objects, nested tuple/list/dict) round-trips with types.
  - `TestFromContainersRelease`: 100 rounds of failing builds (invalid UTF-8 top-level and
    nested, unhashable key, converted item then nested failure, converted item then a Go
    `chan`). Controls: Go release off `refcount of an item: 3 before, 203 after`; C
    `values_release` off `3 before, 190 after`.
  - `TestDecodedArguments`: each accessor's decoded answer against the C API path
    (`NewRef()`), over None/bools, int64 extremes and ±2⁶³/2⁶⁴, floats (±, NaN, ±inf,
    ±0), bytes with NUL and empty, ASCII/non-ASCII/astral str, bytearray, list, int and str
    subclasses, calls of at most 6 arguments, one past the decoded range, 5 keywords
    including a non-ASCII name. Controls: `Int64` off by one
    (`argument 3 Int64: decoded 1; C API 0`); truncated keyword names; `Truth` of a float
    as `> 0` (caught on -2.5 and NaN).
  - `TestCallArguments`: all argument kinds, more than 8, keywords, failure in C and Go,
    refcounts over 100 rounds. Controls: Go release off `3 before, 103 after`; C
    post-call `Py_DECREF` loop off `3 before, 403 after`. Its first failure,
    `3 before, 7 after`, was the test's own globals holding the object (4 references),
    not a leak.
  - `TestSingletonResults`: read, convert, double `Close`, `NewRef`, pass back, return
    from a Go function. Control (None preset as `pvFalse`): `None: Go() = false`.
- `py/py_test.go`:
  - `TestNestedUnderLoad`: the `[42, '']` out-parameter bug (1 in 45 before the fix, 60/60
    after, amd64 shared-mode image under qemu).
  - `TestGoroutines`: 2000 goroutines × 5 `Do`+`Exec`; caught the stack-resident
    `cString` buffer.
  - `TestBufferCleanup` (resize fails while exported, succeeds after GC),
    `TestNewBufferRoundTripAndRelease` (`Stats.GoBuffers` rises by one and falls back),
    `TestBufferInto`, `TestBufferBytearray`, `TestBufferReadOnly`, `TestBufferTyped`,
    `TestBufferStrided`, `TestNewBuffer`, `TestNewBufferLayouts`.
- Escape check: `go build -gcflags=-m ./py ./aio ./asgi` must list no buffer passed to C
  that `does not escape`.

## Pitfalls
- **`[42, '']`:** `(*Object).GoString` passed `&n` to `PyUnicode_AsUTF8AndSize` through a
  wrapper without `//go:uintptrescapes`. `n` stayed on the stack, the stack grew on the
  way into the C call, C wrote the length to the old copy, and Go read 0. `'x' * 3` came
  back as `''` with no error. Shared mode and qemu only changed timing; the deep stacks of
  nested Go callbacks are why `TestNestedUnderLoad` hit it. The fix was the pragma on
  `call` and `callStr`; `-gcflags=-m` then reports `moved to heap: n` at every
  out-parameter site.
- **A pointer that reaches C other than as a direct converted argument is not covered by
  the pragma.** `cString` inlined into `callStr` put the pointer inside `append(...)`, so
  `make([]byte, len(s)+1)` did not escape and Go 1.25+ placed it on the stack. Python read
  different bytes: `UnicodeDecodeError: 'utf-8' codec can't decode byte 0xce ...` and
  `counter 9985, want 10000`. purego's deeper frames hid it (0/25); the faster thunk
  exposed it (about 1 in 3). Which runtime path overwrites the stack mid-call was never
  established; the rule holds regardless.
- **After `-prebuild-glue`, rebuild `pythago-dist` before regenerating images.** It embeds
  `glue/prebuilt`; otherwise the image carries the old glue
  (`Init: py: pythago_py_set_callback is missing from pythago_symtab`).
- **Regenerate the test image (`.work/testimage`) after any glue change.** A stale image
  with a changed layout misreads memory unless `glueABI` was bumped.
- **A differential test must put each case inside the fast path's range.** Floats past the
  eighth argument were never decoded, so a broken `Truth` passed at first.
- **Check host load before an end-to-end run.** With another project's test using about
  14 cores (load 26 on 20, including the server's core), stock uvicorn itself fell from
  72.2k to 58.1k req/s, and the container builder's end-to-end number had to wait for a
  quiet host. Report both servers measured under the same conditions only.
- **Test helpers that touch Python must run inside `do`.** `evalObj` outside `Do`
  segfaulted.
- **Objects created with `py.From` outside Python crash** (SIGSEGV in `fromRaw` under
  `call2`); create them inside `py.Do`.
- **Never call Go from a CPython deallocator;** queue a token and drain on the next entry.
- **The singleton shortcut relies on immortality** (3.12+). Do not extend it to objects
  that are not immortal.
- **`PYTHAGO_PY_ABI` and `glueABI` must change together,** on every change to
  `struct pythago_call`/`callFrame` or `struct pythago_value`/`pyValue`.
