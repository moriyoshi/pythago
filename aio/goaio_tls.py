"""TLS on Go's side for goaio.GoEventLoop, when an ssl.SSLContext allows it.

An SSLContext does not tell what it was configured with (the certificate chain, the
trust anchors, ALPN). aio's import hook therefore patches ssl.SSLContext as ssl is
imported: each configuring method still does its work, and also records its arguments on
the context. go_config then describes a context to Go's crypto/tls, or returns None when
Go could not do exactly what OpenSSL would (a Python callback, a password-protected key,
CRL checks, a subclass such as truststore's), and asyncio's own SSLProtocol is used.
"""

from __future__ import annotations

import os

TYPE_CHECKING = False
if TYPE_CHECKING:
    import ssl as _ssl
    from collections.abc import Callable
    from types import ModuleType
    from typing import Any, TypedDict

    from _goaio import _TLSSpec

    class _Record(TypedDict):
        """What a context was configured with."""

        certs: list[tuple[str, str]]
        cafiles: list[str]
        capaths: list[str]
        cadata: list[str | bytes]
        alpn: list[str]
        curves: list[str]
        unmappable: list[str]

_KEY = "_goaio_tls"

# TLS 1.2 suites crypto/tls offers, by OpenSSL's names (aio/tls.go cipherOpenSSLNames).
GO_CIPHERS = frozenset({
    "ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
    "ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
    "ECDHE-ECDSA-CHACHA20-POLY1305", "ECDHE-RSA-CHACHA20-POLY1305",
    "ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
    "ECDHE-ECDSA-AES128-SHA", "ECDHE-RSA-AES128-SHA",
    "ECDHE-ECDSA-AES256-SHA", "ECDHE-RSA-AES256-SHA",
})
GO_CURVES = frozenset({"prime256v1", "secp256r1", "P-256", "secp384r1", "P-384",
                       "secp521r1", "P-521", "X25519"})


def _new_record() -> _Record:
    return {"certs": [], "cafiles": [], "capaths": [], "cadata": [], "alpn": [],
            "curves": [], "unmappable": []}


def _rec(ctx: _ssl.SSLContext) -> _Record:
    d: _Record | None = ctx.__dict__.get(_KEY)
    if d is None:
        d = ctx.__dict__[_KEY] = _new_record()
    return d


def record(ssl: ModuleType) -> None:
    """Patches ssl.SSLContext (the module ssl, just executed) to record its configuration."""
    C = ssl.SSLContext
    if C.__dict__.get("_goaio_recording"):
        return
    C._goaio_recording = True

    def wrap(name: str, note: Callable[..., None]) -> None:
        orig = getattr(C, name)

        def method(self: _ssl.SSLContext, *args: Any, **kwargs: Any) -> Any:
            result = orig(self, *args, **kwargs)  # errors propagate, and nothing is recorded
            note(_rec(self), *args, **kwargs)
            return result

        method.__name__, method.__qualname__ = name, "SSLContext." + name
        method.__doc__ = orig.__doc__
        setattr(C, name, method)

    def chain(r: _Record, certfile: str | os.PathLike[str], keyfile: str | os.PathLike[str] | None = None,
              password: object = None) -> None:
        if password is not None:  # crypto/tls reads no encrypted keys
            r["unmappable"].append("load_cert_chain(password=...)")
        r["certs"].append((os.fspath(certfile), "" if keyfile is None else os.fspath(keyfile)))

    def verify(r: _Record, cafile: str | os.PathLike[str] | None = None,
               capath: str | os.PathLike[str] | None = None,
               cadata: str | bytes | bytearray | memoryview | None = None) -> None:
        if cafile is not None:
            r["cafiles"].append(os.fspath(cafile))
        if capath is not None:
            r["capaths"].append(os.fspath(capath))
        if cadata is not None:
            r["cadata"].append(cadata if isinstance(cadata, str) else bytes(cadata))

    def defaults(r: _Record) -> None:
        # What OpenSSL's default verify paths are (SSL_CERT_FILE, SSL_CERT_DIR, or the
        # build's): the same files Python's context then trusts.
        paths = ssl.get_default_verify_paths()
        if paths.cafile:
            r["cafiles"].append(paths.cafile)
        if paths.capath:
            r["capaths"].append(paths.capath)

    def alpn(r: _Record, protocols: list[str]) -> None:
        r["alpn"] = list(protocols)

    def curve(r: _Record, name: str) -> None:
        r["curves"] = [name]

    def unmappable(what: str) -> Callable[..., None]:
        return lambda r, *args, **kwargs: r["unmappable"].append(what)

    wrap("load_cert_chain", chain)
    wrap("load_verify_locations", verify)
    wrap("set_default_verify_paths", defaults)  # load_default_certs calls it
    wrap("set_alpn_protocols", alpn)
    wrap("set_ecdh_curve", curve)
    wrap("set_npn_protocols", unmappable("NPN"))
    wrap("set_psk_client_callback", unmappable("PSK"))
    wrap("set_psk_server_callback", unmappable("PSK"))
    # load_dh_params: DHE suites, which crypto/tls does not have; nothing to record.


