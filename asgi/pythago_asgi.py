"""ASGI workers for package asgi (Go): each worker is a thread running a
goaio.GoEventLoop. Go posts requests to the worker's loop; the worker runs the
application and hands response actions back to Go through _pythago_asgi."""

from __future__ import annotations

import asyncio
import asyncio.events
import collections
import importlib
import itertools
import logging
import sys
import threading
import time
import traceback

import goaio
import _pythago_asgi as _g

TYPE_CHECKING = False
if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Iterable, Sequence
    from typing import Any, Literal

    # An ASGI message or scope, and the application's callables (ASGI 3.0).
    Message = dict[str, Any]
    Scope = dict[str, Any]
    Receive = Callable[[], Awaitable[Message]]
    Send = Callable[[Message], Awaitable[None]]
    Application = Callable[[Scope, Receive, Send], Awaitable[None]]
    Headers = Iterable[Sequence[bytes]]  # (name, value) pairs
    # Package asgi's Options.Lifespan.
    LifespanMode = Literal["auto", "on", "off"]
    # The response head held until the first body: status, packed headers, their count,
    # and whether trailers follow.
    Head = tuple[int, bytes, int, bool]

logger = logging.getLogger("pythago.asgi")


class ClientDisconnected(OSError):
    """send() on a WebSocket the client closed (ASGI spec 2.4)."""
_ASGI = {"version": "3.0", "spec_version": "2.4"}  # each worker scopes a copy of its own


def _disconnect() -> Message:
    return {"type": "http.disconnect"}


_chain = itertools.chain.from_iterable


def _pack(headers: Headers) -> tuple[bytes, int]:
    """Headers as (one "name\\0value\\0..." bytes object, the number of pairs) for Go;
    joining checks they are bytes, and Go checks the count, so a NUL inside a name or
    value cannot shift the pairs. Go validates the fields themselves."""
    if not isinstance(headers, (list, tuple)):
        headers = list(headers)
    return b"\0".join(_chain(headers)), len(headers)


def _unpack(packed: bytes) -> list[tuple[bytes, bytes]]:
    fields = iter(packed.split(b"\0"))
    return list(zip(fields, fields))


def load(spec: str) -> Any:
    """Imports "module:attribute" (dots allowed in the attribute)."""
    module, _, attr = spec.partition(":")
    obj = importlib.import_module(module)
    for name in (attr or "app").split("."):
        obj = getattr(obj, name)
    return obj


_threads: dict[int, list[threading.Thread]] = {}  # server -> its worker threads, until join() saw them exit


def start(server: int, spec: str, workers: int, root_path: str, lifespan: LifespanMode) -> None:
    threads = _threads[server] = []
    for i in range(workers):
        t = threading.Thread(target=_thread, args=(server, i, spec, root_path, lifespan),
                             name="asgi-worker-%d" % i, daemon=True)
        threads.append(t)
        t.start()


def join(server: int, timeout: float | None) -> bool:
    """Waits until the server's worker threads have exited, CPython's teardown of their
    thread states included (Thread.join returns after it); timeout is in seconds, or
    None. Returns whether they all have.

    The workers are daemon threads, and they detach before that teardown. One still
    tearing down when Py_FinalizeEx runs can be cleared twice: past the point where
    PyThreadState_Clear removes it from the biased-refcount table, a contended lock
    detaches it, finalization's stop-the-world then counts it as stopped and clears its
    thread state again (CPython 3.14t crashed in _Py_brc_remove_thread)."""
    deadline = None if timeout is None else time.monotonic() + timeout
    for t in _threads.get(server, ()):
        t.join(None if deadline is None else max(0.0, deadline - time.monotonic()))
        if t.is_alive():
            return False
    _threads.pop(server, None)
    return True


def _thread(server: int, index: int, spec: str, root_path: str, lifespan: LifespanMode) -> None:
    worker = _Worker(server, index, root_path, lifespan)
    try:
        goaio.run(worker.main(spec))
    except BaseException:
        logger.exception("ASGI worker %d crashed", index)
        if not worker.attached:
            _g.failed(server, index, traceback.format_exc())
    finally:
        if worker.attached:
            _g.detach(server, index)


