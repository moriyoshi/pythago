# WSGI Server: Design, Rules and Measurements

## Summary
Package `wsgi` serves a PEP 3333 application from Go: `wsgi.New("module:app", Options)`
returns an `http.Handler`. There is no event loop and no worker thread: each request
runs on the goroutine net/http serves it on, through `py.Do`, so the admission slots
(`py.Config.Slots`) are the server's thread pool. On one core it serves 2.7× gunicorn
gthread's requests per second, and Flask 3.1 behaves as under gunicorn.

## Key Facts
- **One Go→Python call per request** (`pythago_wsgi.serve(app, base, h, packed, body)`)
  and, for a simple response, one Python→Go call (`respond`: head plus first chunk).
- **Concurrency is `Config.Slots`:** min(4, GOMAXPROCS) with the GIL, GOMAXPROCS
  free-threaded. A request blocked in Python (DB driver, `time.sleep`) holds its slot; a
  request blocked on the client (body read, response write) does not (`py.Release`).
- **HTTP_HOST is `r.Host`;** an HTTP/2 `host` field that differs from `:authority` gets
  a 400 (RFC 9113 §8.3.1). Go's HTTP/2 server leaves it in `r.Header`, and the app saw
  `evil.example` while Go routed by `good.example` (asgi had the same hole).
  **CONTENT_LENGTH is `r.ContentLength`,** not the header as sent (an HTTP/2 `05`).
- **The prefetch buffer grows with the data** (`prefetch`, `io.ReadAll` over a
  `LimitReader`): a declared 64 KiB and no body no longer costs 64 KiB per connection.
- **The environ crosses as one latin-1 byte string** `name\0value\0...` (PEP 3333's
  "bytes as str"), split in C on the Python side. **Any NUL rejects the request (400):**
  the decoded path can carry one (`/env%00HTTP_X_ADMIN%00yes`), which would shift the
  pairs and set environ keys.
- **Response heads are validated in Go before anything is written** (`parseHead`):
  status `NNN reason` with 200-599, names as RFC 9110 tokens, values without control
  characters (`internal/httpfield`, shared with `asgi`), the header pair count, no
  hop-by-hop headers (PEP 3333), at most one decimal Content-Length.
- **Go alone decides 500 vs abort:** Python reports DONE, FAILED or ABORT (client gone);
  FAILED becomes a 500 if the head was not written (`q.started`), else a dropped
  connection.
- **Flushing:** yielded chunks are flushed one by one (PEP 3333: no delaying); a list or
  tuple result is written unflushed with a computed Content-Length (unless the app set
  one).
- **Nothing is written to the client while attached.** Small unflushed chunks gather in
  a Go buffer (`pending`, up to 16 KiB) that is written detached, or after `py.Do`
  returns; larger chunks and flushes go through `py.Release`. An attached write blocks on
  a client that does not read (or an HTTP/2 window of 0), which with the GIL stops every
  Python thread.
- **The request lock is never waited for attached:** `with` tries `q.mu`, and otherwise
  waits inside `py.Release`. A thread holding it can be detached in a slow body read and
  need its thread state back.
- **Each request runs in a fresh `contextvars.Context`;** thread states are cached per OS
  thread, so a ContextVar left set would otherwise reach the next request. `threading.local`
  still persists per thread, as in gunicorn gthread.
- **`wsgi.file_wrapper`** sends a regular file from Go (fstat on the fd, `dup`,
  `io.CopyN` → sendfile), from `tell()`; Content-Length is the app's (which must fit the
  file, else a 500) or the remainder. Non-regular files and file-likes fall back to
  iteration.
- **Request headers with "_" are dropped** (spoofing `X-Forwarded-For` as
  `X_Forwarded_For`), as nginx and gunicorn do; stock wsgiref passes them on. `Proxy`
  is dropped (httpoxy). Repeats join with ", ", Cookie with "; ".
- **Body:** known length ≤ `Options.Prefetch` (64 KiB) and no `Expect` → read before
  Python, `wsgi.input` is a `BytesIO`; otherwise `io.BufferedReader` over a `RawIOBase`
  whose `readinto` reads into Python's buffer from Go, detached.
  `wsgi.input_terminated` is True.
- **net/http enforces a declared Content-Length** on both protocols: a shorter body
  closes the HTTP/1.1 connection or resets the HTTP/2 stream. A check of our own was
  dead code (its mutant survived) and was removed.

## Details

### Request path
`ServeHTTP` → 503 if closing, 405 for CONNECT, 404 outside `ScriptName`, `environ()`
(400 on NUL), optional prefetch, register the request (`reqs`, a `sync.Map` of handles
that are never reused, so a stale `write()` finds nothing), `py.Do(serve.Call(...))`.
`serve` (Python) builds the environ (`base.copy()` plus the decoded pairs), calls the
app, sends the result, and always calls `close()`.

