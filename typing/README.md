# types-pythago

Type stubs for the Python modules that [pythago](https://github.com/moriyoshi/pythago)
provides to embedded Python code. pythago embeds CPython in Go programs, so these modules
exist only inside a pythago program. Install this package where your type checker runs.

| Module | What it is |
|---|---|
| `goaio` | asyncio event loops driven by Go: `GoEventLoop`, `GoSelectorEventLoop`, `run`, and `Channel`, the awaitable a Go channel becomes |
| `goaio_tls` | TLS on Go's side for `GoEventLoop` |
| `pythago_asgi` | the ASGI server's workers (`ClientDisconnected`, `load`) |
| `pythago_wsgi` | the WSGI server's helpers (`ClientDisconnected`, `FileWrapper`) |
| `_goaio`, `_pythago_asgi`, `_pythago_wsgi` | the Go functions behind them (internal) |

`goaio.Channel` exists only for type checking: each Go channel has a class of its own at
runtime. Use it in annotations, for example `ch: goaio.Channel[int]`, with
`from __future__ import annotations` or under `if TYPE_CHECKING:`.

```
pip install types-pythago
```

The stubs are checked against the live modules by pythago's tests
(`typing/stubcheck.py`), and the annotated sources against the stubs by
`ci/typecheck.sh`.
