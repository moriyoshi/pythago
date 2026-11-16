"""One bench/tls run: a TLS client configuration against the Go peer, for one workload.

    pyrun bench/tls/client.py CONFIG WORKLOAD SECONDS CONCURRENCY CA PORT

CONFIG:
    go     goaio.GoEventLoop, a plain SSLContext: Go's crypto/tls
    py     goaio.GoEventLoop, an SSLContext subclass: Python's ssl (asyncio SSLProtocol)
    stock  asyncio's own event loop, Python's ssl
    plain  goaio.GoEventLoop without TLS (the peer still speaks TLS: only for bulk and rr
           against a plain peer port, PORT+1)
WORKLOAD:
    bulk       download as fast as possible on each connection (MB/s)
    rr         1 KiB request, 1 KiB response, back to back on each connection (req/s)
    handshake  connect, finish the handshake, close (handshakes/s)

Prints one JSON line: the rate, the client process's CPU seconds per unit (Go's TLS
goroutines included: they are this process), and what was negotiated.
"""

from __future__ import annotations

import asyncio
import json
import os
import ssl
import sys
import time
from collections.abc import Awaitable, Callable

import goaio

config, workload = sys.argv[1], sys.argv[2]
seconds, conns = float(sys.argv[3]), int(sys.argv[4])
ca, port = sys.argv[5], int(sys.argv[6])


class PythonSSL(ssl.SSLContext):
    """A subclass: goaio leaves it to Python's ssl."""


def context() -> ssl.SSLContext:
    if config == "py":
        ctx = PythonSSL(ssl.PROTOCOL_TLS_CLIENT)
        ctx.load_verify_locations(ca)
        return ctx
    return ssl.create_default_context(cafile=ca)


info: dict[str, str] = {}


async def connect() -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
    if config == "plain":
        return await asyncio.open_connection("127.0.0.1", port + 1)
    reader, writer = await asyncio.open_connection("127.0.0.1", port, ssl=context(),
                                                   server_hostname="localhost")
    if not info:
        obj = writer.get_extra_info("ssl_object")
        info.update(version=obj.version(), cipher=obj.cipher()[0],
                    transport=type(writer.transport).__name__)
    return reader, writer


async def close(writer: asyncio.StreamWriter) -> None:
    writer.close()
    try:
        await writer.wait_closed()
    except (ssl.SSLError, ConnectionError):
        pass


async def bulk(deadline: float, counter: list[int]) -> None:
    reader, writer = await connect()
    writer.write(b"B" + (1 << 40).to_bytes(8, "big"))
    while time.monotonic() < deadline:
        data = await reader.read(256 << 10)
        if not data:
            break
        counter[0] += len(data)
    writer.transport.abort()


async def rr(deadline: float, counter: list[int]) -> None:
    reader, writer = await connect()
    writer.write(b"E")
    msg = b"x" * 1024
    while time.monotonic() < deadline:
        writer.write(msg)
        await reader.readexactly(len(msg))
        counter[0] += 1
    await close(writer)


async def handshake(deadline: float, counter: list[int]) -> None:
    while time.monotonic() < deadline:
        _, writer = await connect()
        writer.write(b"Q")  # the peer closes
        await close(writer)
        counter[0] += 1


async def main() -> None:
    run: Callable[[float, list[int]], Awaitable[None]] = {"bulk": bulk, "rr": rr, "handshake": handshake}[workload]
    # Warm-up: a connection, so imports and the first handshake are not measured.
    _, w = await connect()
    w.write(b"Q")
    await close(w)
    counter = [0]
    cpu0, t0 = os.times(), time.monotonic()
    deadline = t0 + seconds
    await asyncio.gather(*(run(deadline, counter) for _ in range(conns)))
    elapsed, cpu1 = time.monotonic() - t0, os.times()
    cpu = (cpu1.user - cpu0.user) + (cpu1.system - cpu0.system)
    n = counter[0]
    if workload == "bulk":
        rate, unit, per = n / elapsed / 1e6, "MB/s", "cpu_s_per_GB"
        cost = cpu / (n / 1e9) if n else None
    else:
        rate = n / elapsed
        unit = "req/s" if workload == "rr" else "handshakes/s"
        per, cost = "cpu_us_per_op", (cpu / n * 1e6 if n else None)
    print(json.dumps({"config": config, "workload": workload, "conns": conns, "rate": round(rate, 1),
                      "unit": unit, per: round(cost, 3) if cost else None,
                      "cpu_util": round(cpu / elapsed, 2), **info}))


factory = asyncio.new_event_loop if config == "stock" else goaio.GoEventLoop
asyncio.run(main(), loop_factory=factory)
