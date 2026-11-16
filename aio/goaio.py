"""asyncio event loops driven by Go.

GoEventLoop (proactor) keeps asyncio's own scheduling (BaseEventLoop: ready queue, timers,
Tasks, Futures) while goroutines do the I/O with Go's net package (dialing and DNS
included, so no executor threads) and queue completions. Its add_reader and add_writer
(for code that waits on an fd's readiness, such as zmq.asyncio) watch the fd as GoSelector
does, and the readiness comes back in the same queue.

GoSelector (readiness) is a selectors.BaseSelector whose registrations are goroutines
parked on Go's netpoller; use it with asyncio.SelectorEventLoop, which then does its own
socket calls as usual (so TLS, UDP, pipes and sock_* work unchanged).

Both wait for Go inside _goaio.poll / _goaio.sel_poll, with the thread state released.

run(coro, selector=False) is asyncio.run with one of them.
"""

from __future__ import annotations

import asyncio
import os
import selectors
import socket
import stat
import subprocess
import sys
from asyncio import (base_events, base_subprocess, constants, events, futures, protocols,
                     sslproto, tasks, transports)

import _goaio
import goaio_tls

if sys.platform == "win32":
    from asyncio import windows_utils

TYPE_CHECKING = False
if TYPE_CHECKING:
    import ssl as _ssl
    from collections.abc import Callable, Coroutine, Iterable
    from typing import Any, TypeAlias, TypeVar, TypeVarTuple, Unpack

    from _typeshed import FileDescriptorLike

    from _goaio import _Event, _Failure, _TLSSpec

    _T = TypeVar("_T")
    _Ts = TypeVarTuple("_Ts")
    _ProtocolT = TypeVar("_ProtocolT", bound=protocols.BaseProtocol)
    # A completion's handler: (kind, a, b), as _goaio.poll returns them.
    _Handler: TypeAlias = Callable[[int, Any, Any], None]
    # A registered fd's data: its reader's and its writer's handles.
    _FDData: TypeAlias = tuple[events.Handle | None, events.Handle | None]
    _Address: TypeAlias = tuple[str, int] | tuple[str, int, int, int]
    # A TLS server's (context, Go spec or None, handshake timeout, shutdown timeout).
    _ServerTLS: TypeAlias = tuple[_ssl.SSLContext, _TLSSpec | None, float | None, float | None]

(EV_CONNECTED, EV_DATA, EV_EOF, EV_ERROR, EV_ACCEPTED, EV_RESOLVED, EV_DRAINED,
 EV_CLOSED, EV_FILLED, EV_GO, EV_EXITED, EV_READY) = range(12)


# Go reports name resolution failures as Linux's EAI_* codes (negative); this platform's
# may differ (darwin's are positive).
_EAI = {-2: socket.EAI_NONAME, -3: socket.EAI_AGAIN, -4: socket.EAI_FAIL}


def _error(errno: int, msg: str) -> OSError:
    """The exception for a Go errno, or for a negative EAI_* name resolution code."""
    if errno < 0:
        return socket.gaierror(_EAI.get(errno, errno), msg)
    return OSError(errno, msg)


def _tls_error(code: int, msg: str) -> _ssl.SSLError:
    """The exception for a failed Go handshake: OpenSSL's verify code (tls.go tlsErrorBase),
    or another TLS failure, raised as Python's ssl would."""
    import ssl
    if code == -999:
        return ssl.SSLError(1, "[SSL] %s" % msg)
    verify_code = -1000 - code
    exc = ssl.SSLCertVerificationError(
        1, "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: %s" % msg)
    exc.verify_code, exc.verify_message = verify_code, msg
    exc.library, exc.reason = "SSL", "CERTIFICATE_VERIFY_FAILED"
    return exc


def _check(r: _T | _Failure) -> _T:
    """Raises Go's (None, errno, message) failure results as OSError."""
    if isinstance(r, tuple) and r and r[0] is None:
        raise _error(r[1], r[2])
    return r  # type: ignore[return-value]  # not a failure



class ChannelClosed(Exception):
    """A receive on a closed and drained Go channel."""


_CHANNEL_READY, _CHANNEL_CLOSED, _CHANNEL_PENDING, _CHANNEL_ERROR = range(4)


def _channel_start(handle: int, send: bool, value: Any) -> tuple[GoEventLoop, int, int, Any]:
    loop = asyncio.get_running_loop()
    if not isinstance(loop, GoEventLoop):
        raise RuntimeError("Go channels require goaio.GoEventLoop")
    # Reserve a token without a handler or Future. A ready operation needs neither.
    # No loop callbacks can run until this Python thread next yields to the loop.
    token = loop._next_token + 1
    if send:
        status, result = _goaio.channel_send(handle, loop._id, token, value)
    else:
        status, result = _goaio.channel_recv(handle, loop._id, token)
    if status != _CHANNEL_PENDING:
        return loop, token, status, result
    operation = result
    loop._next_token = token
    try:
        fut = loop.create_future()

        def complete(kind: int, result: Any, unused: Any) -> None:
            loop._channel_ops.pop(token, None)
            loop._handlers.pop(token, None)
            if not fut.done():
                fut.set_result(result)

        loop._handlers[token] = complete
        loop._channel_ops[token] = operation
    except BaseException:
        loop._handlers.pop(token, None)
        loop._channel_ops.pop(token, None)
        _goaio.channel_cancel(operation)
        raise
    return loop, token, _CHANNEL_PENDING, fut


