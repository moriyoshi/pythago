# Performance

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- **Localize before optimizing:**
  - **Microbenchmarks against the same stock interpreter.** Equal function-call speed
    with 2–3× slower allocation pointed straight at a per-access runtime cost, not the
    eval loop.
  - **Then profile:** callgrind works on Go binaries (`GODEBUG=asyncpreemptoff=1` avoids
    its signal assertion). Go's pprof samples every thread, so time inside Python shows
    as `cgocall`.
  - **Function-level comparisons of a stripped `.so` are unreliable** (statics are charged
    to the previous export). Compare totals.
- **The cost of a mechanism is in its fixed per-call parts.** In order of what mattered:
  - bao's TLS thunk saved every register on each TLS access;
  - each purego callback dispatched by reflection (gone: crosscall2 with a C-decoded
    frame, 561 → 113 ns per Python→Go call);
  - each purego call from Go into C had a fixed overhead (now `runtime.cgocall` and a
    fixed-arity thunk, 61 → 33 ns);
  - every cross-thread handoff costs two wakeups.

  Allocation pooling (buffer reuse) changed nothing measurable. Measure per-operation
  cost at small sizes, where fixed costs dominate.
- **Avoid round trips first:** a speculative non-blocking read on the loop thread removed
  the goroutine handoff and nearly tripled small-read throughput.
- **A goroutine running Python holds a Go scheduler slot while in C** until sysmon
  reclaims it (≥ 20 µs). When Go and Python share cores, `GOMAXPROCS` above the CPU count
  lets the Go side keep running.
- **Know the baseline's floor:** net/http costs about 9 µs per request, against about
  14 µs in total for uvicorn + uvloop + httptools. No amount of Python-side work lets a
  net/http front end win on one core.
- **Benchmark with the server pinned and the load generator on other cores,** and repeat
  the run after a change in the stack (bao, static TLS). Do no other work during a run.
- **On this shared host, measure intrusion; don't assume idle cores.** Other sessions pin
  their own work to the same fast cores (5-9, 15-19), and unpinned processes land on a
  benchmark core whenever its server idles. `bench/http/run.sh` records, per run, the
  share of the server's core other processes ran on: the core's `rq_cpu_time` in
  `/proc/schedstat` minus the server threads' `/proc/PID/task/*/schedstat` run time,
  both scheduler nanoseconds. Mixing `/proc/stat` ticks with process `utime+stime` gave
  -8 to -28% on an idle core (IRQ time is accounted differently). Control: 0 on an idle
  core for every run, 16-97% with a spinner pinned there. Intrusion on a mostly idle
  server (sleep loads) cost nothing; on a busy one it cost up to 41%. Pick cores with
  `ps -eo pid,psr,pcpu,comm` and `taskset -cp`, and ask the session that owns busy
  pinned jobs for a window rather than contending.
