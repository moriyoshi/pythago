#!/usr/bin/env bash
# Unit tier for one variant (gil: CPython 3.13, ft: 3.14t):
#   - generate the test image (.work/testimage) for the host and the other architecture
#     (PYTHAGO_OTHER_ARCH, ci/lib.sh: run under qemu-user, the default when it can; build
#     only; or none);
#   - vet and gofmt;
#   - packages py's, aio's, asgi's, wsgi's and workers' tests (embedded stdlib) natively and under
#     qemu, asgi's again on the loop thread (PYTHAGO_ASGI_LOOP_THREAD=1), package env's tests
#     (PYTHAGO_TEST_FETCH=1: fetches a stock interpreter);
#   - py's and asgi's (both loop modes) again with the fusion on (PYTHAGO_TEST_FUSION=1:
#     py.Config.FusedBytes), natively and under qemu, and aio's and wsgi's natively;
#   - aio's and asgi's with real binary wheels (ci/test-wheels.txt, fetched per arch:
#     PYTHAGO_TEST_WHEELS) in every run above;
#   - examples/hello (gil only: it go-generates its own 3.13 image) from an empty
#     directory with an empty environment, on each arch.
#
#   ci/unit.sh gil|ft
. "$(dirname "$0")/lib.sh"
variant=${1:?usage: ci/unit.sh gil|ft}
arches=("$HOST_ARCH")
OTHER_ARCH_MODE=$(other_arch_mode)
case $OTHER_ARCH_MODE in
  run) arches+=("$(other_arch)") ;;
  build) arches+=("$(other_arch)"); log "linux/$(other_arch): image and test binaries built, not run (PYTHAGO_OTHER_ARCH=build)" ;;
  none) log "linux/$(other_arch): skipped (no qemu-user, or PYTHAGO_OTHER_ARCH=none): native only" ;;
esac
gen_image "$variant" "${arches[@]}"
cd "$ROOT"

pkgs=(./py/... ./aio ./asgi ./wsgi ./workers ./internal/httpfield ./internal/h2raw ./internal/testwatch ./internal/arena ./internal/lease ./internal/fusion ./internal/testwheels ./env ./cmd/... ./internal/pbs ./internal/ghrelease ./internal/tools/... ./glue)
if [ "$variant" = gil ]; then # examples/hello go-generates its own CPython 3.13 image
  log "examples/hello: go generate, linux/$HOST_ARCH"
  go generate ./examples/hello
  pkgs+=(./examples/...)
fi
log "vet, gofmt"
# go reports module downloads ("go: downloading ...") on stderr, which vet's output is read
# from: fetch the modules first (a cold module cache on CI), and never count those lines.
go mod download
vet=$(go vet "${pkgs[@]}" 2>&1 | grep -v "possible misuse of unsafe.Pointer\|^#\|^go: downloading" || true)
[ -z "$vet" ] || { echo "$vet"; exit 1; }
fmt=$(gofmt -l py aio asgi wsgi workers internal/httpfield internal/h2raw internal/testwatch internal/arena internal/lease internal/fusion internal/testwheels env cmd internal/pbs internal/ghrelease internal/tools glue examples)
[ -z "$fmt" ] || { echo "gofmt: $fmt"; exit 1; }

log "testwatch, arena, fusion, testwheels and fetchwheels tests"
go test -count=1 ./internal/testwatch ./internal/arena ./internal/fusion ./internal/testwheels ./internal/tools/fetchwheels
log "env tests"
PYTHAGO_TEST_FETCH=1 go test -count=1 ./env

