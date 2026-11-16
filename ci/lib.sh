#!/usr/bin/env bash
# Shared settings and helpers for pythago's CI scripts (sourced, not run).
#
#   PYTHAGO_LLVM            LLVM bin dir, at least the PBS clang version (default: fetched by
#                           pythago-dist into the user cache)
#   PYTHAGO_BAO             bao executable (default: the bao built into pythago-dist, from go.mod)
#   PYTHAGO_SYSROOT_<ARCH>  glibc sysroot for cross targets, e.g. PYTHAGO_SYSROOT_AMD64
#                           (default: built by ci/sysroot.sh)
#   PYTHAGO_CI_CACHE        cache root (default: ~/.cache/pythago-ci)
#   PYTHAGO_MODE            pythago-dist -mode for test images: shared (default) or lto
#   PYTHAGO_DIST_ARGS       further pythago-dist arguments for test images (e.g. -static-tls)
#   PYTHAGO_OTHER_ARCH      the other architecture: run (under qemu-user), build (image and
#                           binaries only) or none (default: run when qemu-user can, else none)
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CACHE=${PYTHAGO_CI_CACHE:-$HOME/.cache/pythago-ci}
export GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0
HOST_ARCH=$(go env GOARCH)
MODE=${PYTHAGO_MODE:-shared}
# The test image lives outside the published tree, in .work/testimage: its own module
# (path github.com/moriyoshi/pythago/internal/testimage, which the tests import), joined
# to this one by .work/go.work. gen_image writes both.
WORK_DIR=$ROOT/.work
IMAGE_DIR=$WORK_DIR/testimage
if [ -f "$WORK_DIR/go.work" ]; then export GOWORK=$WORK_DIR/go.work; else export GOWORK=off; fi
mkdir -p "$CACHE/bin"
(cd "$ROOT" && go build -o "$CACHE/bin/pythago-dist" ./cmd/pythago-dist)
# The toolchain pythago-dist resolves (PYTHAGO_LLVM / PYTHAGO_BAO, else fetched).
eval "$("$CACHE/bin/pythago-dist" -print-toolchain | sed 's/^llvm=/export PYTHAGO_LLVM=/; s/^bao=/export PYTHAGO_BAO=/')"

log() { printf '== %s\n' "$*" >&2; }

# other_arch prints the cross architecture tested under qemu.
other_arch() { [ "$HOST_ARCH" = arm64 ] && echo amd64 || echo arm64; }

# python_args VARIANT prints pythago-dist's version flags: gil is CPython 3.13, ft 3.14t.
python_args() {
  case $1 in
    gil) echo "-python 3.13" ;;
    ft) echo "-python 3.14 -variant freethreaded" ;;
    *) echo "unknown variant $1" >&2; return 1 ;;
  esac
}

# test_wheels VARIANT GOOS ARCH fetches the wheels in ci/test-wheels.txt for the variant
# and target (internal/tools/fetchwheels; PyPI's published sha256 checked, downloads
# cached in $CACHE/wheels/cache) into $CACHE/wheels/VARIANT-GOOS-ARCH and prints that
# directory: the PYTHAGO_TEST_WHEELS of aio's and asgi's tests.
test_wheels() {
  local dir=$CACHE/wheels/$1-$2-$3
  [ -x "$CACHE/bin/fetchwheels" ] && [ -z "$(find "$ROOT/internal/tools/fetchwheels" -newer "$CACHE/bin/fetchwheels")" ] ||
    (cd "$ROOT" && GOWORK=off go build -o "$CACHE/bin/fetchwheels" ./internal/tools/fetchwheels)
  # shellcheck disable=SC2046
  "$CACHE/bin/fetchwheels" $(python_args "$1") -target "$2/$3" -cache "$CACHE/wheels/cache" -o "$dir" -r "$ROOT/ci/test-wheels.txt" >&2
  echo "$dir"
}

# sysroot ARCH prints the glibc sysroot for a cross target ("" for the host).
sysroot() {
  local arch=$1 var
  [ "$arch" = "$HOST_ARCH" ] && return
  var=PYTHAGO_SYSROOT_$(echo "$arch" | tr a-z A-Z)
  if [ -n "${!var:-}" ]; then echo "${!var}"; return; fi
  [ -d "$CACHE/sysroot-$arch/usr" ] || "$ROOT/ci/sysroot.sh" "$arch" "$CACHE/sysroot-$arch" >&2
  echo "$CACHE/sysroot-$arch"
}

# gen_image VARIANT ARCH... generates the test image (.work/testimage) for the variant and
# targets, and the workspace that lets this module import it.
gen_image() {
  local variant=$1; shift
  local dist="$CACHE/bin/pythago-dist" arch sr
  rm -rf "$IMAGE_DIR" "$WORK_DIR/go.work" "$WORK_DIR/go.work.sum"
  export GOWORK=off
  for arch in "$@"; do
    log "pythago-dist $variant linux/$arch ($MODE)"
    sr=$(sysroot "$arch")
    # shellcheck disable=SC2046
    "$dist" -mode "$MODE" ${PYTHAGO_DIST_ARGS:-} $(python_args "$variant") -target "linux/$arch" ${sr:+-sysroot "$sr"} -o "$IMAGE_DIR"
  done
  write_workspace
}