def _channel_cancel(loop: GoEventLoop, token: int) -> None:
    # Completed workers retire themselves. Only cancellation/shutdown crosses Go.
    operation = loop._channel_ops.pop(token, None)
    loop._handlers.pop(token, None)
    if operation is not None:
        _goaio.channel_cancel(operation)


class _GoProactor:
    """The loop's _selector: select() returns Go completions."""

    def __init__(self, loop_id: int) -> None:
        self._id = loop_id

    def select(self, timeout: float | None = None) -> list[_Event]:
        return _goaio.poll(self._id, timeout)

    def close(self) -> None:
        pass


class GoSelector(selectors._BaseSelectorImpl):
    """Level-triggered selector on Go's netpoller."""

    if TYPE_CHECKING:  # _BaseSelectorImpl's, which typeshed leaves out
        _fd_to_key: dict[int, selectors.SelectorKey]

        def _fileobj_lookup(self, fileobj: FileDescriptorLike) -> int: ...

    def __init__(self, loop_id: int | None = None) -> None:
        # With loop_id, readiness is reported to that GoEventLoop's poll as EV_READY
        # (its readers and writers), not to select.
        super().__init__()
        self._id = _goaio.sel_new() if loop_id is None else _goaio.sel_new(loop_id)

    def register(self, fileobj: FileDescriptorLike, events: int,
                 data: Any = None) -> selectors.SelectorKey:
        key = super().register(fileobj, events, data)
        try:
            _check(_goaio.sel_register(self._id, key.fd, events))
        except OSError:
            super().unregister(fileobj)
            raise
        return key

    def unregister(self, fileobj: FileDescriptorLike) -> selectors.SelectorKey:
        key = super().unregister(fileobj)
        _goaio.sel_unregister(self._id, key.fd)
        return key

    def modify(self, fileobj: FileDescriptorLike, events: int,
               data: Any = None) -> selectors.SelectorKey:
        key = self._fd_to_key.get(self._fileobj_lookup(fileobj))
        if key is None:
            raise KeyError(f"{fileobj!r} is not registered")
        if events != key.events:
            _goaio.sel_modify(self._id, key.fd, events)
        key = key._replace(events=events, data=data)
        self._fd_to_key[key.fd] = key
        return key

    def select(self, timeout: float | None = None) -> list[tuple[selectors.SelectorKey, int]]:
        if timeout is not None and timeout < 0:
            timeout = 0
        ready = []
        for fd, mask in _goaio.sel_poll(self._id, timeout):
            key = self._fd_to_key.get(fd)
            if key is not None and mask & key.events:
                ready.append((key, mask & key.events))
        return ready

    def close(self) -> None:
        _goaio.sel_close(self._id)
        super().close()


def GoSelectorEventLoop() -> asyncio.SelectorEventLoop:
    """asyncio.SelectorEventLoop on Go's netpoller (a loop_factory)."""
    return asyncio.SelectorEventLoop(GoSelector())


