# The Go-Backed asyncio Loop (`aio`)

## Summary
Package `aio` provides `GoEventLoop`, an asyncio event loop whose I/O runs on Go's
netpoller and goroutines, on top of `py`. It began as the spike in
`.agents/attic/spike/goaio`; the port fixed three bugs the spike never hit (a lost wakeup,
blocking fds, the wrong DNS exception). Its read path supports `BufferedProtocol` and
tries one inline non-blocking read before handing off to a goroutine, which is what made
small reads fast.

## Key Facts
- `aio.Configure(&cfg)` adds the `_goaio` module to `Config.Modules` and an import hook
  to `Config.Setup` (run in its own namespace) that serves `goaio.py` with source
  available to tracebacks.
- The loop waits in `poll(handle, timeout)`, blocking in Go under `py.Release`. Only the
  loop's OS thread blocks, as it would in `epoll_wait`. No glue change was needed.
- **A wake is never discarded unconsumed.** `call_soon_threadsafe` only kicks; a leftover
  wake costs at most one empty poll.
- **Blocking fds** (asyncio's pidfds, blocking pipes) cannot use the netpoller; each gets
  a watcher goroutine in `ppoll` on a dup plus a cancel eventfd (one OS thread per fd).
- **`*net.DNSError` maps to negative `EAI_*` codes** so resolution failures raise
  `socket.gaierror`, not `OSError(EIO)`.
- **Writes are zero-copy:** writer goroutines send from `(*Object).UnsafeBytes` while the
  loop holds a reference.
- **Reads:** `read_into` first tries one non-blocking `read(2)` on the loop thread; only
  on EAGAIN does it hand off to the per-connection reader goroutine. This nearly tripled
  4 KiB-read throughput. Buffer reuse changed nothing.
- Each cross-thread handoff costs two wakeups (about 11–12 µs per read measured here).

## Details

### Design
- `.agents/attic/spike/goaio` became `aio`. The spike's loop slept in a C event
  (`pythago_vtev`) so a virtual thread could park. With goroutine-backed 1:1 threads the
  loop blocks in Go under `py.Release` instead.
- Completions from goroutines go into a mutex-protected `batch` that the loop thread
  drains; producers `kick` the loop's `wake` channel on an empty→non-empty transition.
- `GoEventLoop.create_connection` dials by name through `net.Dial`.
- The selector loop (stock asyncio) still runs in the same process; `aio` tests cover both.

### Bug 1: a lost wakeup (a 300.15 s test)
- `TestWakeups` took `300.15s`: asyncio's `THREAD_JOIN_TIMEOUT`.
- `wait()` drained any "stale" wake before checking the batch. `call_soon_threadsafe`
  queues nothing in Go and only kicks. A kick landing between the loop computing
  `timeout=None` and calling `poll` was eaten. The executor shutdown at the end of
  `asyncio.run` then waited out the timeout.
- Fix: never discard a wake unconsumed. A leftover wake returns early at worst, which
  asyncio treats as a timeout.

### Bug 2: the loop hung on child processes
- Sockets worked; waiting for `/bin/true` never returned.
- asyncio watches children through a pidfd from `pidfd_open`, and that fd is blocking.
  `os.NewFile` keeps blocking fds out of the netpoller, so `RawConn.Read` failed at once
  and the watcher goroutine exited silently. The spike had only tested sockets.
- Fix: blocking fds get watcher goroutines blocked in `ppoll` on the dup and an eventfd.
  `close` writes the eventfd; the dup and eventfd are closed only after the watchers return
  (`running sync.WaitGroup`). Cost: one OS thread per such fd while it waits.

### Bug 3: the wrong exception for resolution failures
- A failed lookup became `OSError: [Errno 5] dial tcp: lookup bad..host: no such host`.
- `errnoOf` (`aio/aio.go`) now maps `*net.DNSError` to `EAI_NONAME` (-2), `EAI_AGAIN`
  (-3) or `EAI_FAIL` (-4), carried as negative codes. On glibc `socket.EAI_NONAME` is
  itself -2, so the code is passed as is, not negated.

### Test hermeticity for name resolution
- `no-such-host.invalid` went to real DNS (3.6 s, sometimes far longer).
- `bad..host` fails on the selector loop before any lookup, with stock asyncio behaviour:
  `UnicodeEncodeError: 'idna' codec can't encode character '\x2e' in position 4: label empty`.
- `bad!host` is a valid IDNA label but an invalid hostname. It fails locally in both Go's
  resolver and glibc. Use it.

### Reads: BufferedProtocol
- Zero-copy buffers do not help uvicorn: httptools, h11 and asyncio streams use
  `data_received(bytes)`. A bulk receive did show a gap, because `GoEventLoop` copied
  twice (kernel → pooled Go buffer → new bytes) where the selector loop copies once:

  | 2 GiB, one stream | MB/s |
  |---|---|
  | stock, selector, `data_received` | 9660 |
  | pythago, selector, `data_received` | 9704 |
  | pythago, selector, `BufferedProtocol` | 9739 |
  | pythago, `GoEventLoop`, `data_received` | 6813 |

- Proactor-style design:
  - The transport calls `get_buffer(-1)` on the loop thread. `read_into` exports it
    (`BufferInto` into the connection's reused `rbuf`) and hands `readReq{*py.Buffer,
    []byte}` to the per-connection `bufferedReader` goroutine.
  - The reader passes the export to the loop's release list *before* posting `evFilled`.
    The loop closes released items after taking completions, so the export is released by
    the time `buffer_updated` runs.
  - Pause holds a completed read, and an EOF behind it, until resume (as asyncio's
    proactor loop does).
  - `set_protocol` across kinds (streaming ↔ buffered) copies.
- Result, 2 GiB: 8173–8230 MB/s steady, against 4605–8328 noisy for `data_received`.

### Reads: buffer reuse (no effect)
- `py.Buffer` was made reusable (see [go-c-ffi.md](go-c-ffi.md)): 2 allocations per
  export+close against 9 fresh.

  | BufferedProtocol, MB/s | before | reuse | stock selector |
  |---|---|---|---|
  | 4 KiB | 300 | 302–304 | 1798 |
  | 64 KiB | 3309 | 3291–3345 | 8829 |

- The per-read gap was about 11–12 µs: two cross-thread wakeups per read, not allocation.

### Reads: inline non-blocking read (the fix that worked)
- `read_into` first tries one non-blocking `read(2)` on the loop thread through
  `RawConn.Read` with a callback that never waits (it retries only on EINTR).
  - Data: the export is closed at once and n is returned.
  - 0: EOF.
  - EAGAIN or any other error: hand off to the goroutine (returns `None`; `evFilled` or
    `evEOF` follows).
- Python schedules the next read with `call_soon`, so there is one inline read per loop
  iteration and other callbacks still run.

  | MB/s | before | inline | stock selector |
  |---|---|---|---|
  | 4 KiB | 300 | 834–843 | 1627–1957 |
  | 64 KiB | 3309 | 6312–6412 | 8621–8823 |
  | 256 KiB | ~8200 | 8300–8450 | 8519–8539 |

### Throughput on uvicorn
`GoEventLoop` under uvicorn (64 KiB responses, two cores) reached 4059–4112 MB/s against
3224 for uvloop; request rates are in
[bao-tls-and-uvicorn-parity.md](bao-tls-and-uvicorn-parity.md).

### Pipes and subprocesses (2026-11-17)
- `_make_read_pipe_transport`/`_make_write_pipe_transport` duplicate the pipe's descriptor
  for Go (`_goaio.pipe`, `pipeConn` over a non-blocking `*os.File`, so the same
  `aioConn` reader/writer goroutines and backpressure) and close the Python object at
  once: Go's duplicate is the only descriptor, so closing the transport closes the pipe
  and a child sees end of input.
- `_make_subprocess_transport`: `base_subprocess` with `subprocess.Popen` (as
  `_UnixSubprocessTransport`), and the exit reaped in Go (`wait_pid`: `wait4` in a
  goroutine, exit status or minus the signal; ECHILD reports 255, as asyncio's watchers do).
- A write pipe must notice its reader going away, or a child that exits with its stdin
  open never finishes the transport. Linux: the netpoller reports POLLERR/POLLHUP as read
  readiness (`RawConn.Read`) and `poll` with no events confirms it. darwin: `poll` reports
  no POLLHUP for a pipe's write end, so `hangup_darwin.go` waits on a kqueue
  `EVFILT_WRITE` for `EV_EOF` (500 ms checks of `ac.stop`). Named FIFOs on darwin are not
  watched, as asyncio (gh-145030).
- Every `_goaio` connection function is a no-op on an unregistered handle: asyncio calls
  `pause_reading`/`resume_reading` after `close()` (StreamReader), which panicked (nil
  connection) in about 1 run in 6 of the subprocess scenarios.

### TLS: crypto/tls when the context maps, SSLProtocol otherwise (2026-11-17)
- User decision: hybrid. An SSLContext cannot be read back (no certificate chain, ALPN or
  trust getters; PBS's libpython exports no OpenSSL symbols, so the glue cannot read the
  `SSL_CTX`), so aio's import hook (`_GoaioSSLHook`, first on `sys.meta_path`) wraps the
  loader of `ssl` and `goaio_tls.record` patches `ssl.SSLContext`'s configuring methods to
  record their arguments (`load_cert_chain`, `load_verify_locations`,
  `set_default_verify_paths` (load_default_certs), `set_alpn_protocols`,
  `set_ecdh_curve`; NPN and PSK mark the context unmappable). Costs nothing until `ssl`
  is imported.
- `goaio_tls.unmappable` says why a context stays with Python: a subclass (truststore),
  Python callbacks (SNI, PSK, `_msg_callback`), a password on the key, CRL flags,
  post-handshake auth, a server context without a chain, unknown curves, TLS 1.2 only with
  no Go cipher.
- Verification on the Go path is Python's: the pool is the recorded `cafile`/`capath`/
  `cadata` and `ssl.get_default_verify_paths()` (not Go's system pool), x509 `Verify`
  with server-auth key usage, `check_hostname`, and OpenSSL's common-name fallback for
  certificates without DNS SANs (`hostname_checks_common_name`, on by default in Python:
  first treated as unmappable, which turned every default context away). Failures carry
  OpenSSL's verify codes (20, 18, 19, 10, 9, 26, 62) and raise
  `ssl.SSLCertVerificationError` as Python's ssl does.
- `_GoTLSTransport` gives `ssl_object` (version, cipher by OpenSSL's names, ALPN,
  `getpeercert()` decoded by OpenSSL's own `_ssl._test_decode_cert`, `{}` under
  CERT_NONE), `peercert`, `cipher`, `sslcontext`; `can_write_eof()` is False.
- `_GoTransport` needed `_start_tls_compatible` (start_tls) and `_force_close` (SSLProtocol's
  fatal errors) for the fallback.
- Checked: `TestTLS` (stock loop, GoEventLoop, GoSelectorEventLoop give identical results
  for echo, client certificates, hostname mismatch 62, untrusted 20, CERT_NONE, start_tls,
  a subclass, certifi's three forms in certifi's file format, a CN-only certificate with
  the fallback on and off; GoEventLoop's path per case asserted). Seven controls caught.
  By hand: certifi 2026.10.23 (`cafile=`, `load_verify_locations`, `cadata=`) against
  pypi.org, github.com and www.python.org gives the same status, version and issuer on
  the stock loop and on the Go path; so does `create_default_context()` alone
  (`capath=/etc/ssl/certs`).

### TLS throughput: crypto/tls against Python's ssl (2026-11-17)
`bench/tls/run.sh`: a Python asyncio client (`bench/tls/client.py` under pyrun) against one
Go TLS peer (`bench/tls/peer`, ECDSA P-256, TLS 1.3, TLS_AES_128_GCM_SHA256 for every
configuration), 5 s runs, 3 interleaved rounds, medians. Configurations: `go` (GoEventLoop,
crypto/tls), `py` (GoEventLoop, SSLContext subclass: SSLProtocol and OpenSSL 3.5.9),
`stock` (asyncio's own loop, Python's ssl), `plain` (GoEventLoop, no TLS). Go 1.26.11,
Cortex-X925.

Client on core 5, peer on 6-9:

| | 3.14t go | py | stock | plain | 3.13 go | py | stock | plain |
|---|---|---|---|---|---|---|---|---|
| bulk 1 conn, MB/s | 790 | 2,505 | 2,687 | 846 | 1,321 | 1,638 | 1,876 | 1,353 |
| rr 1 KiB, 1 conn, req/s | 48.2k | 37.9k | 52.8k | 52.8k | 50.6k | 42.2k | 56.9k | 55.7k |
| rr 1 KiB, 64 conns | 78.1k | 52.1k | 85.4k | 84.6k | 79.1k | 57.7k | 95.3k | 85.2k |
| handshakes, 1 at a time /s | 1,369 | 1,175 | 1,310 | | 1,377 | 1,290 | 1,390 | |
| handshakes, 64 at once /s | 1,671 | 1,619 | 1,802 | | 1,740 | 1,688 | 1,890 | |

Client on cores 5-6, peer on 15-18:

| | 3.14t go | py | stock | plain | 3.13 go | py | stock | plain |
|---|---|---|---|---|---|---|---|---|
| bulk 1 conn, MB/s | 1,403 | 2,602 | 2,641 | 2,100 | 2,273 | 2,107 | 1,854 | 4,544 |
| rr 1 conn | 27.6k | 22.4k | 32.5k | 29.6k | 27.8k | 23.0k | 34.6k | 28.7k |
| rr 64 conns | 109.1k | 85.1k | 69.3k | 126.3k | 113.3k | 95.4k | 80.4k | 125.5k |
| handshakes, 1 at a time | 1,245 | 1,167 | 1,323 | | 1,289 | 1,198 | 1,371 | |
| handshakes, 64 at once | 3,026 | 1,895 | 1,815 | | 2,965 | 1,990 | 1,865 | |

- On GoEventLoop, Go's TLS beats Python's ssl everywhere but 1-connection bulk: request
  /response +18 to +50%, handshakes +3 to +60%.
- On one core it stays a little behind asyncio's own loop (rr -9 to -17%, 64 handshakes
  -7 to -8%). Given a second core it overtakes: crypto/tls runs in goroutines beside the
  Python thread (client CPU 1.4-1.7 cores), while Python's ssl does every record on the
  loop thread: rr 64 +41 to +57%, 64 handshakes +59 to +67% over stock.
- Bulk on the Go path is no faster than GoEventLoop without TLS on one core (790 against
  846 MB/s on 3.14t). Superseded: the cause was the unbounded copy-mode reader, see
  "Read credits" below (1-core Go-TLS bulk on 3.13 is now 3.5 GB/s, plain 7.9 GB/s).
- 1-connection rr is latency-bound: moving the peer to cores 15-18 cut every
  configuration (stock 52.8k to 32.5k), so compare it only within one layout. Round
  outliers (3.13 plain bulk 3,230; go bulk 925) are the shared host.

### Coalesced reads (2026-11-17)
- After a read of at least 16 KiB (one TLS record's plaintext), the reader goroutines keep
  reading what is ready without waiting, up to the buffer (`aio/coalesce.go`), so one
  `EV_DATA`/`EV_FILLED` carries it. Plain descriptors use a non-blocking `read(2)` through
  `RawConn`; a TLS connection's socket is wrapped (`nbConn`) so that `crypto/tls` reads
  it non-blockingly while coalescing and gets a temporary timeout (`errWouldBlock`), which
  it does not make sticky. A past read deadline would not work: the poller then fails the
  read without trying the socket. An EOF or error found while coalescing is delivered
  after the data.
- The 16 KiB threshold: trying after every read cost about 3% on 1 KiB request/response
  with 64 connections (the extra read finds nothing); with it, request/response is
  unchanged (within +-2%).
- Measured interleaved against the same build without coalescing (`go build -overlay`),
  3.13: Go-TLS bulk +31% and +37% (two runs) with the client on two cores; on one core no
  change (+0.8% over 8 rounds; 1-core bulk rounds spread 1,049-1,398 MB/s). Python's ssl
  and asyncio's own loop unaffected. `TestCoalescedReads`: a paused client then gets
  chunks well over 16 KiB; control (no coalescing) gives 263 chunks of exactly 16,384.

### Read credits: one EV_DATA in flight per connection (2026-11-17)
- **Symptom:** on one core, Go-TLS bulk stayed at 1.2 GB/s (stock loop 1.83) while the same
  run with `GOMAXPROCS=2`, still on that one core, gave 2.7 GB/s. CPU per GB doubled at
  GOMAXPROCS=1, all of it in the Python thread (`runtime.cgocall` 0.54 against 0.12 s/GB),
  and chunk sizes were the same (256 KiB per `feed_data`), so it was not per-event cost.
- **Cause:** 2.1M minor page faults in 5 s (about one per 4 KiB page received; 250k at
  GOMAXPROCS=2 for twice the data), nearly all in the Python thread, with system time
  2.6× its user time. strace showed glibc releasing 26-43 MB ranges (`MADV_DONTNEED`) and
  unmapping and re-creating 64 MB arena heaps. The copy-mode reader kept reading until
  Python's `pause_reading` arrived, and with one P the reader goroutine held it for a
  whole time slice while the Python thread, back from C, waited for a P: about 100
  buffers (25 MB) queued, the StreamReader took them at once, and its bytearray was
  grown, shrunk and copied onto fresh pages every round. Nothing bounded a connection's
  queued data except `maxBatch` (65,536 events: 16 GB).
- **Fix:** `aioConn.credits`, capacity `dataInFlight` (1). The reader takes a credit
  before posting `evData` and the event carries it (`completion.held`); `poll` hands it
  back after copying the event into bytes. Data the loop has not taken stays in the
  socket, where TCP pushes back, and the next read coalesces it. The BufferedProtocol
  reader already had one read in flight (`reads`).
- **Limits compared** (3.13, 5 s, 3 interleaved rounds, client on core 5 or 5-6, peer on
  6-9; GB/s, then minor faults per GB received):

  | in flight | 1 core | 2 cores | faults/GB |
  |---|---|---|---|
  | unbounded (before) | 1.14-1.25 | 3.21-3.79 | 326k-392k (1 core) |
  | 1 | 3.49-3.52 | 3.82-4.30 | 340-380 |
  | 2 | 3.46-3.50 | 3.61-4.54 | up to 63k |
  | 4 | 2.35-3.29 | 4.50-4.54 | up to 181k |
  | stock loop | 1.79-1.86 | 1.86-2.84 | 294k |

  1 is the only setting without fault episodes and the fastest on one core. The stock
  loop takes about 294k faults/GB too (its own buffering; not investigated).
- **Elsewhere on one core** (medians, before → after): plain bulk 2,385 → 7,875 MB/s; rr
  1 KiB with 64 connections 78.8k → 78.5k, 1 connection 51.2k → 50.4k, plain rr 64
  86.3k → 85.5k, 64 handshakes 1,839 → 1,830/s: within noise.
- **GOMAXPROCS=2 on one core, after the fix:** bulk 3-5% slower (3.39-3.43 against
  3.53-3.58 GB/s), rr with 64 connections +37% (107.1k-108.3k against 78.3k-78.9k req/s;
  the stock loop: 95.3k): with one P, the Python thread returning from C waits for the
  reader goroutines. Whether pythago should raise the default is open (TODO).
- **How it was found:** `PYRUN_CPUPROFILE`/`PYRUN_TRACE` (pyrun), `/usr/bin/time -f
  "%R"` for minor faults, per-thread `minflt` from `/proc/PID/task/*/stat`, and `strace -f
  -e trace=madvise,munmap,mremap,brk` (perf is not allowed here:
  `perf_event_paranoid` = 4).
- `TestReaderWaitsForPoll` (internal): a flooding peer, nothing polled for 300 ms: exactly
  `dataInFlight` events queued, and one more after a credit is handed back. Control
  (post without a credit): "3502 EV_DATA events queued while the loop did not poll, want 1".

## Files
- `aio/aio.go`: `Configure`, the `_goaio` module (`module()`), `batch` and `kick`, the
  `poll` wait under `py.Release`, `aioConn` (`wake`, `resume`, `stop`, `rbuf`, `rc`),
  `bufferedReader`, `read_into`, the `ppoll`/eventfd watchers for blocking fds, `errnoOf`.
- `aio/goaio.py`: `GoEventLoop` (subclass of `base_events.BaseEventLoop`) and its
  transports (`get_buffer`, `buffer_updated`, `_read_next`).
- `aio/channel*.go`, `aio/channel.py`: Go channels for Python (not covered here).
- `.agents/attic/spike/goaio`: the original spike.

## Test Coverage
`GOWORK=$PWD/.work/go.work go test ./aio`; `aio` is in the unit tier (`ci/unit.sh gil`,
`ci/unit.sh ft`).

`aio/aio_test.go`:
- `TestEcho`: echo with 1 MiB backpressure, 300 concurrent connections, name resolution.
- `TestWakeups`: the lost-wakeup case.
- `TestErrors`: includes `socket.gaierror` for `bad!host`.
- `TestSelectorLoopFeatures`: UDP, a subprocess and `sock_*` on the selector loop.
- `TestLoopReleasesThreadState`: the thread state is released while waiting.
- `TestLoopsOnThreads`: loops on 8 threads at once.
- `TestChildProcess`: `sys.executable` children.
- `TestSource`: `goaio.py` source available to tracebacks.
- `TestBufferedProtocol`.
- `TestCloseWhileReading`: graceful close while a paused reader waits. Control (the
  `stop` `select` reverted): fails with `TimeoutError`.

At the port: 30/30 on 3.13 and 3.14t natively, 5/5 per variant on amd64 under qemu.

## Pitfalls
- **A kick-only wakeup must never be drained speculatively.** Nothing else records the
  work; drop it and the loop sleeps until the next timeout.
- **Test blocking fds, not just sockets.** The netpoller silently refuses blocking fds,
  and a goroutine that exits on the first `RawConn.Read` error hides it.
- **A paused reader blocks graceful close.** The writer waits for the reader, so
  `connection_lost` never came. The writer now closes a `stop` channel after closing the
  socket, and the reader selects on it.
- **Do not negate glibc `EAI_*` values;** they are already negative.
- **Use hermetic bad hostnames** (`bad!host`). `*.invalid` goes to real DNS; `bad..host`
  fails in IDNA before the loop sees it.
- **Measure the fixed per-operation cost at small sizes before optimizing.** Reuse was a
  reasonable request; the measurement showed the cost was the wakeup round trip.
- **Only one read may be in flight per connection** (`goaio: a read is already pending`);
  the reused `rbuf` depends on it.
