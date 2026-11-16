# ASGI Inline Requests and Flat Combining

## Summary
By default `asgi` runs each HTTP request's application code on the goroutine (OS thread)
that serves it, as an eager asyncio task of a worker's loop, instead of handing it to the
loop thread. Requests that arrive while a worker is busy are run in the same batch by
whoever holds the worker (flat combining). This removed the per-request thread handoff
that made the loop-thread design lose to uvicorn on one core: 58.5k against 46.5k req/s
(uvicorn 72k), and it is what the 4-process result builds on.

## Key Facts
- **Inline is the default.** The zero `asgi.Options{}` serves inline;
  `Options.LoopThread` opts out (every request posted to the loop thread). `asgiserve
  -loop-thread` is the flag. WebSockets and lifespan always use the loop thread.
- **Cost of the default:** application code runs on whichever thread served the batch, so
  `threading.local`, `threading.get_ident` and `sqlite3`'s same-thread check see a
  per-batch thread, not a per-request one. contextvars are unaffected.
- **Requests are eager tasks:** `_Worker.serve_inline` sets the worker's loop as the
  running loop on the calling thread and starts each request with
  `asyncio.Task(..., eager_start=True)`, so `get_running_loop()`, `current_task()`, anyio
  and sniffio work. A request that completes synchronously never involves the loop thread.
- **Fallback to the loop thread is automatic:** a request awaiting anything the loop must
  deliver (timer, child task, more body, I/O) stays a pending task; `serve_inline`
  returns whether the loop thread has work and the goroutine calls `aio.Loop.Wake`.
- **One loop per worker, whatever the thread:** inline requests run on the worker's own
  loop, so loop-bound resources created at startup (asyncpg pools, `asyncio.Lock`s), which
  assume one loop and one thread at a time, keep working. Touching the loop from another
  thread without holding the worker is unsafe.
- **One worker lock** (`worker.busy` in `asgi/asgi.go`) serializes all Python of a worker,
  because asyncio is not thread-safe and free-threaded builds have no GIL to fall back on.
  The loop thread takes part through `aio.Hooks` (`Serve`/`Unlock`/`Lock`).
- **Flat combining, not GIL handoff:** under contention, hand off work instead of the
  lock. A request finding no combiner becomes one and runs every queued request in one
  `py.Do`; waiters park on a Go channel. No thread state ever moves between OS threads,
  so this is safe on free-threaded builds too (the lesson of the virtual-thread work).
  Both halves are needed: a Go lock makes waiters cheap (a goroutine wakeup), combining
  makes acquisitions rare.
- **In a Go-hosted combiner, every return from C into Go costs a P acquisition**; under
  load that is a scheduling wait while holding the critical section.
- **A dedicated (C-resident) combiner is slower** than flat combining: it reintroduces the
  handoff.
- **Deferred releases need a drain other than the loop thread** (`worker.release`); the
  release-path leak grew RSS by about 70 MB per 290k requests.
- **Responses still go through the `respond` callback,** not the batch result, for
  fidelity (errors at the call site, backpressure, first-byte timing, ordering).
- **Remaining one-core gap** to uvicorn is about 3 µs of user time per request: net/http
  (3.9 µs alone) plus the Go side of the request, against httptools and uvloop in C.
  Kernel time already matches.

## Details

### Why the loop-thread design cannot win on one core
After the FFI work (crosscall2 callbacks, decoded arguments), the loop-thread design was
level with or ahead of stock uvicorn + uvloop on two cores (74.0k against 72.8k, 70.4k
against 67.5k req/s), because net/http and Python run in parallel there, but behind on
one core (45.5k against 72.0k).

Profile of loop-thread `asgiserve -cpuprofile`, core 5, 64 connections, CPython 3.13.18:
45.5k req/s, about 22 µs per request.

| Share of the core | What | µs per request |
|---|---|---|
| 27.7% | `write` of the response (`finishRequest` → `Flush`) | about 6.1 |
| 24.5% | in C: Python (the app, `pythago_asgi`, the loop) | about 5.4 |
| 10% | `futex` (thread wakeups) | about 2.2 |
| 11.5% | Go scheduling (cumulative) | about 2.5 |
| 5.4% | `net/http` request read and parse | about 1.2 |
| 8.6% | malloc and GC | about 1.9 |

