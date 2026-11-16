"""A framework-free ASGI app for the uvicorn benchmark (bench/uvicorn/run.sh).

/      a small JSON response
/big   a 64 KiB response
/sleep a small response after asyncio.sleep(0.001), so requests overlap in the loop
/upload reads the request body and answers with its length
"""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from typing import Any

Message = dict[str, Any]

SMALL = b'{"message": "hello, world"}'
BIG = bytes(range(256)) * 256


async def app(scope: dict[str, Any], receive: Callable[[], Awaitable[Message]],
              send: Callable[[Message], Awaitable[None]]) -> None:
    if scope["type"] == "lifespan":
        while True:
            msg = await receive()
            if msg["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            elif msg["type"] == "lifespan.shutdown":
                await send({"type": "lifespan.shutdown.complete"})
                return
    path = scope["path"]
    if path == "/sleep":
        await asyncio.sleep(0.001)
    body = BIG if path == "/big" else SMALL
    if path == "/upload":
        n, more = 0, True
        while more:
            msg = await receive()
            n += len(msg.get("body", b""))
            more = msg.get("more_body", False)
        body = b'{"received": %d}' % n
    await send({"type": "http.response.start", "status": 200,
                "headers": [(b"content-type", b"application/json"),
                            (b"content-length", str(len(body)).encode())]})
    await send({"type": "http.response.body", "body": body})
