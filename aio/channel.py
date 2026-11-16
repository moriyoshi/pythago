from __future__ import annotations

TYPE_CHECKING = False
if TYPE_CHECKING:
    from collections.abc import Callable, Generator
    from typing import Any, Self

    # Set in the namespace this source runs in, one per channel (aio/channel.go).
    _handle: int
    _release: Callable[[int], None]


class Channel:
    """A Go channel. Successful buffer sends transfer ownership to the receiver."""

    __module__ = "goaio"

    def __init__(self, handle: int, release: Callable[[int], None]) -> None:
        self._handle = handle
        self._release = release
        self._api: Any = None  # goaio, imported on first use

    def __del__(self) -> None:
        self._release(self._handle)

    def __await__(self) -> Generator[Any, None, Any]:
        return self.recv().__await__()

    def _start(self, send: bool, value: Any = None) -> tuple[Any, int, int, Any]:
        # Configuration can wrap channels before the goaio import hook is installed.
        if self._api is None:
            import goaio
            self._api = goaio
        return self._api._channel_start(self._handle, send, value)

    async def send(self, value: Any) -> None:
        """Send a value; buffer-channel sends transfer ownership."""
        loop, token, status, value = self._start(True, value)
        if status == 2:
            try:
                status, value = await value
            except BaseException:
                self._api._channel_cancel(loop, token)
                raise
        if status == 3:
            raise RuntimeError(value)

    async def recv_ok(self) -> tuple[Any, bool]:
        """Receive (value, ok), including the Go zero value after close."""
        loop, token, status, value = self._start(False)
        if status == 2:
            try:
                status, value = await value
            except BaseException:
                self._api._channel_cancel(loop, token)
                raise
        if status == 3:
            raise RuntimeError(value)
        return value, status != 1

    async def recv(self) -> Any:
        """Receive a value; raise goaio.ChannelClosed after close and drain."""
        loop, token, status, value = self._start(False)
        if status == 2:
            try:
                status, value = await value
            except BaseException:
                self._api._channel_cancel(loop, token)
                raise
        if status == 3:
            raise RuntimeError(value)
        if status == 1:
            raise self._api.ChannelClosed()
        return value

    def close(self) -> None:
        import _goaio
        _goaio.channel_close(self._handle)

    def __aiter__(self) -> Self:
        return self

    async def __anext__(self) -> Any:
        try:
            return await self.recv()
        except self._api.ChannelClosed:
            raise StopAsyncIteration from None


_channel = Channel(_handle, _release)
