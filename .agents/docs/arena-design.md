# Generic borrow arena: design

- **Status (2026-11-19):** the user decided D1 to D7 (section 6).
  - Migration steps 1 and 2 have landed together, with one glue rebuild: call scopes in
    py on `internal/arena`, and ABI 4 with uncapped decoding.
  - Step 3 has landed: asgi request scopes with the lock-free take path (D6).
  - Step 4 has landed: wsgi request scopes.
  - Section 7 describes what landed.
  - Sections 0 to 5 are the proposal as approved. They describe the prototype, whose
    exported `py.Scope`, `ScopePool`, `Lane` and `Take` API was not adopted (D3).
- **Author:** round-5 unit "arena".
- **Request:** the user asked for "a more generic solution for the allocation
  reconciliation" and chose a **generic borrow arena**: one facility for call-scoped and
  request-scoped borrowed memory, used by aio, asgi and wsgi too, replacing their separate
  pools and recycling schemes and removing fixed caps.
- **Prototype:** `.agents-workspace/tmp/r5/arena/`.
  - `proto/` is the prototype tree; `base/` is the tree as of 2026-11-19 11:15.
  - `arena-proto.patch` is `diff -ruN base proto`, without `glue/prebuilt`.

## 0. Summary

- **One type, `py.Scope`:** borrowed memory with one lifetime.
  - A **call scope** serves one Python→Go call of a `Func`, or one Go→C build or call.
    It is private to one goroutine, pooled, and its first chunks are inline.
  - A **request scope** serves one server request. It comes from a `py.ScopePool[T]`
    together with a root value `T` (the server's request struct), is reference-counted,
    and its takes are serialized.
- **Lanes:** memory is taken per element type. py has its own lanes (Objects, `*Object`,
  strings, described values, bytes), and packages register typed lanes with
  `py.NewLane[T]`.
  - Each lane grows to fit what a lifetime takes, so there are no fixed caps.
  - Each lane keeps up to 64 KiB for the next lifetime and trims the rest.
- **Detecting misuse:** a release poisons the borrowed `*Object` values. Any accessor
  used on one later panics with a clear message. `PYTHAGO_CHECK_BORROWS=1` also stops
  released scopes from being reused, so a kept argument can never alias a later call's
  argument.
- **C side:** `struct pythago_call` passes `decoded` and `names` as pointers.
  - C decodes **every** argument and keyword name: into a stack array of 24 values, or
    `PyMem_Malloc` past that.
  - `PYTHAGO_DECODED` and `PYTHAGO_NAMES` leave the ABI. This is one bump, ABI 3 → 4.
- **Results, unchanged at or below 8 arguments** (ft):

  | call | before | after |
  |---|---|---|
  | 1 argument | 94.5 ns | 94.5 ns |
  | 8 arguments | 127.7 ns | 128.7 ns |
  | asgi respond-shaped | 124.8 ns | 122.5 ns |

- **Results past the old caps** (ft):

  | call | before | after |
  |---|---|---|
  | 9 arguments | 291 ns, 2 allocations | 136 ns, 0 allocations |
  | 16 arguments | 403 ns, 2 allocations | 174 ns, 0 allocations |
  | 8 keywords | 664 ns, 4 allocations | 206 ns, 0 allocations |

- **asgi:** its request recycling (sync.Pool, `refs`, kept buffers, the `[8][2]string`
  head pairs) becomes a `ScopePool[request]`, and its tests pass unchanged.
  - Allocations per request stay at 7.
  - A 12-header response goes from 18 to 17 allocations and from 825 to 440 B.
  - Hello req/s shows no measurable difference.
- **Left alone, deliberately:**
  - kwNameCache, asgi's interned constants and per-worker address cache: these are
    caches, not borrowed memory.
  - aio's 256 KiB read buffers: they are handed across goroutines with flow-control
    credits.
  - aio channel values: these are owned Go values.
  - Section 2.6 explains each.

## 1. Inventory of today's schemes

Measured on 2026-11-19 on the base copy. Section 4 gives the commands, cores and
versions. "ft" is CPython 3.14.10t; "gil" is 3.13.18.

### 1.1 Schemes and what they guarantee

**py dispatch `argFrame` (`py/func.go`)**
- **Borrows:** a call's `argSlot`s (Object plus decoded value), the positional
  `[]*Object` and the keyword names `[]string`.
- **Lifetime:** one Func call. It ends in the dispatch defer, after `returnValue`.
- **Cap and fallback:** 8 slots (`pooledArgs = len(callFrame{}.decoded)`). Past that it
  makes `slots`, `pos` and `names` on every call: 2 allocations, plus 1 with keywords.
- **Safety:** one frame per active call from a `sync.Pool`. One defer both recovers and
  puts the frame back. Each Object is rewritten whole on reuse. A Func that keeps an
  argument silently aliases a later call's argument.

**glue `struct pythago_call` (`glue/pythago_py.c`)**
- **Borrows:** decoded scalars `decoded[8]` and names `names[4]`, inline in the struct
  on the C stack.
- **Lifetime:** one `pythago_py_fastcall`.
- **Cap and fallback:**
  - Arguments past 8 are not decoded, so Go calls C per access.
  - Names past 4 are not decoded, so `kwName` makes 2 extra C calls, about 80 ns per
    name.
  - Both sizes are part of the ABI (`PYTHAGO_PY_ABI 3`).
- **Safety:** none needed, since the data is on the stack for the call.

**kwNameCache (`py/kwnames.go`)**
- **Holds:** Go copies of keyword names: 256 sets of 2 `atomic.Pointer[string]`.
- **Lifetime:** the process. Entries are evicted by newer names.
- **Cap:** 512 names. A miss copies the name.
- **Safety:** entries are immutable strings checked by contents. It holds no Python
  object.

**buildPool `buildScratch` (`py/convert.go`)**
- **Borrows:** `[]pyValue` items and a payload arena `[]byte` for
  `pythago_py_build` and `pythago_py_call`.
- **Lifetime:** one build or one `callValues`.
- **Cap and fallback:** it grows as needed. It is not pooled when over 4096 items or
  64 KiB.
- **Safety:** `sync.Pool` Get and Put in the same function. Nested builds take scratch
  of their own.

**glue `pythago_py_call` `small[8]`**
- **Borrows:** the argument `PyObject*` vector.
- **Lifetime:** one call.
- **Cap and fallback:** `PyMem_Malloc` past 8. It is already uncapped.
- **Safety:** C stack.

**asgi request recycling (`asgi/asgi.go`, `fields.go`)**
- **Borrows:**
  - the `request` struct, its `kick` channel and its `[2]action` queue array;
  - `buf` (raw path, query and packed headers, kept up to 16 KiB);
  - `pairs [8][2]string` for the first head that fits;
  - `body`, the first small body chunk.
- **Lifetime:** one HTTP request. It ends at the last `unref`, after ServeHTTP, the
  queued inline batch and every `_pythago_asgi` call that holds the request.
- **Cap and fallback:**
  - A head with more than 8 fields makes its pairs array.
  - Later small chunks are copied into memory of their own.
  - A WebSocket request is not pooled.
- **Safety:**
  - `refs` (atomic) and `Server.acquire`, which retains under `s.mu` only while the
    request is in `s.reqs`.
  - CAS claims (`pairsTaken`, `bodyTaken`).
  - Handles (`h`) are never reused.
  - `TestRecycleHeldRequest` and `TestRecycleNoCrossTalk`.

**asgi constants and `worker.addrs`**
- **Holds:** interned method, version and scheme strings, the empty bytes object, and
  each worker's server host and port objects.
- **Lifetime:** the process, or the worker.
- **Cap:** 64 addresses per worker.
- **Safety:** immortal or per-worker objects. This is not borrowed memory.

**asgi `worker.args` and `spare`**
- **Borrows:** a batch's `serve_inline` arguments and the pending array.
- **Lifetime:** one batch.
- **Cap:** none. The arrays are reused by the holder.
- **Safety:** one holder at a time (`busy`).

**wsgi request (`wsgi/wsgi.go`)**
- **Borrows:** nothing is reused. Each request allocates its `request` struct, the
  environ buffer, the header pairs and `strings.Split` fields, the `pending` copies, a
  `sync.Map` entry and the call boxing.
- **Lifetime:** one request.
- **Cap:** not applicable.
- **Safety:** never-reused handles plus the `done` flag under `q.mu`. A stale `with`
  that already did `lookup` may lock a finished request, which is safe only because
  requests are not reused.

**aio `readBufs` (`aio/aio.go`)**
- **Borrows:** 256 KiB read buffers.
- **Lifetime:** from a reader goroutine to the loop thread's `poll`, which copies the
  data into a bytes object and puts the buffer back. A `credits` channel bounds
  in-flight reads.
- **Cap:** a fixed size per buffer. `sync.Pool` drops idle buffers at GC.
- **Safety:** ownership moves with the completion. Only poll puts a buffer back, and
  `closeChannelQueue` does on close.

**aio channel values (`aio/channel*.go`)**
- **Holds:** decoded Go values (`[]T`, `map`, `string`) that a Go receiver owns for
  good.
- **Lifetime:** owned by the receiver.
- **Safety:** not borrowed. They are leases released in `discardChannelValue`.

**`py.Buffer` reuse (wsgi `q.buf`, aio `ac.rbuf`)**
- **Borrows:** a `Py_buffer` export struct, reused by its owner.
- **Lifetime:** one read.
- **Safety:** one read at a time per owner.

### 1.2 Measured cost today (base)

| benchmark | ft ns/op | ft allocs (B) | gil ns/op | gil allocs (B) |
|---|---|---|---|---|
| PyGo 1 positional | 94.5 | 0 | 94.3 | 0 |
| PyGo 8 positional | 127.7 | 0 | 124.4 | 0 |
| PyGo 9 positional | 291.0 | 2 (720) | 296.8 | 2 (720) |
| PyGo 16 positional | 402.8 | 2 (1280) | 395.2 | 2 (1280) |
| PyGo 1 keyword | 115.4 | 0 | 115.0 | 0 |
| PyGo 4 keywords | 149.7 | 0 | 158.0 | 0 |
| PyGo 8 keywords (1 positional) | 663.8 | 4 (784) | 685.4 | 4 (784) |
| PythonGoCall8 (asgi respond shape) | 124.8 | 0 | 122.0 | 0 |
| GoPy `Call(1, "str", []byte)` | 159.2 | 1 (24) | 164.0 | 1 (24) |
| GoPy `From([]any{8 scalars})` | 403.5 | 4 (104) | 406.4 | 4 (104) |
| asgi `BenchmarkInlineRequest` (hello) | 5460 | 7 (176) | 4297 | 7 (176) |
| asgi 12-header response | 14322 | 18 (825) | 11489 | 18 (825) |
| wsgi request (404 fallback) | 9849 | 20 (1057) | 9139 | 20 (1056) |
| aio `BenchmarkPythonGoCall` | 157.2 | 0 | 157.2 | 0 |
| aio `BenchmarkChannelReady` | 715.0 | 0 | 635.1 | 0 |

- **Where 9+ arguments go:** the time is the 2 or 4 allocations, plus the C calls for
  undecoded arguments and names.
- **Where wsgi's 20 allocations go** (memprofile, base ft, per request):
  - `py.own`: 3, the results' Objects with their GC cleanups;
  - ServeHTTP: 2.8 (the request struct, the Do closure, boxing);
  - `environ`: 2.2;
  - `start`: 2;
  - `write`: 2, the pending copy;
  - `parseHeaders`: 2, the pairs and `strings.Split`;
  - the packed-string copy: 1;
  - the `sync.Map` entry: 1;
  - `Int64`: 0.8;
  - the call's argument boxing: 1.2.

## 2. The abstraction

### 2.1 Lifetime model

- **A lifetime begins and ends:**
  - A call scope begins at `acquireCall` and ends at `release`.
  - A request scope begins at `ScopePool.Get`, with one reference, and ends at the
    last `Release`.
  - Memory taken in a lifetime is valid until that lifetime ends. Then it is reset and
    reused.
- **Nested calls on one OS thread (Python → Go → Python → Go)** each acquire a call
  scope of their own. Scopes never nest inside each other, so release order does not
  matter and there are no marks. Greenlet-style C stack switching across Go frames is
  out of scope: it already breaks Go's own `crosscall2` frames.
- **Concurrency on 3.14t:**
  - Call scopes are private to the goroutine that runs the call: no locks, no
    atomics.
  - Request scopes are shared by the ServeHTTP goroutine, the inline holder and every
    Python thread in a `_pythago_asgi` call. Their takes hold the scope's mutex, and
    their reference count is atomic.
  - `Retain` is valid only for a holder that already holds a reference, or under the
    server's lock while the request is still registered (asgi's `acquire`). That is
    exactly today's `refs` rule.
- **Panics:** dispatch releases in the same defer that recovers, after `returnValue`
  has read a result that may be a borrowed argument. A panicking Func allocates as much
  as a failing one (`TestDispatchPanicAllocs` still passes). Request scopes are
  released by `defer` in the servers, as before.
- **Over-release:** a `Release` past zero panics with "Scope released more often than
  retained". Today's `refs` went negative silently. This new check is what caught two
  of the asgi controls (section 4.4).

### 2.2 API (as prototyped, `proto/py/arena.go`)

```go
// A Scope is borrowed memory with one lifetime: a call (a Python→Go call of a Func, a
// Go→Python call or a container built in C) or a request of a server. Memory taken
// from a scope is valid until the scope's last reference is released; the scope is
// then reset and reused, and the same memory is handed to a later scope's user.
// Nothing taken from a scope may be kept past its release: copy it, or for a borrowed
// *Object keep a NewRef. A scope grows to whatever its lifetime takes and keeps up to
// a bound per lane for its next lifetime.
type Scope struct{ /* refs, shared, detached, mu, lanes */ }

func (s *Scope) Retain()            // the caller already holds a reference
func (s *Scope) Release()           // the last one resets and reuses s
func (s *Scope) Detach()            // never reused: the GC reclaims it (WebSockets)
func (s *Scope) Bytes(n int) []byte // n zeroed bytes

// A Lane is the memory of one element type in every scope (make each once).
type Lane[T any] struct{ /* id, reset */ }
func NewLane[T any](reset func(*T)) *Lane[T]       // zeroed on reset, or reset(e)
func Take[T any](s *Scope, l *Lane[T], n int) []T // a full slice: append copies

// A ScopePool hands out scopes with a root T (a server's request), reused together.
type ScopePool[T any] struct{ /* sync.Pool, reset */ }
func NewScopePool[T any](reset func(*T)) *ScopePool[T]
func (p *ScopePool[T]) Get() (*T, *Scope)
```

- **Unexported, py only:**
  - `callScope` embeds `Scope` and adds inline `[8]` chunks for Objects, `*Object`,
    strings and values, plus `kwName`'s scratch word.
  - `acquireCall` and `(*callScope).release`.
  - The lanes `objs`, `ptrs`, `strs`, `vals` and `bytes`.
- **Fast path:** a call that fits the inline chunks uses them directly and skips the
  lanes' bookkeeping. Without that, a 1-argument call cost 99 ns against 94 ns.
