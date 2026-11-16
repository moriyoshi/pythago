#!/bin/bash
# The macOS half of ci/darwin.sh, run on the host from the synced tree (macOS's own bash
# 3.2 runs it). gil and ft generate the test image for darwin/ARCH (.work/testimage and
# .work/go.work, as ci/lib.sh does on Linux) and run py's, aio's, asgi's, wsgi's and
# workers' tests, asgi's again on the loop thread (PYTHAGO_ASGI_LOOP_THREAD=1), py's and
# asgi's (both loop modes) with the fusion on (PYTHAGO_TEST_FUSION=1), aio's and asgi's
# with binary wheels (PYTHAGO_TEST_WHEELS), and (gil) examples/hello from an
# empty directory and environment. glue rebuilds the darwin glue
# objects (pinned LLVM and macOS SDK, see cmd/pythago-dist/prebuild.go) into ../ci/glue and
# compares them with glue/prebuilt/SHA256SUMS; glue-update only rebuilds them.
#
#   PYTHAGO_LLVM  clang directory for compiling the glue when there is no prebuilt object
#                 (default: Xcode's clang)
#   PYTHAGO_BAO   bao executable (default: the bao built into pythago-dist)
#   PYTHAGO_DARWIN_CACHE  directory for the built tools, test binaries and rebuilt glue
#                 (default: ../ci, next to the synced tree; ci/darwin.sh glue-update
#                 copies the glue from there)
#
# Over ssh (ci/darwin.sh) the tree is ~/pythago-work/src and Go 1.26.11 is
# ~/pythago-work/go/bin/go when present, else the go on PATH (GOTOOLCHAIN fetches 1.26.11).
# On a GitHub Actions runner, ci/darwin-gha.sh runs this script from the checkout.
#
#   bash ci/darwin-host.sh gil|ft|glue|glue-update ARCH
set -euo pipefail
variant=${1:?usage: darwin-host.sh gil|ft|glue|glue-update ARCH}
arch=${2:?usage: darwin-host.sh gil|ft|glue|glue-update ARCH}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=$(cd "$ROOT/.." && pwd)
export GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0
if [ -x "$BASE/go/bin/go" ]; then export PATH="$BASE/go/bin:$PATH"; fi
CACHE=${PYTHAGO_DARWIN_CACHE:-$BASE/ci}
WORK_DIR=$ROOT/.work
IMAGE_DIR=$WORK_DIR/testimage
log() { printf '== %s\n' "$*" >&2; }

[ "$(go env GOARCH)" = "$arch" ] || { echo "this host runs $(go env GOARCH), not $arch" >&2; exit 1; }
case $variant in
  gil) pyargs="-python 3.13" ;;
  ft) pyargs="-python 3.14 -variant freethreaded" ;;
  glue|glue-update) ;;
  *) echo "unknown variant $variant" >&2; exit 1 ;;
esac

cd "$ROOT"
mkdir -p "$CACHE"
export GOWORK=off
go build -o "$CACHE/pythago-dist" ./cmd/pythago-dist
case $variant in glue|glue-update)
  out=$CACHE/glue
  rm -rf "$out"
  mkdir -p "$out"
  cp glue/prebuilt/SHA256SUMS "$out/" # the Linux lines, which a darwin prebuild keeps
  log "darwin glue objects ($(sw_vers -productVersion), darwin/$arch)"
  "$CACHE/pythago-dist" -prebuild-glue "$out"
  [ "$variant" = glue ] || exit 0
  if ! diff -u glue/prebuilt/SHA256SUMS "$out/SHA256SUMS"; then
    echo "glue/prebuilt's darwin objects differ from a rebuild: run ci/darwin.sh glue-update and commit the result" >&2
    exit 1
  fi
  # The rebuilt objects themselves, so that a prebuild that built nothing cannot pass.
  grep -- '-darwin_' glue/prebuilt/SHA256SUMS | (cd "$out" && shasum -a 256 -c --quiet -)
  (cd glue/prebuilt && shasum -a 256 -c --quiet SHA256SUMS)
  log "darwin glue objects reproduce glue/prebuilt/SHA256SUMS"
  exit 0
