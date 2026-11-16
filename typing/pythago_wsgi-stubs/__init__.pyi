"""The Python half of pythago's package wsgi: the module a WSGI application served by
wsgi.Server can import, for ClientDisconnected and FileWrapper."""

import logging
from collections.abc import Callable, Iterator
from typing import Any, Final

from _typeshed import SupportsRead
from _typeshed.wsgi import WSGIApplication

__all__ = ["ClientDisconnected", "FileWrapper"]

logger: logging.Logger

DONE: Final = 0
FAILED: Final = 1
ABORT: Final = 2

class ClientDisconnected(OSError):
    """The client went away, or its request body could not be read: raised by write(),
    by the body iterator's sends and by wsgi.input."""

class FileWrapper:
    """wsgi.file_wrapper. Returned from the application, a wrapped regular file is sent
    by Go (sendfile where the platform has it) from its current position; anything else
    is read in blocks of blksize."""

    filelike: SupportsRead[bytes]
    blksize: int
    close: Callable[[], object]  # present when filelike has close
    def __init__(self, filelike: SupportsRead[bytes], blksize: int = 8192) -> None: ...
    def __iter__(self) -> Iterator[bytes]: ...

def serve(app: WSGIApplication, base: dict[str, Any], h: int, packed: bytes, body: bytes | None) -> int:
    """Runs one request (called by Go): DONE, FAILED or ABORT."""
