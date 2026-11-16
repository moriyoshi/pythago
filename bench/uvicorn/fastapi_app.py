"""The benchmark's endpoints (bench/uvicorn/app.py) as a FastAPI application."""

import asyncio

from fastapi import FastAPI
from fastapi.responses import Response

app = FastAPI()
SMALL = b'{"message": "hello, world"}'
BIG = bytes(range(256)) * 256


@app.get("/")
async def small() -> Response:
    return Response(SMALL, media_type="application/json")


@app.get("/sleep")
async def sleep() -> Response:
    await asyncio.sleep(0.001)
    return Response(SMALL, media_type="application/json")


@app.get("/big")
async def big() -> Response:
    return Response(BIG, media_type="application/json")