esac
log "pythago-dist $variant darwin/$arch ($(sw_vers -productVersion), $(xcrun --show-sdk-version) SDK)"
rm -rf "$IMAGE_DIR" "$WORK_DIR/go.work" "$WORK_DIR/go.work.sum"
# shellcheck disable=SC2086
"$CACHE/pythago-dist" $pyargs -target "darwin/$arch" -o "$IMAGE_DIR"
purego=$(go list -m -f '{{.Version}}' github.com/ebitengine/purego)
printf '%s\n' \
  "module github.com/moriyoshi/pythago/internal/testimage" "" "go 1.26.0" "" \
  "require github.com/ebitengine/purego $purego" >"$IMAGE_DIR/go.mod"
printf '%s\n' "go 1.26.0" "" "use (" "	.." "	./testimage" ")" >"$WORK_DIR/go.work"
export GOWORK=$WORK_DIR/go.work

# aio's and asgi's tests load binary wheels (ci/test-wheels.txt, for the variant and
# darwin/ARCH: internal/tools/fetchwheels, which checks PyPI's sha256).
log "test wheels, $variant darwin/$arch"
GOWORK=off go build -o "$CACHE/fetchwheels" ./internal/tools/fetchwheels
# shellcheck disable=SC2086
"$CACHE/fetchwheels" $pyargs -target "darwin/$arch" -cache "$CACHE/wheels/cache" \
  -o "$CACHE/wheels/$variant-darwin-$arch" -r ci/test-wheels.txt
export PYTHAGO_TEST_WHEELS=$CACHE/wheels/$variant-darwin-$arch

unset PYTHONHOME
for pkg in py aio asgi wsgi workers; do
  log "$pkg tests, $variant darwin/$arch"
  go test -c -o "$CACHE/$pkg-$variant.test" "./$pkg"
  (cd "$pkg" && "$CACHE/$pkg-$variant.test" -test.timeout 2400s)
done
log "asgi tests, loop thread (Options.LoopThread), $variant darwin/$arch"
(cd asgi && PYTHAGO_ASGI_LOOP_THREAD=1 "$CACHE/asgi-$variant.test" -test.timeout 600s)
# The fusion (py.Config.FusedBytes) on: py, and asgi inline and on the loop thread.
log "py tests, fusion on, $variant darwin/$arch"
(cd py && PYTHAGO_TEST_FUSION=1 "$CACHE/py-$variant.test" -test.timeout 2400s)
log "asgi tests, fusion on, $variant darwin/$arch"
(cd asgi && PYTHAGO_TEST_FUSION=1 "$CACHE/asgi-$variant.test" -test.timeout 600s)
log "asgi tests, fusion on, loop thread, $variant darwin/$arch"
(cd asgi && PYTHAGO_TEST_FUSION=1 PYTHAGO_ASGI_LOOP_THREAD=1 "$CACHE/asgi-$variant.test" -test.timeout 600s)

# examples/hello generates its own CPython 3.13 image (gil only, as on Linux), then runs
# from an empty directory with an empty environment.
if [ "$variant" = gil ]; then
  log "examples/hello, darwin/$arch"
  GOWORK=off go generate ./examples/hello
  GOWORK=off go build -o "$CACHE/hello" ./examples/hello
  empty=$(mktemp -d)
  cp "$CACHE/hello" "$empty/hello"
  out=$(cd "$empty" && env -i ./hello 2>&1)
  rm -rf "$empty"
  echo "$out"
  echo "$out" | grep -q '"greeting": \["GOPHER!", "PYTHON 3\.' || { echo "hello failed" >&2; exit 1; }
fi
log "darwin/$arch ($variant) passed"