class GoEventLoop(base_events.BaseEventLoop):
    if TYPE_CHECKING:  # BaseEventLoop's, which typeshed leaves out
        def _check_closed(self) -> None: ...
        def _add_callback(self, handle: events.Handle) -> None: ...

    def __init__(self) -> None:
        super().__init__()
        self._id = _goaio.new_loop()
        self._selector = _GoProactor(self._id)
        self._handlers: dict[int, _Handler] = {0: self._on_ready}  # token 0: EV_READY
        self._next_token = 0
        self._channel_ops: dict[int, int] = {}
        self._fd_selector: GoSelector | None = None  # readers and writers, made by the first

    def _token(self, handler: _Handler) -> int:
        self._next_token += 1
        self._handlers[self._next_token] = handler
        return self._next_token

    def _write_to_self(self) -> None:
        _goaio.wakeup(self._id)

    def _process_events(self, event_list: list[_Event]) -> None:
        for token, kind, a, b in event_list:
            handler = self._handlers.get(token)
            if handler is not None:
                handler(kind, a, b)

    # Readers and writers, as asyncio's selector loop has them (selector_events), on a
    # GoSelector whose readiness comes back through poll.

    def _on_ready(self, kind: int, fd: int, mask: int) -> None:
        key = self._fd_selector._fd_to_key.get(fd) if self._fd_selector is not None else None
        if key is None:
            return
        mask &= key.events
        reader: events.Handle | None
        writer: events.Handle | None
        reader, writer = key.data
        if mask & selectors.EVENT_READ and reader is not None:
            if reader._cancelled:
                self._remove_reader(key.fileobj)
            else:
                self._add_callback(reader)
        if mask & selectors.EVENT_WRITE and writer is not None:
            if writer._cancelled:
                self._remove_writer(key.fileobj)
            else:
                self._add_callback(writer)

    def _add_fd_callback(self, fd: FileDescriptorLike, event: int,
                         callback: Callable[..., object], args: tuple[Any, ...]) -> events.Handle:
        self._check_closed()
        if self._fd_selector is None:
            self._fd_selector = GoSelector(self._id)
        handle = events.Handle(callback, args, self, None)
        key = self._fd_selector.get_map().get(fd)
        data: _FDData
        if key is None:
            data = (handle, None) if event == selectors.EVENT_READ else (None, handle)
            self._fd_selector.register(fd, event, data)
            return handle
        reader: events.Handle | None
        writer: events.Handle | None
        reader, writer = key.data
        old: events.Handle | None
        if event == selectors.EVENT_READ:
            old, data = reader, (handle, writer)
        else:
            old, data = writer, (reader, handle)
        self._fd_selector.modify(fd, key.events | event, data)
        if old is not None:
            old.cancel()
        return handle

    def _remove_fd_callback(self, fd: FileDescriptorLike, event: int) -> bool:
        if self.is_closed() or self._fd_selector is None:
            return False
        key = self._fd_selector.get_map().get(fd)
        if key is None:
            return False
        reader: events.Handle | None
        writer: events.Handle | None
        reader, writer = key.data
        old: events.Handle | None
        data: _FDData
        if event == selectors.EVENT_READ:
            old, data = reader, (None, writer)
        else:
            old, data = writer, (reader, None)
        mask = key.events & ~event
        if not mask:
            self._fd_selector.unregister(fd)
        else:
            self._fd_selector.modify(fd, mask, data)
        if old is not None:
            old.cancel()
            return True
        return False

    def _add_reader(self, fd: FileDescriptorLike, callback: Callable[..., object], *args: Any) -> events.Handle:
        return self._add_fd_callback(fd, selectors.EVENT_READ, callback, args)

    def _remove_reader(self, fd: FileDescriptorLike) -> bool:
        return self._remove_fd_callback(fd, selectors.EVENT_READ)

    def _add_writer(self, fd: FileDescriptorLike, callback: Callable[..., object], *args: Any) -> events.Handle:
        return self._add_fd_callback(fd, selectors.EVENT_WRITE, callback, args)

    def _remove_writer(self, fd: FileDescriptorLike) -> bool:
        return self._remove_fd_callback(fd, selectors.EVENT_WRITE)

    def add_reader(self, fd: FileDescriptorLike, callback: Callable[[Unpack[_Ts]], object],
                   *args: Unpack[_Ts]) -> None:
        """Add a reader callback."""
        self._add_reader(fd, callback, *args)

    def remove_reader(self, fd: FileDescriptorLike) -> bool:
        """Remove a reader callback."""
        return self._remove_reader(fd)

    def add_writer(self, fd: FileDescriptorLike, callback: Callable[[Unpack[_Ts]], object],
                   *args: Unpack[_Ts]) -> None:
        """Add a writer callback."""
        self._add_writer(fd, callback, *args)

    def remove_writer(self, fd: FileDescriptorLike) -> bool:
        """Remove a writer callback."""
        return self._remove_writer(fd)

    def close(self) -> None:
        if self.is_closed():
            return
        super().close()
        if self._fd_selector is not None:
            self._fd_selector.close()
            self._fd_selector = None
        for operation in self._channel_ops.values():
            _goaio.channel_cancel(operation)
        for token in self._channel_ops:
            self._handlers.pop(token, None)
        self._channel_ops.clear()
        _goaio.close_loop(self._id)

    async def getaddrinfo(  # type: ignore[override]  # the family and type as given
            self, host: str, port: str | int | None, *, family: int = 0, type: int = 0,
            proto: int = 0, flags: int = 0) -> list[tuple[int, int, int, str, _Address]]:
        fut: asyncio.Future[list[str]] = self.create_future()

        def on_resolved(kind: int, a: Any, b: Any) -> None:
            del self._handlers[token]
            if kind == EV_RESOLVED:
                fut.set_result(a)
            else:
                fut.set_exception(_error(a, b))

        token = self._token(on_resolved)
        _goaio.resolve(self._id, token, host)
        port = int(port or 0)
        out: list[tuple[int, int, int, str, _Address]] = []
        for ip in await fut:
            fam = socket.AF_INET6 if ":" in ip else socket.AF_INET
            if family in (0, fam):
                addr: _Address = (ip, port, 0, 0) if fam == socket.AF_INET6 else (ip, port)
                out.append((fam, type or socket.SOCK_STREAM, proto or socket.IPPROTO_TCP, "", addr))
        return out

    async def _dial(self, start: Callable[..., None | _Failure], *args: Any) -> int:
        fut: asyncio.Future[int] = self.create_future()

        def on_connected(kind: int, a: Any, b: Any) -> None:
            del self._handlers[token]
            if kind == EV_CONNECTED:
                fut.set_result(a)
            elif a <= -999:
                fut.set_exception(_tls_error(a, b))
            else:
                fut.set_exception(_error(a, b))

        token = self._token(on_connected)
        try:
            _check(start(self._id, token, *args))
        except BaseException:
            del self._handlers[token]
            raise
        return await fut

    async def create_connection(
            self, protocol_factory: Callable[[], _ProtocolT], host: str | None = None,
            port: int | str | None = None, *, ssl: bool | _ssl.SSLContext | None = None,
            server_hostname: str | None = None, ssl_handshake_timeout: float | None = None,
            ssl_shutdown_timeout: float | None = None,
            **kw: Any) -> tuple[transports.Transport, _ProtocolT]:
        if server_hostname is not None and not ssl:
            raise ValueError("server_hostname is only meaningful with ssl")
        if ssl_handshake_timeout is not None and not ssl:
            raise ValueError("ssl_handshake_timeout is only meaningful with ssl")
        if ssl_shutdown_timeout is not None and not ssl:
            raise ValueError("ssl_shutdown_timeout is only meaningful with ssl")
        if not ssl:
            conn = await self._dial(_goaio.dial, host, int(port))  # type: ignore[arg-type]
            protocol = protocol_factory()
            return _GoTransport(self, conn, protocol), protocol
        if server_hostname is None:
            if not host:
                raise ValueError("You must set server_hostname when using ssl without a host")
            server_hostname = host
        sslcontext = _client_context(ssl, server_hostname)
        import ssl as ssl_module
        spec = goaio_tls.go_config(ssl_module, sslcontext, False, server_hostname,
                                   ssl_handshake_timeout)
        if spec is not None:  # Go's crypto/tls does the handshake and the records
            conn = await self._dial(_goaio.dial_tls, host, int(port), spec)  # type: ignore[arg-type]
            protocol = protocol_factory()
            return _GoTLSTransport(self, conn, protocol, sslcontext, False, server_hostname), protocol
        # Python's ssl, over a Go connection, as BaseEventLoop does it.
        conn = await self._dial(_goaio.dial, host, int(port))  # type: ignore[arg-type]
        protocol = protocol_factory()
        waiter = self.create_future()
        ssl_protocol = sslproto.SSLProtocol(
            self, protocol, sslcontext, waiter, server_side=False,
            server_hostname=server_hostname,
            ssl_handshake_timeout=ssl_handshake_timeout,  # type: ignore[arg-type]  # typeshed: int
            ssl_shutdown_timeout=ssl_shutdown_timeout)
        transport = _GoTransport(self, conn, ssl_protocol)
        try:
            await waiter
        except BaseException:
            transport.close()
            raise
        return ssl_protocol._app_transport, protocol

    async def create_server(  # type: ignore[override]  # an AbstractServer, not a Server
            self, protocol_factory: Callable[[], protocols.BaseProtocol],
            host: str | list[str] | tuple[str, ...] | None = None, port: int | str | None = None,
            *, backlog: int = 100, ssl: _ssl.SSLContext | None = None, start_serving: bool = True,
            ssl_handshake_timeout: float | None = None, ssl_shutdown_timeout: float | None = None,
            **kw: Any) -> _GoServer:
        if ssl is not None and not hasattr(ssl, "wrap_bio"):
            raise TypeError("ssl argument must be an SSLContext or None")
        if ssl_handshake_timeout is not None and ssl is None:
            raise ValueError("ssl_handshake_timeout is only meaningful with ssl")
        if isinstance(host, (list, tuple)):
            host = host[0]
        tls: _ServerTLS | None = None
        if ssl is not None:
            import ssl as ssl_module
            spec = goaio_tls.go_config(ssl_module, ssl, True, None, ssl_handshake_timeout)
            tls = (ssl, spec, ssl_handshake_timeout, ssl_shutdown_timeout)
        server = _GoServer(self, protocol_factory, host or "0.0.0.0", int(port or 0), backlog, tls)
        if start_serving:
            server._serving = True
        return server

    # Pipes and subprocesses: Go owns a duplicate of the pipe's descriptor and moves its
    # data like a connection's.

    def _make_read_pipe_transport(self, pipe: Any, protocol: protocols.BaseProtocol,
                                  waiter: asyncio.Future[None] | None = None,
                                  extra: dict[str, Any] | None = None) -> _GoTransport:
        return self._pipe_transport(_GoTransport, pipe, protocol, waiter, extra)

    def _make_write_pipe_transport(self, pipe: Any, protocol: protocols.BaseProtocol,
                                   waiter: asyncio.Future[None] | None = None,
                                   extra: dict[str, Any] | None = None) -> _GoTransport:
        return self._pipe_transport(_GoWritePipeTransport, pipe, protocol, waiter, extra)

    def _pipe_transport(self, cls: type[_GoTransport], pipe: Any, protocol: protocols.BaseProtocol,
                        waiter: asyncio.Future[None] | None,
                        extra: dict[str, Any] | None) -> _GoTransport:
        if sys.platform == "win32":
            # Go takes a Win32 handle, a duplicate: a subprocess's overlapped pipe
            # (windows_utils.PipeHandle, see _GoSubprocessTransport), which Go's I/O
            # completion port serves, or a descriptor's (os.pipe), which Go reads and
            # writes synchronously on a thread of its own.
            import _winapi
            st: os.stat_result | None
            if isinstance(pipe, windows_utils.PipeHandle):
                handle, is_fifo, is_socket, st = pipe.handle, True, False, None
            else:
                import msvcrt
                st = os.fstat(pipe.fileno())
                handle = msvcrt.get_osfhandle(pipe.fileno())
                is_fifo, is_socket = stat.S_ISFIFO(st.st_mode), stat.S_ISSOCK(st.st_mode)
                if not (is_fifo or is_socket or stat.S_ISCHR(st.st_mode)):
                    raise ValueError("Pipe transport is only for pipes, sockets and character devices")
            proc = _winapi.GetCurrentProcess()
            h = _winapi.DuplicateHandle(proc, handle, proc, 0, False, _winapi.DUPLICATE_SAME_ACCESS)
            conn = _check(_goaio.pipe(self._id, h))
        else:
            fd = pipe.fileno()
            st = os.fstat(fd)
            is_fifo, is_socket = stat.S_ISFIFO(st.st_mode), stat.S_ISSOCK(st.st_mode)
            if not (is_fifo or is_socket or stat.S_ISCHR(st.st_mode)):
                raise ValueError("Pipe transport is only for pipes, sockets and character devices")
            conn = _check(_goaio.pipe(self._id, os.dup(fd)))
        # Go's duplicate is the only descriptor left: closing the transport closes the pipe
        # (a child sees end of input), as asyncio's own pipe transports do.
        pipe.close()
        extra = dict(extra or {}, pipe=pipe)
        transport: _GoTransport
        if cls is _GoWritePipeTransport:
            # As asyncio's: the end of a reader is seen on pipes and sockets, except on
            # named FIFOs on macOS, where the trick only misfires (gh-145030).
            watch = is_socket or (is_fifo and not (sys.platform == "darwin" and st is not None and st.st_nlink > 0))
            transport = cls(self, conn, protocol, extra=extra, watch=watch)
        else:
            transport = cls(self, conn, protocol, extra=extra)
        if waiter is not None:  # after connection_made, which the transport schedules
            self.call_soon(futures._set_result_unless_cancelled, waiter, None)  # type: ignore[attr-defined]
        return transport

    async def _make_subprocess_transport(
            self, protocol: protocols.SubprocessProtocol, args: Any, shell: bool, stdin: Any,
            stdout: Any, stderr: Any, bufsize: int, extra: dict[str, Any] | None = None,
            **kwargs: Any) -> _GoSubprocessTransport:
        waiter: asyncio.Future[None] = self.create_future()
        transport = _GoSubprocessTransport(self, protocol, args, shell, stdin, stdout, stderr,
                                           bufsize, waiter=waiter, extra=extra, **kwargs)

        def on_exit(kind: int, code: int, _: Any) -> None:
            del self._handlers[token]
            transport._process_exited(code)

        token = self._token(on_exit)
        _goaio.wait_pid(self._id, token, transport.get_pid())  # type: ignore[arg-type]  # started
        try:
            await waiter
        except (SystemExit, KeyboardInterrupt):
            raise
        except BaseException:
            transport.close()
            await tasks.shield(transport._wait())
            raise
        return transport