The Go callbacks normally run on the request's own OS thread (the Python call it made),
but each takes the request's mutex and checks `done`, so a write from another Python
thread, or after the response, is harmless.

`pythago_wsgi` is created from the embedded source at the first `New`, as a real module
in `sys.modules` with a `linecache` entry, so tracebacks show its source. The Go
functions are `_pythago_wsgi` (`py.NewModule`); no `Configure` step is needed.

### PEP 3333 behaviour
- `start_response` type-checks eagerly (str status, list of 2-tuples of str, latin-1
  encodable) and packs the headers with their count; a second call needs `exc_info`,
  which re-raises if the head is out, else replaces status and headers.
- Body chunks must be `bytes` (TypeError otherwise, as wsgiref).
- Errors before the head: logged (`pythago.wsgi` logger), 500 `Internal Server Error`.
  After: the connection is dropped. `close()` errors are logged; they fail the request
  only if nothing was sent.
- HEAD: the head a GET gets (Content-Length included for list results), no body. 204
  and 304: no Content-Length, a body is an error.
- net/http writes its own reason phrase (`302 Found`, where Flask says `302 FOUND`) and
  canonical header names (`Etag`). Canonical keys are also how net/http finds
  Content-Length and friends, so they are kept.

### Measurements (2026-11-17)
CPython 3.13.18, gunicorn 27.0.1 on the stock PBS python3.13, `bench/wsgi/run.sh`
(plain WSGI app `bench/wsgi/app.py`): one process pinned to core 5, 4 threads
(gunicorn `-k gthread --threads 4`, wsgiserve `-threads 4`), `httpload` on cores 15-19,
8 s per run, 2 interleaved rounds. req/s, round 1 / round 2:

| | `/` c=1 | `/` c=64 | `/big` (64 KiB) c=64 | `/sleep` (10 ms) c=64 |
|---|---|---|---|---|
| gunicorn sync | 15.4k / 8.5k | 20.3k / 10.6k | 16.7k / 9.6k | 95 / 97 |
| gunicorn gthread 4 | 21.2k / 21.8k | 25.5k / 26.4k | 21.6k / 21.8k | 381 / 380 |
| wsgi (net/http) 4 | 44.7k / 44.8k | 69.9k / 71.2k | 42.7k / 42.2k | 386 / 386 |

- gunicorn sync closes every connection (`httpload` redials on `Connection: close`);
  its round-2 drop is that churn on a shared host, the other configurations are stable.
- `/sleep` is bound by threads × 100/s (4 × 100 ≈ 390) for every threaded server: raise
  `Config.Slots` for blocking applications.
- p50 at c=64: 0.9 ms against gunicorn gthread's 2.3 ms.

### Stock comparisons
- `TestEnvironMatchesWsgiref` runs `wsgiref.simple_server` on a Python thread of the
  test process and sends both servers the same raw requests (latin-1 header, `%C3%A9`,
  `%2F`, dot segments, HTTP/1.0, a POST body). PEP 3333 and CGI keys and every HTTP_*
  match, except the intended underscore difference; wsgiref also copies `os.environ`
  in and defaults CONTENT_TYPE to text/plain, CONTENT_LENGTH to "".
- `TestValidator` serves `wsgiref.validate.validator(app)` with WSGIWarning made an
  error: environ, `wsgi.input` (read/readline/iteration), `wsgi.errors`, write(),
  streaming, file_wrapper fallback and `exc_info` all pass.
- Flask 3.1.3 / Werkzeug 3.1.9 under wsgiserve and gunicorn gthread: the same bodies,
  hashes and headers for JSON echo with cookies and a UTF-8 query, a 300 KB multipart
  upload, `send_file` (2,000,123 bytes; ETag equal), `stream_with_context`, redirect,
  `set_cookie`, 404, 500 and a UTF-8 path parameter.
- strace: `sendfile(5, 9, NULL, 1999611)` for Flask's `send_file` (net/http writes the
  first 512 bytes itself, as with asgi's pathsend).

## Files
- `wsgi/wsgi.go` — `Server`, `Options`, `New`, `Close`, `ServeHTTP`, head validation,
  writes, sendfile, body reads, the `_pythago_wsgi` functions.
- `wsgi/environ.go` — the packed environ, header mapping and drops.
- `wsgi/pythago_wsgi.py` — `serve`, `_Response` (`start_response`, `write`), `_Input`,
  `FileWrapper`, `ClientDisconnected`.
