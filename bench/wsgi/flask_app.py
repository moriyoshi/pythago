"""The same routes as app.py, through Flask."""
import time

from flask import Flask

app = Flask(__name__)
BIG = b"x" * (64 << 10)


@app.get("/")
def hello() -> str:
    return "Hello, world!"


@app.get("/big")
def big() -> bytes:
    return BIG


@app.get("/sleep")
def sleep() -> str:
    time.sleep(0.01)
    return "slept"
