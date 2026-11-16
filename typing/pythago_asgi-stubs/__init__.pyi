"""ASGI workers for package asgi (Go): each worker is a thread running a
goaio.GoEventLoop. Applications see ASGI 3.0 (spec 2.4) scopes and messages; this
module's own API is ClientDisconnected and load. start and join are called by Go."""

from logging import Logger
from typing import Any, Literal

logger: Logger

class ClientDisconnected(OSError):
    """send() on a WebSocket the client closed (ASGI spec 2.4)."""

def load(spec: str) -> Any:
    """Imports "module:attribute" (dots allowed in the attribute; "app" if omitted)."""

def start(server: int, spec: str, workers: int, root_path: str, lifespan: Literal["auto", "on", "off"]) -> None:
    """Starts the server's worker threads (called by Go)."""

def join(server: int, timeout: float | None) -> bool:
    """Waits until the server's worker threads have exited, their thread states' teardown
    included; timeout is in seconds, or None. Returns whether they all have."""