- **The `Func` doc:** its wording stays ("args and kw are borrowed: they are valid only
  during the call ... later calls reuse their memory"). It gains one sentence: "a
  borrowed *Object used after its call returned panics while its memory is unused;
  with PYTHAGO_CHECK_BORROWS=1 it always does."

### 2.3 Release and misuse detection

- **Borrowed `*Object`:**
  - On release, each Object the lifetime handed out gets `p = 0` and
    `pre = &releasedValue`, a Go-only tag `pvReleased`.
  - Every Object accessor calls `o.live()` first: one compare of `o.pre` with a global
    address.
  - A kept argument therefore panics with `errReleased` ("py: a borrowed *Object was
    used after the call or scope that lent it returned (keep a NewRef instead)"). It
    does not read freed memory or another call's argument while the memory is idle.
  - `Close` stays a no-op on it.
  - Poisoning only the two fields the check reads costs about 3 ns at 8 arguments.
    Poisoning the whole 40-byte Object cost 7 ns.
- **Reuse:** once a later call has taken the same memory, a kept argument aliases that
  call's argument. Neither today's tree nor the arena can see this without a
  generation word per Object, which every accessor would then have to compare.
  - `PYTHAGO_CHECK_BORROWS=1` (read at package init in the prototype) makes released
    scopes never reused, so a kept argument stays poisoned for good.
  - `TestCheckBorrowsNeverReuses` asserts it from inside later calls.
  - Its control showed that with reuse, 24 of 48 later calls read the kept argument as
    their own.
- **Request scopes:**
  - Handles stay as they are: asgi's `h` is never reused, which is the generation
    check.
  - Retaining requires a registered request.
  - Over-release panics.
  - Takes return memory zeroed, or reset by the lane's function.
- **Generation counters were considered** and not adopted for Objects. They would cost a
  load and a compare in every accessor, plus a word per Object, to catch a bug class the
  debug mode already catches deterministically.

### 2.4 Retention and limits

- **Growth:** a lane takes from its current chunk. When one take does not fit, it starts
  a new chunk of `max(n, 2 × current, 8)` elements; the earlier chunk stays valid until
  the reset.
- **Reset:**
  - Reset zeroes what was used (user lanes), runs the lane's reset function, or
    poisons (Objects).
  - It drops every chunk but the current one.
  - It drops the current one too if it is over `laneKeep` (64 KiB per lane per scope),
    returning to the inline chunk.
  - The next lifetime of the same shape then fits in one chunk.
- **The 64 KiB bound** replaces buildPool's 4096 items and 64 KiB, asgi's 16 KiB
  `maxKeptBuf` and its fixed `[8]` pairs. One huge call (1639 Objects or 65537 bytes in
  the test) leaves nothing pinned (`TestScopeLanesGrowAndTrim`).
- **Zeroing:**
  - py's own `ptrs`, `strs`, `vals` and `bytes` lanes are not zeroed on reset, only
    trimmed: their takers overwrite every element. The clears cost write barriers in
    the profile.
  - User lanes are always zeroed or reset, because they are exported.
- **Idle scopes:** `sync.Pool` drops idle scopes at GC, as today's pools do. A dropped
  call scope costs one allocation to remake, since its inline chunks are part of it.

### 2.5 How each scheme maps onto it

**Mapped (prototyped):**
- dispatch `argFrame` → a call scope: Objects, `*Object` and names lanes, with no
  cap.
- buildPool → a call scope: `vals` lane, and `bytes` lane as the payload arena built
  in place (`rest` and `adopt`).
- glue `decoded[8]` and `names[4]` → pointer + count, sized by C (section 3).
- asgi `request` pool, `refs`, `buf`, `pairs` and the first body chunk →
  `ScopePool[request]`.
  - `refs` becomes `Retain` and `Release`.
  - `buf` comes from `sc.Bytes`.
  - Pairs come from `py.Take(sc, pairLane, n)` for heads and trailers, with no cap.
  - The first body chunk comes from `sc.Bytes`.
  - WebSockets call `Detach`.

**Mapped (designed, not prototyped):**
- wsgi request → `ScopePool[request]`: the root, the environ buffer, the pairs (parse
  without `strings.Split`) and the first `pending` buffer.
  - Its handle table needs the same rule as asgi: `with` must `Retain` under a lock,
    and `done` stays.
  - This saves an estimated 5 to 7 of its 20 allocations.
- aio `poll`'s per-poll slices (`out`, `pools`, `helds`) → a call scope, if a Func can
  reach its own call scope. That needs API (decision D4).

**Kept separate:**
- **kwNameCache:** it caches immutable strings across calls by content; nothing is
  borrowed or released.
- **asgi constants and `worker.addrs`:** immortal or per-worker Python objects, chosen
  for refcount contention on 3.14t, not for memory.
- **asgi `worker.args` and `spare`:** one holder reuses them; a scope would add a pool
  round trip for nothing.
- **aio `readBufs`:** a 256 KiB buffer moves from a reader goroutine to the loop thread
  and back, and in-flight reads are bounded by `credits`. That lifetime is not a call
  or a request. A scope per buffer would add a reference count to replace a `Put`, and
  would not bound memory any better than the credits. At most the size could come from
  a shared constant.
- **aio channel values:** these are owned by the Go receiver with no end of lifetime
  the server knows. The read-only containers TODO covers the Python side.
- **`py.Buffer` reuse:** already one struct per owner.

### 2.6 What a scope must not be used for

A scope holds memory for its whole lifetime. Per-event allocations in a long lifetime
would grow without bound, for example every chunk of a streamed response that lasts
minutes. So asgi copies only the first small body chunk into the request scope, as it
does today, and later chunks into memory of their own. The doc for `Bytes` and `Take`
should say: "for memory bounded per lifetime".

## 3. The C side

- **ABI 4** (`PYTHAGO_PY_ABI 4`, `glueABI = 4`):

  ```c
  struct pythago_call {            /* py/func.go callFrame */
      PyObject *const *args; Py_ssize_t nargs; PyObject *kwnames; Py_ssize_t nkw;
      Py_ssize_t index; PyObject *result; Py_ssize_t borrowed;
      struct pythago_value *decoded; /* nargs + nkw values: every argument */
      struct pythago_value *names;   /* nkw values: every keyword name */
  };
  ```

- **`pythago_py_fastcall`:**
  - It decodes all `nargs + nkw` arguments and all `nkw` names into
    `struct pythago_value small[PYTHAGO_STACK_VALUES]` (24 values, 576 B on the C
    stack).
  - Past that it uses `PyMem_Malloc`, and returns `PyErr_NoMemory()` on failure. It
    frees after the Go call.
  - `PYTHAGO_STACK_VALUES` is internal to the glue. Go reads through the pointers
    whatever the size.
  - The struct itself shrinks from 344 to 72 bytes, so the designated initializer
    zeroes less.
- **No copy in Go:** an argument's `pre` points straight into C's array, where Go used
  to copy each value into its slot. The array lives until the call returns, exactly
  like the argument, which is borrowed for the call.
  - A pointer to C stack or PyMem memory in a Go object is fine for the GC: it ignores
    non-Go memory and pointers into stacks.
  - The rule "C must never read goroutine-stack memory" is untouched. This is Go
    reading C memory.
- **Why C owns the memory rather than the Go arena:** C decodes before Go is entered.
  Handing it Go arena memory would need a Go→C round trip first, or a per-thread arena
  handle in a C thread-local, with its lifetime tied to thread exit.
  - The C-only change alone measured 92.7 ns against 94.3 ns for one argument, so it
    is a small gain by itself (variant `exp2`).
- **Decoding everything:** decoding is side-effect-free and cannot fail; this is
  `decode`, unchanged. A call `f(*range(10000))` now spends about 10 µs decoding values
  Go may never read. A C-internal ceiling (for example decode the first 64 and leave
  the rest `PV_OBJECT`) would keep that bounded without touching the ABI.
- **Landing it:**
  - Bump `PYTHAGO_PY_ABI` and `glueABI` together.
  - Rebuild `glue/prebuilt` (all 12 objects and DLLs) with `ci/glue.sh`,
    `ci/darwin.sh glue-update` on the arm64 Mac, and the Windows SDK path. The
    coordinator does this.
  - Stale images then fail at Init with the existing ABI message.
- **Measured in scratch:** linux/arm64 images were built with a scratch `pythago-dist`
  from the prototype tree. The linux arm64 prebuilt objects were removed in the copy, so
  the glue compiled from source (LLVM 23.1.5). The base images were built the same way,
  so only the glue diff differs.

## 4. Prototype and measurements

### 4.1 Files (`.agents-workspace/tmp/r5/arena/proto/`)

- **`glue/pythago_py.c`:** ABI 4 and uncapped decoding.
- **`py/capi.go`:** `glueABI = 4`.
- **`py/arena.go`** (new): Scope, lanes, Lane, Take, ScopePool, call scopes, poisoning
  and `PYTHAGO_CHECK_BORROWS`.
- **`py/func.go`:** dispatch on a call scope. `Kwargs.vals` is now `[]Object`, and
  `argSlot`, `argFrame` and `argFrames` are gone.
- **`py/kwnames.go`:** reads names through the pointer.
- **`py/convert.go`:** build and callValues on a call scope (`buildPool` is gone), and
  `pvReleased`.
- **`py/object.go`, `convert.go`, `unicode.go`, `buffer.go`, `owned_buffer.go`,
  `string.go`:** `o.live()` in the accessors.
- **`asgi/asgi.go`, `asgi/fields.go`:** requests on `py.NewScopePool[request]`. Gone:
  `Server.pool`, `refs`, `pooled`, `buf`, `pairs`, `pairsTaken`, `body` and
  `maxKeptBuf`.
- **Tests:**
  - `py/arena_internal_test.go` (new);
  - `py/dispatch_internal_test.go`: new shapes in `TestDispatchAllocs`, and a wide
    variant of `TestDispatchNestedConcurrent`;
  - `py/kwnames_internal_test.go`: the frame helper;
  - `py/kwargs_internal_test.go`: comments.
- **Benchmark-only files, identical in base and proto, not for landing:**
  - `py/arena_bench_test.go`;
  - `asgi/alloc12_test.go`, plus one line in `asgi/asgi_test.go`;
  - `wsgi/wsgi_alloc_bench_test.go`.

### 4.2 Numbers

- **Setup:**
  - go1.26.11, linux/arm64. CPython 3.14.10t (ft) and 3.13.18 (gil), with images built
    in scratch as described in section 3.
  - ft on core 5 and gil on core 6, in parallel.
  - base and proto rotated within each of 5 rounds, `-benchtime=1s` (`bench.sh`).
  - Medians are taken over runs whose foreign CPU on the core (`/proc/schedstat` busy
    time minus the run's own user+sys) was at most 2%. 8 of 80 runs were dropped while
    the round-5 gate loaded the host; the median foreign CPU was 0.6%.
  - Summaries come from `summarize.py`; raw data is in `results/{ft,gil}.txt`.

| benchmark | ft base → proto (ns) | ratio | gil base → proto (ns) | ratio | allocs base → proto |
|---|---|---|---|---|---|
| PyGo 1 positional | 94.5 → 94.5 | 1.000 | 94.3 → 94.0 | 0.996 | 0 → 0 |
| PyGo 8 positional | 127.7 → 128.7 | 1.008 | 124.4 → 131.2 | 1.055 | 0 → 0 |
| PyGo 9 positional | 291.0 → 136.3 | 0.468 | 296.8 → 142.7 | 0.481 | 2 → 0 |
| PyGo 16 positional | 402.8 → 174.1 | 0.432 | 395.2 → 177.2 | 0.448 | 2 → 0 |
| PyGo 1 keyword | 115.4 → 113.2 | 0.981 | 115.0 → 113.4 | 0.986 | 0 → 0 |
| PyGo 4 keywords | 149.7 → 150.0 | 1.002 | 158.0 → 156.8 | 0.992 | 0 → 0 |
| PyGo 8 keywords | 663.8 → 205.6 | 0.310 | 685.4 → 211.6 | 0.309 | 4 → 0 |
| PythonGoCall8 (respond shape) | 124.8 → 122.5 | 0.981 | 122.0 → 121.4 | 0.995 | 0 → 0 |
| GoPy call3 | 159.2 → 160.1 | 1.006 | 164.0 → 159.1 | 0.970 | 1 → 1 |
| GoPy build8 | 403.5 → 407.7 | 1.010 | 406.4 → 419.2 | 1.031 | 4 → 4 |
| asgi InlineRequest | 5460 → 5571 | 1.020 | 4297 → 4408 | 1.026 | 7 → 7 |
| aio PythonGoCall | 157.2 → 156.9 | 0.998 | 157.2 → 155.3 | 0.988 | 0 → 0 |
| aio ChannelReady | 715.0 → 718.8 | 1.005 | 635.1 → 651.8 | 1.026 | 0 → 0 |

- **Allocation counts, single runs on cores 0-4:**
  - asgi 12-header response (`BenchmarkInlineRequest12`): 18 → 17 allocations and
    825 → 440 B, on both images.
  - `TestInlineAllocs`: 7 → 7.
  - wsgi request: 20 → 20 (not ported).
- **The asgi InlineRequest +2% splits** (ft, 3 interleaved rounds, 2 s each):
  - Base asgi on the prototype's py (`exp4`): 5464 to 5509 ns against base 5402 to
    5498, so +0.2 to 1.1%.
  - The rest, about 70 ns per request, is the asgi port: three mutex-guarded takes and
    the release's lane cleaning.
  - A lock-free fast path for request-scope takes (an atomic bump on a chunk struct,
    with the lock only to grow) should take most of it back. It is designed, not built.
- **hello req/s:**
  - Setup: `asgiserve` inline with GOMAXPROCS=1 on core 5; `httpload -c 64 -d 5s -w 2s`
    with GOMAXPROCS=2 on cores 6 and 16 (`hello.sh`). 6 rounds, base and proto
    alternating; the server's foreign CPU was ≤0.7% on ft, with one gil round at
    10.7%.
  - ft: 64993 → 65155 req/s, median per-round ratio 1.008.
  - gil: 68330 → 69816 req/s, median per-round ratio 1.021.
  - Per-round ratios spread from 0.945 to 1.148, so there is no measurable end-to-end
    difference. p99 was 2.13 → 2.10 ms on ft and 2.04 → 2.01 ms on gil.

### 4.3 Tests (scratch copy, both images)

- **Checks:** `gofmt -l` is clean. `go vet` prints only the "possible misuse of
  unsafe.Pointer" lines that `ci/unit.sh` filters.
- **Results:** py, aio, wsgi, workers and asgi pass on ft and gil, and so does asgi with
  `PYTHAGO_ASGI_LOOP_THREAD=1` on both. Runs used `taskset -c 0-4,10-14`, logged in
  `tests-proto-final.log`.
- **Not run:** qemu amd64, darwin, Windows and the full `ci/unit.sh` tiers. Those need
  the rebuilt prebuilt glue.

### 4.4 New tests and their controls

Each control ran in a mutated copy under `ctl/` (`control.py`, `controls.sh`) with ft;
the starred ones also ran with gil.

| test | what it observes | control (mutation) | result under the control |
|---|---|---|---|
| `TestDispatchAllocs` (9, 16, 40 positional; 8 and 16 keywords: 0 allocations) | no caps, lanes kept | C1: reset always trims to the inline chunk | `p9: ... allocates 2.00 times, want 0` (also p16, p40, k8long, k16) |
| `TestBorrowedArgumentPoisoned`* | accessors panic with `errReleased` after the call | C2: no poison (`pre = nil`) | the binary crashes (SIGSEGV: an accessor passes `p = 0` to C); no errReleased |
| `TestCheckBorrowsNeverReuses`* | with the debug mode, no later call aliases a kept argument | C3: reuse regardless of the flag, in both release paths | `24 of 48 later calls: during call 3, the first call's argument panicked with Int64 read 3` |
| `TestScopeLanesGrowAndTrim` | growth kept; a huge lifetime trimmed; user lanes zeroed | C7: no trim | `after a 1639-argument lifetime the lane kept 1639 slots; want its inline 8` (and bytes) |
| `TestSharedScopeTakes` | concurrent takes on a request scope never overlap | C4: request scopes unlocked | `round 0: goroutine 0's pairs hold "7": two takes overlapped` (3/3 runs) |
| `TestScopePoolRoot` | the root is reset at the last release only; an extra release panics | not controlled separately: the asgi controls exercise both paths | |
| `TestDispatchNestedConcurrent` (narrow and wide, 20 + 6 extra arguments) | nested and concurrent calls never share arguments | C5: one scope for every call | `call (id 450, depth 0) after Release: its string is 's210.6': it shares its arguments with another active call` |
| same | same | C6: scope released before the Func runs | `Go function panicked: py: a borrowed *Object was used after ...`: the poison catches it first |
| asgi `TestRecycleHeldRequest` | a held `_pythago_asgi` call keeps its request | A1: `Release` recycles whatever references are left | LoopThread: `the request a _pythago_asgi call held (handle 1) was recycled under it: it went on with request 2`; inline: `py: Scope released more often than retained` |
| same | same | A2: no reference for the queued batch | `py: Scope released more often than retained` |

Findings from the controls:
- **C3 first passed** for two reasons, and both were fixed in the test and the control:
  - Every release poisons again, so aliasing is visible only from inside a later call.
  - The non-inline release path still dropped scopes, so dispatch never reused one.
- **C2 cannot fail with a message:** without the poison, nothing can tell a released
  Object from one with `p = 0`.
- **The over-release panic** now catches the asgi early-recycle bugs (A1 inline, A2)
  before the tests' own observations do.

## 5. Migration plan

Each step lands on its own and passes the full gate (`ci/unit.sh gil` and `ft`, the
darwin and Wine tiers).

1. **Call scopes in py, Go only** (ABI unchanged).
   - Replace argFrame and buildPool with `callScope`, keeping the `[8]`/`[4]` C arrays:
     dispatch reads `c.decoded` only for the first 8.
   - Removes the Go allocations for 9+ arguments. Its share of the time saved was not
     measured separately from step 2.
   - Adds `o.live()` and the poison.
   - Risk: low. The Func contract is unchanged; the poison turns some silent misuse into
     panics, which is the point but may surface latent bugs in user Funcs.
   - The API stays unexported in this step.
2. **ABI 4: uncapped decoding in C** (glue, `capi.go`, prebuilt rebuild on all
   platforms).
   - Removes the per-argument and per-name C calls past 8 and 4. This is the remaining
     2 to 3× for many keywords.
   - Risk: medium, mechanical. It touches every prebuilt object and DLL, and the darwin
     objects need the arm64 Mac.
   - Users regenerate their images; stale ones fail at Init clearly.
3. **Request scopes for asgi** (`ScopePool`, `Lane`, `Take` exported, or internal: D3).
   - Replaces asgi's pool, `refs` and kept buffers; removes the `[8]` pairs cap.
   - Risk: medium. It is the recycling that `TestRecycleHeldRequest` and
     `TestRecycleNoCrossTalk` guard; they pass unchanged and the controls above fail.
   - Land it with the lock-free take fast path, or accept about 1% on the inline
     micro-benchmark (no req/s change measured).
   - The asgi package's current owner must agree.
4. **Request scopes for wsgi.**
   - Root request, environ buffer, pairs (no `strings.Split`) and the first pending
     buffer: an estimated 5 to 7 of 20 allocations.
   - Needs `with` to retain under a lock before reuse is safe (the asgi rule).
   - Risk: medium. It needs a recycle test like asgi's, with a held-call control.
5. **Optional:** let a Func reach its call scope (D4), then aio's `poll` slices; a
   C-internal decode ceiling; a shared read-buffer size constant for aio.

## 6. Open decisions for the user

- **D1. ABI 4 now or later:** land steps 1 and 2 together (one glue rebuild), or step 1
  first. The C change alone is slightly faster for ≤8 arguments and is what removes the
  per-name C calls.
- **D2. Detecting misuse:**
  - (a) Poison always plus `PYTHAGO_CHECK_BORROWS=1` for no reuse, as prototyped. It
    costs about 1 to 3 ns per call for 1 to 8 arguments.
  - (b) Poison only in the debug mode: no cost, silent aliasing otherwise.
  - (c) A build tag instead of the environment variable.
  - Whichever is chosen: should it be a `Config` field rather than an environment
    variable?
- **D3. Where the API lives:**
  - (a) Exported from `py` (`Scope`, `ScopePool`, `Lane`, `Take`, as prototyped): user
    servers can use it, but it is a public API commitment.
  - (b) `internal/arena` for the generic part, with py's call scope built on it: no
    public API.
  - The recommendation is (b) until a second user outside the module asks.
- **D4. Func access to its call scope:** for example `kw.Scope()`, so a Func can borrow
  call-lifetime memory (aio poll). It would be new public API.
- **D5. Decode ceiling:** decode every argument (as prototyped), or decode the first N
  (C-internal, say 64) to bound `f(*huge)`.
- **D6. Request-scope takes:** mutex (as prototyped) or the lock-free bump fast path
  (more code, about 1% back on the asgi micro-benchmark).
- **D7. aio `readBufs`:** keep it separate (recommended), or move it onto a shared byte
  pool for one retention policy.

### Decisions (user, 2026-11-19)

- **D1:** steps 1 and 2 land together, with one glue rebuild; step 3 lands separately.
- **D2:** released borrowed Objects are always poisoned. Never reusing them is
  `Config.CheckBorrows`, not an environment variable.
- **D3:** the generic part lives in `internal/arena`, and py's call scope is built on
  it. py exports nothing new except the Config field.
- **D4:** a Func has no access to its call scope.
- **D5:** C decodes every argument.
- **D6:** step 3 builds the lock-free take path.
- **D7:** aio `readBufs` stays separate.

## 7. As landed (steps 1 and 2)

- **`internal/arena`:** `Lane[T]`, which has `Init`, `Take`, `Rest`, `Adopt`, `Touched`,
  `Reset`, `ResetWith`, `Cap` and `AtHome`, and the bound `Keep` (64 KiB).
  - A Lane is single-goroutine; request scopes, with their reference counts and locking,
    come with step 3.
- **py:**
  - `py/callscope.go` holds `callScope`: lanes for Objects, `*Object`, names, values and
    bytes, the inline `[8]` arrays and the inline fast path, the `sync.Pool`, the
    poisoning, and `checkBorrows`.
  - `Config.CheckBorrows` is applied in `start`.
  - `dispatch`, `build` and `callValues` run on call scopes. `argFrame`, `argFrames`,
    `argSlot`, `buildPool` and `buildScratch` are gone.
  - `Kwargs` holds `[]Object`.
  - `o.live()` is in the Object accessors.
- **glue:** `PYTHAGO_PY_ABI 4` with `glueABI = 4`. `decoded` and `names` are pointers,
  with `PYTHAGO_STACK_VALUES 24` on the stack and `PyMem` past that. `glue/prebuilt` was
  rebuilt for every target.

### Step 3: asgi request scopes

- **`internal/arena` additions:**
  - `SharedLane[T]` is a lane that several goroutines take from at once.
  - `Pool[T]` hands out `Scope[T]`: a root value `T` with an atomic reference count.
    `Retain`, `Release` (the last one resets the root and reuses it) and `Detach`
    (never reused).
  - A `Release` past zero panics.
- **The take path:**
  - A take is `cur.Load()` followed by one `used.Add(n)` on the current chunk. If
    `end <= len(buf)`, the take returns `buf[end-n:end:end]`.
  - Otherwise the taker locks, tries the (possibly new) current chunk once more,
    retires it to `full`, and publishes a new chunk of `max(n, 2 × len)` with
    `used = n`.
- **Why the takes are safe (memory ordering):**
  - A new chunk's `buf`, zeroed by `make`, and its counter are written before
    `cur.Store` publishes it. A taker's `cur.Load` synchronizes with that store (Go's
    atomics are sequentially consistent), so it sees both.
  - Within a chunk, the atomic adds give successful takes disjoint intervals. A take
    that overflows wastes only the tail of that chunk. The counter only grows during a
    lifetime.
  - A taker that loaded a chunk just before it was retired may still take from it. Its
    interval is still unique, and stays valid: retired chunks are kept in `full` until
    the reset.
  - The reset happens after every take of the lifetime. Takers hold the scope's
    references, and the last `Release`'s atomic decrement comes after each of theirs.
  - The reset clears the used part (unless `SetNoZero`), drops the retired chunks, and
    drops the current chunk too when it is past `Keep`.
- **asgi:**
  - `request` is the root of an `arena.Scope[request]` from the package-level
    `requests` pool. `q.sc` holds the reference count, which replaces `refs`;
    `acquire`, `submit` and `unref` call `Retain` and `Release`.
  - `q.mem` (a `requestMem`, kept across recycling) holds a bytes lane (`SetNoZero`)
    and a `[2]string` pairs lane. The bytes lane takes the packed inline values (it was
    `buf`, kept up to 16 KiB) and the first small body chunk (it was `body`). The pairs
    lane takes every head's and trailers' pairs: the `[8][2]string` array and its CAS
    are gone, and so is the 8-pair cap.
  - The reset function drains `kick`, resets both lanes, and zeroes the request but for
    `kick`, `sc` and `mem`.
  - WebSocket requests `Detach`.
  - `Server.pool`, `refs`, `pooled`, `buf`, `body`, `pairs`, `pairsTaken` and
    `maxKeptBuf` are gone.

### Step 4: wsgi request scopes

- **`internal/arena`:** each `Pool.Get` gives its lifetime a process-unique `ID()`, set
  before the reference count. `TryRetain(id)` adds a reference only while the scope
  still serves lifetime `id`:
  - it CAS-increments the count unless the count is zero;
  - then it checks the ID, and drops the reference again if the scope has moved on to
    another lifetime.
- **Handles:**
  - A wsgi request's handle is its scope's ID. `reqs` (a `sync.Map`) maps handles to
    scopes; the separate handle counter is gone.
  - `acquire` loads the scope and calls `TryRetain(h)`. Lookups stay lock-free, and a
    stale handle (a `write()` kept by a thread) can never act on a later request that
    reuses the memory.
  - `with` holds the reference until its call returns (the release comes after the
    unlock).
  - `ServeHTTP` gets the scope first and releases it last, after `reqs.Delete`, the
    deferred writes and `releaseWritten`.
- **Lanes, kept across recycling in `requestMem`:**
  - `env` (a Lane with noZero): the packed environ, built in place with `Rest` and
    `Adopt`.
  - `pending` (a Lane with noZero): the first pending buffer, one `Take(maxPending)`,
    which the copies before the next detached write never outgrow.
  - `pairs` (a SharedLane): the response head's pairs, parsed with `bytes.Count` and
    `strings.Cut` instead of `strings.Split`.
  - `buf`: wsgi.input's `py.Buffer`.
- **Fewer copies:** header names are lower-cased into a stack buffer for the hop-by-hop
  and Content-Length checks, and environ `HTTP_` names are built in a stack buffer and
  appended without a string copy.
- **Effect:** a `/hello` request goes from 21 to 12 allocations and from 1152 to 296 B (`TestRequestAllocs`).

## 8. OwnedBuffer and the scope-backed lease (2026-11-20)

### (a) Lease costs, fixed without the arena

- **Measured cost per lease** (aio `BenchmarkChannelBuffer`, Workers1, 3.14t):
  - **Go→Python:** 3.2 µs and 25 allocations. Five owned Objects, each with about three
    allocations and a GC cleanup; `AddCleanup` alone was 17% of the CPU. Then
    `Import("builtins")` and `memoryview` through Objects (37%), and `NewBuffer`'s
    Pinner, shape slice, format C string and format map literals.
  - **Python→Go:** 2.5 µs and 11 allocations. A new `Buffer` per lease, with its
    `Py_buffer` and GC cleanup (23%); a `NewRef` of the exporter (10%); and `Close`
    through `Do` from a goroutine (18%, which stays: it is the documented prompt
    release).
- **Fixes:**
  - A lease's export `Buffer` comes from a pool and goes back closed.
  - The lease no longer takes a `NewRef`: the export's `Py_buffer` holds the
    exporter's reference.
  - `TakeObject` makes its memoryview with `PyMemoryView_FromObject`, from the
    exporter or from a GoBuffer made by `newGoBuffer`. The GoBuffer gets no Object,
    and it is pinned by a Pinner and shape array held in the lease, with a static "B"
    format.
  - `formatOf` uses a switch instead of map literals, and `drainGoBuffers` uses a
    static token array.
- **Result:** 1 and 5 allocations per lease, where they were 9 and 20
  (`TestOwnedBufferAllocs`). The benchmark ratios (now/before) are 0.75 to 0.94 of the
  time Go→Python and 0.49 to 0.71 Python→Go.

### (b) The scope-backed memoryview lease: not landed

- **The idea:** hand the first request-body chunk, held in the request's bytes lane, to
  Python as a read-only memoryview, with the lease holding a scope reference.
- **Why it is not landed:**
  - **Not spec-compliant:** ASGI's `http.request` `body` is `bytes`, and applications
    call `body.decode()` or `json.loads(body)`, which fail on a memoryview.
  - **No win:** a scratch prototype (`proto6`, a GoBuffer memoryview) was slower for
    100 B (7374 against 7240 ns, 4 more allocations) and equal for 16 KiB. Making a
    memoryview costs more than copying a small body.
  - **The other memory in the lane** (raw_path, query_string, headers) must be `bytes`
    by the spec too. The response's first chunk is Go's own.
- **Its hazards, had it landed:**
  - A kept view holds the scope until the view is freed (GC), so recycling is delayed
    without bound unless the lease detaches the scope.
  - The release reaches Go at the next `drainGoBuffers`.
  - NoZero lanes would be safe only because the reference delays the reset.
  - The lease would have to go through the GoBuffer path, read-only.
  - LoopThread posts hold no reference, so the posted body would need one.

### What landed instead for the body path (inline mode)

- **Change:** an inline request's first chunk, when the declared length fits one chunk,
  is read to that length (`readFirstChunk`), and its growth steps come from the
  request's bytes lane.
  - The queued batch holds the request until `serve_inline` has copied the chunk into a
    bytes object.
  - Before, the buffer came from the heap, and the read went on to the chunk size just
    to see EOF, which cost a last doubling.
  - A one-byte probe read still reaches the body's EOF, as before: net/http starts its
    background read of the connection there.
- **Memory still follows what the client sent:** the read starts at 4 KiB and doubles
  as data arrives (`TestFirstChunkFollowsData`).
- **LoopThread mode is unchanged:** `readChunk`, heap memory.
  - The posted values hold no reference, so a lane is not safe there.
  - Reading to the declared length there raised throughput 6 to 8% for 100-byte bodies
    but doubled p99 latency, for an unexplained reason, so it was left as it was.

## 9. Go-owned `bytes` (2026-11-20): designed, measured, not landed

- **The idea (user):** an object of exact type `bytes` whose header and data live in
  Go-owned memory. Python frees it through its normal path, and the free goes back to
  Go. The first user would be asgi request bodies (the first chunk and every `receive()`
  chunk, in both modes), with no `PyBytes_FromStringAndSize` copy.

### What CPython does

Checked against CPython 3.13.18 and 3.14.10 source.

- **Layout:** both versions have `PyObject_VAR_HEAD; Py_hash_t ob_shash (deprecated, -1
  until hashed); char ob_sval[1]`, with `ob_sval[ob_size] == 0`.
- **Construction (`_PyBytes_FromSize`):**
  - `PyObject_Malloc(PyBytesObject_SIZE + n)`, then `_PyObject_InitVar(op,
    &PyBytes_Type, n)` (the type and `_Py_NewReference`; on 3.14t that also sets
    `ob_tid` to the calling thread), then `ob_shash = -1` and the NUL.
  - `PyBytes_FromStringAndSize` returns the immortal singletons for lengths 0 and 1.
  - The public `PyObject_InitVar` does the same initialization.
- **Free:** `bytes` has no `tp_dealloc`, so `object_dealloc` calls `tp_free`:
  `PyObject_Del` on 3.13 and `PyObject_Free` on 3.14. Both go through the object domain
  (`_PyObject.free`). Neither version has a bytes freelist.
- **Resize:** `_PyBytes_Resize` calls `PyObject_Realloc` on uniquely referenced bytes.
  3.14 copies instead if the hash was computed. A hook must copy a Go-owned object out.
- **The object domain on 3.14t:** `PyObject_Free` still dispatches through
  `_PyObject.free`, so a hook installed with `PyMem_SetAllocator` sees every object
  free. GC objects are allocated from mimalloc directly (`_PyMem_MimallocEnabled()` is
  always 1 under `Py_GIL_DISABLED`), which a chaining hook leaves alone.
- **The decisive fact, on 3.14t:**
  - `bytes` live in `_Py_MIMALLOC_HEAP_OBJECT`, which has `page_use_qsbr = true`.
  - mimalloc keeps the first bytes of a freed block, up to `ob_type`, intact. In
    CPython's own words: "We may access ob_tid and the refcount fields in the dict and
    list lock-less accesses, so they must remain valid for a while after deallocation."
  - A lock-free reader (`_Py_TryIncrefCompare`) CASes `ob_ref_shared` of an object it
    loaded from a list or dict, which may have been freed meanwhile. It is safe only
    because a dead header reads 0 or `_Py_REF_MERGED`.
- **What that means for Go-owned memory:** Go may not write body data over a freed
  object's header until a QSBR grace period has passed. QSBR (`_Py_qsbr_*`) is internal
  and not exported from libpython, so the glue cannot call it in shared mode.
  - The sketched design (variable-size lane chunks, reused when their live count is 0)
    is therefore unsound on 3.14t: header positions move between lifetimes.
  - It is sound on 3.13, which has no lock-free readers.

### A design that would be sound

- **Storage:** fixed-size slots per size class (for example 4, 16 and 64 KiB plus the
  header) in one reserved address range, so that one range check classifies a pointer.
  - The reservation is `mmap` or `VirtualAlloc` in the glue: portable to darwin and
    Windows.
  - Slots are never repartitioned while the process lives, so a header address stays a
    header address.
- **Writes:** Go writes body data only at and after `offsetof(PyBytesObject, ob_sval)`,
  as the glue reports it. The header region keeps a freed object's dead header (or zeros
  if never used) until `pythago_bytes_adopt` initializes a new object with
  `PyObject_InitVar`, `ob_shash = -1` and the NUL. A stale reader sees either a dead
  header or a new live object, which is what mimalloc's own block reuse gives.
- **Construction:** `adopt` runs on the thread handing the body to Python (it has a
  thread state, so `ob_tid` is a Python thread). It returns the singletons for lengths 0
  and 1.
- **The hook:**
  - It replaces only `free` and `realloc` of `PYMEM_DOMAIN_OBJ`; `malloc` and `calloc`
    stay the originals.
  - `free`: if the pointer is in range, an atomic store of the slot's state, with no Go
    callback; else a tail call to the original. `realloc`: copy out into the original
    allocator, then free the slot.
  - It is installed at Init, on top of whatever is there, chaining to it.
  - With `PYTHONMALLOC=debug`, ours sits above the debug hooks and takes its own
    pointers before they would check their header.
  - tracemalloc started later wraps ours. Our objects are untraced, and its free of an
    untraced pointer is harmless.
  - The allocator is process-global, so subinterpreters are covered.
  - Finalize may free our objects late; the region stays mapped for the process.
- **Retention:** a kept body pins only its slot, for as long as Python keeps it. Peak
  concurrency sets the slab size; on 3.14t slabs can never shrink without QSBR.
- **LoopThread and `receive()` chunks:** these need a value that `py.From` turns into an
  adopted object on the loop thread, which means new conversion API.

### Measured (scratch prototypes)

Setup: go1.26.11, linux/arm64, 3.14.10t on core 5 and 3.13.18 on core 6, interleaved,
foreign CPU checked.

- **The most it could save (`proto9`):** a hack that passes an existing bytes object of
  the same length, so there is no copy, no allocation and no free. This is above what
  fusion could reach, since it pays no `adopt` and no slot management. 5 rounds, 0 of 20
  runs dropped.

  | inline BenchmarkUpload | ft | gil |
  |---|---|---|
  | 100 B | 1.00 | 1.00 |
  | 16 KiB | 0.972 (6604 → 6419 ns) | 0.944 (5535 → 5226 ns) |
  | 64 KiB | 0.952 | 0.968 |
  | 16 KiB chunked | 0.968 | 0.946 |

  Over the network (`asgiserve` inline, `httpload -c 64 -body N`, 5 rounds): ft 1.008
  (16 KiB) and 0.999 (64 KiB); gil 1.020 and 1.027. Every round range spans 1.0.
- **What every workload would pay (`proto10`):** a glue hook on `free` and `realloc` with
  one range check, chaining to the original. Same binaries, `PYTHAGO_FUSION_HOOK` 0
  against 1, 6 rounds.
  - Python loops whose objects are freed through `PyObject_Free` (`bytes(64)`, str
    concatenation, class instances): ft 1.007, 1.017 and 1.009; gil 1.011, 1.023 and
    1.010. That is 0.4 to 0.7 ns per free.
  - asgi InlineRequest: 0.998 (ft) and 1.003 (gil).
  - A scratch test confirmed the hook takes the frees in its range. Its control,
    without the hook, aborted with `free(): invalid pointer`.

### Decision

Not landed.

- **Unsound as sketched:** the lane design is unsound on 3.14t, as explained above.
- **The sound design isn't worth it:** its fixed-slot discipline adds a second
  allocator, a glue ABI bump, a hook on every object free of every program (up to 2%
  on allocation-heavy code), and new conversion API for LoopThread. In return it can
  save at most 3 to 6% of an in-process 16 to 64 KiB inline upload, nothing at 100 B,
  and nothing measurable over the network. The copy it removes costs about 0.2 to
  0.3 µs per 16 KiB.

## 10. Go-owned `bytes` with a GC grace (2026-11-20): working prototype, not landed

- **The idea (user):** let Python's own stop-the-world (STW) be the grace period that
  section 9 found missing on 3.14t, so freed fused memory can return to a lane.
- **Prototype:** `.agents-workspace/tmp/r5/arena/proto11`, with images `p11img-{ft,gil}`
  built from its glue. It is a scratch copy of the tree; the shared tree's code is
  unchanged.

### What the 3.14.10 source says (verified)

- **The GC callbacks bracket a STW:**
  - `gc_collect_main` (Python/gc_free_threading.c) calls `invoke_gc_callback("start")`,
    then `gc_collect_internal`, then `invoke_gc_callback("stop")`.
  - `gc_collect_internal` begins with `_PyEval_StopTheWorld` and ends with
    `_PyEval_StartTheWorld` (a second STW in between handles resurrection).
  - This holds for every collection the callbacks report: any generation (the
    free-threaded collector always collects the whole heap), whether triggered by the
    allocation threshold or by `gc.collect()`. Callbacks are skipped only at shutdown,
    and a threshold check that fails returns before "start".
- **A STW waits for every thread:** `stop_the_world` counts every other thread state and
  returns only once all are parked (detached, or attached and stopped at a safepoint).
- **Lock-free readers make no safepoint mid-read:** `list_get_item_ref` and the dict
  lookups load the item pointer and run `_Py_TryXGetRef` / `_Py_TryIncrefCompare` in
  straight-line C, with no eval-breaker check and no detach in between.
- **The grace argument:**
  - A stale reader loaded the pointer before the container dropped it. The container
    dropped it before the final decref reached zero (the refcount is an atomic chain),
    and that was before the free.
  - A STW that began after the free completes only once that reader has left its read.
  - So a free stamped with epoch `e` is safe once a STW has completed whose start bumped
    the epoch past `e`: bump at "start", publish as safe at "stop".
  - The free hook stamps a chunk (an atomic max) before it decrements the chunk's live
    count (release), so Go sees the final stamp once it reads live 0 (acquire).
- **A cheap STW without a collection:** `PyFunction_SetDefaults`, public C API, does
  `_PyEval_StopTheWorld` / `_PyEval_StartTheWorld` around setting a function's
  defaults (`func_set_defaults`, Objects/funcobject.c). Bump the epoch, set a private
  function's defaults to the same tuple, publish the epoch. This is an implementation
  detail of 3.14t that is not documented as a STW; it must be re-checked for every
  CPython release.

### The prototype

- **glue:** the `free`/`realloc` hook from section 9 (one range check, chained), now
  with per-chunk live counts and epoch stamps in Go memory that C updates atomically;
  `pythago_bytes_adopt` (`PyObject_InitVar`, `ob_shash = -1`, NUL); and
  `pythago_bytes_header`.
- **py:**
  - `FusedBytes` takes room from 256 KiB chunks of a reserved region (a mutex-guarded
    bump). `py.From` (and so call arguments and posted tuples) adopts it as an exact
    `bytes`; bodies under 2 bytes become the singletons.
  - Retired chunks are reused at live 0: at once with the GIL, after a grace on 3.14t.
  - Graces come from `gc.callbacks`, and from a forced `ForceGrace`, which a background
    goroutine runs (outside the allocator's lock) once a quarter of the region or less
    is free.
  - With no memory free, a body falls back to a copy.
- **asgi:** the first chunk and every `receive()` chunk, in both modes, go to Python as
  fused bytes. LoopThread works through the posted tuples' conversion on the loop
  thread; no new path was needed beyond `py.From` adopting.
- **The GC never ran under load on 3.14t:** 0 collections in 60,000 requests (16 KiB
  uploads, 100 B uploads, hello). Without forced graces, the 64 MiB region filled after
  3840 fused 16 KiB bodies and the rest fell back to copies. With them: 4 to 5 forced
  graces per 20,000 requests, 89% of 16 KiB bodies and 100% of 100 B bodies fused.
- **Memory held:** the region (64 MiB reserved, touched as used). A kept body pins its
  256 KiB chunk until Python frees it. A taken but never converted body (a posted tuple
  the loop never ran) pins its chunk for good.

### Tests (scratch) and controls

- **Python-visible behaviour** (`TestFusedBytesBehaviour`), against
  `PyBytes_FromStringAndSize` bytes of the same content: type, `==`, hash, len,
  `sys.getsizeof`, pickle, marshal, slicing, `bytes()` and `copy.copy` identity,
  memoryview and read-only, decode, concatenation, `gc.is_tracked`, dict key, repr and
  find; 2 B to 60 KB, in the region.
  - Control: adopt without `ob_shash = -1` (its own image) fails with `KeyError`.
- **Singletons, `_PyBytes_Resize`, tracemalloc:** `_PyBytes_Resize` moves the object out
  of the region with its contents and decrements the chunk's count; tracemalloc started
  after the hook works.
  - `PYTHONMALLOC=debug` also passed, but I could not confirm that the embedded
    configuration honours it (no `_testcapi` in the image).
- **Grace:** `TestFusedBytesGrace` and `TestFusedBytesNoReuseBeforeCollection` (3.14t):
  a chunk whose objects Python freed is not handed out again before a STW, and is after.
  - Controls, both by the `PYTHAGO_FUSION_NOGRACE` flag and by a code mutation
    (`recycleLocked` ignoring the stamp): `no chunk waits for a collection` and `a chunk
    whose objects Python freed was handed out again before any collection`.
- **The lock-free stress test** (`TestFusedBytesLockFreeStress`, 3.14t): readers in
  threads read fused bodies out of a shared dict and list while writers replace them and
  chunks recycle.
  - **Its control does not fail.** Without the grace, with 8 KiB chunks and a 2 MiB
    region, 16 million takes over 2 minutes showed no corruption and no crash.
  - The window (a stale reader between its pointer load and its incref) is a few
    nanoseconds, so this test proves nothing. Soundness on 3.14t rests on the source
    argument above, not on a test.
- **Suites:** the asgi suite passes with the fusion on in both modes on both images, and
  so do py, aio and wsgi.

### Measured

Setup: go1.26.11, linux/arm64, 3.14.10t on core 5 and 3.13.18 on core 6, interleaved,
foreign CPU at most 2% (no runs dropped). The current tree is the baseline.

- **In process** (`BenchmarkUpload`, 5 rounds), now/before:

  | case | ft | gil |
  |---|---|---|
  | inline 100 B | 1.017 | 1.030 |
  | inline 16 KiB | 1.022 | 0.947 |
  | inline 64 KiB | 0.488 | 0.183 |
  | inline chunked 16 KiB | 1.026 | 0.903 |
  | LoopThread 100 B | 0.936 | 0.960 |
  | LoopThread 16 KiB | 0.991 | 0.705 |
  | LoopThread 64 KiB | 0.980 | 0.294 |
  | LoopThread chunked 16 KiB | 0.754 | 0.695 |

- **Network** (`asgiserve` with GOMAXPROCS=1 on core 5, `httpload -c 64 -body N` on cores
  6 and 16, 5 rounds, `bench/uvicorn/app.py` `/upload`), req/s ratio:

  | case | ft | gil |
  |---|---|---|
  | inline 100 B | 1.017 | 1.023 |
  | inline 16 KiB | 1.033 | 1.118 |
  | inline 64 KiB | 2.507 | 2.873 |
  | LoopThread 100 B | 1.049 | 1.038 |
  | LoopThread 16 KiB | 1.390 | 1.480 |
  | LoopThread 64 KiB | 2.048 | 2.420 |

  p99 fell in every configuration (64 KiB inline, ft: 6.28 → 3.22 ms).
- **The hook tax** (fusion off against on, same binaries, 6 rounds):

  | workload | ft | gil |
  |---|---|---|
  | `bytes(64)` loop | 1.006 | 1.012 |
  | str concatenation loop | 1.013 | 1.019 |
  | class instance loop | 1.009 | 1.010 |
  | hello (InlineRequest) | 1.005 | 1.010 |

- **Attribution:**
  - Copy removal alone is worth at most 3 to 6% (section 9's upper bound).
  - The large 64 KiB and LoopThread gains come mostly from the current tree's read path,
    which the fusion bypasses:
    - Inline, a 64 KiB first chunk's growth steps (124 KiB) exceed the bytes lane's
      64 KiB bound, so every request reallocates: about 258 KB per request.
    - LoopThread reads into doubling heap buffers (35 KB per 16 KiB body).
  - Letting the bytes lane keep 256 KiB, without the fusion, takes inline 64 KiB from
    34.6 to 8.3 µs on ft and from 30.8 to 6.9 µs on gil (`proto12`, 4 rounds). That is
    better than the fusion on ft (17.0 µs) and close to it on gil (5.7 µs).

### Assessment

- **Sound:** on 3.13 by construction. On 3.14t by the source argument: STW-bracketed
  graces, and `PyFunction_SetDefaults` as a forced STW, which is version-specific.
- **Untestable:** the soundness on 3.14t; its stress test cannot fail.
- **Costs:**
  - a hook on every object free of every program (0.5 to 2%);
  - forced STWs on 3.14t (one per few thousand 16 KiB bodies);
  - a second allocator;
  - a glue ABI bump;
  - region management to port to darwin and Windows (the prototype uses Linux
    `mmap`).
- **Win:** large over the network, but most of it is available without the fusion:
  fix the bytes lane's bound for 64 KiB first chunks, and LoopThread's doubling reads.
  What remains after those fixes is not measured.
- **The user's call.** I recommend fixing the read path first and re-measuring the
  fusion against that.

## 11. Read-path fixes (landed) and the fusion re-measured against them (2026-11-20)

### The inline 64 KiB lane cliff (fixed)

- **Cause:** `readFirstChunk` grows a first chunk in steps of 4, 8, 16, 32 and 64 KiB
  from the request's bytes lane.
  - With the default bound (`Keep`, 64 KiB), the lane's current chunk after a 64 KiB
    body is larger than the bound, so `Reset` dropped it.
  - The next request made it again: about 258 KB allocated per 64 KiB upload.
- **Fix, in `internal/arena`:**
  - `SharedLane.Extend` grows the lane's last take in place, lock-free: one CAS on the
    chunk's `used`, refused once another take followed or without room. Growth steps no
    longer copy, and no longer waste the earlier steps.
  - `SharedLane.SetKeep(n)` lets a lane keep a chunk of up to `n` elements past the
    default bound while its lifetimes use more than the bound. After 16 lifetimes in a
    row that did not, it drops the chunk.
- **asgi:** each request sets `SetKeep(2 × BodyChunk)` on its bytes lane, which holds
  the first chunk plus the inline values, and the first-chunk read tries `Extend` before
  taking.
- **Retained memory** (scratch measurement, one pooled request):
  - Before: 0 bytes after 64 KiB uploads (dropped, made again every time).
  - After: 128 KiB while uploads continue, back to 300 bytes after 20 small requests.
  - Worst case: the number of pooled requests × 2 × BodyChunk, for requests that
    recently served large uploads. `sync.Pool` drops idle entries after two GC cycles.

### LoopThread's first chunk (fixed)

- **Change:** posted requests read their first chunk with `readFirstChunk` on the heap.
  It still starts at 4 KiB and doubles, so memory follows the data, but is capped at the
  declared length, with the EOF probe. Before, they read with `readChunk`, which ran past
  the end to see EOF and allocated 4 KiB even for 100 bytes.
- **The earlier "doubled p99" did not reproduce:**
  - 2 × 6 alternating rounds: the new read is 5% faster for 100 B with a lower p99 (ft
    3.35 → 2.79 ms, gil 3.08 → 2.76), and at parity for 16 KiB.
  - With `GOGC=off`, both reads give p99 of about 1.55 ms: the tail is the Go GC, which
    the new read runs half as often (about 600 against 1150 collections per run).
  - The earlier result was most likely interference. The round-5 gate was running
    unpinned, and the foreign-CPU check watched core 5 but not its hyperthread sibling
    15 or the load cores. The scripts now record core 15: it was busy up to 99.7% in
    some later gil rounds.
- **Not changed:** LoopThread 16 and 64 KiB bodies still double on the heap. A lane
  there needs posted values that hold a reference.

### Tests and controls

- **`TestSharedLaneExtend`:** in place at the top, refused after another take or without
  room. Control: `Extend` never extends → `Extend of the last take: false`.
- **`TestSharedLaneKeepDecay`:**
  - Control: `SetKeep` ignored → fails.
  - Control: no decay → `after 16 small lifetimes the lane still keeps 66536`.
- **`TestInlineUpload64KiBNotAllocated`:** 154 bytes per request. Control: no `SetKeep`
  → `allocates 258942 bytes`.
- **`TestLoopThreadSmallUploadSized`:** 1420 bytes per posted 100 B upload. Control:
  posted requests read as before → `allocates 5389 bytes`.

### Numbers for the fixes

Setup: go1.26.11, linux/arm64, 3.14.10t on core 5 and 3.13.18 on core 6, 5 interleaved
rounds, no in-process run dropped. Network: `asgiserve` with GOMAXPROCS=1 on core 5,
`httpload -c 64` on cores 6 and 16, hello is a GET of `/`.

- **In process** (`BenchmarkUpload`, InlineRequest, LoopThreadRequest), now/before:

  | case | ft | gil |
  |---|---|---|
  | inline 64 KiB | 0.223 | 0.202 |
  | inline 16 KiB | 0.977 | 0.969 |
  | inline chunked 16 KiB | 0.953 | 0.945 |
  | LoopThread 100 B | 0.950 | 0.936 |
  | LoopThread 64 KiB | 0.810 | 0.803 |
  | everything else (hello included) | 0.988 to 1.004 | 0.988 to 1.004 |

- **Network** req/s ratio:

  | case | ft | gil |
  |---|---|---|
  | inline 64 KiB | 2.520 (p99 6.01 → 3.18 ms) | 2.668 (p99 5.76 → 2.95 ms) |
  | LoopThread 100 B | 1.067 | 1.034 (p99 2.90 → 2.33 ms) |
  | LoopThread 16 and 64 KiB | 1.010 and 0.988 | 0.988 and 1.007 |
  | inline 100 B and 16 KiB | 0.982 and 1.004 | 1.007 and 0.991 |
  | hello inline | 0.987 (range 0.966-1.006) | 0.983 (range 0.950-1.010) |
  | hello LoopThread | 0.999 | 0.989 |

  - The hello ratios' ranges span 1.0, and the in-process hello is at parity, so I read
    them as noise. Core 15 was busy during the gil network rounds.
- **Gate:** gofmt and vet are clean; asgi passes in both modes and internal/arena, wsgi
  and workers pass, on both images; `ci/unit.sh gil` and `ft` pass.

### The fusion (`proto15`: the fixed tree plus `proto11`'s fusion) against the fixed tree

Same setup, 5 rounds, no in-process run dropped. Ratios are fusion/fixed.

- **In process:**

  | case | ft | gil |
  |---|---|---|
  | inline 100 B | 1.023 | 1.027 |
  | inline 16 KiB | 1.049 | 0.971 |
  | inline 64 KiB | 1.060 | 0.901 |
  | inline chunked 16 KiB | 1.059 | 0.958 |
  | LoopThread 100 B | 1.014 | 1.002 |
  | LoopThread 16 KiB | 0.980 | 0.700 |
  | LoopThread 64 KiB | 0.965 | 0.360 |
  | LoopThread chunked 16 KiB | 0.751 | 0.694 |
  | hello (InlineRequest, LoopThreadRequest) | 0.996 and 1.004 | 1.006 and 1.007 |

- **Network** req/s ratio:

  | case | ft | gil |
  |---|---|---|
  | inline 100 B | 0.990 | 1.028 |
  | inline 16 KiB | 1.025 | 1.082 |
  | inline 64 KiB | 0.926 | 1.079 |
  | LoopThread 100 B | 0.975 | 0.993 |
  | LoopThread 16 KiB | 1.360 (p99 4.44 → 3.38 ms) | 1.464 (p99 4.38 → 2.58 ms) |
  | LoopThread 64 KiB | 2.078 (p99 8.11 → 4.06 ms) | 2.450 (p99 8.23 → 3.53 ms) |
  | hello inline | 1.026 | 1.024 |
  | hello LoopThread | 0.994 | 1.017 |

- **Reading:**
  - Inline, after the fixes, the fusion costs 2 to 6% on 3.14t (adopt, grace upkeep and
    the hook) and gains 3 to 10% on 3.13.
  - Its large gains are in LoopThread 16 and 64 KiB bodies, where the fixed tree still
    reads into doubling heap buffers and copies; the fusion reads in place.
  - A LoopThread read fix without the fusion is the next comparison to make. Posted
    values would need to hold a reference to the request's lane memory until the loop
    thread converts them. Until that is measured, the LoopThread gains can't be credited
    to the fusion itself.
- **The fusion is not landed.** The user decides.

## 12. LoopThread body leases (landed) and the fusion against every non-fusion fix (2026-11-20)

### Design

- **`internal/lease`:** the interface `Bytes` (`LeasedBytes() []byte`, `ReleaseLease()`).
  - `py.From`, and so a call's arguments and a posted value converted on a loop thread,
    turns one into a bytes object with `PyBytes_FromStringAndSize` (a copy), then calls
    `ReleaseLease` once. py exports no new API: the interface lives in an internal
    package.
- **asgi `bodyLease`:** a lent slice of a request's memory, with a request-scope
  reference taken when it is posted (`lend`) and dropped by `ReleaseLease`, after the
  copy. Each request has two, in its root: the first chunk's and the current `receive()`
  chunk's. Posting allocates nothing.
- **First chunk (LoopThread):** read into the bytes lane as inline mode does (in place,
  kept up to 2 × BodyChunk), and posted as a lease.
- **`receive()` chunks (both modes, since they are always posted):**
  - They don't use the lane, which would keep every chunk of a long upload until the
    request ends (section 2.6).
  - Each request has one receive buffer, kept up to BodyChunk across recycling and
    dropped after 16 lifetimes without use. A chunk is read into it and lent.
  - The buffer is busy until the loop thread has copied the chunk. A read that finds it
    busy (a second `receive()` before the first chunk was converted; Starlette's
    disconnect listener can do that) reads into memory of its own, as before.
- **Error and cancel paths:**
  - **Client gone before the loop thread runs:** ServeHTTP returns and drops its own
    reference; the lease's reference keeps the request from being recycled until the
    conversion. The posted value can never read a recycled request.
  - **Loop shutdown, or a post never consumed:** the lease is never released, so the
    request is never recycled and the garbage collector reclaims it. Memory is never
    reused under an unconverted value.
  - **A failed read:** the receive buffer is freed and nothing is lent.
- **WebSockets:** their requests are detached and never take this path.
- **CheckBorrows:** applies to call scopes only.

### Tests and controls (controls in scratch copies)

| test | what it checks | control | control result |
|---|---|---|---|
| `TestPostedBodyHeldUntilConverted` | with GOMAXPROCS=1 and the loop thread held, B's posted body stays B's after B's client goes away and C reads its own body (C would take B's memory if B were recycled) | lease holds no reference | `request B's application saw 1000 bytes starting "C"`, 3 of 3 runs on 3.14t and on 3.13 |
| `TestLeaseCopiedThenReleased` (py) | copied, then released once | released before the copy | `the bytes object holds "XXXXXXXXXXXXXXX"` |
| `TestReceiveBufferBusy` | a second read while lent gets its own memory; freed after release | busy ignored | `a second read while the first chunk was lent overwrote it` |
| `TestLoopThreadUploadNotAllocated` | posted 16, 64 and 200 KiB uploads cost 1.3 to 3.2 KB per request | receive chunks into heap memory | `a posted 204800-byte upload allocates 348155 bytes` |

### Numbers (the leases against the tree with section 11's fixes)

Same setup as section 11, 5 rounds, no in-process run dropped. Ratios are after/before.

- **In process:**

  | case | ft | gil |
  |---|---|---|
  | LoopThread 16 KiB | 0.714 | 0.710 |
  | LoopThread 64 KiB | 0.390 | 0.385 |
  | LoopThread chunked 16 KiB | 0.712 | 0.716 |
  | everything else (inline, 100 B, hello) | 0.994 to 1.003 | 0.994 to 1.003 |

- **Network** req/s ratio:

  | case | ft | gil |
  |---|---|---|
  | LoopThread 16 KiB | 1.364 (p99 4.45 → 3.65 ms) | 1.371 (p99 4.14 → 3.39 ms) |
  | LoopThread 64 KiB | 2.171 (p99 8.23 → 6.27 ms) | 2.217 (p99 8.23 → 5.92 ms) |
  | LoopThread 100 B | 0.994 | 1.004 |
  | inline and hello | 0.975 to 1.029 (ranges span 1.0) | 0.975 to 1.029 (ranges span 1.0) |

  - Core 15 (core 5's sibling) was busy up to 99.7% in most gil network rounds, from
    another user's process, so those rounds carry that noise.

### The fusion against every non-fusion fix

`proto17` is the fusion (section 10's `proto15` patch) rebased onto the tree with the
leases, measured against that tree. Same setup and 5 rounds. The in-process runs dropped
nothing. Ratios are fusion/leases.

- **In process (ns ratio):**

  | case | ft | gil |
  |---|---|---|
  | inline 100 B | 1.017 | 1.016 |
  | inline 16 KiB | 1.036 | 0.965 |
  | inline 64 KiB | 1.042 | 0.897 |
  | inline chunked | 1.047 | 0.965 |
  | LoopThread 100 B | 1.007 | 1.004 |
  | LoopThread 16 KiB | 1.033 | 0.987 |
  | LoopThread 64 KiB | 1.036 | 0.943 |
  | LoopThread chunked | 1.032 | 0.983 |
  | hello (both modes) | 0.995 to 1.002 | 0.995 to 1.002 |

- **Network (req/s ratio):**

  | case | ft | gil |
  |---|---|---|
  | LoopThread 16 KiB | 1.007 (p99 3.63 → 3.04 ms) | 1.060 (p99 3.52 → 2.53 ms) |
  | LoopThread 64 KiB | 0.986 (p99 5.89 → 3.90 ms) | 1.128 (p99 5.54 → 3.73 ms) |
  | inline 16 and 64 KiB | 1.027 and 0.979 | 1.064 and 1.066 |
  | 100 B and hello | 0.987 to 1.019 | 0.938 to 1.003 |

  - The gil inline 100 B result is 0.938, but its range reaches 1.001 and core 15 was
    busy up to 100% in that round.
  - Core 15 was busy up to 100% in several rounds (ft LoopThread 64 KiB; gil LoopThread
    16 and 64 KiB, inline 100 B and 16 KiB), from another user's process.

- **Verdict:** the fusion's large LoopThread gains in section 11 (×1.36 to ×2.45) were
  heap read buffers, which the leases now remove.
  - Beyond every non-fusion fix, the fusion adds the saved copy:
    - on gil, 3-10% in process and 6-13% on the network for 16 and 64 KiB bodies;
    - on ft, nothing in throughput, and it costs 3-5% in process;
    - a lower LoopThread p99 on both images (64 KiB: about 34% lower).
  - That doesn't pay for its unsoundness (section 10: it relies on CPython's GC and
    allocator internals). It stays unlanded.

## 13. Slotted Go-owned `bytes` without a reuse grace (2026-11-20): prototype, not landed

- **The idea (user):** reuse freed fused memory the way CPython's own allocator does on
  3.14t, without waiting: a freed block is reused at once, but only as another object of
  the same size class, so a header address stays a header address. Only a class change
  needs a grace.
- **Prototype:** `.agents-workspace/tmp/r5/arena/proto18`, a copy of `proto17`
  (section 12), which was still the shared tree (`tree16`) plus the fusion patch.
  - Images `p18img-{ft,gil}` are built from its glue by `bin/pythago-dist-p18`, with the
    linux/arm64 prebuilt glue objects removed in the copy. Workspaces are
    `ws/proto18-{ft,gil}.work`.
  - `proto18.patch` is `diff -ruN tree16 proto18`, without `glue/prebuilt`.
  - The shared tree's code is unchanged.

### What the 3.14.10 source says (verified)

Source: `.agents-workspace/tmp/race314/src/Python-3.14.10`.

- **mimalloc keeps a freed object's refcount fields:**
  - `tstate_mimalloc_bind` (Python/pystate.c:3222-3243) sets the object heaps'
    `debug_offset` to `offsetof(PyObject, ob_type)`: "We may access ob_tid and the
    refcount fields in the dict and list lock-less accesses, so they must remain valid
    for a while after deallocation".
  - The debug fill skips those bytes (`mi_debug_fill`, Objects/mimalloc/alloc.c:25).
  - The free-list link itself is at offset 0, over `ob_tid` (`mi_block_t`,
    Include/internal/mimalloc/mimalloc/types.h:250, `mi_block_set_next`, internal.h:883). The refcount
    fields at 8 to 23 are kept.
- **Only page reuse waits:**
  - A freed block goes back to its page's free list and is reused for the same block
    size.
  - An all-free page leaves its size class (`_mi_page_free`) only once its QSBR goal is
    reached (`_PyMem_mi_page_maybe_free`, Objects/obmalloc.c:161-191). The object heaps
    have `page_use_qsbr = true` (pystate.c:3244-3246).
- **What a freed bytes header holds:**
  - Freed by its owner (local count to 0 with a shared count of 0):
    `_Py_MergeZeroLocalRefcount` (Objects/object.c:436) deallocates at once. That leaves
    `ob_ref_local` 0, `ob_ref_shared` 0 and `ob_tid` the owner, which is the thread doing
    the free.
  - Otherwise the object was merged first: `ob_tid` 0 (stored before the merge, :450),
    then `ob_ref_shared` `_Py_REF_MERGED` (3) with a count of 0 (:453-462; the brc queue's
    merge via `_Py_ExplicitMergeRefcount`, :467, does the same).
- **What a stale lock-free reader does:**
  - `_Py_TryIncrefFast` (Include/internal/pycore_object.h:550) writes `ob_ref_local` only
    if `ob_tid` is the reader's thread, and returns 1 without writing if the local count
    is `UINT32_MAX` (immortal).
  - `_Py_TryIncRefShared` (:570) returns 0 without writing when `ob_ref_shared` is 0 or
    `_Py_REF_MERGED`, and CASes otherwise.
  - `_Py_TryIncrefCompare` (:595) decrefs again if `*src` changed; it does not compare
    after the fast path.
  - `_Py_TryIncrefCompareStackRef` (Include/internal/pycore_stackref.h:792) returns 1
    without any check when `ob_gc_bits` has `_PyGC_BITS_DEFERRED` (bit 6).
  - Users include `list_get_item_ref` (Objects/listobject.c:369) and the dict lookups.
- **So a dead header refuses a stale incref without a write**, whatever its source holds:
  the local count is 0, `ob_tid` is 0 or the freeing thread (never the reader: a reader is
  mid-read in straight-line C and cannot be the thread that frees or adopts), the shared
  count is 0 or merged, and `ob_gc_bits` is 0.
- **Adoption is the same as mimalloc's reuse:** `new_reference` (Objects/object.c:2634)
  writes `ob_tid`, `ob_flags`, `ob_gc_bits`, `ob_ref_local = 1` and `ob_ref_shared = 0`
  with plain stores, as for any `PyObject_Malloc`ed object. A reader that meets the new
  object increfs a live header, sees its source changed, and decrefs.
- **The grace for a class change:**
  - `gc_collect_main` calls the "start" callback before `gc_collect_internal`, whose first
    statement is `_PyEval_StopTheWorld` (Python/gc_free_threading.c:2194, 2353, 2422).
  - `PyGC_Collect` (:2660) returns 0 without a collection, a stop-the-world or callbacks
    when gc is disabled, or (:2332) when a collection is already running.

### Design

- **Chunks and classes:**
  - The region is 64 MiB of 1 MiB chunks. A chunk in use has one size class and equal
    slots: header (to `ob_sval`), payload capacity, NUL, rounded to 16 bytes.
  - 44 classes by payload capacity: 16 to 128 bytes in steps of 16, then four per
    doubling up to 64 KiB.
  - Bodies under 2 bytes become the singletons; bodies over 64 KiB, or with no memory
    left, are copied as before.
  - Capacities rather than slot sizes are geometric, so power-of-two bodies fit exactly:
    16 KiB takes a 16448-byte slot on ft (63 per chunk, 1.2% tail), 64 KiB a 65600-byte
    slot (15 per chunk, 6.2% tail).
- **The invariant:**
  - Once a slot has held an object, only `pythago_bytes_adopt` (a whole new header at
    the slot's start) writes its bytes before `ob_type`.
  - Go writes payload only from `ob_sval` on.
  - The free lists' links live in a Go array, not in slots.
- **Free (C hook, every thread):**
  - It stamps the chunk with the epoch (an atomic max), pushes the slot, then decrements
    the chunk's live count (release).
  - The slot index is found without a division: the odd part's inverse mod 2^32, since
    offsets are exact multiples of the slot size.
  - The free list is a Treiber stack per chunk: head = tag << 32 | (index + 1), with
    links in Go memory.
    - C only pushes (a CAS loop); it never takes a lock, so it cannot block on a lock Go
      holds while Go calls into Python.
    - Go pops, under its allocator's mutex, so there is one popper. The tag makes ABA
      impossible anyway.
    - Per-chunk stacks make reclaiming a chunk trivial: at live 0 every slot is on its
      own stack.
  - `_PyBytes_Resize` (realloc) copies the object out and frees the slot the same way.
    The old header stays as it was, as mimalloc's realloc leaves it.
- **Take (Go):** the class's current chunk is tried first (pop, then carve the next
  unused slot), then its other chunks, then a chunk from the pool.
- **Class change:**
  - It happens only when the pool is empty: Go reclaims chunks of other classes that have
    live 0 and, on 3.14t, either no Python free since they were assigned or a stamp below
    the safe epoch.
  - If none qualifies but some wait only for a grace, a background goroutine runs
    `PyGC_Collect` (outside the mutex), and the body is copied meanwhile.
  - The `PyFunction_SetDefaults` forced grace of proto11/17 is gone.
  - On 3.13 the same code reclaims at live 0 at once.
- **Read path (asgi):**
  - A known length up to BodyChunk takes its exact class.
  - An unknown length starts at 4 KiB. A full slot is probed for one more byte; if the
    body goes on, it moves to a slot four times larger (a copy, counted as a move), up to
    BodyChunk.
  - `receive()` chunks of a known-length body take the class of what is left.
  - LoopThread posts go through `py.From` as in proto17.

### Tests (scratch) and controls

- **Carried over and passing on both images:** `TestFusedBytesBehaviour` (now 2 B to
  64 KiB, across class boundaries), `Singletons`, `Resize`, `Tracemalloc`.
- **New tests, passing on both images:**
  - `Classes`: every length maps to the smallest class that holds it.
  - `SameClassReuse`: a freed slot is taken again at once, with no collection, and
    taking it and writing its payload leaves its header bytes as Python left them.
  - `Grow`.
  - `ClassChangeGrace`.
  - `CollectFallback` (3.14t): with the region exhausted, one background `PyGC_Collect`
    brings the chunk back.
  - The key tests passed 5 runs in a row on each image.
- **`TestFusedBytesStaleReader` (3.14t, the deterministic test):**
  - It runs CPython's real `_Py_TryIncrefCompare` and `_Py_TryIncrefCompareStackRef`,
    compiled from the PBS internal headers with `Py_BUILD_CORE` in a test-only glue file
    (`glue/pythago_fusiontest.c`).
  - The reader runs on a Python thread that neither freed nor adopted the object, which
    the test asserts through `_Py_IsOwnedByCurrentThread`.
  - The object is held only by a list; the list drops it.
    - (a) The slot is freed.
    - (b) Go takes the slot again (LIFO, asserted) and adopts a new object, owned by
      another thread. In a second run the reader's thread also holds a reference, so
      `ob_ref_shared` is 0x5 and the CAS and decref run.
    - (c) Go takes the slot again and writes a 0xa5 payload, not adopted.
  - Each stage runs the reader with the real list slot as `src`. Stages (a) and (c) also
    run it with a `src` that still holds p, so the dead header alone must refuse.
  - The whole 1 MiB chunk is compared across the reader's calls.
  - Result: every call returned 0, and no byte changed.
    - (a) saw `ob_ref_shared` 0x0 and (c) saw 0x3 (merged), so both dead states are
      covered.
    - In (b), with a shared reference, the CAS and decref left the chunk byte for byte
      as it was.
- **Controls**, each in its own scratch copy (`control18.py`, `specs18/`); glue controls
  get their own image:

| control | result | diagnostic |
|---|---|---|
| c1: Go writes the payload from the slot's start | fails | `(c) taken, payload written, a source still holding p: the stale reader's incref succeeded (_Py_TryIncrefCompare 1, _Py_TryIncrefCompareStackRef 1)` and `changed the chunk at slot offset +16 (a5 a5 ... -> a9 a5 ...)` |
| c1c: the free-list link also stored in the slot over `ob_ref_shared` | fails | stage (c), `ob_ref_shared before 0x200000000`, `incref succeeded (1, 1)`, `changed the chunk at slot offset +16 (00 00 00 00 02 00 00 00 -> 04 00 00 00 02 00 00 00)` |
| c2: the hook fills the freed header with 0xdd | fails | already in (a), with the real list slot: `incref succeeded (_Py_TryIncrefCompare 0, _Py_TryIncrefCompareStackRef 1)`, because the fill sets the deferred bit; with the other `src`: `slot offset +16 (dd dd ... -> e1 dd ...)` |
| c3: a chunk changes class without a grace | fails | `a chunk whose objects Python freed changed class before any collection`, then `a stale reader of a freed object in it now: _Py_TryIncrefCompare 1, the old header's bytes (class B payload) a5 ... -> ... a9 a5 ...` |
| c1b: the link stored at offset 0 (`ob_tid`), as mimalloc does | passes, as predicted | a link in `ob_tid` is harmless: the reader only compares it with its own thread id |
| c2b: the hook zeroes the freed header | passes, as predicted | zeros are a valid dead header (local 0, shared 0, tid 0) |

- **What the controls show:**
  - The test catches Go data or allocator links in bytes 8 to 23, and nonzero garbage in
    the header.
  - It does not flag the two harmless variants.
  - Stage c2 is a use-after-free in the deferred-refcount reader even with the real
    source.
- **The lock-free stress test** (`TestFusedBytesLockFreeStress`) passes. As expected,
  it also passes under c2 (16 million takes) and under c3 (a 4 MiB region, 4 class
  changes): it cannot fail either way, and the deterministic test replaces it.
- **Suites with the fusion on, both images:** py, aio, wsgi, and asgi inline and
  `PYTHAGO_ASGI_LOOP_THREAD=1` all pass. aio's and wsgi's TestMain install the fusion in
  the copy.
  - asgi exercised it: about 3500 takes per run, 14 growth moves, 18 chunks, 0
    fallbacks.

### Measured

Setup:

- go1.26.11, linux/arm64, PBS 20261110 (3.14.10t and 3.13.18).
- Binaries: `bin/t5/{asgi,asgiserve}-{tree16,p17,p18}-{ft,gil}`. tree16 and p17 are
  section 12's.
- The three variants are interleaved, and their order rotates every round.
- The host was shared: other users' compiles and a `J2-php2gotest` ran on many cores.

**In process** (`bench18.sh`):

- `BenchmarkUpload`, `InlineRequest` and `LoopThreadRequest`; ft on core 5, gil on
  core 6.
- 5 rounds. 1 of 30 runs dropped (gil p17, round 1, 2.9% foreign CPU).
- Ratios are ns/op against the tree:

| case | ft p17 | ft p18 | gil p17 | gil p18 |
|---|---|---|---|---|
| inline 100 B | 1.020 | 1.002 | 1.021 | 1.009 |
| inline 16 KiB | 1.060 | 0.958 | 0.962 | 0.951 |
| inline 64 KiB | 1.073 | 0.869 | 0.896 | 0.878 |
| inline chunked 16 KiB | 1.064 | 0.986 | 0.957 | 0.979 |
| LoopThread 100 B | 1.010 | 0.991 | 1.010 | 0.999 |
| LoopThread 16 KiB | 1.040 | 0.983 | 0.982 | 0.975 |
| LoopThread 64 KiB | 1.073 | 0.918 | 0.939 | 0.932 |
| LoopThread chunked 16 KiB | 1.047 | 0.999 | 0.976 | 0.989 |
| hello (inline, LoopThread) | 1.004, 1.000 | 0.991, 1.001 | 1.003, 1.005 | 0.993, 0.995 |

- The chunked case pays one move, a 4 KiB copy and 2 allocations, so it gains least.

**Network** (`load18.sh`):

- `asgiserve` with GOMAXPROCS=1 on core 5, both images, as in section 12;
  `httpload -c 64 -d 5s -w 2s` on cores 6 and 16; `bench/uvicorn/app.py`; hello is
  `GET /`.
- 5 rounds. Rounds with more than 2% foreign CPU on core 5 were dropped. Kept 5/5 except
  ft hello 4/5; gil hello, 100 B and 16 KiB inline 4/5; gil LoopThread 64 KiB 3/5; gil
  inline 64 KiB 2/5.
- **Core 15 (core 5's sibling) was 82 to 100% busy in every configuration**, from other
  users' processes. That noise hits all three variants alike, since they are
  interleaved, but it makes absolute numbers and p99 less reliable.
- req/s ratios against the tree (median, with the round range), and p99 in ms for tree,
  p17 and p18:

| case | ft p17 | ft p18 | ft p99 | gil p17 | gil p18 | gil p99 |
|---|---|---|---|---|---|---|
| hello inline | 1.006 | 1.011 (0.975-1.042) | 2.50 / 2.44 / 2.38 | 1.011 | 1.018 (0.970-1.028) | 2.23 / 2.21 / 2.21 |
| inline 100 B | 0.991 | 1.041 (0.983-1.054) | 2.79 / 2.75 / 2.58 | 0.994 | 1.021 (1.003-1.075) | 2.40 / 2.35 / 2.29 |
| inline 16 KiB | 1.006 | 1.093 (1.064-1.100) | 3.13 / 3.19 / 2.87 | 1.040 | 1.055 (1.043-1.106) | 2.65 / 2.68 / 2.49 |
| inline 64 KiB | 0.976 | 1.172 (1.119-1.189) | 3.99 / 3.94 / 3.30 | 1.116 | 1.197 (1.149-1.244) | 3.05 / 2.77 / 2.64 |
| hello LoopThread | 1.019 | 1.038 (0.986-1.067) | 3.89 / 3.64 / 2.41 | 1.003 | 1.032 (0.998-1.055) | 2.52 / 2.32 / 2.29 |
| LoopThread 100 B | 1.003 | 1.018 (0.999-1.039) | 6.72 / 7.76 / 2.45 | 0.979 | 1.023 (0.976-1.032) | 2.67 / 3.37 / 2.04 |
| LoopThread 16 KiB | 0.995 | 1.087 (1.044-1.104) | 4.59 / 6.19 / 3.08 | 1.066 | 1.058 (0.996-1.091) | 3.88 / 2.94 / 2.37 |
| LoopThread 64 KiB | 0.945 | 1.173 (1.088-1.306) | 8.43 / 5.73 / 3.57 | 1.091 | 1.167 (1.153-1.174) | 6.09 / 3.75 / 2.43 |

- No errors.
- p18 against p17: 1.08 to 1.19 on ft and 1.02 to 1.07 on gil for 16 and 64 KiB bodies.
- The hello ranges span 1.0. The hello and 100 B LoopThread p99 differences are most
  likely the core-15 noise, since hello takes no slot.

**Hook tax** (`tax18.sh`):

- The same p18 binaries with `PYTHAGO_FUSION` 0 against 1, 6 rounds, ft core 5, gil
  core 6; 1 of 48 runs dropped.
- `bytes(64)`, str concatenation and class-instance loops: ft 1.006, 1.012 and 1.009;
  gil 1.012, 1.013 and 1.008. InlineRequest (hello): 1.001 and 1.004.
- That is section 10's tax: frees outside the region take the same range check and
  chained call. The slotted work (stamp, push, live count) runs only for fused frees,
  and it is inside the upload numbers above.

**Memory** (the p18 network runs, read through SIGUSR1):

- Region resident (mincore), per configuration:

  | body | inline | LoopThread |
  |---|---|---|
  | 100 B | 4 KiB | 12 KiB |
  | 16 KiB | 20 KiB | 2.0 MiB (2 chunks) |
  | 64 KiB | 70 to 200 KiB | 4.9 MiB (5 chunks, at most 75 bodies in flight) |

  - Inline reuses one hot slot: Reuses = Takes - 1.
- No class changes, no `PyGC_Collect`, no fallbacks and no moves in any benchmark run.
- **Class waste** (payload capacity over body bytes): 100 B bodies take the 112-byte
  class (1.12, 10.7% of capacity unused). 16 and 64 KiB are exact (1.000).
  - The slot and chunk-tail overhead is separate: 176-byte slots for 100 B on ft, and
    1.2% or 6.2% chunk tails for 16 or 64 KiB.
- **Exhaustion** (`exhaust18.sh`):
  - Four concurrent `httpload -c 16` runs of 100 B, 16 KiB, 64 KiB and 200000 B uploads,
    10 s, with 2, 4 and 64 MiB regions.

  | config | PyGC_Collect | class changes | fallbacks | moves |
  |---|---|---|---|---|
  | ft inline 2 MiB | 3922 | 405 | 11217 | 0 |
  | ft inline 4 MiB | 155 | 287 | 429 | 48 |
  | ft LoopThread 2 MiB | 3361 | 4581 | 90687 | 6 |
  | ft LoopThread 4 MiB | 198 | 232 | 1953 | 0 |
  | gil inline 2 MiB | 0 | 27352 | 746 | 56 |
  | gil LoopThread 2 MiB | 0 | 157289 | 5142 | 50 |
  | any 64 MiB | 0 | 0 | 0 | 0 |

  - ft at 2 MiB loses about two thirds of its small-body throughput to the collections.
    At 4 MiB it keeps 80 to 90%.
  - No errors anywhere.
  - Moves appear only after a fallback, which stops `receive()` from tracking the
    remaining length.
  - The skewed per-size throughput of inline mode under mixed loads is the same with the
    tree's binary, so it is not the fusion.

### Assessment

- **The design holds**, for the CPython internals below.
  - Reuse needs no grace on 3.14t.
  - A deterministic test, run with CPython's own readers, shows a freed, reused or
    rewritten slot never lets a stale reader write.
  - Its controls fail on every unsound variant tried, and not on the two harmless ones.
  - Class changes are the only grace, and at 64 MiB they never happened under load.
- **Against proto17:** strictly better.
  - No forced stop-the-world (proto17's `PyFunction_SetDefaults`, an undocumented
    implementation detail).
  - Faster: +8 to 19% req/s on ft and +2 to 7% on gil for 16 and 64 KiB bodies, and
    5 to 15% less time in process on ft.
  - Lower p99.
  - LIFO reuse keeps memory resident at the peak in-flight bodies per class (kilobytes
    inline) instead of a bump region.
- **Against the tree with leases:**
  - **Gains:** 16 and 64 KiB bodies gain 9 to 17% req/s on ft and 6 to 20% on gil, with a
    lower p99 (64 KiB LoopThread: 8.43 → 3.57 ms ft, 6.09 → 2.43 ms gil), and 4 to 13%
    less time in process. Bodies of 100 B and below are at parity.
  - **Costs:**
    - the free hook on every object free (0.6 to 1.3%);
    - a second allocator;
    - glue functions, with no struct ABI change in the prototype;
    - an mmap region to port to darwin and Windows;
    - unconverted posts pinning their slots;
    - a cliff when the region is too small (ft: one `PyGC_Collect` per few
      milliseconds).
- **What it relies on, and how to re-check each release:**
  1. **Freed objects keep bytes 8 to `ob_type`.** Nothing between `tp_free`'s caller and
     our hook writes them, `ob_ref_local`/`ob_ref_shared` are where the reader looks, and
     a dead header has local 0 and shared 0 or `_Py_REF_MERGED`.
     - Check: run `TestFusedBytesStaleReader` against the release's headers (it
       compiles them), and read `_Py_MergeZeroLocalRefcount` and `new_reference`.
  2. **The readers stay header-only.** `_Py_TryIncref*` touch only the bytes before
     `ob_type`, and the deferred bit of `ob_gc_bits` is 0 on bytes.
     - Check: diff `pycore_object.h` and `pycore_stackref.h`. The test exercises both
       readers.
  3. **CPython keeps relying on mimalloc's `debug_offset`** (pystate.c), i.e. it still
     reuses blocks within a size class without QSBR. Our soundness is the same argument.
     - Check: grep for the comment and `page_use_qsbr`.
  4. **gc callbacks bracket a stop-the-world** (`gc_collect_main` /
     `gc_collect_internal`), for the class-change grace only.
  5. **`bytes` are freed through `PyObject_Free`**, and `_PyBytes_Resize` through
     `PyObject_Realloc`, both seen by `PyMem_SetAllocator(PYMEM_DOMAIN_OBJ)`, on both
     versions.
- **Unsound or untested:**
  - **Interleavings are untested.** The test checks each state a stale reader can meet,
    not a reader racing an adoption half-written. That case rests on the source
    argument: the old and the new header both read local 0 or another thread's tid, and
    shared 0 or merged.
  - **A moved (realloc'ed) object's old header looks live.** It has local 1, or a merged
    nonzero shared count. If a stale reader CASes that count and Go reuses the slot
    before its decref, the adoption's `ob_ref_shared = 0` loses the reader's +4 and its
    decref then queues the new object. mimalloc's realloc has the same window, so this
    is not worse than CPython. It needs a uniquely referenced bytes that a container held
    a moment earlier.
  - **If gc is disabled,** `PyGC_Collect` gives no grace, and an exhausted region falls
    back to copies until a collection happens.
- **The fusion is not landed.** The user decides.

## 14. Landing plan for the slotted fusion as an opt-in (2026-11-20): landed, see section 15

The user asked for a plan to land section 13's design (proto18) as an opt-in, ported to
darwin and Windows. They accepted the proposed defaults and chose a Go heap slice for the
region (step 2).

### Shape

- **One switch, off by default:** `py.Config.FusedBytes int`, the region size in bytes
  (0: off). When it is off, nothing is installed: no allocator hook, no region and no
  gc callback, so programs that don't opt in pay nothing.
- **No new public type in v1.** The allocator moves into a new internal package,
  `internal/fusion`. It is pure Go: the region, size classes, chunk metadata and the Go
  side of the free stacks, so it can be unit tested without Python.
  - py installs the glue hook and the gc callback, and adopts a `*fusion.Bytes` in
    `From` (as it now recognizes `lease.Bytes`).
  - asgi takes bodies from `internal/fusion`.
  - A public `py.FusedBytes` can come later, once the design has run in production.
- **asgi follows py:** when the fusion is on, request bodies use it, with no asgi option.
  When a body gets no slot (oversize, region exhausted, a subinterpreter), the landed
  lane and lease paths run as today.
- **Supported versions, fail closed:**
  - The glue reports whether it was compiled against a verified CPython (`PY_VERSION_HEX`):
    - 3.13 with the GIL;
    - 3.14 free-threaded.
  - On any other version, or if the region can't be reserved, `Init` returns an error.
    Opting in never silently does nothing.
  - A new minor version (3.15) is added only after the stale-reader test passes against
    its headers, together with the section 13 per-release checklist.

### Steps

1. **`internal/fusion`, the allocator:**
   - port proto18's classes, chunk metadata, Treiber stack pop and push, class assignment
     and reclaim;
   - remove the `PYTHAGO_FUSION*` variables and the scratch counters, keeping the
     counters tests need behind an internal accessor;
   - unit tests without Python: class mapping and capacities, concurrent push and pop
     under stress, and reclaim only at live 0 and past the stamp.
2. **The memory: per-chunk Go allocations (user decision, 2026-11-20; it replaced "one
   heap slice pinned for the life of the process" the same day).**
   - Each 1 MiB chunk is its own `make([]byte, ChunkSize)`, made when a class needs one.
     `Config.FusedBytes` is the cap on the chunks held, not an allocation made up front.
   - Each chunk has its own `runtime.Pinner`, pinned while it is registered for the C hook.
   - **A chunk goes back to Go** when it has no live slot and is past its grace (a
     stop-the-world began after its last Python free; at once with the GIL): the
     condition a class change already waits for. It is unregistered from the hook's
     lookup, unpinned and dropped, and Go's GC reclaims it.
   - **Idle chunks:** a few empty chunks stay idle, with a decay like `internal/arena`
     SharedLane's keep/idle, so a steady load does not churn allocations. An idle chunk is
     past its grace and can take any class: a class change is "reuse an idle chunk past its
     grace, or release and allocate".
   - **The hook's lookup** can no longer check one contiguous range. A bounds prefilter
     (atomics for the lowest and highest chunk addresses) rejects most frees at one
     compare; then a lock-free table keyed by the 1 MiB address window (`p >> 20`). A chunk
     is only 8 KiB-aligned, so it may span two windows: both are registered, and the entry
     found is checked with `p - base < size`. C reads it with a constant number of loads
     and no lock; Go writes it under its mutex.
   - **Ordering:** C only looks up pointers to live objects in registered chunks, and Go
     unregisters a chunk only at live 0 and past its grace, so there is no lookup race for
     its objects.
   - **Why per chunk rather than one pinned slice** (measured, go1.26.11, linux/arm64, a
     64 MiB region against no region, in `.agents-workspace/tmp/heapregion`):
     - The pacer counts a slice as live in full. The heap goal went from 4 to 128 MiB,
       and 125 MiB of garbage ran 1 GC instead of 30.
     - Pages are resident only when touched: +0.9 MiB after `make`, +4.1 MiB after
       touching 4 MiB.
     - A slice allocated after a dropped large buffer reuses those already-resident
       pages, and Go can't return them to the OS while the slice holds them.
     - It counts against `GOMEMLIMIT` in full.
     - Per-chunk allocations cost the pacer and `GOMEMLIMIT` only the chunks in use, and
       give them back once idle.
   - **README guidance:** a cap of 8-16 MiB. Section 13's loads used 2 to 4.9 MiB at 64
     connections.
   - The `mincore` resident-memory count stays test-only and Linux-only.
3. **Glue:**
   - **`pythago_py.c`:** proto18's free and realloc hook, `pythago_bytes_adopt`, the
     header offsets, `pythago_fusion_supported`, and a query reporting whether the
     current OBJ-domain `free` is ours (used by tests).
   - **`pythago_fusioncheck.c`** (new): the real `_Py_TryIncrefCompare` and
     `_Py_TryIncrefCompareStackRef` helpers, compiled with `Py_BUILD_CORE`.
     - It ships in every image, because the tests run against the same glue.
     - Init runs it once as a self-check on a scratch slot, failing closed: the freed and
       not-yet-adopted states must refuse without writing.
   - **darwin:** `cmd/pythago-dist/darwin.go` builds the glue as one unity file, where
     `Py_BUILD_CORE` would leak into the other sources. Compile the check file as its own
     object.
   - **ABI bump:** raise `PYTHAGO_PY_ABI` and `glueABI` (`py/capi.go`) together.
   - **Rebuild `glue/prebuilt`:** run `ci/glue.sh` for Linux and Windows (with the Wine
     SDK). darwin's objects need `ci/darwin.sh glue` on the Mac.
4. **py:**
   - **Install the hook between the init phases**, after `cInitializeCore` and before
     `cInitializeMain` (`py/runtime.go`, around lines 482-499).
     - The reason is tracemalloc. `-X tracemalloc` / `PYTHONTRACEMALLOC` starts it in the
       main phase (`Python/pylifecycle.c:1233`). `tracemalloc.stop()` restores the
       allocator it saved at start (`Python/tracemalloc.c:841, 873`). If it started before
       our hook, stopping it would remove the hook, and the next free of a fused object
       would go to mimalloc.
     - Installed before it, tracemalloc saves ours and restores ours.
   - **Register the gc callback after the main phase.** Until it is registered no grace
     is published, so chunks only cannot change class, which is the safe direction.
     Clearing `gc.callbacks` has the same, fail-closed, effect.
   - **Main interpreter only:** `From` copies when called in a subinterpreter. Its
     collections are not the main interpreter's stop-the-world, and its objects can't be
     shared anyway.
   - **Finalize** leaves the hook and the region in place.
5. **asgi:**
   - port proto18's integration: the inline first chunk, `receive()` chunks, and
     LoopThread posts through `From`;
   - exact classes for known lengths, and 4 → 16 → 64 KiB moves for unknown ones;
   - a request recycled with an unconverted body releases its slot.
6. **Tests:**
   - **py**, behind `PYTHAGO_TEST_FUSION=1`, which TestMain reads to set
     `Config.FusedBytes`:
     - port proto18's `TestFusedBytes*` tests: behaviour, singletons, resize,
       tracemalloc, the stale reader, class-change grace, collect fallback, same-class
       reuse, growth and the stress test;
     - new: started with `-X tracemalloc`, then `tracemalloc.stop()`, our hook is still
       installed and a fused free works;
     - new: with the fusion off, the OBJ allocator is untouched and no region exists;
     - new: Init fails cleanly on an unsupported version, by forcing the check.
   - **aio, wsgi and asgi:** TestMain sets the config from the same variable.
   - **`ci/unit.sh`:**
     - add a fusion pass: py and asgi in both loop modes, natively and under qemu, and
       aio and wsgi natively;
     - vet and gofmt `internal/fusion`.
   - **`ci/windows-wine.sh` and `ci/darwin.sh`:** a fusion pass for py and asgi, both
     variants.
7. **Controls, on the landed code, each in its own scratch copy:**
   - section 13's c1 (payload from the slot start), c1c (link over `ob_ref_shared`), c2
     (header filled on free) and c3 (class change without grace);
   - the hook installed after the main phase: the tracemalloc test must fail;
   - the hook installed with the fusion off: the off test must fail.
8. **Measure:** interleaved and pinned, as in section 13.
   - Fusion off against the tree before landing: expect 1.00 within noise.
   - Fusion on against off: expect section 13's ratios.
   - The hook tax with it on.
9. **Docs:**
   - **README:** what the option does, when it helps (bodies of 16 KiB and more), the
     costs (about 1% on every object free, an `Init` error on unverified versions), sizing
     (at least 4 MiB; section 13's 2 MiB cliff), the gc-disabled caveat, and the
     incompatibility with tools that replace the OBJ allocator without chaining it;
   - **PLAN:** the status;
   - **this document:** a section on what landed;
   - **JOURNAL and TODO.**
10. **Gate:** every tier, as for gates 5-7:
    - ci/unit.sh gil and ft, arm64 and amd64 (qemu);
    - ci/windows-wine.sh gil and ft on Wine 11.21;
    - ci/darwin.sh gil and ft on arm64 and amd64.

### Out of scope for v1

- wsgi bodies and aio channel `bytes` messages (both could use it later).
- A public `FusedBytes` API.
- Bodies over 64 KiB (they copy, as today), and growing the region at run time.
- Subinterpreters.

### Risks to watch while landing

- **Windows:** the glue is a DLL there, so check that `PyMem_SetAllocator` in it
  installs the hook that the interpreter's `PyObject_Free` calls.
- **`PYTHONMALLOC=debug`:** still unconfirmed in the embedded configuration (section 10).
  Test it, and refuse the fusion if the debug hooks change what our hook receives.

## 15. The slotted fusion as landed (2026-11-20): opt-in, `py.Config.FusedBytes`

Steps 1 to 9 of section 14 are done. Step 10 (the Wine and darwin tiers) is left to the
coordinator. The scratch work is in `.agents-workspace/tmp/r5/land`: `base` is the tree
before landing, `bench-*` the measured copies, `ctl-*` the controls, `*.sh` the scripts.

### What landed

- **`py.Config.FusedBytes int`:** the cap, in bytes, on the Go memory the fusion may hold
  (0: off). Off installs nothing: no hook, no allocator, no gc callback, no sweeper.
- **`internal/fusion`** (pure Go, no import of py):
  - 1 MiB chunks, each its own `make([]byte)`, pinned while registered; 44 classes
    (section 13); per-chunk Treiber free stacks with their links in a per-chunk Go array;
    class assignment, reclaim, release to Go.
  - The hook's lookup: the bounds prefilter, then a two-level radix table (14 + 14 bits
    over 48-bit addresses) of 1 MiB windows, each entry holding the cells of the at most
    two chunks that overlap it. The ordering argument is in the package comment.
  - The sweeper: once a second. A chunk empty and past its grace for 4 sweeps becomes
    idle; the newest 2 idle chunks go back to Go after 16 sweeps, the others after 4.
    Free-threaded, it runs `PyGC_Collect` when more than 2 empty chunks wait for a grace
    and no collection ran for 10 sweeps.
  - `ReadStats` (takes, moves, collections, collections run, class changes, chunks made
    and released, held, idle) and test accessors in `inspect.go`.
- **Glue (`PYTHAGO_PY_ABI` and `glueABI` 5):** in `pythago_py.c` the OBJ-domain free and
  realloc hook, `pythago_bytes_adopt` (NULL outside the main interpreter),
  `pythago_fusion_supported`, `_install`, `_hooked`, `_find`, `_bounds` and the header
  offsets. `pythago_fusioncheck.c` (new) holds CPython's readers, compiled with
  `Py_BUILD_CORE_MODULE`. `pythago_glue.c` gains `pythago_config_set_tracemalloc` (tests).
  All 12 `glue/prebuilt` objects were rebuilt and reproduce (`ci/glue.sh`, `ci/darwin.sh
  glue`).
- **py (`py/fusion.go`):**
  - `Init` fails before Python starts if the glue's CPython is not 3.13 with the GIL or
    3.14t.
  - The hook goes in between `cInitializeCore` and `cInitializeMain`.
  - After the main phase: the gc callback, which a forked child drops
    (`os.register_at_fork`); the startup check; then the sweeper.
  - `From` adopts a `*fusion.Bytes`.
- **asgi:** proto18's integration. The inline first chunk is released after
  `serve_inline` unless it was converted. A LoopThread post needs no lease. The first
  chunk and `receive()` chunks take exact classes for known lengths, 4, 16 and 64 KiB moves
  for unknown ones.
- **`asgiserve -fused-bytes N`.**

### Deviations from section 14, and why

1. **Per-chunk memory instead of one pinned slice** (the user's change during the work,
   step 2 as rewritten above).
2. **The bounds live in C static storage** (`pythago_fusion_bounds`), written by Go. Read
   through a pointer to Go memory they cost a dependent load on every free. Measured
   before the move: GIL `FreePath/inst` +2.8% against proto18 (45.7 against 44.45 ns,
   core 7). After it the median ratio did not change (1.031); see "Unresolved".
3. **Sweeps run only from the sweeper, not at each collection.** The first version swept
   at the gc callback's "stop", which a server rarely reaches: its gen0 count is
   allocations minus frees, which balance. The pacer runs showed no collection at all in
   3 s of `GET /`, so chunks were never given back after a load (heap goal 13.7 MiB
   against 4.2 MiB with the fusion off). With sweeps at every collection, the decay also
   ran at the pace of the application's collections. A first ticker with 2-sweep decay
   churned chunks under steady GIL load (13 made and 8 released in one 5 s run); with 4
   sweeps, and the extra idle chunks also waiting 4, no chunk was released during a load.
4. **darwin's glue stays one translation unit** (bao takes one object; ld64.lld has no
   `-r`). `pythago_fusioncheck.c` is included last, with `PYTHAGO_GLUE_UNITY`, and defines
   the core macros only before the internal headers, so the other sources parse
   `Python.h` as before. The two readers compiled this way are instruction for
   instruction the same as compiled alone (3.14t headers, aarch64, objdump).
5. **`Py_BUILD_CORE_MODULE`, not `Py_BUILD_CORE`:** the internal headers then reference
   no libpython thread-local (`_Py_tss_tstate`) and import data on Windows (dllimport).
6. **`-X tracemalloc` and `PYTHONTRACEMALLOC` never started tracemalloc in pythago** at
   landing. `PyConfig_InitIsolatedConfig` sets `config->tracemalloc = 0`, and the -X
   option and the variable are read only when it is -1 (Python/initconfig.c:1152, 2336 in
   3.14.10). Since 2026-11-20 `py.Main` sets it back to -1 with the command line
   (`pythago_config_set_argv`), so `-X tracemalloc` starts tracemalloc in the main phase,
   after the hook; `PYTHONTRACEMALLOC` stays ignored, as in `python -I`.
   `TestFusedBytesTracemallocAtInit` uses the real flag. `-X dev` is decided at
   preinitialization (`pythago_config_new` reads the command line) and puts the
   allocators' debug hooks under the fusion's hook (`TestFusedBytesDevMode`).
   `PYTHONMALLOC=debug` stays ignored: the isolated pre-config ignores it.
7. **Subinterpreters:** no Go function is reachable from one today (Go modules exist in the
   main interpreter only). The guard is in C (`PyInterpreterState_Get() !=
   PyInterpreterState_Main()`), and the test calls the conversion with a
   `Py_NewInterpreter` thread state current. Measured without the guard: no faster.
8. **The startup check** (free-threaded only; the GIL has no lock-free readers) adopts a
   3000-byte object on the init thread and frees it. A new Python thread then runs both
   readers on the freed slot and on the slot retaken with a payload written, comparing the
   whole slot. It costs one thread start at `Init`.
9. **A forked child drops the gc callback.** Found with the fusion on: every collection in
   a forkserver child printed

   ```
   Exception ignored in: <built-in method pythago_fusion_gc of int object at 0x12f4168>
   RuntimeError: Go functions are not available in a forked child process
   ```

### Tests

- **`internal/fusion` (no Python):**
  - `TestClasses`: lengths to classes, slot sizes and the division-free index for every
    slot of every class.
  - `TestReuse`.
  - `TestConcurrentPushPop`: 8 goroutines, 100 000 rounds each, frees through a Go copy
    of the C hook that reads the same table.
  - `TestReclaimGrace`, `TestReclaimUnstamped`: live 0 and the stamp.
  - `TestLookupStraddle`, `TestTableShared`.
  - `TestRelease`: unregistered, the bounds shrink, and a weak pointer to the chunk dies
    after `runtime.GC`.
  - `TestIdleKeep`, `TestSweeper` (driven ticks).
  - All pass under `-race`.
- **py, `PYTHAGO_TEST_FUSION=1` (16 MiB):**
  - section 13's `Behaviour`, `Singletons`, `Resize`, `SameClassReuse`, `StaleReader`,
    `ClassChangeGrace`, `CollectFallback`, `Grow`, `LockFreeStress` and `Tracemalloc`
    (which also checks that `stop()` leaves the hook);
  - new: `ConvertOnce`, `Release`, `Straddle`, `Sweeper`, `Subinterpreter`;
  - `TracemallocAtInit`: a child, tracemalloc started in the main phase, then stopped;
    the hook is installed, a fused free reaches it and the slot is taken again;
  - `Off` (runs with the fusion off);
  - `Unsupported`: a child with the check forced; also runs with the fusion off.
- **asgi `TestFusedBodies`:** 1 B to 200 000 B, known and unknown lengths, both modes,
  checked by SHA-256. One take per 64 KiB chunk, moves for unknown lengths, and no slot
  live afterwards.
- **aio and wsgi:** TestMain reads the same variable.
- **CI:**
  - `ci/unit.sh` adds the fusion passes: py and asgi in both modes natively and under
    qemu, aio and wsgi natively; it vets, gofmts and tests `internal/fusion`.
  - `ci/windows-wine.sh` and `ci/darwin-host.sh` add py and asgi, both modes.
  - `ci/unit.sh gil` and `ft` passed, arm64 and amd64 (qemu).

### Controls (each in its own copy, `.agents-workspace/tmp/r5/land/ctl-*`)

| control | caught by | diagnostic |
|---|---|---|
| c1: payload from the slot's start | `Init` (ft) | `startup check: a retaken slot: a stale reader's incref succeeded (_Py_TryIncrefCompare 1, _Py_TryIncrefCompareStackRef 1)` |
| c1, startup check off | StaleReader (c) | `the stale reader changed the chunk at slot offset +16 (a5 a5 a5 a5 a5 a5 a5 a5 -> a9 a5 a5 a5 a5 a5 a5 a5)` |
| c1, GIL | SameClassReuse | `taking the slot and writing its payload changed its header: a5 a5 a5 a5 a5 a5 a5 a5, was 00 00 00 00 00 00 00 00` |
| c1c: the link over `ob_ref_shared` | `Init` (ft) | `startup check: a freed object: a stale reader's incref succeeded (_Py_TryIncrefCompare 1, _Py_TryIncrefCompareStackRef 1)` |
| c1c, startup check off | StaleReader (a), (c) | `ob_ref_shared before 0x600000000`, `slot offset +16 (00 00 00 00 06 00 00 00 -> 04 00 00 00 06 00 00 00)` |
| c2: the freed header filled with 0xdd | `Init` (ft) | `startup check: a freed object: a stale reader's incref succeeded (...)` |
| c2, startup check off | StaleReader (a) | `the list slot: the stale reader's incref succeeded (_Py_TryIncrefCompare 0, _Py_TryIncrefCompareStackRef 1)`; `slot offset +16 (dd dd ... -> e1 dd ...)` |
| c3: a class change without a grace | ClassChangeGrace (ft) | `a chunk whose objects Python freed changed class before any collection`; `a stale reader of a freed object in it now: _Py_TryIncrefCompare 1, ... -> ... a9 a5 ...` |
| crel: a sweep releases before the grace | Release (ft) | `a chunk left its class with no collection after its last free (held false, class -1)` |
| cafter: the hook after the main phase | TracemallocAtInit (both) | `AssertionError: tracemalloc.stop() removed the fusion's hook` |
| coff: the hook with the fusion off | Off (both) | `the fusion's hook is installed with the fusion off` |
| csweep: no sweeper | Sweeper (both) | `the sweeper released 0 chunks, want at least 3` |
| cwin: a chunk registered in its first window only | unit TestLookupStraddle | `chunk at 0x514fc9198000: offset 0x68000 not found (window boundary at 0x68000)` |

- c3 and crel pass with the GIL, as they should: there is no grace there.
- c1c and c2 were run twice: before the bounds moved into C, and on the final glue, with
  the same results.

### Measured

Setup:

- go1.26.11, linux/arm64, PBS 20261110 (3.13.18, 3.14.10t), bao v0.0.8, LLVM 23.1.5.
- `base` is the pre-landing tree with ABI-4 glue. `off`, `on16` and `on64` are the landed
  tree with `FusedBytes` 0, 16 and 64 MiB. The variants are interleaved and their order
  rotates each round.
- No run was dropped (at most 2% foreign CPU on its core).
- Scripts: `bench19.sh`, `tax19.sh`, `load19.sh`, `pacer19.sh`; analysis
  `analyze19.py`, `analyze-load19.py`; results in `results19/`.

**In process** (`bench19.sh`; ft on core 5, gil on core 6; 5 rounds; final binaries):

| case | ft off/base | ft on16/off | ft on64/off | gil off/base | gil on16/off | gil on64/off |
|---|---|---|---|---|---|---|
| InlineRequest (hello) | 1.000 | 0.997 | 0.995 | 0.995 | 1.004 | 1.003 |
| LoopThreadRequest | 1.000 | 1.012 | 1.011 | 0.999 | 1.011 | 1.011 |
| inline 100 B | 0.998 | 1.031 | 1.032 | 1.002 | 1.036 | 1.043 |
| inline 16 KiB | 0.996 | 0.983 | 0.987 | 1.001 | 0.970 | 0.975 |
| inline 64 KiB | 0.995 | 0.890 | 0.895 | 1.002 | 0.898 | 0.895 |
| inline chunked 16 KiB | 1.000 | 1.008 | 1.005 | 0.989 | 0.994 | 0.992 |
| LoopThread 100 B | 0.998 | 1.019 | 1.020 | 0.995 | 1.014 | 1.012 |
| LoopThread 16 KiB | 0.998 | 0.993 | 0.986 | 0.994 | 0.992 | 0.990 |
| LoopThread 64 KiB | 0.999 | 0.929 | 0.930 | 0.995 | 0.948 | 0.940 |
| LoopThread chunked 16 KiB | 1.002 | 1.009 | 1.003 | 1.003 | 1.002 | 1.005 |

- Fusion off against the tree: 0.99 to 1.00.
- 100 B costs 2 to 4% in process, about 1.8% more than proto18 (gil core 7: off 4810,
  proto18 4930, landed 5020 ns/op).

**Network** (`load19.sh`):

- `asgiserve` with GOMAXPROCS=1 on core 5; `httpload -c 64 -d 5s -w 2s` on cores 6 and 16;
  `bench/uvicorn/app.py`; 5 rounds; no errors.
- Core 15 (core 5's sibling): median 2% busy, but up to 100% in some runs.
- This ran on the build before the sweeper. Its data and hook paths are the final ones,
  but sweeps then ran at gc callbacks, which happened 3 to 11 times per run.
- Median req/s ratio (round range), and median p99 in ms for base, off, on16 and on64:

| config | off/base | on16/off | on64/off | p99 ms |
|---|---|---|---|---|
| ft inline hello | 1.010 (0.969-1.063) | 1.012 | 1.004 | 2.24 / 2.23 / 2.21 / 2.21 |
| ft inline 100 B | 0.998 | 0.992 (0.987-1.044) | 1.008 | 2.32 / 2.30 / 2.29 / 2.30 |
| ft inline 16 KiB | 0.995 | 1.055 (0.985-1.079) | 1.050 | 2.58 / 2.62 / 2.50 / 2.50 |
| ft inline 64 KiB | 0.957 (0.949-1.032) | 1.121 (1.112-1.168) | 1.147 | 3.15 / 3.19 / 2.83 / 2.85 |
| ft LoopThread hello | 0.990 | 1.003 | 0.991 | 2.31 / 2.38 / 2.05 / 2.03 |
| ft LoopThread 100 B | 0.978 (0.969-0.992) | 0.999 | 1.013 | 2.49 / 2.92 / 2.08 / 2.16 |
| ft LoopThread 16 KiB | 0.995 | 1.091 (1.079-1.102) | 1.086 | 3.90 / 3.81 / 2.23 / 2.22 |
| ft LoopThread 64 KiB | 0.991 | 1.162 (1.133-1.164) | 1.146 | 5.91 / 6.08 / 2.35 / 2.43 |
| gil inline hello | 1.024 | 0.999 | 1.019 | 2.06 / 2.04 / 2.05 / 2.04 |
| gil inline 100 B | 1.008 | 0.981 (0.937-1.002) | 1.001 | 2.12 / 2.11 / 2.15 / 2.15 |
| gil inline 16 KiB | 0.984 | 1.047 (1.022-1.133) | 1.058 | 2.42 / 2.45 / 2.30 / 2.31 |
| gil inline 64 KiB | 1.009 | 1.136 (1.096-1.228) | 1.126 | 2.97 / 2.98 / 2.68 / 2.67 |
| gil LoopThread hello | 1.003 | 0.991 | 0.992 | 2.14 / 2.27 / 2.40 / 2.44 |
| gil LoopThread 100 B | 0.980 (0.963-1.015) | 1.012 | 1.012 | 2.32 / 2.62 / 2.10 / 2.17 |
| gil LoopThread 16 KiB | 0.990 | 1.096 (1.077-1.106) | 1.087 | 3.56 / 3.62 / 2.04 / 2.13 |
| gil LoopThread 64 KiB | 0.984 | 1.159 (1.140-1.191) | 1.155 | 5.68 / 5.96 / 2.44 / 2.42 |

- These are section 13's gains: +5 to 10% for 16 KiB, +12 to 16% for 64 KiB, and the
  LoopThread p99 down by more than half.
- 16 and 64 MiB give the same numbers: no load here needed more than 6 chunks.
- Off against base is within the round ranges. The lowest medians (ft inline 64 KiB
  0.957, LoopThread 100 B 0.978 and 0.980) have ranges reaching 0.99 to 1.03. The more
  precise in-process numbers put off at 0.99 to 1.00.

**Hook tax** (`tax19.sh`, 6 rounds, after the bounds moved into C; FreePath runs
`bytes(64)`, str concatenation and instance loops; `pon`/`poff` are proto18's binaries
with `PYTHAGO_FUSION` 1/0):

| case | ft on/off | ft proto18 on/off | ft on/proto18 on | gil on/off | gil proto18 on/off | gil on/proto18 on |
|---|---|---|---|---|---|---|
| FreePath bytes | 1.006 | 1.003 | 1.000 | 1.009 | 1.011 | 1.002 |
| FreePath instances | 1.008 | 1.009 | 0.999 | 1.028 | 1.011 | 1.031 |
| FreePath str | 1.009 | 1.014 | 0.996 | 1.018 | 1.020 | 1.000 |
| InlineRequest | 1.000 | 1.003 | 0.995 | 1.015 | 1.002 | 1.007 |

- The two-level lookup costs what proto18's single range check did, except GIL
  instances.
- There, landed off is itself 1.020 against base with no hook installed. Its runs are
  bimodal (43.5 to 45.3 ns), so about 2 of its 3 points are binary placement.
- The free hook's miss path is one load of the bounds and a compare before the tail
  call (objdump).

**Pacer** (`pacer19.sh`, final build):

- `asgiserve` with GOMAXPROCS=1; 64 KiB uploads, `-c 64`, 5 s.
- Go's heap goal / live heap in MiB, and the chunks held:

| image, mode | off: idle / load / 25 s after | on16: idle / load / 25 s after | chunks held (on16) idle / load / after |
|---|---|---|---|
| ft LoopThread | 4.0 / 20-33 / 33 | 4.5 / 16.5 / 16.5 | 1 / 6 / 0 (one collection run) |
| ft inline | 4.0 / 4.0 / 4.0 | 4.5 / 8.1 / 8.1 | 1 / 2 / 1 |
| gil LoopThread | 4.0 / 33 / 32 | 4.0 / 14.4 / 14.4 | 0 / 5 / 0 |
| gil inline | 4.0 / 4.0 / 4.0 | 4.0 / 6.0 / 6.0 | 0 / 1 / 0 |

- Idle, the fusion costs nothing with the GIL. Free-threaded it holds the startup
  check's chunk: +0.5 MiB of heap goal.
- Under LoopThread load the goal is lower with the fusion on (14-17 MiB against 20-34):
  bodies no longer pass through Go heap buffers.
- After the load every chunk but the idle keep went back to Go; free-threaded, the
  sweeper ran one collection for their grace.
- The heap goal shown is the last Go collection's: an idle Go process does not collect,
  so the dropped chunks are reclaimed at its next GC.
- 64 MiB gave the same as 16 MiB.

### Unresolved

- **The Wine and darwin tiers were not run here** (step 10). The scripts have fusion
  passes. Watch the Windows DLL's `PyMem_SetAllocator` reaching python3XY.dll's
  `PyObject_Free`: Resize and the live-count tests would show it.
- **100 B uploads in process: about 1.8% (90 ns) behind proto18, unexplained.** It is not
  the main-interpreter check (measured without it). Candidates: the larger
  `*fusion.Bytes` (80 against 48 bytes allocated), the release CAS after
  `serve_inline`. On the network, 100 B is at parity within noise.
- **GIL instance frees: about 1 point of tax over proto18,** after placement. No `perf`
  access on the host (`perf_event_paranoid` 4) to count instructions.
- **An `Init` that fails in the startup check leaves Python initialized,** as every
  other error after initialization does (Config.Setup's, for one).
- **A LoopThread post never converted (its loop closed) keeps its slot,** as a lease keeps
  its request.
- **Two idle chunks may stay held on free-threaded builds** until a collection runs: the
  sweeper forces one only when more than `idleKeep` chunks wait.
