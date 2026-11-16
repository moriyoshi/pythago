"""The Python half of package wsgi: serve() runs one request of a WSGI application
(PEP 3333) on the request goroutine's thread, and _pythago_wsgi's Go functions write
the response to the client and read the request body from it.

Go builds the CGI-style environ strings and packs them as one latin-1 byte string,
"name\\0value\\0...", split here in C. The response goes to Go as it is produced: the
status and the headers with the first non-empty body chunk (respond), then each chunk
(body). Go validates the status and every header before anything is written.
"""

from __future__ import annotations

import io
import logging
import sys
from contextvars import Context
from itertools import chain

import _pythago_wsgi as _g

TYPE_CHECKING = False
if TYPE_CHECKING:
    from collections.abc import Callable, Iterable, Iterator
    from typing import Any

    from _typeshed import OptExcInfo, SupportsRead, WriteableBuffer
    from _typeshed.wsgi import WSGIApplication

logger = logging.getLogger("pythago.wsgi")

__all__ = ["ClientDisconnected", "FileWrapper"]

# serve()'s results, which Go acts on.
DONE = 0  # the response is complete
FAILED = 1  # an error: Go answers 500 if nothing was sent, else drops the connection
ABORT = 2  # the client is gone: Go drops the connection


class ClientDisconnected(OSError):
    """The client went away, or its request body could not be read: raised by write(),
    by the body iterator's sends and by wsgi.input."""


class _Input(io.RawIOBase):
    """The streamed request body, read by Go into the caller's buffer. wsgi.input is
    an io.BufferedReader over it (read, readline, readlines and iteration in C)."""

    def __init__(self, h: int) -> None:
        self._h = h

    def readable(self) -> bool:
        return True

    def readinto(self, b: WriteableBuffer) -> int:
        n = _g.readinto(self._h, b)
        if n < 0:
            raise ClientDisconnected("the request body could not be read")
        return n


class FileWrapper:
    """wsgi.file_wrapper. Returned from the application, a wrapped regular file is sent
    by Go (sendfile where the platform has it) from its current position; anything
    else is read in blocks of blksize."""

    close: Callable[[], object]  # set when filelike has close

    def __init__(self, filelike: SupportsRead[bytes], blksize: int = 8192) -> None:
        self.filelike = filelike
        self.blksize = blksize
        if hasattr(filelike, "close"):
            self.close = filelike.close

    def __iter__(self) -> Iterator[bytes]:
        read, size = self.filelike.read, self.blksize
        while True:
            data = read(size)
            if not data:
                return
            yield data


class _Response:
    __slots__ = ("h", "status", "packed", "count", "sent")

    packed: bytes
    count: int

    def __init__(self, h: int) -> None:
        self.h = h
        self.status: bytes | None = None
        self.sent = False

    def start_response(self, status: str, headers: list[tuple[str, str]],
                       exc_info: OptExcInfo | None = None) -> Callable[[bytes], None]:
        if exc_info:
            try:
                if self.sent:
                    # PEP 3333: exc_info is sys.exc_info() from an except clause.
                    raise exc_info[1].with_traceback(exc_info[2])  # type: ignore[union-attr]
            finally:
                exc_info = None
        elif self.status is not None:
            raise AssertionError("start_response() was already called")
        if type(status) is not str:
            raise TypeError("the status must be a str, not %s" % type(status).__name__)
        if type(headers) is not list:
            raise TypeError("the headers must be a list, not %s" % type(headers).__name__)
        for item in headers:
            if type(item) is not tuple or len(item) != 2 or type(item[0]) is not str or type(item[1]) is not str:
                raise TypeError("each header must be a (str, str) tuple, not %r" % (item,))
        # Packed with their count: a NUL inside a name or value would shift the pairs,
        # which Go detects by the count. Non-latin-1 text raises here, as PEP 3333 asks.
        self.packed = "\0".join(chain.from_iterable(headers)).encode("latin-1")
        self.count = len(headers)
        self.status = status.encode("latin-1")
        return self.write

    def send(self, data: bytes, length: int) -> None:
        """Sends a chunk (the head with the first): length is the whole body's length
        when it is known, else -1 (each chunk is flushed, as PEP 3333 requires)."""
        if type(data) is not bytes:
            raise TypeError("the response body must be bytes, not %s" % type(data).__name__)
        if self.sent:
            rc = _g.body(self.h, data)
        elif self.status is None:
            raise AssertionError("the application did not call start_response()")
        else:
            rc = _g.respond(self.h, self.status, self.packed, self.count, data, length)
            self.sent = True
        if rc:
            raise ClientDisconnected("the client went away")

    def write(self, data: bytes) -> None:
        """The write() callable start_response returns."""
        self.send(data, -1)


def _sendfile(r: _Response, result: FileWrapper) -> bool:
    """Sends a FileWrapper's regular file from Go; False if it cannot (not a real
    file, or the head is out already), and the wrapper is iterated instead."""
    if r.sent or r.status is None:
        return False
    f: Any = result.filelike  # fileno and tell, if it is a file
    try:
        fd = f.fileno()
        pos = f.tell()
        if sys.platform == "win32":  # Go takes the file's handle, not the C runtime's fd
            import msvcrt
            fd = msvcrt.get_osfhandle(fd)
    except (AttributeError, OSError, ValueError):
        return False
    rc = _g.sendfile(r.h, r.status, r.packed, r.count, fd, pos)
    if rc == 2:  # not a regular file
        return False
    r.sent = True
    if rc:
        raise ClientDisconnected("the client went away")
    return True


def serve(app: WSGIApplication, base: dict[str, Any], h: int, packed: bytes, body: bytes | None) -> int:
    """Runs one request; returns DONE, FAILED or ABORT. body is the request body if
    Go read it already, or None to stream it.

    Each request runs in a fresh contextvars.Context: the thread state, and with it the
    current context, is reused by the next request on the same OS thread, and a
    ContextVar set and never reset (the current user, a tenant) must not carry over."""
    return Context().run(_serve, app, base, h, packed, body)


def _serve(app: WSGIApplication, base: dict[str, Any], h: int, packed: bytes, body: bytes | None) -> int:
    environ = base.copy()
    it = iter(packed.decode("latin-1").split("\0"))
    environ.update(zip(it, it))
    environ["wsgi.input"] = io.BytesIO(body) if body is not None else io.BufferedReader(_Input(h), 65536)
    r = _Response(h)
    result: Iterable[bytes] | None = None
    try:
        result = app(environ, r.start_response)
        if type(result) is list or type(result) is tuple:
            # The whole body is known: Go sends a Content-Length (unless the
            # application did) and need not flush each chunk.
            length = 0
            for data in result:
                if type(data) is not bytes:
                    raise TypeError("the response body must be bytes, not %s" % type(data).__name__)
                length += len(data)
            for data in result:
                if data or not r.sent:
                    r.send(data, length)
            if not r.sent:
                r.send(b"", 0)
        elif type(result) is FileWrapper and _sendfile(r, result):
            pass
        else:
            for data in result:
                if data:
                    r.send(data, -1)
                elif type(data) is not bytes:
                    raise TypeError("the response body must be bytes, not %s" % type(data).__name__)
            if not r.sent:
                r.send(b"", 0)
        outcome = DONE
    except ClientDisconnected:
        outcome = ABORT
    except BaseException:
        logger.exception("Exception in WSGI application")
        outcome = FAILED
    close = getattr(result, "close", None)
    if close is not None:
        try:
            close()
        except BaseException:
            logger.exception("Exception in the WSGI response's close()")
            if not r.sent:  # after a complete response, only logged
                outcome = FAILED
    return outcome