def _versions(ssl: ModuleType, ctx: _ssl.SSLContext) -> tuple[int, int]:
    lo, hi = int(ctx.minimum_version), int(ctx.maximum_version)
    if lo == int(ssl.TLSVersion.MINIMUM_SUPPORTED) or lo < 0x0301:
        lo = 0x0301
    if hi == int(ssl.TLSVersion.MAXIMUM_SUPPORTED) or hi > 0x0304:
        hi = 0x0304
    # The deprecated OP_NO_* options narrow the range too.
    for version, flag in ((0x0301, "OP_NO_TLSv1"), (0x0302, "OP_NO_TLSv1_1"),
                          (0x0303, "OP_NO_TLSv1_2"), (0x0304, "OP_NO_TLSv1_3")):
        if ctx.options & getattr(ssl, flag):
            if version == lo:
                lo += 1
            elif version == hi:
                hi -= 1
    return lo, hi


def unmappable(ssl: ModuleType, ctx: _ssl.SSLContext, server_side: bool) -> str | None:
    """Why Go cannot do what OpenSSL would with ctx, or None if it can."""
    if type(ctx) is not ssl.SSLContext:
        return "a subclass of ssl.SSLContext (%s) may verify differently" % type(ctx).__qualname__
    r: _Record = ctx.__dict__.get(_KEY) or _new_record()
    if r["unmappable"]:
        return ", ".join(r["unmappable"])
    if ctx.sni_callback is not None:
        return "sni_callback"
    if getattr(ctx, "_msg_callback", None) is not None:
        return "_msg_callback"
    if ctx.post_handshake_auth:
        return "post_handshake_auth"
    if ctx.verify_flags & (ssl.VERIFY_CRL_CHECK_LEAF | ssl.VERIFY_CRL_CHECK_CHAIN):
        return "CRL checks (verify_flags)"
    if server_side and not r["certs"]:
        return "a server context without load_cert_chain"
    bad = [c for c in r["curves"] if c not in GO_CURVES]
    if bad:
        return "curves %s" % bad
    lo, hi = _versions(ssl, ctx)
    if lo > hi:
        return "no TLS version enabled"
    if hi == 0x0303 and not _go_ciphers(ctx):
        return "TLS 1.2 only, and none of its ciphers in crypto/tls"
    return None


def _go_ciphers(ctx: _ssl.SSLContext) -> list[str]:
    return [c["name"] for c in ctx.get_ciphers()
            if c.get("protocol") == "TLSv1.2" and c["name"] in GO_CIPHERS]


def go_config(ssl: ModuleType, ctx: _ssl.SSLContext, server_side: bool,
              server_hostname: str | None, handshake_timeout: float | None) -> _TLSSpec | None:
    """A dict for aio/tls.go describing ctx, or None if Go cannot do what OpenSSL would
    (unmappable says why)."""
    if unmappable(ssl, ctx, server_side) is not None:
        return None
    r: _Record = ctx.__dict__.get(_KEY) or _new_record()
    lo, hi = _versions(ssl, ctx)
    return {
        "server_side": bool(server_side),
        "server_hostname": server_hostname or "",
        "verify_mode": int(ctx.verify_mode),
        "check_hostname": bool(ctx.check_hostname),
        # OpenSSL falls back to the subject's common name for a certificate without DNS
        # names; crypto/x509 never does, so tls.go does it.
        "check_common_name": bool(ctx.hostname_checks_common_name),
        "min_version": lo,
        "max_version": hi,
        "ciphers": _go_ciphers(ctx),
        "curves": list(r["curves"]),
        "alpn": list(r["alpn"]),
        "keylog": ctx.keylog_filename or "",
        "no_tickets": bool(ctx.options & ssl.OP_NO_TICKET),
        "certs": list(r["certs"]),
        "cafiles": list(r["cafiles"]),
        "capaths": list(r["capaths"]),
        "cadata": list(r["cadata"]),
        "handshake_timeout_ms": int((handshake_timeout or 60.0) * 1000),
    }