The FFI layer was no longer visible (`py.build` 3.8%, `py.dispatch` 10% including the
Python→Go calls' own work). Syscalls per request (`strace -f -c`, same load):

| | stock uvicorn + uvloop + httptools | Go `asgi` (loop thread) |
|---|---|---|
| `read` | 1 | 1 |
| `write` | 2 (head and body) | 1 |
| `epoll_pwait` | 1 per about 60 requests | rare |
| `futex` | about 0 | about 4 |

- uvicorn: one thread does the I/O and Python; epoll readiness batches many requests per
  wakeup.
- loop-thread `asgi`: a net/http goroutine per connection hands each request to the
  Python loop thread and takes the response back; on one core every handoff is a thread
  switch.
- More Ps don't help: GOMAXPROCS 1 / 2 / 4 on one core gave 43.8k / 46.1k / 25.3k.
- Arithmetic: net/http alone about 9.2 µs, Python about 5.4 µs, so 14.6 µs before any
  handoff, against uvicorn's 13.9 µs in total.

A hand-written HTTP/1.1 engine on the loop thread was rejected by the user. The chosen
direction: host the Python step on the net/http handler's own thread (what `py.Do`
already does).

### The spike: running the app on the handler thread
`.agents/attic/spike/sinline` (archived; does not build in place). A net/http handler calls
`py.Do` and steps the app's coroutine itself (`coro.send(None)`); `receive()` returns the
prefetched body; `send()` calls Go directly (`go_respond`), with start and first body in
one call. Python builds the scope dict from plain arguments; headers arrive as one
NUL-separated bytes object. Apps awaiting anything else were rejected.

CPU time per request, one core (core 5), 64 connections, `/proc/<pid>/stat` deltas:

| | req/s | user µs | kernel µs |
|---|---|---|---|
| Go `net/http` hello (no Python) | 101.9k | 3.87 | 5.87 |
| uvicorn 0.54.0 + uvloop + httptools | 70.5-70.7k | 6.57-6.68 | 7.34-7.51 |
| Go `asgi` (handoff to the loop thread) | 43.9k | 13.36 | 9.34 |
| inline v1 (scope via `py.From(map)`, result via `Go()`) | 51.7k | 11.98 | 7.29 |
| inline v2 (Python builds the scope, `send` calls Go) | 58.8k | 9.93 | 7.06 |
| inline v2 + one-call `Object.Call` | 68.4k | 7.33 | 7.26 |
| inline v3 (start and body in one callback) | **69.0k** | 7.61 | 6.88 |

- One connection: inline v3 40.9k against uvicorn's 39.8k.
- Kernel time reaches uvicorn's as soon as the handoff is gone; the rest was user-space
  conversion. net/http's user cost alone (3.87 µs) is more than half of uvicorn's.
- Method worth repeating: measure user and kernel time per request (`/proc/<pid>/stat`
  deltas) against the competitor before choosing an architecture. Here the thread
  handoff cost kernel time and the conversions cost user time.
- **General fix found here (`py`): `Object.Call` in one C call.** `CallKw` used to convert
  each argument with `From` and `Close` it afterwards (about 18 C calls, 2.3 µs for 9
  arguments). `pythago_py_call` takes tagged values (as `pythago_py_build`), creates them
  on the C stack (heap past 8), makes a vectorcall and releases them. Conversion errors
  from Go are returned unchanged. A Go→Python call must be one C call, as a Python→Go call
  already is.

More cores, 256 connections, req/s:

| cores | inline, 3.13 (GIL handed off) | inline, 3.14t | uvicorn `--workers N` (3.13) |
|---|---|---|---|
| 1 | 69.0k | 66.5k | 70.7k |
| 2 | 102.2k | 97.2k | 128.6k |
| 4 | 101.4k | 153.6k | 255.4k |

With the GIL, inline stops at about 100k: Python is about 2.8 µs per request, but GIL
handoff between handler threads costs more than that. Free-threaded 3.14t scales, but
sublinearly, and was not profiled (open in TODO.md). uvicorn's workers are separate
processes sharing nothing.

### Flat combining instead of GIL handoff
- **A Go semaphore alone is worse.** `py.Config.Slots` is already a Go channel semaphore
  in front of the GIL (default with the GIL `min(4, GOMAXPROCS)`). With one slot, waiters
  park in Go and the GIL is uncontended, but throughput fell (98.9k → 68.1k on 2 cores,
  99.9k → 73.4k on 4): every request still hands the lock to another goroutine that must
  be woken and scheduled.