class _Worker:
    # Set by main.
    loop: goaio.GoEventLoop
    app: Application
    stopping: asyncio.Future[None]

    def __init__(self, server: int, index: int, root_path: str, lifespan: LifespanMode) -> None:
        # What every request's scope shares is immutable and immortal on free-threaded
        # builds (interned strings), or this worker's own (the asgi dict): a value shared
        # by all workers would have its reference count contended by every request.
        self.server, self.index, self.lifespan = server, index, lifespan
        self.root_path = sys.intern(root_path)
        self.asgi = dict(_ASGI)
        self.attached = False
        self.requests: dict[int, _Request | _WebSocket] = {}
        self.state: dict[str, Any] = {}

    async def main(self, spec: str) -> None:
        loop: goaio.GoEventLoop  # goaio.run's
        self.loop = loop = asyncio.get_running_loop()  # type: ignore[assignment]
        try:
            self.app = load(spec)
        except BaseException:
            _g.failed(self.server, self.index, traceback.format_exc())
            return
        life: _Lifespan | None = None
        if self.lifespan != "off":
            life = _Lifespan(self)
            ok = await life.startup()
            if not ok:
                _g.failed(self.server, self.index, life.message or "lifespan startup failed")
                return
        self.stopping = loop.create_future()
        token = loop._token(self.on_event)
        _g.attach(self.server, self.index, loop._id, token, self.serve_inline)
        self.attached = True
        await self.stopping
        if life is not None:
            await life.shutdown()

    def on_event(self, kind: int, msg: tuple[Any, ...], _: object) -> None:
        tag = msg[0]
        if tag == "r":
            self.on_request(msg)
        elif tag == "w":
            self.on_websocket(msg)
        elif tag == "s":
            if not self.stopping.done():
                self.stopping.set_result(None)
        else:
            req = self.requests.get(msg[1])
            if req is None:
                return
            if tag == "b":
                req.on_body(msg[2], msg[3])  # type: ignore[union-attr]  # an HTTP request's
            elif tag == "d":
                req.on_drained()
            elif tag == "x":
                req.on_disconnect()
            elif tag == "wm":
                req.on_message(msg[2], msg[3])  # type: ignore[union-attr]  # a WebSocket's
            elif tag == "wd":
                req.on_disconnect(msg[2], msg[3])  # type: ignore[call-arg]  # a WebSocket's

    def on_request(self, msg: tuple[Any, ...]) -> None:
        self.start_request(*msg[1:])

    def start_request(self, h: int, method: str, version: str, scheme: str, raw_path: bytes,
                      path: str, query: bytes, headers: bytes, client: tuple[str, int] | None,
                      server: tuple[str, int | None] | None, body: bytes, more: bool,
                      trailers: bool, eager: bool = False) -> asyncio.Task[None]:
        scope: Scope = {
            "type": "http", "asgi": self.asgi, "http_version": version, "method": method,
            "scheme": scheme, "path": path, "raw_path": raw_path, "query_string": query,
            "root_path": self.root_path, "headers": _unpack(headers), "client": client,
            "server": server, "state": self.state.copy(),
            # Trailers are offered only when the client accepts them (TE: trailers) and the
            # protocol can carry them; request trailers are never passed on.
            "extensions": ({"http.response.trailers": {}, "http.response.pathsend": {}} if trailers
                           else {"http.response.pathsend": {}}),
        }
        req = _Request(self, h, body, more, trailers)
        self.requests[h] = req
        if eager:
            task = asyncio.Task(self.run(req, scope), loop=self.loop, eager_start=True)
        else:
            task = self.loop.create_task(self.run(req, scope))
        # asyncio holds tasks weakly, and a request can wait on a future only it holds
        # (reader.readline() on asyncio streams: StreamReaderProtocol holds its reader by a
        # weakref), so the next collection would free it mid-request. self.requests keeps
        # req, and req the task, until req.close() (as uvicorn keeps its tasks in a set).
        req.task = task
        return task

    def serve_inline(self, *a: Any) -> bool:
        """Inline requests (the default; not Options.LoopThread), 15 values each (asgi.go
        appendInline), run as eager tasks of this worker's loop on the calling thread, which holds the worker
        (the loop thread is waiting, or is the caller). A request that completes here never
        involves the loop thread; one that awaits continues there. Returns whether the
        loop thread has something to do."""
        loop = self.loop
        prev = asyncio.events._get_running_loop()
        if prev is not loop:
            asyncio.events._set_running_loop(loop)
        try:
            pending = False
            start = self.start_request
            for i in range(0, len(a), 15):
                (h, method, version, scheme, raw_path, path, query, headers, chost, cport,
                 shost, sport, body, more, trailers) = a[i:i + 15]
                try:
                    if not start(h, method, version, scheme, raw_path, path, query, headers,
                                 None if chost is None else (chost, cport),
                                 None if shost is None else (shost, sport), body, more, trailers,
                                 True).done():
                        pending = True
                except BaseException:
                    logger.exception("ASGI inline request %d failed to start", h)
                    pending = True
            return pending or bool(loop._ready)  # type: ignore[attr-defined]  # BaseEventLoop's
        finally:
            if prev is not loop:
                asyncio.events._set_running_loop(prev)

    def on_websocket(self, msg: tuple[Any, ...]) -> None:
        (_, h, _method, version, scheme, raw_path, path, query, headers, client, server,
         subprotocols) = msg
        scope: Scope = {
            "type": "websocket", "asgi": self.asgi, "http_version": version, "scheme": scheme,
            "path": path, "raw_path": raw_path, "query_string": query,
            "root_path": self.root_path, "headers": _unpack(headers), "client": client,
            "server": server, "subprotocols": subprotocols, "state": self.state.copy(),
            "extensions": {"websocket.http.response": {}},
        }
        ws = _WebSocket(self, h)
        self.requests[h] = ws
        ws.task = self.loop.create_task(self.run_websocket(ws, scope))  # kept: see start_request

    async def run_websocket(self, ws: _WebSocket, scope: Scope) -> None:
        try:
            await self.app(scope, ws.receive, ws.send)
        except BaseException as exc:
            logger.error("Exception in ASGI application", exc_info=exc)
            ws.fail()
        else:
            ws.finish()
        finally:
            self.requests.pop(ws.h, None)

    async def run(self, req: _Request, scope: Scope) -> None:
        try:
            await self.app(scope, req.receive, req.send)
        except BaseException as exc:
            logger.error("Exception in ASGI application", exc_info=exc)
            if not req.started:
                req.respond_error()
            elif not req.done:
                req.flush_head()
                _g.abort(self.server, req.h)
        else:
            if req.disconnected:
                # The client went away: nothing is left to answer, and returning early is
                # what an application should do then (uvicorn is silent here too).
                pass
            elif not req.started:
                logger.error("ASGI callable returned without starting a response")
                req.respond_error()
            elif not req.done:
                logger.error("ASGI callable returned without completing the response")
                req.flush_head()
                _g.abort(self.server, req.h)
        finally:
            req.close()


