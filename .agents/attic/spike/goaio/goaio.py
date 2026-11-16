"""asyncio event loops driven by Go.

GoEventLoop (proactor) keeps asyncio's own scheduling (BaseEventLoop: ready queue, timers,
Tasks, Futures) while goroutines do the I/O with Go's net package (dialing and DNS
included, so no executor threads) and queue completions.

GoSelector (readiness) is a selectors.BaseSelector whose registrations are goroutines
parked on Go's netpoller; use it with asyncio.SelectorEventLoop, which then does its own
socket calls as usual (so TLS, UDP, pipes and sock_* work unchanged).

Both sleep on a C event rather than inside Go: _goaio.ev_wait parks a virtual thread
(or futex-waits on an OS thread) until goroutines bump the event.
"""

import asyncio
import selectors
import socket
from asyncio import base_events, events, transports

import _goaio

(EV_CONNECTED, EV_DATA, EV_EOF, EV_ERROR, EV_ACCEPTED, EV_RESOLVED, EV_DRAINED,
 EV_CLOSED, EV_WAKE) = range(9)


def _wait_and_poll(ev, poll, handle, timeout):
    """Drains Go's queue; if empty (and allowed), sleeps on the event and drains again."""
    seq = _goaio.ev_seq(ev)
    ready = poll(handle)
    if not ready and timeout != 0:
        _goaio.ev_wait(ev, seq, timeout)
        ready = poll(handle)
    return ready


class _GoProactor:
    """The loop's _selector: select() returns Go completions."""

    def __init__(self, loop_id, ev):
        self._id, self._ev = loop_id, ev

    def select(self, timeout=None):
        return _wait_and_poll(self._ev, _goaio.poll, self._id, timeout)

    def close(self):
        pass


class GoSelector(selectors._BaseSelectorImpl):
    """Level-triggered selector on Go's netpoller."""

    def __init__(self):
        super().__init__()
        self._id, self._ev = _goaio.sel_new()

    def register(self, fileobj, events, data=None):
        key = super().register(fileobj, events, data)
        _goaio.sel_register(self._id, key.fd, events)
        return key

    def unregister(self, fileobj):
        key = super().unregister(fileobj)
        _goaio.sel_unregister(self._id, key.fd)
        return key

    def modify(self, fileobj, events, data=None):
        key = self._fd_to_key.get(self._fileobj_lookup(fileobj))
        if key is None:
            raise KeyError(f"{fileobj!r} is not registered")
        if events != key.events:
            _goaio.sel_modify(self._id, key.fd, events)
        key = key._replace(events=events, data=data)
        self._fd_to_key[key.fd] = key
        return key

    def select(self, timeout=None):
        if timeout is not None and timeout < 0:
            timeout = 0
        ready = []
        for fd, mask in _wait_and_poll(self._ev, _goaio.sel_poll, self._id, timeout):
            key = self._fd_to_key.get(fd)
            if key is not None and mask & key.events:
                ready.append((key, mask & key.events))
        return ready

    def close(self):
        _goaio.sel_close(self._id)
        super().close()


def GoSelectorEventLoop():
    """asyncio.SelectorEventLoop on Go's netpoller (a loop_factory)."""
    return asyncio.SelectorEventLoop(GoSelector())


class GoEventLoop(base_events.BaseEventLoop):
    def __init__(self):
        super().__init__()
        self._id, ev = _goaio.new_loop()
        self._selector = _GoProactor(self._id, ev)
        self._handlers = {}
        self._next_token = 0

    def _token(self, handler):
        self._next_token += 1
        self._handlers[self._next_token] = handler
        return self._next_token

    def _write_to_self(self):
        _goaio.wakeup(self._id)

    def _process_events(self, event_list):
        for token, kind, a, b in event_list:
            handler = self._handlers.get(token)
            if handler is not None:
                handler(kind, a, b)

    def close(self):
        if self.is_closed():
            return
        super().close()
        _goaio.close_loop(self._id)

    async def getaddrinfo(self, host, port, *, family=0, type=0, proto=0, flags=0):
        fut = self.create_future()

        def on_resolved(kind, a, b):
            del self._handlers[token]
            if kind == EV_RESOLVED:
                fut.set_result(a)
            else:
                fut.set_exception(socket.gaierror(a, b))

        token = self._token(on_resolved)
        _goaio.resolve(self._id, token, host)
        port = int(port or 0)
        out = []
        for ip in await fut:
            fam = socket.AF_INET6 if ":" in ip else socket.AF_INET
            if family in (0, fam):
                addr = (ip, port, 0, 0) if fam == socket.AF_INET6 else (ip, port)
                out.append((fam, type or socket.SOCK_STREAM, proto or socket.IPPROTO_TCP, "", addr))
        return out

    async def create_connection(self, protocol_factory, host=None, port=None, *, ssl=None, **kw):
        if ssl:
            raise NotImplementedError("TLS over GoEventLoop is not implemented yet")
        fut = self.create_future()

        def on_connected(kind, a, b):
            del self._handlers[token]
            if kind == EV_CONNECTED:
                fut.set_result(a)
            else:
                fut.set_exception(OSError(a, b))

        token = self._token(on_connected)
        _goaio.dial(self._id, token, host, int(port))
        conn = await fut
        protocol = protocol_factory()
        transport = _GoTransport(self, conn, protocol)
        return transport, protocol

    async def create_server(self, protocol_factory, host=None, port=None, *, backlog=100,
                            ssl=None, start_serving=True, **kw):
        if ssl:
            raise NotImplementedError("TLS over GoEventLoop is not implemented yet")
        if isinstance(host, (list, tuple)):
            host = host[0]
        server = _GoServer(self, protocol_factory, host or "0.0.0.0", int(port or 0), backlog)
        if start_serving:
            server._serving = True
        return server