- **Combining (`sinline -combine`).** A request queues its Python step. The goroutine that
  finds no combiner running becomes the combiner and, inside one `py.Do` on its own
  thread state, runs every queued step, its own included, until the queue is empty.
  Waiters park on a channel and then do their own net/http write. The GIL is taken once
  per batch and never handed between threads under load.

256 connections (512 on 8 cores), `app.py` `/`, req/s:

| cores | inline, GIL handed off (3.13) | inline, 3.14t | **combining, 3.13** | uvicorn `--workers N` |
|---|---|---|---|---|
| 1 | 69.0k | 66.5k | 65.0k | 70.7k |
| 2 | 98.9k | 97.2k | 101.3k | 128.6k |
| 4 | 99.9k | 153.6k | **167.0k** | 255.4k |
| 8 | — | — | 166.3k | 395.0k (load-generator ceiling: Go hello did 398.7k) |

Combining plateaus at about 167k, about 6 µs of serialized time per request against
2.8 µs of Python: the combiner also did each request's Go-side argument conversion and
`go_respond` header and body handling.

### Batching the combiner: the limit was waiting for a P
- **First attempt (`sinline -batch`):** `go_bresp` copied status, headers and body into the
  job for the waiter to format and write; `run_batch(*flat)` served the batch through one
  `pythago_py_call`. No gain (1/2/4/8 cores: combine 68.2k/105.0k/164.8k/157.2k, batch
  65.8k/99.3k/160.0k/171.7k).
- **Diagnosis:** 4 cores, 162k req/s at 283% of 400% CPU (not CPU-bound); about 576k of
  about 770k batches held one request; the combiner held the lock 62% of the time; more
  connections (256/512/1024 → 163k/168k/164k) and GOMAXPROCS (4/6/8/16 →
  161k/174k/173k/168k) didn't help while pure net/http did 289k. The execution tracer
  (`go tool trace -pprof=sched`) put 60% of scheduling latency on the combiner's `py.Do`
  closure: each Python→Go callback leaves C and needs a P, every P was busy with net/http
  goroutines, so the combiner waited holding the GIL and the combiner role, and everyone
  else waited behind it. Extra Ps only moved the wait to the OS scheduler.
- **Second attempt, no callbacks while combining:** `run_batch` returns every response in
  one bytes object (`<HII` status, header length, body length, then header and body),
  parsed by Go after the call.

  | cores | req/s | p99 |
  |---|---|---|
  | 1 | 67.0k | 2.0 ms |
  | 2 | 101.8k | 8.6 ms |
  | 4 (512 connections) | **180.6k** | 12.5 ms |
  | 8 | **192.1k** | 9.3 ms |

  uvicorn `--workers N` did 255.4k on 4 cores and 395.0k on 8. The next limit is the same
  wait, once per batch (when `run_batch` returns). Batches grew (5834 of size 63+ in 8 s);
  lock held 65%. Host load average was 5-7 from other projects.

### Rejected: a C-resident combiner
Experimental code: glue `pythago_comb_*`, `py/spike_combiner.go` (`-tags pythago_spike`),
`sinline -cres`. Request goroutines pushed jobs (tagged-value arguments; with a NULL arena,
str and bytes payloads are addresses of Go memory) onto a lock-free stack with one Go→C
call, waking the combiner by futex only if asleep. One Python thread ran
`pythago_comb_run` and never returned to Go: it built all arguments in C, called
`run_batch(*args)` once and pushed the result. Completer goroutines waited in C
(`pythago_comb_take`) holding neither GIL nor P, then woke the requests; consumed results
went back to the combiner, which released them under the GIL. `value_new` gained the NULL-arena path
(instead of undefined `NULL + offset`), and `PYTHAGO_PY_ABI`/`glueABI` went to 3.

- Combiner counters (4 cores, 512 connections, about 153k req/s): `run_batch` 1.75 µs,
  argument building in C 0.14 µs, handing off 0.37 µs per request. Asleep 78% of the
  time with 5.7 requests per batch: capacity about 450k req/s, starved, not saturated.
  The server used about 70% of its 4 cores (no thread above 40%), the load generator
  131% of 500%.
- The tracer put 71% of scheduling latency on goroutines woken by `complete()`: Go puts a
  woken goroutine on the waker's P, so every response write queued on the completer's P.
