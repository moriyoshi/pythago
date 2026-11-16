# Free-threaded CPython under M:N

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- **Ownership is per OS thread.** `_Py_ThreadId()` is the hardware thread pointer, which
  carries all of these:
  - `ob_tid` and the biased-refcount fast path;
  - mimalloc local free;
  - BRC buckets.

  Therefore a VT must never migrate. Home-carrier scheduling keeps the invariants only if
  CPython's notion of "is the owner running?" is also per carrier.
- **Any CPython heuristic that infers "the owner OS thread is idle" from one thread state is
  wrong under M:N.** BRC's TrySuspendDetached merge was one; look for others:
  `find_thread_state`-style lookups by tid, and "no tstate means the thread exited" logic.
  The fix pattern is to keep exactly one representative per carrier, and make it the one
  about to run.
- **PyMutex handoff after 1 ms gives the lock to a waiter even if it is a parked VT.** Even
  without handoff, the oldest waiter takes the single wake. A carrier that blocks on a lock a
  same-carrier VT can park on can therefore deadlock.
  - **Rule for runtime locks:** VTs never park waiting on them, and never park at all while
    one is held. Their waits are then bounded by short running critical sections.
  - **User-level locks** (threading.Lock, PyThread locks, import and warnings locks) must
    stay parkable, because their holders park while holding them.
- **Check a primitive's wake order before reasoning about it.** FIFO wake-one (PyMutex,
  SimpleQueue) is the dangerous kind. LIFO (raw mutex) is safe here, because a carrier-blocking
  waiter is always newer than its carrier's parked VTs. Wake-all primitives are safe.
- **A pinned VT holds a carrier-stack level for as long as it blocks.** Any design that keeps
  Go callbacks on the carrier stack is bounded by how many pinned VTs block at once, so
  heuristics such as release order or depth caps cannot close lock cycles through it.
- **Before tuning heuristics, count the resource the design consumes in the worst case.** The
  deferred-lock fixes failed three ways for one reason, stack levels per blocked pinned VT.
  Counting that under about 100 waiters per carrier would have shown it at the start.
- **Stack-based nesting only works if what runs on top can never wait for what's
  underneath.** For nested runs that means nested VTs must never become pinned, so their Go
  calls are deferred. Anything a scheduler hook can run must include every runnable VT
  (batches already taken out of the queue too).
- **Never decide "park or block in the syscall" once, at entry.** Every no-park condition can
  be transient (a stop-the-world, a lock veto), and a blocked syscall can wait on a peer VT
  of the same carrier. Wait in short slices and re-check instead.
- **Parking rules learned the hard way:**
  - Don't park while any stop-the-world is requested.
  - Don't park with no tstate inside `PyThreadState_New`: it grows QSBR under a stop-the-world.
  - Park only when DETACHED or SUSPENDED and nothing else is attached.
  - Attached waits block the carrier, briefly.
- A VT's thread state must stay fixed while Python frames are on its stack, because CPython
  keeps `_save` in C locals. Thread states can be multiplexed only at task boundaries.
- `PyThreadState_Clear` abandons mimalloc segments, and teardown walks the abandoned list.
  That is O(n²) for churn-heavy workloads. Pooling hides it; real sharing of heaps per carrier
  needs a CPython change.