class _GoTransport(transports.Transport):
    def __init__(self, loop, conn, protocol, server=None):
        super().__init__({"peername": _goaio.peername(conn), "sockname": _goaio.sockname(conn)})
        self._loop, self._conn, self._protocol, self._server = loop, conn, protocol, server
        self._closing = False
        self._lost = False
        self._error = None
        self._paused_writing = False
        self._reading = True
        self._buffered = 0
        self._high, self._low = 64 * 1024, 16 * 1024
        self._token = loop._token(self._on_event)
        _goaio.set_low_water(conn, self._token, self._low)
        if server is not None:
            server._attach(self)
        loop.call_soon(protocol.connection_made, self)
        loop.call_soon(_goaio.start_reading, conn, self._token)

    def _on_event(self, kind, a, b):
        if kind == EV_DATA:
            self._protocol.data_received(b)
        elif kind == EV_EOF:
            if not self._protocol.eof_received():
                self.close()
        elif kind == EV_ERROR:
            self._error = OSError(a, b)
            self.abort()
        elif kind == EV_DRAINED:
            self._buffered = a
            if self._paused_writing and a <= self._low:
                self._paused_writing = False
                self._protocol.resume_writing()
        elif kind == EV_CLOSED:
            self._connection_lost()

    def _connection_lost(self):
        if self._lost:
            return
        self._lost = True
        del self._loop._handlers[self._token]
        try:
            self._protocol.connection_lost(self._error)
        finally:
            if self._server is not None:
                self._server._detach(self)

    def write(self, data):
        if self._closing or self._lost or not data:
            return
        self._buffered = _goaio.write(self._conn, bytes(data))
        if not self._paused_writing and self._buffered > self._high:
            self._paused_writing = True
            self._protocol.pause_writing()

    def writelines(self, list_of_data):
        self.write(b"".join(list_of_data))

    def can_write_eof(self):
        return True

    def write_eof(self):
        _goaio.write_eof(self._conn)

    def get_write_buffer_size(self):
        return self._buffered

    def get_write_buffer_limits(self):
        return (self._low, self._high)

    def set_write_buffer_limits(self, high=None, low=None):
        self._high = 64 * 1024 if high is None else high
        self._low = self._high // 4 if low is None else low
        _goaio.set_low_water(self._conn, self._token, self._low)

    def is_reading(self):
        return self._reading

    def pause_reading(self):
        self._reading = False
        _goaio.pause_reading(self._conn)

    def resume_reading(self):
        self._reading = True
        _goaio.resume_reading(self._conn)

    def is_closing(self):
        return self._closing

    def close(self):
        if not self._closing:
            self._closing = True
            _goaio.close(self._conn)  # flushes queued writes; EV_CLOSED follows

    def abort(self):
        self._closing = True
        _goaio.abort(self._conn)

    def get_protocol(self):
        return self._protocol

    def set_protocol(self, protocol):
        self._protocol = protocol


class _ListenSocket:
    """Stands in for Server.sockets entries (getsockname only)."""

    def __init__(self, host, port):
        self._addr = (host, port)

    def getsockname(self):
        return self._addr


class _GoServer(events.AbstractServer):
    def __init__(self, loop, protocol_factory, host, port, backlog):
        self._loop = loop
        self._factory = protocol_factory
        self._transports = set()
        self._serving = False
        self._closed = False
        self._serve_forever_fut = None
        self._closed_fut = loop.create_future()
        self._token = loop._token(self._on_event)
        self._handle, bound = _goaio.listen(loop._id, self._token, host, port, backlog)
        self.sockets = (_ListenSocket(host, bound),)

    def _on_event(self, kind, a, b):
        if kind == EV_ACCEPTED:
            if self._closed:
                _goaio.abort(a)
                return
            _GoTransport(self._loop, a, self._factory(), server=self)

    def _attach(self, transport):
        self._transports.add(transport)

    def _detach(self, transport):
        self._transports.discard(transport)
        if self._closed and not self._transports and not self._closed_fut.done():
            self._closed_fut.set_result(None)

    def get_loop(self):
        return self._loop

    def is_serving(self):
        return self._serving and not self._closed

    async def start_serving(self):
        self._serving = True

    async def serve_forever(self):
        self._serving = True
        self._serve_forever_fut = self._loop.create_future()
        try:
            await self._serve_forever_fut
        except asyncio.CancelledError:
            self.close()
            await self.wait_closed()
            raise

    def close(self):
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

    def close_clients(self):
        for t in list(self._transports):
            t.close()

    def abort_clients(self):
        for t in list(self._transports):
            t.abort()

    async def wait_closed(self):
        await self._closed_fut
