"""The Go half of pythago_asgi (package asgi, asgi.go module()): a worker's response
actions, handed to the request's goroutine. server is a Server's id, h a request's
handle; headers are packed as b"name\\0value\\0..." with count the number of pairs.
Calls for a request that is over do nothing (the functions answering whether send()
should wait then return False)."""

from collections.abc import Buffer, Callable

def attach(server: int, index: int, loop_id: int, token: int, serve_inline: Callable[..., bool], /) -> None:
    """Worker index serves on the GoEventLoop loop_id; Go posts its events with token.
    serve_inline runs inline requests (not Options.LoopThread). Raises RuntimeError for
    an unknown server or loop."""

def failed(server: int, index: int, message: str, /) -> None:
    """The worker could not start."""

def detach(server: int, index: int, /) -> None:
    """The worker stopped."""

def start(server: int, h: int, status: int, headers: Buffer, count: int, trailers: bool, /) -> None:
    """The response head alone (http.response.start)."""

def body(server: int, h: int, data: Buffer, more: bool, /) -> bool:
    """A body chunk; returns whether send() should wait for the client (a drain event)."""

def respond(
    server: int, h: int, status: int, headers: Buffer, count: int, trailers: bool, data: Buffer, more: bool, /
) -> bool:
    """start and the first body in one call; returns as body does."""

def trailers(server: int, h: int, headers: Buffer, count: int, more_trailers: bool, /) -> None: ...
def ws_accept(server: int, h: int, subprotocol: str, headers: Buffer, count: int, /) -> None: ...
def ws_send(server: int, h: int, is_text: bool, data: Buffer, /) -> bool:
    """A message (text must be UTF-8); returns whether send() should wait for the client."""

def ws_close(server: int, h: int, code: int, reason: str, /) -> None:
    """Closes the WebSocket; before accept, the handshake is answered with a 403."""

def ws_ack(server: int, h: int, /) -> None:
    """The application took a message: the reader may read another."""

def pathsend(server: int, h: int, status: int, headers: Buffer, count: int, trailers: bool, path: str, /) -> None:
    """The held head, and the file at path as the whole body."""

def want_body(server: int, h: int, /) -> None:
    """Read the next request body chunk."""

def abort(server: int, h: int, /) -> None:
    """Drop the connection: the response cannot be completed."""