class _GoSubprocessTransport(base_subprocess.BaseSubprocessTransport):
    def _start(self, args: Any, shell: bool, stdin: Any, stdout: Any, stderr: Any,
               bufsize: int, **kwargs: Any) -> None:
        # On Windows, as asyncio's proactor loop: windows_utils.Popen gives this side of
        # each pipe as an overlapped handle (stdin's duplex, so that a read on it learns
        # when the child closes it), which Go's I/O completion port serves.
        popen = windows_utils.Popen if sys.platform == "win32" else subprocess.Popen  # type: ignore[name-defined, unused-ignore]
        self._proc = popen(args, shell=shell, stdin=stdin, stdout=stdout, stderr=stderr,
                           universal_newlines=False, bufsize=bufsize, **kwargs)


def _client_context(ssl: bool | _ssl.SSLContext, server_hostname: str | None) -> _ssl.SSLContext:
    if not isinstance(ssl, bool):
        return ssl
    import ssl as ssl_module  # ssl=True: asyncio's default client context
    ctx = ssl_module.create_default_context()
    if not server_hostname:
        ctx.check_hostname = False
    return ctx


class _GoTransport(transports.Transport):
    _start_tls_compatible = True  # loop.start_tls wraps it in asyncio's SSLProtocol

    def __init__(self, loop: GoEventLoop, conn: int, protocol: Any,
                 server: _GoServer | None = None, extra: dict[str, Any] | None = None,
                 reading: bool = True) -> None:
        if extra is None:
            extra = {"peername": _goaio.peername(conn), "sockname": _goaio.sockname(conn)}
        super().__init__(extra)
        self._loop, self._conn, self._protocol, self._server = loop, conn, protocol, server
        self._closing = False
        self._lost = False
        self._error: BaseException | None = None
        self._paused_writing = False
        self._reading = True
        self._buffered = 0
        self._high, self._low = 64 * 1024, 16 * 1024
        self._token = loop._token(self._on_event)
        # A BufferedProtocol gets zero-copy reads: Go reads straight into the buffer that
        # get_buffer returns (read_into). The mode is fixed here; set_protocol to the other
        # kind is served by copying, as asyncio's own transports do.
        self._read_into = isinstance(protocol, protocols.BufferedProtocol)
        self._read_pending = False
        self._held: int | None = None  # a read that completed while paused, delivered on resume
        self._eof_held = False
        self._scratch: bytearray | None = None
        _goaio.set_low_water(conn, self._token, self._low)
        if server is not None:
            server._attach(self)
        loop.call_soon(protocol.connection_made, self)
        if reading:
            loop.call_soon(self._start_reading)

    def _start_reading(self) -> None:
        if self._read_into:
            _goaio.start_reading_into(self._conn, self._token)
            self._read_next()
        else:
            _goaio.start_reading(self._conn, self._token)

    def _read_next(self) -> None:
        if self._read_pending or not self._reading or self._closing or self._lost:
            return
        buf: Any
        if isinstance(self._protocol, protocols.BufferedProtocol):
            try:
                buf = self._protocol.get_buffer(-1)
                if not len(buf):
                    raise RuntimeError("get_buffer() returned an empty buffer")
            except (SystemExit, KeyboardInterrupt):
                raise
            except BaseException as exc:
                self._fatal_error(exc, "get_buffer() call failed")
                return
        else:
            if self._scratch is None:
                self._scratch = bytearray(256 * 1024)
            buf = self._scratch
        n = _goaio.read_into(self._conn, buf)
        if n is None:  # the reader goroutine waits for data: EV_FILLED or EV_EOF follows
            self._read_pending = True
        elif n == 0:
            self._eof()
        else:  # data was waiting and is in buf already
            self._filled(n)
            # One inline read per loop iteration, so other callbacks run in between.
            self._loop.call_soon(self._read_next)

    def _eof(self) -> None:
        if not self._protocol.eof_received():
            self.close()

    def _filled(self, n: int) -> None:
        if isinstance(self._protocol, protocols.BufferedProtocol):
            self._protocol.buffer_updated(n)
        else:
            self._protocol.data_received(bytes(memoryview(self._scratch)[:n]))  # type: ignore[arg-type]

    def _fatal_error(self, exc: BaseException, message: str) -> None:
        self._loop.call_exception_handler({"message": message, "exception": exc,
                                           "transport": self, "protocol": self._protocol})
        self._error = exc
        self.abort()

    def _on_event(self, kind: int, a: Any, b: Any) -> None:
        if kind == EV_FILLED:
            self._read_pending = False
            if not self._reading:
                self._held = a  # as asyncio's proactor loop does
                return
            self._filled(a)
            self._read_next()
        elif kind == EV_DATA:
            if isinstance(self._protocol, protocols.BufferedProtocol):
                protocols._feed_data_to_buffered_proto(self._protocol, b)  # type: ignore[attr-defined]
            else:
                self._protocol.data_received(b)
        elif kind == EV_EOF:
            if self._held is not None:
                self._eof_held = True  # after the held data
            else:
                self._eof()
        elif kind == EV_ERROR:
            self._error = _error(a, b)
            self.abort()
        elif kind == EV_DRAINED:
            self._buffered = a
            if self._paused_writing and a <= self._low:
                self._paused_writing = False
                self._protocol.resume_writing()
        elif kind == EV_CLOSED:
            self._connection_lost()

    def _connection_lost(self) -> None:
        if self._lost:
            return
        self._lost = True
        del self._loop._handlers[self._token]
        try:
            self._protocol.connection_lost(self._error)
        finally:
            if self._server is not None:
                self._server._detach(self)

    def write(self, data: bytes | bytearray | memoryview) -> None:
        if self._closing or self._lost or not data:
            return
        self._buffered = _goaio.write(self._conn, bytes(data))
        if not self._paused_writing and self._buffered > self._high:
            self._paused_writing = True
            self._protocol.pause_writing()

    def writelines(self, list_of_data: Iterable[bytes | bytearray | memoryview]) -> None:
        self.write(b"".join(list_of_data))

    def can_write_eof(self) -> bool:
        return True

    def write_eof(self) -> None:
        if self._closing or self._lost:
            return
        _goaio.write_eof(self._conn)

    def get_write_buffer_size(self) -> int:
        return self._buffered

    def get_write_buffer_limits(self) -> tuple[int, int]:
        return (self._low, self._high)

    def set_write_buffer_limits(self, high: int | None = None, low: int | None = None) -> None:
        self._high = 64 * 1024 if high is None else high
        self._low = self._high // 4 if low is None else low
        _goaio.set_low_water(self._conn, self._token, self._low)

    def is_reading(self) -> bool:
        return self._reading

    def pause_reading(self) -> None:
        self._reading = False
        if self._closing or self._lost:
            return
        if not self._read_into:
            _goaio.pause_reading(self._conn)

    def resume_reading(self) -> None:
        self._reading = True
        if self._closing or self._lost:
            return
        if self._read_into:
            if self._held is not None:
                n, self._held = self._held, None
                self._filled(n)
                if self._eof_held:
                    self._eof_held = False
                    self._eof()
                    return
            self._read_next()
        else:
            _goaio.resume_reading(self._conn)

    def is_closing(self) -> bool:
        return self._closing

    def close(self) -> None:
        if not self._closing:
            self._closing = True
            _goaio.close(self._conn)  # flushes queued writes; EV_CLOSED follows

    def abort(self) -> None:
        self._closing = True
        _goaio.abort(self._conn)

    def _force_close(self, exc: BaseException | None) -> None:
        """asyncio's SSLProtocol closes its transport this way on a fatal error."""
        if self._error is None:
            self._error = exc
        self.abort()

    def get_protocol(self) -> Any:
        return self._protocol

    def set_protocol(self, protocol: Any) -> None:
        self._protocol = protocol


