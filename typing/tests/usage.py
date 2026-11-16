"""What an application checks against the installed types-pythago (ci/typecheck.sh,
mypy --strict). Never run."""

from __future__ import annotations

import asyncio

import goaio
import pythago_asgi
import pythago_wsgi


async def pump(ch: goaio.Channel[int]) -> int:
    total = 0
    async for v in ch:
        total += v
    await ch.send(total)
    value, ok = await ch.recv_ok()
    return value if ok else 0


async def main() -> str:
    loop = asyncio.get_running_loop()
    assert isinstance(loop, goaio.GoEventLoop)
    loop.add_reader(0, print, "readable")
    server = await loop.create_server(asyncio.Protocol, "127.0.0.1", 0)
    host, port = server.sockets[0].getsockname()
    return host


s: str = goaio.run(main())
s2: str = goaio.run(main(), selector=True)
e: type[OSError] = pythago_asgi.ClientDisconnected
w = pythago_wsgi.FileWrapper(open("/dev/null", "rb"), 4096)