for arch in "${arches[@]}"; do
  if ! runs_arch "$arch"; then # PYTHAGO_OTHER_ARCH=build: cross-build only
    log "test binaries and examples/hello, built for linux/$arch, not run"
    for pkg in py aio asgi wsgi; do
      GOARCH=$arch go test -c -o "$CACHE/$pkg-$variant-$arch.test" "./$pkg"
    done
    GOARCH=$arch go test -c -o "$CACHE/workers-$arch.test" ./workers
    if [ "$variant" = gil ]; then
      GOARCH=$arch PYTHAGO_SYSROOT=$(sysroot "$arch") go generate ./examples/hello
      GOARCH=$arch go build -o "$CACHE/hello-$arch" ./examples/hello
    fi
    continue
  fi
  log "test wheels, $variant linux/$arch"
  PYTHAGO_TEST_WHEELS=$(test_wheels "$variant" linux "$arch")
  export PYTHAGO_TEST_WHEELS # aio's and asgi's wheel tests (internal/testwheels)
  log "py tests, $variant linux/$arch"
  GOARCH=$arch go test -c -o "$CACHE/py-$variant-$arch.test" ./py
  (cd py && unset PYTHONHOME && run_arch "$arch" "$CACHE/py-$variant-$arch.test" -test.timeout 2400s)
  log "aio tests, $variant linux/$arch"
  GOARCH=$arch go test -c -o "$CACHE/aio-$variant-$arch.test" ./aio
  (cd aio && unset PYTHONHOME && run_arch "$arch" "$CACHE/aio-$variant-$arch.test" -test.timeout 600s)
  log "asgi tests, $variant linux/$arch"
  GOARCH=$arch go test -c -o "$CACHE/asgi-$variant-$arch.test" ./asgi
  (cd asgi && unset PYTHONHOME && run_arch "$arch" "$CACHE/asgi-$variant-$arch.test" -test.timeout 600s)
  log "asgi tests, loop thread (Options.LoopThread), $variant linux/$arch"
  (cd asgi && unset PYTHONHOME && PYTHAGO_ASGI_LOOP_THREAD=1 run_arch "$arch" "$CACHE/asgi-$variant-$arch.test" -test.timeout 600s)
  log "wsgi tests, $variant linux/$arch"
  GOARCH=$arch go test -c -o "$CACHE/wsgi-$variant-$arch.test" ./wsgi
  (cd wsgi && unset PYTHONHOME && run_arch "$arch" "$CACHE/wsgi-$variant-$arch.test" -test.timeout 600s)
  # The fusion (py.Config.FusedBytes) on: py, asgi inline and on the loop thread, and
  # natively aio and wsgi.
  log "py tests, fusion on, $variant linux/$arch"
  (cd py && unset PYTHONHOME && PYTHAGO_TEST_FUSION=1 run_arch "$arch" "$CACHE/py-$variant-$arch.test" -test.timeout 2400s)
  log "asgi tests, fusion on, $variant linux/$arch"
  (cd asgi && unset PYTHONHOME && PYTHAGO_TEST_FUSION=1 run_arch "$arch" "$CACHE/asgi-$variant-$arch.test" -test.timeout 600s)
  log "asgi tests, fusion on, loop thread, $variant linux/$arch"
  (cd asgi && unset PYTHONHOME && PYTHAGO_TEST_FUSION=1 PYTHAGO_ASGI_LOOP_THREAD=1 run_arch "$arch" "$CACHE/asgi-$variant-$arch.test" -test.timeout 600s)
  if [ "$arch" = "$HOST_ARCH" ]; then
    log "aio and wsgi tests, fusion on, $variant linux/$arch"
    (cd aio && unset PYTHONHOME && PYTHAGO_TEST_FUSION=1 "$CACHE/aio-$variant-$arch.test" -test.timeout 600s)
    (cd wsgi && unset PYTHONHOME && PYTHAGO_TEST_FUSION=1 "$CACHE/wsgi-$variant-$arch.test" -test.timeout 600s)
  fi
  log "workers tests, linux/$arch"
  GOARCH=$arch go test -c -o "$CACHE/workers-$arch.test" ./workers
  (cd workers && run_arch "$arch" "$CACHE/workers-$arch.test" -test.timeout 600s)

  [ "$variant" = gil ] || continue # examples/hello generates its own CPython 3.13 image
  log "examples/hello, linux/$arch"
  [ "$arch" = "$HOST_ARCH" ] || GOARCH=$arch PYTHAGO_SYSROOT=$(sysroot "$arch") go generate ./examples/hello
  GOARCH=$arch go build -o "$CACHE/hello-$arch" ./examples/hello
  empty=$(mktemp -d)
  cp "$CACHE/hello-$arch" "$empty/hello"
  out=$(cd "$empty" && run_clean "$arch" ./hello 2>&1 | grep -v '^nomaps:')
  rm -rf "$empty"
  echo "$out"
  echo "$out" | grep -q '"greeting": \["GOPHER!", "PYTHON 3\.' || { echo "hello failed"; exit 1; }
done
log "unit tier ($variant) passed: ${arches[*]} (linux/$(other_arch): $OTHER_ARCH_MODE)"