- Interleaved rounds, 4 cores, 512 connections, req/s (busy host):

  | round | batch (Go-driven, no callbacks) | cres, K=1 | cres, K=4 completers | uvicorn `--workers 4` |
  |---|---|---|---|---|
  | 1 | 178.5k | 153.2k | 139.7k | 254.8k |
  | 2 | 172.1k | 152.4k | 138.2k | 259.7k |
  | 3 | 164.4k | 157.0k | 140.5k | 247.7k |

- **Why:** in flat combining a request that finds no combiner becomes it, so an idle
  system needs no handoff at all. A dedicated combiner is idle most of the time, so most
  submissions pay a futex wake and a thread switch; completion adds a hop back, and
  sharding adds more. The cheapest handoff is none.

### Inline requests in `asgi` (current design)
Files: `asgi/asgi.go` (`worker`, `submit`, `release`, `inlineValues`), `pythago_asgi.py`
(`_Worker.serve_inline`, `start_request`), `aio/aio.go` (`Hooks`, `Loop.SetHooks`,
`Loop.Wake`).

- **No handoff.** `ServeHTTP` queues the request's values (`inlineValues`, 15 values per
  request) on its worker's `pending` instead of posting them. If nobody holds the worker
  (`busy`), the goroutine takes it and, inside one `py.Do`, runs every queued request via
  `serve_inline(*values)` (flat combining).
- **The worker lock and the loop thread.** `aio.Hooks` are called from the loop's poll
  (`batch.wait` in `aio/aio.go`) and make the loop thread hold the worker whenever it runs
  Python:
  - `Serve`: it serves queued requests itself at every poll;
  - `Unlock`: it lets go before it waits, after serving what is queued;
  - `Lock`: it takes the worker back, with the thread state released, before returning to
    Python (waits on `loopWait`).

  A combiner that finishes while the loop thread waits hands the worker directly to it.
- **Response actions are unchanged:** Python calls back into Go (`respond`, ...) and
  `ServeHTTP` performs them.
- **The release-path leak.** Response bodies were written zero-copy and their references
  released with `aio.Loop.Release`, which closes them at the loop thread's next poll. Inline,
  the loop thread may never poll, so every response's reference piled up: RSS 130 → 214 →
  275 MB over three 3-second runs (about 70 MB per 290k requests). GC time was dominated by
  scanning cleanup specials (`markrootSpans`, `tryDeferToSpanScan`, about 14%), since each
  pending `py.Object` carries a `runtime.AddCleanup`. Fix: `worker.release` queues the
  object in `done` for whoever holds the worker; `Serve` closes the queue first. RSS stays
  at about 60 MB.
- **Why the suite missed the leak:** no test counted references after inline requests,
  and every suite run had enough fallback traffic to wake the loop thread now and then
  (hence `TestInlineReleases`).

Results right after the fix (host saturated by other projects, load 15-32; interleaved,
compare within a column), one core, 64 connections, req/s:

| | before the release fix | after |
|---|---|---|
| `asgi` (loop thread) | 41.2-42.9k | 41.9-43.0k |
| `asgi` inline | 39.8-43.8k | **52.6-53.4k** |
| uvicorn + uvloop | 59.9-67.0k | 58.6-61.6k |

