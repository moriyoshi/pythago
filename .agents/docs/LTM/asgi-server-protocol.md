# ASGI Server: Architecture and Protocol

## Summary
Package `asgi` serves an ASGI application from Go: `asgi.New("module:app", Options)` returns
an `http.Handler`, so net/http owns HTTP/1.1, HTTP/2, TLS, keep-alive and slow clients, and
the app can share a mux with Go handlers. On top of the plain request path it implements
response trailers, WebSockets (HTTP/1.1 and HTTP/2 via RFC 8441) and
`http.response.pathsend` (sendfile), each with strict, Go-enforced validation that was
mutation-tested check by check.

## Key Facts
- **Workers** are Python threads, each running `goaio.run(worker.main())` (a
  `GoEventLoop`), with the lifespan protocol per worker, as uvicorn has per worker process.
  `Options.Workers` defaults to 1 with the GIL and to GOMAXPROCS on free-threaded builds.
- **Requests** run inline by default on the request goroutine's thread (see
  [asgi-inline-and-flat-combining.md](asgi-inline-and-flat-combining.md)). With
  `Options.LoopThread`, the goroutine posts the request to the loop thread through the
  `aio.Loop` API (`LookupLoop`, `Post`, `Release`, event kind `EV_GO`). WebSockets and
  lifespan always use the loop thread.
- **Responses:** Python's `send()` queues actions (start, body, read more, abort) through
  `_pythago_asgi` callbacks (`respond`, `body`, `trailers`, `pathsend`, `want_body`,
  `abort`, `ws_*`); the request goroutine performs them. Bodies are written zero-copy
  from the bytes object (copied up to 4 KiB, `maxCopiedBody`), with backpressure past
  `HighWater` (64 KiB). The request goroutine prefetches up to `BodyChunk` (64 KiB) of body.
- **Errors are never repaired:** `send()` raises. Before anything reached the client the
  request rolls back and the error handler answers 500; after, the connection is aborted.
  General rules: [hardening-network-facing-code.md](hardening-network-facing-code.md).
- **Headers cross as one bytes object** `name\0value\0...` plus a pair count. The pair
  count closes the injection hole the packing opened (value `"1\0set-cookie\0evil=1"`).
- **WebSockets are same-origin by default** (`WebSocketOrigins`, `WebSocketAnyOrigin`);
  uvicorn checks nothing here.
- **WebSockets over HTTP/2 need `GODEBUG=http2xconnect=1` in the process environment.**
  `asgi` documents it but never sets it.
- **pathsend uses sendfile:** strace showed `sendfile(8, 9, NULL, 1999488)` for a
  2,000,000-byte Starlette `FileResponse` (net/http writes the first 512 bytes itself).
- **Speed is not the selling point** of the Go front end on one core: HTTP/2, Go's TLS,
  sharing a mux with Go handlers and one process for Go and Python are. See
  [performance.md](performance.md) for the net/http floor (about 9 µs per request).
- FastAPI 0.163.1 / Starlette 1.10.0 worked on the first try: lifespan state, validation
  (422), `StreamingResponse`, `/docs`, SIGTERM shutdown, `@app.websocket`.

## Details

### Request path and the first optimizations
Measured on the loop-thread design (before inline requests existed), raw app, 64
connections, req/s:

| | 1 core | 2 cores |
|---|---|---|
| stock uvicorn + uvloop | 71.8k | 72.5k |
| uvicorn on GoEventLoop | 34.9k | 75.8k |
| Go `asgi`, first version | 27.9k | 45.3k |
| Go `asgi`, after the three fixes below | 38.2k | 65.2k |

The profile (`asgiserve -cpuprofile`; Go samples every thread, so Python shows as `cgocall`):

```text
 7.79s 55.09%  runtime.cgocall
 6.59s 46.61%  reflect.Value.call        (purego's callback dispatch)
 4.08s 28.85%  github.com/moriyoshi/pythago/py.From
 1.37s  9.69%  runtime.AddCleanup[...]   (one per nested converted element)
 1.68s 11.88%  asgi.module.func6         (start(): headers through Object.Go)
```

1. **`py.From` registered a GC cleanup for every nested element,** which the container
   then stole. `From` now builds nested values as raw references (`fromRaw`, `sequence`,
   `dictSet`), and `Go()` reads list and dict items without wrappers. This speeds up every
   conversion in `py`.
2. **Headers crossed as tuples** (several purego calls per header, each way). They now
   travel as one `name\0value\0...` bytes object, packed and split in C on the Python side
   (`b"\0".join(chain(...))`, then `split` plus `zip`; `_pack`/`_unpack` in
   `pythago_asgi.py`).
