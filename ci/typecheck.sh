#!/usr/bin/env bash
# Type checks: builds types-pythago (typing/) and installs it with a pinned mypy into a
# venv of its own (uv), then checks
#   - the stubs themselves;
#   - the annotated sources (aio, asgi, wsgi, and the bench, ci and example scripts) against
#     the installed stubs, with typing/mypy.ini;
#   - typing/tests/usage.py, an application's view of the stubs, with --strict.
# The stubs are checked against the live modules by the aio, asgi and wsgi tests
# (TestStubs). Needs uv and network access on the first run.
#
#   ci/typecheck.sh
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CACHE=${PYTHAGO_CI_CACHE:-$HOME/.cache/pythago-ci}
MYPY=mypy==2.4.1
V=$CACHE/typecheck
cd "$ROOT"
[ -x "$V/venv/bin/mypy" ] && [ "$("$V/venv/bin/mypy" --version | cut -d' ' -f2)" = "${MYPY#mypy==}" ] ||
  { rm -rf "$V/venv"; uv venv -q --python 3.13 "$V/venv"; VIRTUAL_ENV=$V/venv uv pip install -q "$MYPY"; }
rm -rf "$V/dist"
uv build -q --wheel --out-dir "$V/dist" typing
VIRTUAL_ENV=$V/venv uv pip install -q --reinstall "$V"/dist/types_pythago-*.whl
mypy() { "$V/venv/bin/mypy" --cache-dir "$V/cache" "$@"; }
echo "== stubs"
mypy --config-file typing/mypy.ini typing/*-stubs/__init__.pyi
echo "== sources"
mypy --config-file typing/mypy.ini aio/goaio.py aio/goaio_tls.py aio/channel.py asgi/pythago_asgi.py wsgi/pythago_wsgi.py
# One run per script: several share a module name (summarize.py).
for f in bench/*/*.py ci/*.py ci/gpu/*.py examples/hello/app/hello/__init__.py; do
  mypy --config-file typing/mypy.ini "$f" >/dev/null || mypy --config-file typing/mypy.ini "$f"
done
echo "== usage (--strict)"
(cd typing/tests && mypy --strict usage.py)