class _Request:
    __slots__ = ("worker", "h", "body", "more", "started", "done", "disconnected", "rx", "drain",
                 "head", "offered", "trailers", "body_done", "task")
    worker: _Worker
    h: int
    body: bytes | None  # the request body not yet received
    more: bool
    offered: bool  # trailers were offered
    started: bool
    done: bool
    disconnected: bool
    trailers: bool
    body_done: bool
    rx: asyncio.Future[Message] | None
    drain: asyncio.Future[None] | None
    head: Head | None
    task: asyncio.Task[None] | None

    def __init__(self, worker: _Worker, h: int, body: bytes | None, more: bool,
                 offered: bool) -> None:
        self.worker, self.h, self.body, self.more, self.offered = worker, h, body, more, offered
        self.started = self.done = self.disconnected = self.trailers = self.body_done = False
        self.rx = self.drain = self.head = self.task = None

    async def receive(self) -> Message:
        if self.body is not None:
            body, self.body = self.body, None
            return {"type": "http.request", "body": body, "more_body": self.more}
        if self.disconnected or self.done:
            return _disconnect()
        fut = self.rx = self.worker.loop.create_future()
        if self.more:
            _g.want_body(self.worker.server, self.h)
        # Without more body, this waits for the client to go away or the response to end.
        return await fut

    async def send(self, message: Message) -> None:
        kind = message["type"]
        if self.disconnected:
            return
        if kind == "http.response.start":
            if self.started:
                raise RuntimeError("the response has already started")
            trailers = bool(message.get("trailers", False))
            if trailers and not self.offered:
                raise RuntimeError("trailers were not offered for this request "
                                   "(no http.response.trailers in scope['extensions'])")
            packed, count = _pack(message.get("headers", ()))
            self.started, self.trailers = True, trailers
            # Held until the first body, so that both reach Go in one call.
            self.head = (message["status"], packed, count, trailers)
        elif kind == "http.response.body":
            if not self.started:
                raise RuntimeError("http.response.body before http.response.start")
            if self.body_done:
                raise RuntimeError("the response body is already complete")
            more = message.get("more_body", False)
            body = message.get("body", b"")
            head = self.head
            if head is not None:
                self.head = None
                try:
                    wait = _g.respond(self.worker.server, self.h, *head, body, more)
                except BaseException:
                    # Nothing reached the client: the error handler can still answer 500.
                    self.started = self.trailers = False
                    raise
            else:
                wait = _g.body(self.worker.server, self.h, body, more)
            if not more:
                self.body_done = True
                self.done = not self.trailers
            if wait:
                fut = self.drain = self.worker.loop.create_future()
                await fut
            if self.done:
                self.disconnect_rx()
        elif kind == "http.response.pathsend":
            # The whole body, sent by Go from the file (sendfile when possible).
            if not self.started:
                raise RuntimeError("http.response.pathsend before http.response.start")
            if self.head is None:
                raise RuntimeError("http.response.pathsend must replace the body, not follow it")
            head, self.head = self.head, None
            try:
                _g.pathsend(self.worker.server, self.h, *head, message["path"])
            except BaseException:
                self.started = self.trailers = False  # nothing was sent: a 500 can follow
                raise
            self.body_done = True
            self.done = not self.trailers
            if self.done:
                self.disconnect_rx()
        elif kind == "http.response.trailers":
            if not self.trailers:
                raise RuntimeError("http.response.trailers without trailers=True in http.response.start")
            if not self.body_done:
                raise RuntimeError("http.response.trailers before the final http.response.body")
            if self.done:
                raise RuntimeError("the response is already complete")
            packed, count = _pack(message.get("headers", ()))
            more = message.get("more_trailers", False)
            _g.trailers(self.worker.server, self.h, packed, count, more)
            if not more:
                self.done = True
                self.disconnect_rx()
        else:
            raise RuntimeError("unexpected ASGI message %r" % kind)

    def flush_head(self) -> None:
        if self.head is not None:
            head, self.head = self.head, None
            try:
                _g.start(self.worker.server, self.h, *head)
            except BaseException:
                pass  # the connection is aborted next anyway

    def respond_error(self) -> None:
        self.started = self.done = True
        self.head = None
        _g.respond(self.worker.server, self.h, 500,
                   b"content-type\0text/plain; charset=utf-8\0connection\0close", 2, False,
                   b"Internal Server Error", False)

    def resolve_rx(self, message: Message) -> None:
        fut, self.rx = self.rx, None
        if fut is not None and not fut.done():
            fut.set_result(message)

    def disconnect_rx(self) -> None:
        """Resolves a waiting receive() with http.disconnect; the message is built only
        when someone waits, which is rare."""
        if self.rx is not None:
            self.resolve_rx(_disconnect())

    def on_body(self, data: bytes, more: bool) -> None:
        self.more = more
        if self.rx is not None:
            self.resolve_rx({"type": "http.request", "body": data, "more_body": more})
        else:
            self.body = data

    def on_drained(self) -> None:
        fut, self.drain = self.drain, None
        if fut is not None and not fut.done():
            fut.set_result(None)

    def on_disconnect(self) -> None:
        self.disconnected = True
        self.disconnect_rx()
        self.on_drained()

    def close(self) -> None:
        self.worker.requests.pop(self.h, None)
        self.disconnect_rx()
        self.on_drained()


