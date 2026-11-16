# Hardening network-facing code

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- **Reject loudly; never repair or drop silently.** Malformed fields, misplaced trailers
  and bad close codes raise in the application's `send()`. Before anything is sent the
  client gets a clean 500; after, the connection is aborted, so a truncated response
  never looks complete.
- **Mutation-test every defence:** disable each check in turn and require a failing test.
  It found tests that passed for the wrong reason:
  - a relative test path that didn't exist (use one that does);
  - a bad WebSocket key that a library check also caught (aim at a route where the
    application would answer differently, so the test proves the request never reached
    it);
  - an invalid-base64 key that never reached the length check;
  - a forced flush that only a single-chunk response depends on.
- **Read the library's source for what it does not do:**
  - coder/websocket does not validate UTF-8 in text frames, and limits messages to
    32 KiB by default;
  - `Accept` only calls `WriteHeader` and `Hijack`, which is what made an HTTP/2 adapter
    possible;
  - net/http silently drops trailers when it computes a Content-Length.
- **Secure defaults, explicit opt-outs:**
  - same-origin WebSockets (`WebSocketAnyOrigin` to opt out);
  - request trailers never passed on;
  - non-WebSocket CONNECT refused;
  - pathsend opens with `O_NONBLOCK` and serves only regular files, decided on the open
    descriptor;
  - `os.Root` for confinement.
- **Global switches belong to the program, not the library.** Go's extended CONNECT
  (`GODEBUG=http2xconnect=1`) changes every handler of a server, so `asgi` documents it
  rather than setting it.
- **Packed request data is attacker data too** (wsgi, 2026-11-17): the environ crosses
  NUL-packed, and the percent-decoded path can carry a NUL that would shift the pairs and
  set environ keys. Check every packed value, not only headers.
- **A surviving mutant can mean a redundant defence.** In wsgi, our short-body abort was
  net/http's own check, and Python's 500-vs-abort choice was Go's; both were removed
  instead of being kept untested. A mutant that stops the build is not a control.