class _GoSSLObject:
    """What get_extra_info("ssl_object") gives for a connection whose TLS is Go's: the parts
    of ssl.SSLObject applications read."""

    def __init__(self, context: _ssl.SSLContext, server_side: bool, server_hostname: str | None,
                 state: tuple[str, str, int, str, list[bytes]]) -> None:
        self.context, self.server_side, self.server_hostname = context, server_side, server_hostname
        self._version, self._cipher, self._bits, self._alpn, self._chain = state
        self._decoded: dict[str, Any] | None = None

    def version(self) -> str:
        return self._version

    def cipher(self) -> tuple[str, str, int]:
        return (self._cipher, self._version, self._bits)

    def selected_alpn_protocol(self) -> str | None:
        return self._alpn or None

    def compression(self) -> None:
        return None

    def shared_ciphers(self) -> None:
        return None

    def pending(self) -> int:
        return 0

    def get_unverified_chain(self) -> list[bytes]:
        return list(self._chain)

    def getpeercert(self, binary_form: bool = False) -> bytes | dict[str, Any] | None:
        if not self._chain:
            return None
        if binary_form:
            return self._chain[0]
        import ssl
        if self.context.verify_mode == ssl.CERT_NONE:
            return {}  # as ssl: an unverified certificate is not decoded
        if self._decoded is None:
            self._decoded = _decode_cert(self._chain[0])
        return self._decoded