class _Lifespan:
    """The ASGI lifespan protocol for one worker; with "auto", an application that
    raises before completing startup is taken not to support it."""

    def __init__(self, worker: _Worker) -> None:
        self.worker = worker
        self.message: str | None = None
        self.inbox: asyncio.Queue[Message] = asyncio.Queue()
        loop = worker.loop
        # started: True (complete), False (failed) or None (lifespan not supported).
        self.started: asyncio.Future[bool | None]
        self.stopped: asyncio.Future[None]
        self.started, self.stopped = loop.create_future(), loop.create_future()
        self.task: asyncio.Task[None] | None = None

    async def startup(self) -> bool:
        self.task = self.worker.loop.create_task(self.run())
        await self.inbox.put({"type": "lifespan.startup"})
        ok = await self.started
        if ok is None:  # not supported
            self.task = None
            return True
        return ok

    async def shutdown(self) -> None:
        if self.task is None:
            return
        await self.inbox.put({"type": "lifespan.shutdown"})
        await self.stopped

    async def run(self) -> None:
        scope: Scope = {"type": "lifespan", "asgi": self.worker.asgi, "state": self.worker.state}
        try:
            await self.worker.app(scope, self.inbox.get, self.send)
        except BaseException as exc:
            if not self.started.done():
                if self.worker.lifespan == "on":
                    logger.error("ASGI lifespan startup failed", exc_info=exc)
                    self.message = "".join(traceback.format_exception(exc))
                    self.started.set_result(False)
                else:
                    logger.debug("ASGI lifespan not supported: %r", exc)
                    self.started.set_result(None)
            else:
                logger.error("Exception in ASGI lifespan", exc_info=exc)
        finally:
            if not self.started.done():
                self.started.set_result(None)
            if not self.stopped.done():
                self.stopped.set_result(None)

    async def send(self, message: Message) -> None:
        kind = message["type"]
        if kind == "lifespan.startup.complete":
            self.started.set_result(True)
        elif kind == "lifespan.startup.failed":
            self.message = message.get("message", "")
            self.started.set_result(False)
        elif kind in ("lifespan.shutdown.complete", "lifespan.shutdown.failed"):
            if kind.endswith("failed"):
                logger.error("ASGI lifespan shutdown failed: %s", message.get("message", ""))
            self.stopped.set_result(None)


