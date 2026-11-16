"""The Go functions of package wsgi (wsgi/wsgi.go), which pythago_wsgi calls. h is the
request's handle; a request that is over (the client went away, or the handler returned)
answers as a disconnect."""

from _typeshed import WriteableBuffer

def respond(h: int, status: bytes, headers: bytes, count: int, data: bytes, length: int, /) -> bool:
    """Sends the head (the latin-1 status, the NUL-joined header names and values and
    their count) with the first body chunk. length is the body's length, or -1 if
    unknown. Returns whether the client is gone."""

def body(h: int, data: bytes, /) -> bool:
    """Sends a body chunk after the head. Returns whether the client is gone."""

def sendfile(h: int, status: bytes, headers: bytes, count: int, fd: int, offset: int, /) -> int:
    """Sends the head and the regular file fd (a handle on Windows) from offset: 0 sent,
    1 the client is gone, 2 not a regular file (nothing was sent)."""

def readinto(h: int, buffer: WriteableBuffer, /) -> int:
    """Reads request body into buffer: the bytes read (0 at the end), or -1 if the body
    cannot be read."""