def _decode_cert(der: bytes) -> dict[str, Any]:
    """The certificate as ssl's getpeercert() describes it, decoded by the same OpenSSL."""
    import ssl, tempfile
    fd, path = tempfile.mkstemp(suffix=".pem")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(ssl.DER_cert_to_PEM_cert(der))
        return ssl._ssl._test_decode_cert(path)  # type: ignore[attr-defined]
    finally:
        os.unlink(path)


class _GoTLSTransport(_GoTransport):
    """A connection whose TLS is Go's: the protocol sees plaintext, with the TLS extras an
    asyncio SSL transport has."""

    _start_tls_compatible = False

    def __init__(self, loop: GoEventLoop, conn: int, protocol: Any, sslcontext: _ssl.SSLContext,
                 server_side: bool, server_hostname: str | None,
                 server: _GoServer | None = None) -> None:
        obj = _GoSSLObject(sslcontext, server_side, server_hostname, _goaio.tls_state(conn))  # type: ignore[arg-type]  # TLS
        extra = {"peername": _goaio.peername(conn), "sockname": _goaio.sockname(conn),
                 "sslcontext": sslcontext, "ssl_object": obj, "cipher": obj.cipher(),
                 "compression": None}
        super().__init__(loop, conn, protocol, server=server, extra=extra)

    def get_extra_info(self, name: str, default: Any = None) -> Any:
        if name == "peercert":
            return self._extra["ssl_object"].getpeercert()  # type: ignore[attr-defined]
        return super().get_extra_info(name, default)

    def can_write_eof(self) -> bool:
        return False

    def write_eof(self) -> None:
        raise NotImplementedError("SSL doesn't support half-closes")


