"""goaio: asyncio event loops driven by Go (package aio).

GoEventLoop is a proactor: goroutines do the I/O with Go's net package. GoSelectorEventLoop
is asyncio's selector loop over Go's netpoller. Channel is the awaitable a Go channel
becomes in Python; it exists only for type checking (`goaio.Channel[int]`), each channel
having a class of its own at runtime.
"""

import asyncio
import selectors
import ssl
from _typeshed import FileDescriptorLike
from asyncio import base_events, events, protocols, transports
from collections.abc import Callable, Coroutine, Generator
from typing import Any, Final, Generic, Self, TypeVar, TypeVarTuple, Unpack, type_check_only

_T = TypeVar("_T")
_Ts = TypeVarTuple("_Ts")
_ProtocolT = TypeVar("_ProtocolT", bound=protocols.BaseProtocol)

# Completion kinds, as _goaio.poll reports them.
EV_CONNECTED: Final = 0
EV_DATA: Final = 1
EV_EOF: Final = 2
EV_ERROR: Final = 3
EV_ACCEPTED: Final = 4
EV_RESOLVED: Final = 5
EV_DRAINED: Final = 6
EV_CLOSED: Final = 7
EV_FILLED: Final = 8
EV_GO: Final = 9
EV_EXITED: Final = 10
EV_READY: Final = 11

class ChannelClosed(Exception):
    """A receive on a closed and drained Go channel."""

@type_check_only
class Channel(Generic[_T]):
    """A Go channel (chan T, chan<- T or <-chan T) passed to Python. Its operations need
    a running GoEventLoop."""

    def __await__(self) -> Generator[Any, None, _T]: ...
    async def send(self, value: _T) -> None: ...
    async def recv_ok(self) -> tuple[_T, bool]: ...
    async def recv(self) -> _T: ...
    def close(self) -> None: ...
    def __aiter__(self) -> Self: ...
    async def __anext__(self) -> _T: ...

class GoSelector(selectors._BaseSelectorImpl):
    """Level-triggered selector on Go's netpoller."""

    def __init__(self, loop_id: int | None = None) -> None: ...
    def register(self, fileobj: FileDescriptorLike, events: int, data: Any = None) -> selectors.SelectorKey: ...
    def unregister(self, fileobj: FileDescriptorLike) -> selectors.SelectorKey: ...
    def modify(self, fileobj: FileDescriptorLike, events: int, data: Any = None) -> selectors.SelectorKey: ...
    def select(self, timeout: float | None = None) -> list[tuple[selectors.SelectorKey, int]]: ...
    def close(self) -> None: ...

def GoSelectorEventLoop() -> asyncio.SelectorEventLoop:
    """asyncio.SelectorEventLoop on Go's netpoller (a loop_factory)."""

class _ListenSocket:
    def getsockname(self) -> tuple[str, int]: ...

class _GoServer(events.AbstractServer):
    sockets: tuple[_ListenSocket, ...]
    def get_loop(self) -> GoEventLoop: ...
    def is_serving(self) -> bool: ...
    async def start_serving(self) -> None: ...
    async def serve_forever(self) -> None: ...
    def close(self) -> None: ...
    def close_clients(self) -> None: ...
    def abort_clients(self) -> None: ...
    async def wait_closed(self) -> None: ...

class GoEventLoop(base_events.BaseEventLoop):
    """A proactor loop whose I/O is goroutines'. It has no sock_* methods
    (BaseEventLoop's raise NotImplementedError)."""

    # Used by pythago_asgi.
    _id: int
    def _token(self, handler: Callable[[int, Any, Any], None]) -> int: ...
    def __init__(self) -> None: ...
    def add_reader(
        self, fd: FileDescriptorLike, callback: Callable[[Unpack[_Ts]], object], *args: Unpack[_Ts]
    ) -> None: ...
    def remove_reader(self, fd: FileDescriptorLike) -> bool: ...
    def add_writer(
        self, fd: FileDescriptorLike, callback: Callable[[Unpack[_Ts]], object], *args: Unpack[_Ts]
    ) -> None: ...
    def remove_writer(self, fd: FileDescriptorLike) -> bool: ...
    def close(self) -> None: ...
    async def create_connection(
        self,
        protocol_factory: Callable[[], _ProtocolT],
        host: str | None = None,
        port: int | str | None = None,
        *,
        ssl: bool | ssl.SSLContext | None = None,
        server_hostname: str | None = None,
        ssl_handshake_timeout: float | None = None,
        ssl_shutdown_timeout: float | None = None,
        **kw: Any,
    ) -> tuple[transports.Transport, _ProtocolT]: ...
    async def create_server(  # type: ignore[override]  # an AbstractServer, not a Server
        self,
        protocol_factory: Callable[[], protocols.BaseProtocol],
        host: str | list[str] | tuple[str, ...] | None = None,
        port: int | str | None = None,
        *,
        backlog: int = 100,
        ssl: ssl.SSLContext | None = None,
        start_serving: bool = True,
        ssl_handshake_timeout: float | None = None,
        ssl_shutdown_timeout: float | None = None,
        **kw: Any,
    ) -> _GoServer: ...

def run(main: Coroutine[Any, Any, _T], *, debug: bool | None = None, selector: bool = False) -> _T:
    """asyncio.run(main) on GoEventLoop, or on GoSelectorEventLoop with selector=True."""
