# Virtual Threads (Frozen Research)

## Summary
Virtual threads (VTs) ran Python threads M:N on mmap'd C stacks switched on goroutine
carriers, on free-threaded CPython 3.14t. They worked mechanically, but each fix exposed the
next structural deadlock. The last one (a pinned VT waiting for a lock held by a deferred VT)
cannot be fixed without moving Go callbacks off the carrier stack. VTs were frozen on
2026-11-15 in favour of goroutine-backed 1:1 threads
([feasibility-and-threading-decision.md](feasibility-and-threading-decision.md)). This
document records the design and its fixes so a revival does not repeat the work.

## Key Facts
- The code is frozen in `.agents/attic/spike/glue/pythago_vt.c`, `pythago_vt.h`,
  `pythago_vtio.c` and `.agents/attic/spike/svt`. It gets no further development.
- **Refcount corruption** came from CPython's BRC treating a detached thread state as "the
  owner OS thread is idle". Fix: `carrier_brc_owner`, exactly one thread state per carrier
  in the BRC bucket, the one about to run.
- **PyMutex hands a contended lock to a waiter that has waited over 1 ms, even a parked VT.**
  Rule: VTs never park waiting on runtime locks (`vsem_runtime_lock_wait`), and never park
  while the stop-the-world or QSBR mutexes are held by anyone.
- **I/O wrappers must not commit to a blocking syscall at entry.** VTs wait in short slices
  and park as soon as they may.
- **Pinned VTs** (inside Go callbacks) cannot yield. They run their carrier's runnable VTs
  nested, one level deep (`vt__nest_one`); nested VTs that reach Go are deferred to top
  level.
- **Unfixable residual:** a deferred VT keeps the Python locks it holds until top level, so a
  pinned VT waiting for one deadlocks. Root cause: Go accepts C-to-Go callbacks only on
  stacks it knows, so Go callbacks must run on the carrier's own stack.
- **Revive only for** many thousands of `threading`-style threads blocked at once for long
  periods, in code that cannot move to asyncio. Prerequisites: forwarded callbacks and the
  isolation fixes.

## Details

### The design as built (3.14t)
- Home-carrier scheduling: a VT never migrates off its carrier, because free-threaded
  ownership (`ob_tid`, biased refcounts, mimalloc local free, BRC buckets) is keyed on the
  OS thread ([free-threaded-cpython-under-mn.md](free-threaded-cpython-under-mn.md)).
- mmap'd C stacks; a lock-free C run queue per carrier.
- VT-aware semaphores, sockets, poll and sleep (`pythago_vtio.c`).
- `threading.Thread` routed to VTs in two ways: raw `pthread_create`, and "route A", which
  rebinds `_thread` from Python.
- The `goaio` loops used a C event, not Go channels, so a VT could park instead of pinning
  its carrier.
- **Thread-state pool ("O")** in Go: admission per carrier and an idle-time trim. Pooling
  passed after the BRC fix (18/18) but stays off because pooled thread states leak state
  between users ([isolation-verdict.md](isolation-verdict.md)). "M=O" means one thread state
  per VT, no pooling.