- `internal/httpfield/httpfield.go` — `ValidName`, `ValidValue` (from `asgi/fields.go`).
- `internal/tools/wsgiserve` — gunicorn-like command (`-threads`, `-script-name`,
  `-processes` through `workers.Spawn`, `-cert`/`-key`, `-cpuprofile`; header and idle
  timeouts).
- `bench/wsgi/` — `run.sh`, `app.py`, `flask_app.py`.

## Test Coverage
`GOWORK=$PWD/.work/go.work go test ./wsgi`; `ci/unit.sh gil|ft` (natively and under
qemu) and `ci/darwin.sh gil|ft` run it. Tests: `TestEnviron`,
`TestEnvironMatchesWsgiref`, `TestScriptName`, `TestPathEncoding`, `TestBodyStreamed`
(5 MiB chunked), `TestBodyPieces` (prefetched, over Prefetch, chunked, 100-continue),
`TestResponses`, `TestStreamingFlushes`, `TestErrors`, `TestDisconnect`,
`TestBadResponses` (26 malformed heads over a raw socket: 500, nothing injected),
`TestContentLength` (HTTP/1.1 and HTTP/2), `TestFileWrapper`, `TestValidator`,
`TestConcurrency`.

Controls (`.agents-workspace/tmp/mutate_wsgi.py`, not kept): 21 mutants, one per defence,
each caught by the intended test: environ NUL, underscore and Proxy drops, Cookie
separator, pair count, name, value, hop-by-hop, duplicate and malformed Content-Length,
status space, range and control characters, Python header and list type checks, flush,
close(), Go's 500-vs-abort, file_wrapper length, 204 body, exc_info re-raise.
- `status-space` first passed for the wrong reason (`"200"` made the mutant panic on
  `status[4:]`); `"200XOK"` now isolates it.
- The no-body mutant first "failed" by not compiling; it was redone as
  `len(b) < 0`, which builds and is caught.
- Mutants that survived showed redundant defences, which were removed rather than
  kept: our short-body abort (net/http enforces it) and Python's 500-vs-abort choice
  (Go's `q.started` decides). Python's setting `sent` only after `respond` returns is
  no longer load-bearing for the same reason.

### Security review (2026-11-17)
A read-only review agent and my own list, after the first version:

| Finding | Fix | Test (control) |
|---|---|---|
| attached writes block on a non-reading client: all Python stops | `pending` buffer, writes detached | `TestSlowReaderDoesNotStall` |
| `q.mu` held across `py.Release`, waited for attached: two-thread deadlock with the GIL | `TryLock`, else wait detached | `TestTwoThreadsOneRequest` |
| HTTP/2 `host` ≠ `:authority`: app sees the attacker's host | 400; Host skipped in the header loop (wsgi and asgi) | `TestHTTP2RequestFields`, asgi `TestHTTP2HostField` |
| prefetch allocates the declared length up front | `prefetch` grows; asgi `readChunk` grows too (it allocated 64 KiB per body chunk) | `TestPrefetchGrows`, `TestReadChunkGrows` |
| CONTENT_LENGTH from the raw header | from `r.ContentLength` | `TestHTTP2RequestFields` (`05`) |
| a stale `write()` could slip between the final unlock and `done` | `done` set in the final critical section | none deterministic |
| ContextVars carried across requests | fresh `Context()` per request | `TestContextPerRequest` |
| no ReadHeaderTimeout in the tools, none in the doc examples | tools set 10 s header and 2 min idle timeouts; docs show `http.Server` | none |
| file_wrapper sendfile shares the file offset with Python's file object | documented: one file object per response, as with `read()` | none |
| worker marker stays in Python's `os.environ` if `InheritedListener` runs after `py.Init` | documented order | none |

Raw HTTP/2 requests come from `internal/h2raw` (x/net/http2 Framer and hpack).

## Pitfalls
- **Go's HTTP/2 server keeps a `host` field in `r.Header`;** HTTP/1.1 removes it. Never
  pass request headers through without deciding what Host means.
- **An attached call that can block on the network stops every Python thread** with the
  GIL: write to clients only detached.
- **A decoded path is attacker bytes:** check every packed value, not just headers.
- **A test app must not read `wsgi.input` past CONTENT_LENGTH** unless
  `wsgi.input_terminated`: wsgiref's input is the socket and blocks.
- **`py.ExecIn` needs `__builtins__` in the namespace** for `import` to work.
- **Python 3.14 warns on `return` in `finally`** (SyntaxWarning); `serve` computes an
  outcome instead.
- **Header-name case:** keep net/http's canonical keys, or net/http no longer sees the
  application's Content-Length and frames the body itself.
