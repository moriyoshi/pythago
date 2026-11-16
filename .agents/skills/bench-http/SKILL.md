---
name: bench-http
description: "Benchmark pythago's asgi and wsgi servers against uvicorn (and gunicorn for WSGI), plain HTTP and TLS, with bench/http/run.sh; check the results for stalls and noise, publish the tables in README.md, and record the run in the journal."
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Grep, Glob
---

# Bench HTTP: asgi and wsgi against uvicorn

Use this skill to measure the current HTTP server timings and refresh README.md's
"Server benchmarks" tables, e.g. after a change in `asgi`, `wsgi`, `aio`, `py` or bao.

## What the harness measures

`bench/http/run.sh` serves the same application from each configuration, one process
pinned to one core, and drives it with `internal/tools/httpload` from other cores:

- **ASGI** (`bench/uvicorn/app.py`): uvicorn with uvloop and httptools, uvicorn with
  asyncio and h11, pythago's `asgi` (`internal/tools/asgiserve`), and pythago's `asgi`
  with `GOMAXPROCS=2`.
- **WSGI** (`bench/wsgi/app.py`, 10 threads each, the size of a2wsgi's pool): uvicorn with
  a2wsgi, gunicorn gthread, pythago's `wsgi` (`internal/tools/wsgiserve`), and pythago's
  `wsgi` with `GOMAXPROCS=2`.
- **Workloads**, each over plain HTTP and TLS (a fresh ECDSA P-256 certificate; the client
  offers HTTP/1.1 only):
  - a small response with 1 and with 64 connections;
  - a 64 KiB response;
  - a 16 KiB upload;
  - `/sleep`, which overlaps requests: 1 ms `asyncio.sleep` for ASGI, 10 ms
    `time.sleep` for WSGI, which caps each WSGI server at threads / 10 ms;
  - a new connection per request (`-close`; over TLS a full handshake).

The stock servers run on the PBS CPython 3.13 that `pythago-env` provisions, the same
version and build as pythago's image, in `$CACHE/venv-http`. The configurations and
workloads are in the `configs` and `loads` arrays.

## Step 1: Prepare

1. Pick idle fast cores (5-9 and 15-19 run at 3.9 GHz; 0-4 and 10-14 at 2.8 GHz). Other
   sessions pin their own benchmarks to these same cores, so look first:

   ```
   ps -eo pid,psr,pcpu,comm --sort=-pcpu | awk 'NR == 1 || $3 > 5'
   taskset -cp PID    # for each busy one: its affinity
   ```

   Choose a server core and four or five load cores that nothing busy runs on or is
   pinned to, and pass them as `BENCH_SERVER_CPU` and `BENCH_LOAD_CPUS`.
2. Make sure nothing else is using the test image: the harness regenerates
   `.work/testimage` for CPython 3.13 (`gen_image gil`) unless `BENCH_IMAGE=keep`, so an
   agent testing against the image would break.
3. Note the commit and whether the working tree has uncommitted changes (`git status
   --short`). The results record only `git rev-parse --short HEAD`, so say in the
   journal which uncommitted changes were in the build.

## Step 2: Run

```
BENCH_SERVER_CPU=7 BENCH_LOAD_CPUS=15-18 BENCH_OUT=$HOME/.cache/pythago-ci/bench-http.jsonl bench/http/run.sh
```

Defaults: 3 rounds, 5 s measured after 2 s warm-up, server on core 5, load on 15-19.
A full run takes about 35 minutes. The argument is a regex on configuration names (e.g.
`'pythago|uvloop'`). Do no other work on the host meanwhile, and don't edit `run.sh`
while it runs (bash reads the script as it goes). Each run appends a `meta` line and then
one JSON line per measurement to `BENCH_OUT`. Besides httpload's figures, every line has
`host_syn_retransmits` (nstat `TcpExtTCPSynRetrans` during the run),
`foreign_cpu_pct`: the share of the server's core that other processes ran on
(`/proc/schedstat` `rq_cpu_time` minus the server threads' run time; controls: 0 on
an idle core for every run; 16-97% with a spinner pinned to the server core), and
`conntrack_pct`: how full netfilter's connection tracking table was at the fuller end of
the run (`nf_conntrack_count` over `nf_conntrack_max`; the harness itself reaches about
6%, since one server port has at most 14k client ports).

## Step 3: Check before publishing

```
python3 bench/http/summarize.py $HOME/.cache/pythago-ci/bench-http.jsonl
```

- **† cells** had, in some round, no request, errors, a connect over a second, other
  processes on over 5% of the server's core, or a connection tracking table over 90%
  full; the summary lists those runs.
  - `conntrack_pct` near 100: the kernel was dropping new connections' SYNs, loopback
    included (`nf_conntrack: table full, dropping packet` in `dmesg`), so runs complete
    nothing, without errors, until entries expire (closed connections stay 120 s).
    Something on the host opened connections to many ports; wait two minutes and rerun.
  - `foreign_cpu_pct` over 5: another workload shared the core. Rerun on other cores.
  - `requests: 0` with no errors, or `slow_connects` > 0, is a stall: find out whether it
    is the server or the network before publishing. With `host_syn_retransmits` 0, the
    SYNs were not lost: the server was slow to accept or to handshake. `strace -f --seccomp-bpf -ttt -e
  trace=accept4,close,shutdown` on the server shows whether the stuck connections ever
  reached `accept4`. With `host_syn_retransmits` above 0, check `conntrack_pct` first.
  Rerun the configuration with the filter; don't publish a number from a stalled round.
- **Spread:** compare the rounds of a cell (the JSONL has every round). If one round is
  low across many cells, the host was busy then (check `foreign_cpu_pct`); rerun.
- **Sanity:** `/sleep` must sit at its cap for WSGI (about 1,000 req/s with 10 threads),
  and every TLS run must report `tls` in its line.

## Step 4: Publish

```
python3 bench/http/summarize.py $HOME/.cache/pythago-ci/bench-http.jsonl --readme README.md
```

This replaces the block between `<!-- bench-http:begin -->` and `<!-- bench-http:end -->`
in README.md. Then edit the prose just above the block, if it no longer matches the tables
(which side wins where).

## Step 5: Record

Append a journal entry to `.agents/docs/JOURNAL.md` with:

- the command, the environment overrides, cores, rounds and durations;
- versions (the `meta` line), the commit and the uncommitted changes in the build;
- the summary tables or the cells that changed against the previous run, and any rerun
  or excluded round and why;
- what the numbers say: where pythago leads or trails and the likely reason.

Update `.agents/docs/LTM/performance.md` if a durable lesson came out of it.

## Pitfalls

- uvicorn and gunicorn negotiate TLS 1.3 AES-256-GCM (OpenSSL's server preference), Go's
  servers AES-128-GCM; the summary lists them. This is each server's default, not
  something the harness sets.
- `GOMAXPROCS=2` rows are not pythago's default; they are there for the open decision in
  `.agents/docs/TODO.md` about raising it.
- Don't compare these numbers with `bench/uvicorn` or `bench/wsgi` results: those use 8 s
  runs, 4 WSGI threads and other connection counts.