class _GoWritePipeTransport(_GoTransport):
    """A write pipe: no reading; the end of the reader (a child that exits) closes it, as
    asyncio's own write pipe transport does."""

    def __init__(self, loop: GoEventLoop, conn: int, protocol: Any,
                 extra: dict[str, Any] | None = None, watch: bool = True) -> None:
        super().__init__(loop, conn, protocol, extra=extra, reading=False)
        if watch:
            _goaio.watch_hangup(conn)

    def _on_event(self, kind: int, a: Any, b: Any) -> None:
        if kind == EV_EOF:  # the reading end closed
            if self._buffered:
                self._error = BrokenPipeError()
                self.abort()
            else:
                self.close()
            return
        super()._on_event(kind, a, b)


class _ListenSocket:
    """Stands in for Server.sockets entries (getsockname only)."""

    def __init__(self, host: str, port: int) -> None:
        self._addr = (host, port)

    def getsockname(self) -> tuple[str, int]:
        return self._addr


class _GoServer(events.AbstractServer):
    def __init__(self, loop: GoEventLoop, protocol_factory: Callable[[], Any], host: str,
                 port: int, backlog: int, tls: _ServerTLS | None = None) -> None:
        self._loop = loop
        self._factory = protocol_factory
        self._tls = tls  # (sslcontext, Go spec or None, handshake timeout, shutdown timeout)
        self._transports: set[_GoTransport] = set()
        self._serving = False
        self._closed = False
        self._serve_forever_fut: asyncio.Future[None] | None = None
        self._closed_fut: asyncio.Future[None] = loop.create_future()
        self._token = loop._token(self._on_event)
        try:
            if tls is not None and tls[1] is not None:  # handshakes in Go
                self._handle, bound = _check(_goaio.listen_tls(loop._id, self._token, host, port,
                                                               backlog, tls[1]))
            else:
                self._handle, bound = _check(_goaio.listen(loop._id, self._token, host, port, backlog))
        except OSError:
            del loop._handlers[self._token]
            raise
        self.sockets = (_ListenSocket(host, bound),)

    def _on_event(self, kind: int, a: Any, b: Any) -> None:
        if kind == EV_ACCEPTED:
            if self._closed:
                _goaio.abort(a)
                return
            if self._tls is None:
                _GoTransport(self._loop, a, self._factory(), server=self)
            elif self._tls[1] is not None:
                _GoTLSTransport(self._loop, a, self._factory(), self._tls[0], True, None, server=self)
            else:  # Python's ssl over the Go connection
                ssl_protocol = sslproto.SSLProtocol(
                    self._loop, self._factory(), self._tls[0], None,  # type: ignore[arg-type]
                    server_side=True,
                    ssl_handshake_timeout=self._tls[2],  # type: ignore[arg-type]  # typeshed: int
                    ssl_shutdown_timeout=self._tls[3])
                _GoTransport(self._loop, a, ssl_protocol, server=self)

    def _attach(self, transport: _GoTransport) -> None:
        self._transports.add(transport)

    def _detach(self, transport: _GoTransport) -> None:
        self._transports.discard(transport)
        if self._closed and not self._transports and not self._closed_fut.done():
            self._closed_fut.set_result(None)

    def get_loop(self) -> GoEventLoop:
        return self._loop

    def is_serving(self) -> bool:
        return self._serving and not self._closed

    async def start_serving(self) -> None:
        self._serving = True

    async def serve_forever(self) -> None:
        self._serving = True
        self._serve_forever_fut = self._loop.create_future()
        try:
            await self._serve_forever_fut
        except asyncio.CancelledError:
            self.close()
            await self.wait_closed()
            raise

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        self._serving = False
        _goaio.close_listener(self._handle)
        del self._loop._handlers[self._token]
        if self._serve_forever_fut is not None and not self._serve_forever_fut.done():
            self._serve_forever_fut.cancel()
        if not self._transports and not self._closed_fut.done():
            self._closed_fut.set_result(None)

    def close_clients(self) -> None:
        for t in list(self._transports):
            t.close()

    def abort_clients(self) -> None:
        for t in list(self._transports):
            t.abort()

    async def wait_closed(self) -> None:
        await self._closed_fut


def run(main: Coroutine[Any, Any, _T], *, debug: bool | None = None, selector: bool = False) -> _T:
    """asyncio.run(main) on GoEventLoop, or on GoSelectorEventLoop with selector=True."""
    factory: Callable[[], asyncio.AbstractEventLoop] = GoSelectorEventLoop if selector else GoEventLoop
    return asyncio.run(main, debug=debug, loop_factory=factory)