3. **`start` and the first body go in one `respond` call:** Python holds the start until a
   body exists (see [design-lessons.md](design-lessons.md), "Batch boundary crossings").

On two cores neither side was saturated: a goroutine running Python holds its scheduler
slot while in C until sysmon retakes it (≥ 20 µs). Raising GOMAXPROCS above the core count
helped:

| 2 cores, GOMAXPROCS | 2 | 3 | 4 |
|---|---|---|---|
| req/s | 65.8k | 67.9k | **69.9k** |

That was within 4% of uvicorn + uvloop. FastAPI at 64 connections, before these fixes:
stock uvicorn + uvloop 32.2k; Go `asgi` 19.3k (one core) and 26.9k (two). Later work
(inline requests, worker processes) is in
[asgi-inline-and-flat-combining.md](asgi-inline-and-flat-combining.md) and
[asgi-worker-processes.md](asgi-worker-processes.md).

### Trailers and response-field rules (`asgi/fields.go`)
- **Request trailers** (`r.Trailer`) are never passed on and never merged into headers. A
  request header containing NUL gets a 400.
- **Offering:** trailers are offered only for HTTP/1.1 or HTTP/2, not HEAD, and only when
  `TE` lists `trailers`. Go enforces the offer as well as Python.
- **Order:** start with `trailers: True`, then the body, the final body, trailers
  (`more_trailers`), and the last trailers. A response that ends without its last trailers
  is aborted, so a truncated message never looks complete.
- **Forced chunked flush:** with trailers, the head is flushed so net/http commits to
  chunked encoding. Without it, net/http computes a Content-Length for a one-chunk body
  and drops the trailers silently.