### Security self-assessment (four review agents) and the M=O fixes
HIGH findings:
- stale wakers that reach freed VTs;
- errno clobbered by internal futex waits (this caused GoSelector's `TimeoutError(110)`);
- reused `get_ident` values, and VT pointers handed to glibc as `struct pthread`;
- state leaking between users of a pooled thread state;
- per-OS-thread C state shared by all VTs on a carrier.

Fixed (the three M=O blockers):
- VT slots are type-stable and carry a generation; wakers carry a park token
  ([waker-and-lifecycle-safety.md](waker-and-lifecycle-safety.md)).
- errno is saved and restored around every switch and wait.
- Idents are odd tokens resolved through a handle table.

### Refcount corruption (BRC) and its fix
- **Symptom:** in route A threads and Go task VTs, objects were freed while their refcount
  was nonzero: a stray SystemError from `PyObject_ClearWeakRefs`, an event loop freed at
  refcount 4, occasional segfaults. 5–12 of 30 runs failed, correlated with GC.
- **Reproducer:** `SVT_BODY=chain` runs 200-thread join-predecessor chains, 50 times by
  default. It fails even with real OS threads as long as the parent runs on a VT. Stock
  `python3.14t` passes.
- **Root cause (CPython 3.14.10):** `_Py_brc_queue_object` finds the first thread state
  registered for the object's `ob_tid`. If that thread state is DETACHED, or none is found,
  the non-owner merges the refcount fields itself, non-atomically
  (`_PyThreadState_TrySuspendDetached`). Under M:N a detached thread state does not mean
  the carrier is idle: it can belong to a parked VT while another VT on the same carrier
  runs. An always-detached per-carrier "anchor" thread state, added earlier, made the direct
  merge happen on nearly every foreign decref.
- **Fix (`carrier_brc_owner` in `pythago_vt.c`):** each carrier keeps exactly one thread
  state in the BRC bucket, the one about to run. Its real attach state then speaks for the
  whole carrier. The anchor was removed.
- **Results:** every VT workload on 3.14t passed (171/171 runs). The earlier "pool
  corruption" was the same bug (pooled 18/18 after the fix). The 3.13 GIL spikes still
  passed.
- The same mechanism makes "at most one thread state per OS thread" an invariant in the
  product `py` ([py-runtime-and-threads.md](py-runtime-and-threads.md)).

### PyMutex handoff audit
Every CPython 3.14.10 lock and wait was audited for the pattern "a VT that cannot run holds a
lock, or the wake for it, while a waiter that cannot park blocks the carrier". The wake order
of each primitive is what matters:

| Primitive | Wake order |
|---|---|
| PyMutex | oldest waiter; hands it the lock after 1 ms |
| raw mutex | newest waiter |
| PyEvent, OnceFlag, RWMutex, thread-state attach | everyone |

Found and fixed:
- **BRC bucket mutex** (found while fixing BRC): a parked VT received the handoff and a
  carrier blocked on the same lock never got it. First fix `vsem_brc_wait`, then
  generalized.
- **QSBR's mutex and the stop-the-world mutexes** had both parkable and carrier-blocking
  waiters. The QSBR holder runs a whole stop-the-world, so the 1 ms handoff is likely.
  Fix: VTs never park waiting for HEAD_LOCK, either stop-the-world mutex, the runtime RW
  mutex, QSBR's mutex or a BRC bucket (`vsem_runtime_lock_wait` in `pythago_vtio.c`).
- **A detached stop-the-world requester could park while holding HEAD_LOCK** and the
  stop-the-world mutexes, through the parking lot's own bucket locks. Fix: no VT parks while
  the stop-the-world mutexes or QSBR's are held, by anyone.
- **I/O wrappers deadlock.** recv, send, accept, connect, poll and sleep chose "park or real
  syscall" once, at entry. A VT entering recv during a stop-the-world blocked its carrier
  in the syscall while its peer, on the same carrier, could never run. Reproducer:
  `SVT_BODY=sockets SVT_STW=1` on 1 carrier hung 3 of 6 runs. Fix: VTs never make the
  blocking syscall; they wait in short slices and park as soon as they may. After: 20/20.

Left unfixed and documented: SimpleQueue's single wake; finalizers in pool-mode
`tstate_free`.

Results: the full 3.14t matrix and stop-the-world-storm variants of every workload passed.
10k-thread churn was no slower (median 1495 ms against 1584 ms). The 3.13 spikes passed.

**GC side finding:** with all 10k VTs live, a back-to-back `gc.collect` keeps the world
stopped nearly all the time, because GC walks every thread state's heaps. `full` under the
storm took about 3 min, against 8 s for OS threads (far fewer live thread states). That is
slowness, not a deadlock.

### Pinned VTs and nested runs
- **Hazard:** a VT inside a Go callback cannot yield (it is pinned). If it waits on a
  contended PyMutex, a same-carrier VT parked on that mutex takes the wake or the handoff,
  and the carrier deadlocks. Reproducer: `SVT_BODY=pinned`, where Go calls `PyList_Append`
  (builtin `go_append`) on a list that other VTs sort. It hung 5 of 5.
- **Fix:** the pinned VT runs its carrier's runnable VTs itself, one at a time, through a
  scheduler hook (`vt__nest_one`). Nested VTs that reach Go are deferred to top level, so
  nesting is one level deep and Go code is never re-entered.
- **Bugs on the way:**
  - the hook must also see VTs the Go loop had already taken from the C queue;
  - a stale sleep deadline caused an early wake that turned the deferred list into a cycle.
- **Results:** pinned passes at every size, on 1, 2 and 4 carriers, and under the
  stop-the-world storm. The no-nesting control (`SVT_NONEST`) still hangs. The full matrix
  passes at no measurable cost.

### The deferred-VT lock residual (not fixable as designed)
- **Reproducer:** `SVT_BODY=deferlock`: pinned VTs (inside `go_call`) wait for a lock `L`
  that nested VTs hold across Go calls (`go_noop` inside `with L`). It hangs every run.
- **Diagnosis from hung-process dumps** (`SVT_BT=py` adds each VT's Python frames, home
  carrier, pinned and deferred flags). Every carrier, on 1 and on 4 carriers, showed:
  - at the bottom of the carrier stack, one **pinned** VT at `take: with L` (inside
    `go_call`), slicing and nesting;
  - exactly one VT **deferred at `go_noop()` inside `with L`**: it holds `L`;
  - the rest parked at `with L`, or deferred at `go_call(take)` holding nothing.
- **The cycle:** the pinned VT waits for `L`. The holder took `L` while running nested above
  the pinned VT, then reached Go and was deferred. A deferred VT resumes only when its
  carrier is back at top level, which needs the pinned VT to return, which needs `L`. The
  carrier stack is LIFO, so a lock acquired above a pinned frame must be released before
  that frame unwinds. Deferral forbids exactly that.
- **Trigger:** a holder parks while holding `L` (a `sleep` inside `with L`). On release, the
  wake goes to the oldest waiter, often a parked VT that a pinned VT then runs nested. That
  VT acquires `L` and calls Go.
- **Three fixes tried and reverted:**
  1. releasing deferred VTs as a last resort;
  2. handoff refusal plus EINTR retry for pinned PyMutex waits (recognized by frame chain);
  3. letting nested VTs call Go.

  All failed for one reason: every blocked pinned VT occupies a carrier-stack level, and
  with many waiters the stack fills before the holder can run.
- **Root cause:** Go callbacks must run on the carrier's own stack, because Go accepts C-to-Go
  callbacks only on stacks it knows. That follows from running Python threads on custom C
  stacks instead of goroutines.
- **Kept:** depth-1 nesting with deferral, which handles the common case. After the revert:
  pinned 15/15 and 5/5 on 1 carrier; deferlock 0/3 (still hangs); the regression set and
  the 3.13 spikes pass.
- **The complete fix is a design change:** run Go callbacks off the carrier stack and forward
  their C-API calls back to the VT. It keeps thread identity and home-carrier ownership,
  and costs a thread round trip per C-API call inside a callback. Proposal: "fast" pinned
  callbacks for cheap builtins, "safe" forwarded callbacks for user code.

### Revival conditions
VTs are worth reviving only for many thousands of `threading`-style threads blocked at once
for long periods, in code that cannot move to asyncio. A revival first needs:
- the forwarding-callback design above;
- the isolation fixes and other open VT items, tracked in `.agents/docs/TODO.md` under
  "Only if VTs are revived".

## Files
- `.agents/attic/spike/glue/pythago_vt.c`, `pythago_vt.h` — scheduler, context switch,
  `carrier_brc_owner`, `vt__nest_one`, deferral.
- `.agents/attic/spike/glue/pythago_vtio.c` — VT-aware semaphores, sockets, poll, sleep;
  `vsem_runtime_lock_wait` (uses `internal/pycore_brc.h`).
- `.agents/attic/spike/svt/main.go`, `prof.go` — the VT driver and workloads; defines the
  `go_noop`, `go_append` and `go_call` builtins.
- `.agents/attic/spike/s1/build.sh` — builds the spike image; `NO_VT=1` adds
  `-DPYTHAGO_NO_VT` and leaves the VT glue out.

## Test Coverage
The spikes do not build in place: copy `.agents/attic/spike` back to `internal/spike` and run
`s1/build.sh` into `s1/pyembed` first (see `.agents/attic/spike/README.md`). Then the `svt`
driver is controlled by environment variables:

| Variable | Meaning |
|---|---|
| `SVT_BODY=<name>` | workload: `chain` (BRC reproducer), `pinned`, `deferlock`, `sockets`, `asyncio`, `threads`, `locks`, `noop`, `noshare`, `sleeponly`, ... |
| `SVT_NONEST` | disable nested runs (control for `pinned`) |
| `SVT_BT` (`=py` for Python frames) | dump every suspended VT's backtrace |
| `SVT_WATCHDOG=<s>` | watchdog seconds before a hung run is dumped (default 20) |
| `SVT_STW=<µs>` | a plain OS thread runs `gc.collect` in a loop (stop-the-world storm); default pause 200 µs |

Known state when frozen: everything passes except `deferlock` (hangs by design).

## Pitfalls
- A detached thread state does not mean its OS thread is idle under M:N. Any CPython
  heuristic that infers owner idleness from one thread state breaks.
- FIFO wake-one primitives (PyMutex, SimpleQueue) are the dangerous ones; check wake order
  before reasoning about a lock.
- User-level locks (threading.Lock, import and warnings locks) must stay parkable, because
  their holders park while holding them. Runtime locks must not be parkable.
- Count the worst-case resource a design consumes before tuning heuristics: the deferred-lock
  fixes all failed on carrier-stack levels per blocked pinned VT.
- Live thread states make GC slow under VTs: GC walks every thread state's heaps.
- All VTs on a carrier share per-OS-thread state (OpenSSL error queue, glibc mutex ownership,
  sigmask); they are one trust domain.