_CONNECTING, _CONNECTED, _CLOSED, _RESPONDING = range(4)


class _WebSocket:
    """One ASGI websocket connection; Go performs the handshake and the I/O."""

    __slots__ = ("worker", "h", "state", "connected", "inbox", "rx", "drain", "gone", "head", "task")
    worker: _Worker
    h: int
    state: int  # _CONNECTING, _CONNECTED, _CLOSED or _RESPONDING
    connected: bool
    inbox: collections.deque[Message]
    rx: asyncio.Future[Message] | None
    drain: asyncio.Future[None] | None
    gone: Message | None  # websocket.disconnect, once the connection is gone
    head: Head | None
    task: asyncio.Task[None] | None

    def __init__(self, worker: _Worker, h: int) -> None:
        self.worker, self.h = worker, h
        self.state = _CONNECTING
        self.connected = False  # websocket.connect delivered
        self.inbox = collections.deque()
        self.rx = self.drain = self.gone = self.head = self.task = None

    async def receive(self) -> Message:
        if not self.connected:
            self.connected = True
            return {"type": "websocket.connect"}
        if self.inbox:
            return self.take()
        if self.gone is not None:
            return self.gone
        fut = self.rx = self.worker.loop.create_future()
        return await fut

    def take(self) -> Message:
        msg = self.inbox.popleft()
        _g.ws_ack(self.worker.server, self.h)  # the reader may read one more
        return msg

    def on_message(self, is_text: bool, data: bytes) -> None:
        msg: Message
        if is_text:
            msg = {"type": "websocket.receive", "bytes": None, "text": data.decode("utf-8")}
        else:
            msg = {"type": "websocket.receive", "bytes": data, "text": None}
        self.inbox.append(msg)
        fut, self.rx = self.rx, None
        if fut is not None and not fut.done():
            fut.set_result(self.take())

    def on_disconnect(self, code: int = 1006, reason: str = "") -> None:
        if self.gone is None:
            self.gone = {"type": "websocket.disconnect", "code": code, "reason": reason}
        self.state = _CLOSED
        fut, self.rx = self.rx, None
        if fut is not None and not fut.done():
            fut.set_result(self.take() if self.inbox else self.gone)
        self.on_drained()

    def on_drained(self) -> None:
        fut, self.drain = self.drain, None
        if fut is not None and not fut.done():
            fut.set_result(None)

    async def send(self, message: Message) -> None:
        kind = message["type"]
        server, h = self.worker.server, self.h
        if kind == "websocket.send":
            if self.state == _CLOSED:
                raise ClientDisconnected("the WebSocket is closed")
            if self.state != _CONNECTED:
                raise RuntimeError("websocket.send before websocket.accept")
            data: Any
            data, text = message.get("bytes"), message.get("text")
            if (data is None) == (text is None):
                raise RuntimeError("websocket.send needs exactly one of bytes and text")
            if text is not None:
                if _g.ws_send(server, h, True, text.encode("utf-8")):
                    await self.wait_drain()
            elif _g.ws_send(server, h, False, bytes(data)):
                await self.wait_drain()
        elif kind == "websocket.accept":
            if self.state != _CONNECTING:
                raise RuntimeError("websocket.accept in the wrong state")
            packed, count = _pack(message.get("headers", ()))
            _g.ws_accept(server, h, message.get("subprotocol") or "", packed, count)
            self.state = _CONNECTED
        elif kind == "websocket.close":
            if self.state == _CLOSED:
                return
            if self.state == _RESPONDING:
                raise RuntimeError("websocket.close during a denial response")
            code, reason = message.get("code", 1000), message.get("reason") or ""
            _g.ws_close(server, h, code, reason)  # before accept: the handshake gets a 403
            self.closed(code if self.state == _CONNECTED else 1006, reason)
        elif kind == "websocket.http.response.start":
            if self.state != _CONNECTING:
                raise RuntimeError("websocket.http.response.start after the handshake")
            if message["status"] == 101:
                raise RuntimeError("a denial response cannot switch protocols")
            packed, count = _pack(message.get("headers", ()))
            self.state = _RESPONDING
            self.head = (message["status"], packed, count, False)
        elif kind == "websocket.http.response.body":
            if self.state != _RESPONDING:
                raise RuntimeError("websocket.http.response.body without a response start")
            more, body = message.get("more_body", False), message.get("body", b"")
            head, self.head = self.head, None
            wait = (_g.respond(server, h, *head, body, more) if head is not None
                    else _g.body(server, h, body, more))
            if not more:
                self.closed(1006, "")
            elif wait:
                await self.wait_drain()
        else:
            raise RuntimeError("unexpected ASGI message %r" % kind)

    async def wait_drain(self) -> None:
        fut = self.drain = self.worker.loop.create_future()
        await fut

    def closed(self, code: int, reason: str) -> None:
        self.state = _CLOSED
        self.on_disconnect(code, reason)

    def finish(self) -> None:
        """The application returned: close an open connection normally; a handshake it
        never answered gets a 500."""
        if self.state == _CONNECTED:
            self.close_quietly(1000)
        elif self.state == _CONNECTING:
            logger.error("ASGI callable returned without answering the WebSocket handshake")
            self.respond_error()
        elif self.state == _RESPONDING:
            _g.abort(self.worker.server, self.h)

    def fail(self) -> None:
        if self.state == _CONNECTED:
            self.close_quietly(1011)  # internal error
        elif self.state == _CONNECTING:
            self.respond_error()
        elif self.state == _RESPONDING:
            _g.abort(self.worker.server, self.h)

    def close_quietly(self, code: int) -> None:
        try:
            _g.ws_close(self.worker.server, self.h, code, "")
        except Exception:
            pass
        self.closed(code, "")

    def respond_error(self) -> None:
        self.state = _CLOSED
        _g.respond(self.worker.server, self.h, 500,
                   b"content-type\0text/plain; charset=utf-8\0connection\0close", 2, False,
                   b"Internal Server Error", False)