- **Forbidden trailers** (RFC 9110 §6.5.1 plus state management):
  - framing and connection: Content-Length, Transfer-Encoding, Trailer, TE, Connection,
    Keep-Alive, Proxy-Connection, Upgrade;
  - routing: Host;
  - authentication: Authorization, Proxy-Authorization, WWW-Authenticate,
    Proxy-Authenticate, Realm;
  - request modifiers and controls: Expect, Max-Forwards, Range, the If-* conditionals,
    Cache-Control, Pragma;
  - response controls: Age, Date, Expires, Location, Retry-After, Vary, Warning;
  - content metadata: Content-Type, Content-Encoding, Content-Range, Content-Language,
    Content-Location, Content-Disposition;
  - state management: Set-Cookie and Cookie (Go's own list misses Set-Cookie).
- **Limits:** 64 trailer fields and 32 KiB (`maxTrailerBytes`).
- **Every response field, headers included:** names must be RFC 9110 tokens; values may
  contain no control characters except HTAB, and no DEL.
- **Framing:**
  - at most one Content-Length, a non-negative decimal (net/http would write duplicates);
  - no Content-Length with trailers;
  - status 200-599 only. 1xx would be interim (101 switches protocols), and out-of-range
    codes make `WriteHeader` panic.
- **The header-injection hole.** The NUL-packed format would let a value
  `"1\0set-cookie\0evil=1"` shift the pairs and emit a real `Set-Cookie`. Python sends the
  pair count with the packed bytes and Go rejects any mismatch; the value check catches it
  independently.

### WebSockets (`asgi/websocket.go`)
Engine: `github.com/coder/websocket` v1.8.15 (framing, masking, fragmentation, control
frames, pings, closing handshake). It does not validate UTF-8 in text frames and its
default read limit is 32 KiB, so both are handled here. Rules, enforced in Go before the
app sees anything:

1. **Origin:** same-origin by default; a request without `Origin` (non-browsers) passes.
   `WebSocketOrigins` adds `path.Match` host patterns ("scheme://host" when the pattern
   has `://`); `WebSocketAnyOrigin` hands the check to the app.
2. **Handshake:** GET over HTTP/1.1, the Upgrade and Connection tokens, version 13 (else
   426 with `Sec-WebSocket-Version: 13`), and a base64 key of 16 bytes.
3. **Answer first:** close before accept → 403; a `websocket.http.response` denial is sent
   as is; no answer or an exception → 500; an exception after accept → 1011; a return
   after accept → 1000.
4. **Subprotocol:** must be one the client offered.
5. **Accept headers:** validated, and may not set `Sec-WebSocket-*`, Upgrade, Connection,
   Content-Length, Transfer-Encoding, TE or Trailer.
6. **Messages:** `WebSocketMaxMessage`, 16 MiB by default as uvicorn (larger → 1009); no
   compression; invalid UTF-8 → 1007.
7. **Close codes:** 1000-1003, 1007-1014 or 3000-4999, with reasons of at most 123 bytes.
8. **Credits:** a window of 16 incoming messages, acked per `receive()` (`ws_ack`). Sends
   wait for drain.
9. **After disconnect:** `receive()` returns the peer's code and reason; `send()` raises
   `pythago_asgi.ClientDisconnected` (an `OSError`).

### WebSockets over HTTP/2, RFC 8441 (`asgi/websocket_h2.go`)
- **The switch.** Go 1.26's HTTP/2 server rejects `:protocol` unless the process
  environment has `GODEBUG=http2xconnect=1`, read in `h2_bundle.go`'s `init`
  (`http2disableExtendedConnectProtocol`, Go issue #71128). A `//go:debug` directive does
  not reach it. It is off by default because browsers then try WebSockets over HTTP/2
  against every handler, so the program decides.
- **Our handshake:** CONNECT with `:protocol websocket`, version 13, the origin check.
  Accept is a 200 with `Sec-WebSocket-Protocol`, flushed.
- **The stand-in hijack.** `coder/websocket` only accepts hijacked HTTP/1.1, but its
  `accept` only calls `WriteHeader(101)` and then `Hijack()` and writes nothing to the
  connection. So after our own handshake it gets:
  - a stand-in `ResponseWriter` whose `Hijack` returns the HTTP/2 stream as a `net.Conn`:
    reads from `r.Body`, writes flushed through `http.ResponseController`;
  - a stand-in GET request shaped like the upgrade it verifies.

  Its core uses no deadlines (context timeouts and `rwc.Close()` only), so the stream
  conn needs none.
- **Other CONNECTs get 405** over both protocols: proxy tunnels, and `:protocol` values
  other than websocket.
- `asgiserve` has `-cert`/`-key` for HTTPS and HTTP/2.

### pathsend (`asgi/pathsend.go`)
Starlette's `FileResponse` uses `http.response.pathsend` when the extension is advertised.
- **Opening:** absolute paths only, opened once with `O_NONBLOCK|O_NOCTTY|O_CLOEXEC`, so a
  FIFO cannot block the open. `fstat` on that descriptor must say regular file.
- **Confinement:** `Options.PathSendRoots` confines paths through `os.Root` (`..` and
  symlink escapes are refused). Without it, any file the process can read may be sent.
- **Length:** the size at open becomes the Content-Length unless trailers follow. An
  app-set Content-Length must match it (else a clean 500). Exactly `size` bytes are sent;
  a file that shrinks mid-send aborts the connection.
- **Body:** pathsend replaces the body. After a body chunk it raises and the connection is
  aborted (the head is already out, so a 500 is impossible).
- **Proof of sendfile:**
  ```text
  $ strace -f -e trace=sendfile … asgiserve … fileapp:app   # Starlette FileResponse, 2,000,000 bytes
  4064021 sendfile(8, 9, NULL, 1999488)   = 1999488
  ```
  net/http writes the first 512 bytes itself (sniffing, through `response.ReadFrom`). The
  download was byte-identical.

## Files
- `asgi/asgi.go` — `Server`, `Options`, `New`, workers, `ServeHTTP`, the `_pythago_asgi`
  module of Go callbacks (`respond`, `body`, `trailers`, `pathsend`, `ws_*`, ...).
- `asgi/pythago_asgi.py` — the Python side: `_Worker`, `_Request`, `_Lifespan`,
  `_WebSocket`, `ClientDisconnected`, header packing (`_pack`, `_unpack`).
- `asgi/fields.go` — response-field and trailer validation, forbidden list, limits. The
  name and value checks live in `internal/httpfield` (shared with `wsgi`).
- `asgi/websocket.go` — WebSocket handshake checks, origin policy, credits, close codes.
- `asgi/websocket_h2.go` — RFC 8441 handshake and the stand-in hijack adapter.
- `asgi/pathsend.go` — pathsend open, confinement and sendfile path.
- `internal/tools/asgiserve` — command-line server (`-addr`, `-app-dir`, `-workers`,
  `-cert`, `-key`, `-loop-thread`, `-processes`, `-cpuprofile`).

## Test Coverage
Run with `GOWORK=$PWD/.work/go.work go test ./asgi`; `ci/unit.sh gil|ft` runs the suite
natively and under qemu, inline and again with `PYTHAGO_ASGI_LOOP_THREAD=1`. `TestMain`
(`asgi/asgi_test.go`) re-execs itself with `GODEBUG=http2xconnect=1` via `syscall.Exec`
(works under qemu's binfmt).

- **Trailers** (`asgi/trailers_test.go`): `TestTrailers`, `TestTrailersHTTP2`,
  `TestTrailerRejections`, `TestHeaderValidation`, `TestRequestTrailersIgnored`. Verified
  over HTTP/1.1 chunked and HTTP/2 with TLS; raw-socket injection attempts (NUL, CR/LF, a
  bad name, status 99/101/600/1000, duplicate Content-Length) give 500 with no
  `Set-Cookie` and no `evil`; a chunked request with an `X-Smuggle` trailer never reaches
  the app.
  - Controls: nine defences disabled in turn (pair count, forbidden list, value and name
    validation, limits, duplicate Content-Length, Content-Length with trailers, status
    range, forced chunked flush). Eight were caught at once. The flush mutant survived
    because the two-chunk test response already committed net/http to chunked; the
    one-chunk `/t/single` case now catches it:
    `trailers_test.go:49: single body: <nil> "short" trailer map[] content-length 5`.
- **WebSockets** (`asgi/websocket_test.go`): `TestWebSocketEcho` (text, binary, 100 KB,
  subprotocol `chat`, an accept header), `TestWebSocketHandshakeAnswers` (403, 401
  "nope", 500 before accept, 1011 after), `TestWebSocketOrigin` (cross-site 403, same
  origin OK, `*.trusted.example` allowed, look-alike `trusted.example.evil.example`
  refused), `TestWebSocketProtocolErrors` (1007, 1009, malformed handshakes),
  `TestWebSocketBackpressure` (2000 pushes to a pausing reader; a 1000-message flood while
  the app sleeps keeps at most 16 queued in Python, `receive.__self__.inbox`, and delivers
  all 1000), `TestWebSocketClientClose` (close 4001 "client bye"; send after disconnect
  raises). FastAPI `@app.websocket` under the `websockets` 17.4 client was checked by hand
  (subprotocol `chat`, JSON echo, close 4321 "done", cross-origin 403).
  - Controls: ten mutants, one per check, all caught. The key-length mutant first survived
    twice: the malformed-handshake tests went to `/ws/echo`, where the library's own
    accept-time check gave 400 anyway (they now go to `/ws/deny`, which would answer 403,
    so 400/426 proves the app never saw the request); and the key `short` is not valid
    base64, so the decode-error half caught it (`c2hvcnQ=` exercises the length check
    alone).
- **WebSockets over HTTP/2** (`asgi/websocket_h2_test.go`): `TestWebSocketHTTP2`,
  `TestWebSocketHTTP2Refusals`. net/http's `Transport` rejects the pseudo-header
  (`net/http: invalid header field name ":protocol"`, validated for http(s) URLs before
  HTTP/2), so the test uses `golang.org/x/net/http2`'s Transport (test-only dependency,
  v0.62.0, whose `:protocol` exception lives in `internal/httpcommon`) and a ~40-line
  frame codec (masked frames out, unmasked in).
  - Controls: origin and version checks caught; the CONNECT refusal caught as a timeout
    (the tunnel request reached the app and hung).
- **pathsend** (`asgi/pathsend_test.go`): `TestPathSend`, `TestPathSendRoots`.
  - Controls: abs, regular-file, Content-Length and roots caught; `O_NONBLOCK` caught as
    a test timeout (exactly the hazard). The abs mutant first survived because the
    relative path `data.bin` did not exist; the test now uses `pathsend_test.go`.

## Pitfalls
- **The worker threads are daemon threads; `Close` must join them.** After a worker
  detaches it still runs CPython's teardown of its thread state. If `Py_FinalizeEx`
  starts meanwhile, a worker past `_Py_brc_remove_thread` that detaches on a contended
  lock is cleared a second time (3.14t SIGSEGV in `llist_remove`, seen on darwin/amd64).
  `Close` calls `pythago_asgi.join` (Thread.join returns after the thread state is
  deleted); `TestCloseJoinsWorkers` slows each worker's teardown by 0.2 s to see it.
- **Packing values into a delimited format reopens injection.** Send a count and
  validate values independently.
- **net/http drops trailers silently** when it computes a Content-Length for a single
  write; trailers need the forced chunked flush.
- **pathsend after a body chunk is an abort, not a 500:** the head is out.
- **Do not set `GODEBUG=http2xconnect=1` from the library.** It changes every handler of
  the server; a `//go:debug` directive does not reach `h2_bundle.go`'s `init` anyway.
- **coder/websocket does no UTF-8 validation and limits reads to 32 KiB by default.**
- **A test can pass for the wrong reason** when another layer rejects the same input
  (library check, decode error, missing file). Aim tests where the app would answer
  differently.
- **The profile hides Python:** Go's pprof shows time in Python as `runtime.cgocall`.
- **Know the baseline's floor before promising a win** (net/http alone did 108.5k req/s
  on one core).