# write_workspace makes .work/testimage a module and joins it to this one (.work/go.work).
write_workspace() {
  local purego
  purego=$(cd "$ROOT" && GOWORK=off go list -m -f '{{.Version}}' github.com/ebitengine/purego)
  # No requirement on this module: the workspace provides it (one at v0.0.0 is looked up).
  printf '%s\n' \
    "module github.com/moriyoshi/pythago/internal/testimage" "" "go 1.26.0" "" \
    "require github.com/ebitengine/purego $purego" >"$IMAGE_DIR/go.mod"
  printf '%s\n' "go 1.26.0" "" "use (" "	.." "	./testimage" ")" >"$WORK_DIR/go.work"
  export GOWORK=$WORK_DIR/go.work
}

# run_arch ARCH CMD... runs a binary built for ARCH: natively, or under qemu-user with the
# target sysroot and the /proc/self/maps shim (qemu-user 8.2 crashes emulating that file).
run_arch() {
  local arch=$1; shift
  if [ "$arch" = "$HOST_ARCH" ]; then "$@"; return; fi
  local sr qarch
  sr=$(sysroot "$arch")
  qarch=$([ "$arch" = amd64 ] && echo x86_64 || echo aarch64)
  if [ ! -f "$sr/nomaps.so" ] || [ "$ROOT/ci/qemu/nomaps.c" -nt "$sr/nomaps.so" ]; then
    nomaps_shim "$arch" "$sr/nomaps.so"
  fi
  QEMU_LD_PREFIX="$sr" QEMU_SET_ENV=LD_PRELOAD=/nomaps.so "$@"
}

# nomaps_shim ARCH OUT [LIBDIR] builds the /proc/self/maps shim for ARCH into OUT, linked
# against the glibc libraries in LIBDIR (default: the ARCH sysroot's), so that it loads on
# that glibc: an old release's libraries bind the old versions and libraries (dlsym in
# libdl.so.2, pthread_* in libpthread.so.0).
nomaps_shim() {
  local arch=$1 out=$2 libdir=${3:-} sr qarch libs=()
  sr=$(sysroot "$arch")
  [ -n "$sr" ] || sr=/
  qarch=$([ "$arch" = amd64 ] && echo x86_64 || echo aarch64)
  # Versioned like glibc's own definitions, so that versioned lookups (bao binds a shared
  # libpython's imports with dlvsym) find the shim first, as unversioned ones do.
  printf '%s { global: open; open64; openat; fopen; fopen64; pthread_getattr_np; local: *; };\n' \
    "$([ "$arch" = amd64 ] && echo GLIBC_2.2.5 || echo GLIBC_2.17)" > "$out.map"
  if [ -n "$libdir" ]; then
    libs=(-nostdlib "$libdir/libc.so.6" "$libdir/libdl.so.2" "$libdir/libpthread.so.0")
  else
    libs=(-B"$(echo "$sr"/usr/lib/gcc/*-linux-gnu/*)" -L"$(echo "$sr"/usr/lib/gcc/*-linux-gnu/*)")
  fi
  "$PYTHAGO_LLVM/clang" --target="$qarch-linux-gnu" --sysroot="$sr" -fuse-ld=lld -shared -fPIC -O1 \
    -Wl,--version-script="$out.map" -o "$out" "$ROOT/ci/qemu/nomaps.c" "${libs[@]}"
}

# have_qemu ARCH reports whether binaries for ARCH can run here.
have_qemu() {
  [ "$1" = "$HOST_ARCH" ] && return 0
  local f=/proc/sys/fs/binfmt_misc/qemu-$([ "$1" = amd64 ] && echo x86_64 || echo aarch64)
  [ -e "$f" ] && grep -q enabled "$f"
}

# other_arch_mode prints what the tiers do with the other architecture, from
# PYTHAGO_OTHER_ARCH: run (generate its image, build its binaries and run them under
# qemu-user), build (generate and build, run nothing: CI, whose native runners cover both
# architectures) or none. Unset: run when qemu-user can, else none.
other_arch_mode() {
  local m=${PYTHAGO_OTHER_ARCH:-}
  if [ -z "$m" ]; then
    have_qemu "$(other_arch)" && m=run || m=none
  fi
  case $m in
    run) have_qemu "$(other_arch)" || { echo "PYTHAGO_OTHER_ARCH=run, but qemu-user cannot run $(other_arch) here" >&2; return 1; } ;;
    build|none) ;;
    *) echo "PYTHAGO_OTHER_ARCH must be run, build or none, not $m" >&2; return 1 ;;
  esac
  echo "$m"
}

# runs_arch ARCH reports whether the tiers run binaries for ARCH (MODE: other_arch_mode).
runs_arch() { [ "$1" = "$HOST_ARCH" ] || [ "$OTHER_ARCH_MODE" = run ]; }

# run_clean ARCH CMD... runs CMD with an empty environment (except qemu's own settings).
run_clean() {
  local arch=$1; shift
  if [ "$arch" = "$HOST_ARCH" ]; then env -i "$@"; return; fi
  local sr
  sr=$(sysroot "$arch")
  run_arch "$arch" true # builds the shim if needed
  env -i QEMU_LD_PREFIX="$sr" QEMU_SET_ENV=LD_PRELOAD=/nomaps.so "$@"
}
