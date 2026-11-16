# Waker and lifecycle safety

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- CPython's parking lot keeps the semaphore on the waiter's stack, and the waiter may free it
  as soon as a post is visible. A post must be one atomic step that also claims the waiter.
- Every asynchronous waker needs a generation check: timers, semaphore posts, epoll data and
  events. VT structs must be type-stable (never freed), so a stale token is a no-op instead of
  a use-after-free.
- Internal waits must save and restore errno. A 1 ms futex timeout left `ETIMEDOUT` behind and
  surfaced as a socket `TimeoutError` much later.
- Thread identifiers must stay unique until join or detach, and must never be values that glibc
  could interpret as its own pointers.
