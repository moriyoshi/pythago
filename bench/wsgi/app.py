"""The WSGI benchmark application: a plain WSGI callable (no framework)."""
from __future__ import annotations

import time
from collections.abc import Iterable

TYPE_CHECKING = False
if TYPE_CHECKING:
    from _typeshed.wsgi import StartResponse, WSGIEnvironment

HELLO = b"Hello, world!"
BIG = b"x" * (64 << 10)


def app(environ: WSGIEnvironment, start_response: StartResponse) -> Iterable[bytes]:
    path = environ["PATH_INFO"]
    if path == "/big":
        body = BIG
    elif path == "/sleep":  # blocking I/O stand-in: the GIL is released, the thread is not
        time.sleep(0.01)
        body = b"slept"
    elif path == "/upload":  # reads the request body, answers with its length
        n = len(environ["wsgi.input"].read(int(environ.get("CONTENT_LENGTH") or 0)))
        body = b"received %d" % n
    else:
        body = HELLO
    start_response("200 OK", [("Content-Type", "text/plain"), ("Content-Length", str(len(body)))])
    return [body]
