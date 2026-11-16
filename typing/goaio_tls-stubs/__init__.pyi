"""goaio_tls: TLS on Go's side for goaio.GoEventLoop, when an ssl.SSLContext allows it."""

import ssl
from types import ModuleType
from typing import Final

from _goaio import _TLSSpec

# TLS 1.2 suites and curves crypto/tls offers, by OpenSSL's names.
GO_CIPHERS: Final[frozenset[str]]
GO_CURVES: Final[frozenset[str]]

def record(ssl: ModuleType) -> None:
    """Patches ssl.SSLContext (the module ssl, just executed) to record its configuration."""

def unmappable(ssl: ModuleType, ctx: ssl.SSLContext, server_side: bool) -> str | None:
    """Why Go cannot do what OpenSSL would with ctx, or None if it can."""

def go_config(
    ssl: ModuleType, ctx: ssl.SSLContext, server_side: bool, server_hostname: str | None, handshake_timeout: float | None
) -> _TLSSpec | None:
    """A dict for aio/tls.go describing ctx, or None if Go cannot do what OpenSSL would."""