On four cores inline gained 10-15% over the loop-thread path; uvicorn with 4 workers stayed
far ahead (worker processes, [asgi-worker-processes.md](asgi-worker-processes.md), close
most of that). Python was then about 21% of one core (about 4.2 µs per request, against
the spike's 2.1 µs): the real path adds an asyncio Task, `_Request`, the state copy, the
extensions dict, and a Python→Go callback with header validation per response.

### Rejected: returning responses in the batch result
The spike gained about 8% by returning the common response in `serve_inline`'s result
instead of the `respond` callback. The user rejected it for fidelity:
- **Errors at the call site:** `send()` raises when Go rejects the status or a field; the
  rollback to a 500 depends on it.
- **Backpressure:** a large body makes `send()` wait for the client to drain.
- **Timing:** the first byte could not leave before the batch ends.
- **Ordering:** ordering with `http.disconnect` and the end of the response depends on
  actions reaching Go in call order.

Validating in Python instead would duplicate the security-sensitive field rules
([asgi-server-protocol.md](asgi-server-protocol.md)).

### Per-request Python setup (`pythago_asgi.py`)
Measured with a scratch benchmark (`pyprof/bench_inline.py`, not in the tree):
`serve_inline` with the Go modules stubbed, the benchmark app, batches of 8, core 9, PBS
CPython 3.13.18. About 2.45 µs per request:

| | |
|---|---|
| the app and its two `send()` calls | 1240 ns |
| the scope | 476 ns (header unpacking 311) |
| `_Request` with registration and close | 265 ns |
| the eager Task | 252 ns (a bare coroutine `send` is 128) |

Changes, none visible to the application:
- the `http.disconnect` message is built only when a `receive()` waits for it (it had been
  created twice per request and dropped);
- `serve_inline` calls `start_request` directly instead of packing and unpacking a message
  tuple (`on_request` keeps the tuple for posted requests);
- headers unpack through one iterator (`zip(it, it)`) instead of two slices: 280 → 218 ns
  for 3 headers, 520 → 440 ns for 10.

Result: 2.45-2.50 → 2.25-2.31 µs per request (about 8%). pythago's interpreter runs this
as fast as stock CPython (2476 against 2448 ns, via `pyrun --python`). In the server the
Python share is about 4.2 µs per request; the rest surrounds the call (15 arguments created
in C, the `respond` callback's C half, entering and leaving per batch).

### Argument and callback costs on the asgi side
- **Measure the piece in isolation first.** The callback, the suspected cost, turned out
  small; per-call allocation and wrapping mattered more. Inline one-core profile: the
  whole `respond` callback path (`py.dispatch` and below) was only about 3.5% of the
  core (about 0.6 µs per request); the batch call `call5` (argument creation, Python,
  callbacks) about 22%. A temporary `BenchmarkArgsAB` (no-op Python function, core 9):
  no arguments 284 ns, an inline request's 15 values 515 ns, the same with constants as
  cached objects 411 ns.
- **Cached constants (`inlineValues`).** Common methods, HTTP versions, schemes, empty
  bytes and the server's own address are created once and passed as borrowed objects
  (immutable values Python would create equal anyway). Server addresses are cached per
  server in `Server.addrs`, at most 64 (`addrCount`); a cache miss creates them inside
  `py.Do`, because `serverAddr` first used `py.From` from `ServeHTTP`, outside Python
  (SIGSEGV in `fromRaw` under `call2`).
- **Small response bodies are copied** (up to 4 KiB, `maxCopiedBody`) instead of holding
  the bytes object: no incref, `runtime.AddCleanup` or release on the worker's thread per
  response. Larger bodies stay zero-copy.
- **Shared immortal results** (a `py` change): None/True/False results come back as shared
  borrowed Objects; `serve_inline`'s None result benefits.
- **Not changed:** the field parser (0.6% of the core); it is the security-sensitive
  validation code.

Quiet host (load 1.3-1.9), CPython 3.13.18, 64 connections, interleaved, three rounds:

| one core | req/s | user µs/request | kernel µs/request |
|---|---|---|---|
| inline, before | 56.5-56.8k | 10.42-10.46 | 7.1-7.2 |
| inline, after | **58.3-58.7k** | 9.65-9.88 | 7.1-7.4 |
| uvicorn + uvloop | 71.6-72.3k | 6.60-6.78 | 7.0-7.3 |
| loop-thread `asgi` (reference) | 46.5k | | |

Four cores (512 connections): inline 150.8-152.2k, uvicorn `--workers 4` 267.9-269.6k.
Python's own share (about 2.3 µs) is close to its floor.

### Inline as the default
`Options.Inline` became `Options.LoopThread` with the meaning inverted, and `asgiserve
-inline` became `-loop-thread`. Reasons: inline was ahead everywhere measured (58.5k
against 46.5k on one core; it is what reaches 234k with 4 processes), and its fallback is
automatic, so apps awaiting real I/O keep working. README and the option's doc state the
thread-identity cost and point to `LoopThread`.

## Files
- `asgi/asgi.go` — `Options.LoopThread`; `worker` (`busy`, `pending`, `loopWait`, `done`,
  `serve`), `worker.release`, `Server.inlineValues`, `Server.serverAddr`, `maxCopiedBody`.
- `asgi/pythago_asgi.py` — `_Worker.serve_inline`, `start_request`, `on_request`,
  `_Request` (`resolve_rx`, `disconnect_rx`).
- `aio/aio.go` — `Hooks` interface, `Loop.SetHooks`, `Loop.Wake`, `Loop.Post`,
  `Loop.Release`; hook calls in `batch.wait`.
- `asgi/inline_test.go` — inline tests.
- `internal/tools/asgiserve` — `-loop-thread`, `-cpuprofile`.
- `.agents/attic/spike/sinline` — the archived spike (`-combine`, `-batch`, `-cres`).
- `py/spike_combiner.go`, glue `pythago_comb_*` in `glue/pythago_py.c` — the rejected
  C-resident combiner, still compiled into images.

## Test Coverage
`GOWORK=$PWD/.work/go.work go test ./asgi` runs the whole suite inline; `ci/unit.sh gil` and
`ci/unit.sh ft` run it again with `PYTHAGO_ASGI_LOOP_THREAD=1` (arm64 and amd64).

- **`TestInline`:** `/` runs inline with a running loop and a current task; `/sleep` and
  `/task` start inline and finish on the loop thread (the child task too); a 300 KiB body
  arrives in chunks.
- **`TestInlineConcurrent`:** 400 concurrent requests across the paths, including
  `/exclusive`: a critical section with a GIL-releasing `time.sleep` inside, which raises
  `the worker ran two threads at once` if two threads run the worker's Python at once.
- **`TestInlineReleases`:** serves one bytes constant 300 times inline, then checks its
  refcount. Control (release through the loop thread):
  `references to a response body: 2 before 300 requests, 302 after`.
- **`TestLoopThread`:** the opt-out keeps requests on the loop thread, so a broken opt-out
  cannot pass as inline.
- **Controls for the inline tests, each caught:**
  1. delivery to the loop thread instead: `/: ran at {LoopThread:true ...}`;
  2. no loop wake after a batch: `/sleep` hangs (`test timed out after 20s`);
  3. the loop thread ignores the worker lock: `the worker ran two threads at once` (the
     first test version passed 5 of 5 until `/exclusive` was added);
  4. combiners ignore the worker lock: `the worker ran two threads at once`.
- **`TestDisconnect`** caught a search-and-replace slip that made `disconnect_rx` call
  itself (`RecursionError` on a disconnect with a pending `receive()`), but only as a hang
  (`panic: test timed out after 2m0s`).
- **`py` tests from this work:** `TestCallArguments` (`py/ffi_test.go`; every argument
  kind, more than 8 arguments, keywords, failures in C and Go, refcounts over 100 rounds;
  controls: Go's release removed `3 before, 103 after`, C's post-call `Py_DECREF` loop
  removed `3 before, 403 after`; its first failure, `3 before, 7 after`, was the test's
  own globals holding the object, not a leak) and `TestSingletonResults` (control: None
  preset as `pvFalse` fails with `None: Go() = false`).

## Pitfalls
- **Thread identity is per batch inline.** `threading.local`, `threading.get_ident` and
  `sqlite3`'s same-thread check differ from uvicorn; use `LoopThread` if an app relies on
  them.
- **Anything deferred to the loop thread (releases, wakeups) needs another drain** once
  requests stop passing through it.
- **Never create Python objects outside `py.Do`** (the `serverAddr` SIGSEGV).
- **A mutual-exclusion test must hold the section across a GIL release;** under the GIL,
  plain bytecode never shows the overlap.
- **A Go semaphore in front of the GIL is not enough:** it still hands the lock to a woken
  goroutine per request.
- **When CPU is idle but throughput is capped,** use the execution tracer's
  scheduling-latency profile (`go tool trace -pprof=sched`), not the CPU profile, and look
  at where Go wakes goroutines (the waker's P).
- **Keep a serialized section entirely in C or Python;** each Python→Go callback inside it
  waits for a P.
- **On this shared host compare variants interleaved;** only cores 5-9 and 15-19 are
  3.9 GHz (0-4 and 10-14 are 2.8 GHz), so earlier "8 cores" runs on 5-12 mixed in slow
  cores.
- **A refactor by search and replace must exclude the definition it introduces,** and a
  test that waits on a request needs a short deadline so a hang reads as a failure.
- **Under `set -euo pipefail`, `grep -c` counting zero exits 1:** it silently skipped the
  image step twice during the C-resident combiner work, and the spike called
  `pythago_comb_stats` at address 0. Spikes should check their symbols up front.
- **The ABI check works:** a stale image failed at once with
  `glue has ABI 2, but this package needs 3`.
