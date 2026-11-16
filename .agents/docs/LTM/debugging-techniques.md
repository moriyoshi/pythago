# Debugging techniques that worked

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- **Shrink to a standalone reproducer first,** then bisect by toggling one mechanism at a time
  (anchor on/off, GC off, merges off). Compare against stock `python3.14t` running the same
  script, to separate our bugs from CPython's.
- **Read the CPython source for the exact patch version** (3.14.10 here). The BRC behaviour that
  mattered lives only in `Python/brc.c`.
- **gdb:** breakpoints on inlined functions don't hit. Find the inlined sites by a constant they
  use (`mov w4,#0x413`, the line number passed to `_PyErr_BadInternalCall`).
- **PYTHONHOME breaks gdb's embedded Python.** Set it with `set environment` inside the gdb
  script instead of in the shell.
- **Build diagnostics into the runtime:** a watchdog that dumps stop-the-world state,
  backtraces of suspended VTs from their saved contexts, and per-reason counters for
  carrier-blocking waits. They turned hangs into readable reports.
- **Judge pass rates by matrix runs** (`matrix.sh tag count carriers n env…`), not single runs:
  several bugs showed up 1 time in 10 or less.
- **Know what your watchdog measures.** svt's watchdog is a 20 s deadline (`SVT_WATCHDOG`),
  not a no-progress detector. Slow stress runs looked like hangs until compared against the
  OS-thread baseline and given more time.
- **Check that a reproducer really puts the interacting parties together.** VT placement is
  `i % carriers`; selecting by `i % 4` put every pinned VT on one carrier and hid the bug.
- **A CPython call path can be identified exactly at run time** with frame records plus code
  ranges from the binary's ELF symbol table (`_PySemaphore_Wait` ← `_PyParkingLot_Park` ←
  `_PyMutex_LockTimed`). Check the disassembly first for what LTO inlined; the raw-mutex slow
  path stayed a separate function. This was tried and reverted, but the technique is sound.
- **matrix.sh treats any "want" in the result line as a counter check.** Keep that word out of
  `check()` strings.
- **Stress the transitions, not just the steady state.** A collector that loops `gc.collect()`
  (`SVT_STW`) exposed the I/O deadlock that 170 normal runs never hit.
- **An old `ld.so` run directly** (`ld-2.28.so --library-path … ./prog`) is a cheap,
  faithful stand-in for an old system. It reproduced every floor failure exactly as a
  debian:buster container did. `LD_DEBUG=libs,versions` or `all` shows where loading dies.
- **strace confirms what a fast path really does:** one `sendfile` for the file body.
- **A failure that differs from a minimal program narrows the cause.** A plain purego
  binary survived the same dlopens on buster, which pointed at our image's DT_NEEDED order.
- **Symbolizing a crash inside bao's darwin dylib image** (2026-11-17). The PC lies in the
  region bao loads the dylib into, which no symbol table covers. Keep the crashing binary;
  `nm` gives the image symbol (`_bao_dylib_<hash>_image`) and a Go frame from the same crash
  gives the PIE slide (`pc - 0x4b - nm(runtime.cgocall)` for `runtime.cgocall+0x4b`). Then
  `rip - slide - image` is the dylib offset, and PBS's unstripped `install_only` dylib has
  the same layout as `install_only_stripped` (compare `__text` addr and size), so its
  `nm -n` names the function. Check the guess against the disassembly: the offset must
  start an instruction that uses the registers the crash dump shows as faulting. A binary
  built from nearly the same source is not enough by itself: the image is placed after
  Go's text, so its distance from `runtime.cgocall` moves with the Go code.
- **When a crash will not reproduce, print the state it needs instead.** The finalize crash
  came once in about 160 runs, but a diagnostic in the glue that listed every thread state
  just before `Py_FinalizeEx` (`_status.finalizing`/`cleared`, the BRC node at its 3.14t
  offset) showed the precondition in most runs: worker threads mid-`PyThreadState_Clear`.
  That turned a 1-in-160 crash into a deterministic before/after check (24 such thread
  states in 20 runs before the fix, 0 after), and into a test that slows the teardown
  instead of hoping for the race.
- **lldb cannot run a process over ssh without an interactive authorization** ("this is a
  non-interactive debug session, cannot get permission to debug processes"), and `/cores`
  needs root on macOS; plan on in-process diagnostics instead.
- **Loopback connects that hang in SYN-SENT: look at netfilter's connection tracking table
  first** (2026-11-17). This host tracks loopback connections (`nf_conntrack`, max 262144),
  and a closed TCP connection keeps its entry for 120 s (`nf_conntrack_tcp_timeout_time_wait`).
  When `/proc/sys/net/netfilter/nf_conntrack_count` reaches `nf_conntrack_max`, the kernel
  drops every new connection's SYN in the output hook: connects time out or succeed on a
  retransmit seconds later, established connections are unaffected, servers look idle
  (listen queue empty, no errors), and `nstat` shows `TcpExtTCPSynRetrans` =
  `TcpExtTCPRetransFail`. `dmesg` says `nf_conntrack: table full, dropping packet`, but
  needs root here.
  - One server port holds at most about 14k entries (connect() takes every other port of
    32768-60999), so churn against one port cannot fill the table; a test that listens on
    a new port each run can. `workers` TestSpawn leaves about 1,760 entries per run:
    looping it faster than about 150 runs in two minutes fills the table, and runs fail
    until entries expire. That was the "SYN stall after a worker exits" (29/200, 7/200):
    made by the loop, not by the worker's exit. Loop such tests at most about 100 times
    per two minutes, or watch the count.
  - `bench/http/run.sh` records `conntrack_pct` with every run and `summarize.py` marks
    runs over 90%.
