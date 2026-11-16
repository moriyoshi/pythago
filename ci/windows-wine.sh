#!/usr/bin/env bash
# Windows tier under Wine, on an arm64 Linux host with docker: windows/arm64 only (amd64
# would need x86-64 emulation). A local aid, not native Windows: it runs the Windows build
# of pythago (pythago-dist's windows target, py's Windows files) where no Windows machine
# is at hand.
#
#   ci/windows-wine.sh image     build the Wine image (ci/windows-wine.Dockerfile)
#   ci/windows-wine.sh sdk       copy its headers and import libraries to $CACHE/windows-sdk:
#                                the -windows-sdk for compiling the glue DLL
#                                (pythago-dist -prebuild-glue, ci/glue.sh)
#   ci/windows-wine.sh gil|ft    build the windows/arm64 test binaries in $CACHE/windows-<v>
#                                (ci/windows-build.sh; not .work, so the Linux tiers keep
#                                theirs) and run the py, aio, asgi (inline and loop thread),
#                                wsgi and workers tests under Wine, and py's and asgi's
#                                again with the fusion on (PYTHAGO_TEST_FUSION=1); aio's
#                                and asgi's with the binary wheels in run/wheels
#                                (PYTHAGO_TEST_WHEELS, relative to the working directory)
. "$(dirname "$0")/lib.sh"
IMAGE=pythago-wine:11.21-1 # with the x86_64 import libraries
SDK=$CACHE/windows-sdk
cmd=${1:?usage: ci/windows-wine.sh image|sdk|gil|ft}

image() {
  docker image inspect "$IMAGE" >/dev/null 2>&1 && return
  log "docker build $IMAGE"
  docker build -t "$IMAGE" -f "$ROOT/ci/windows-wine.Dockerfile" "$ROOT/ci"
}

sdk() {
  image
  local c
  rm -rf "$SDK" && mkdir -p "$SDK/lib/arm64" "$SDK/lib/amd64"
  c=$(docker create "$IMAGE")
  docker cp "$c:/opt/wine/include/wine/msvcrt" "$SDK/msvcrt"
  docker cp "$c:/opt/wine/include/wine/windows" "$SDK/windows"
  for l in kernel32 kernelbase ucrtbase vcruntime140; do
    docker cp "$c:/opt/wine/lib/wine/aarch64-windows/lib$l.a" "$SDK/lib/arm64/"
    docker cp "$c:/opt/wine/lib/wine/x86_64-windows/lib$l.a" "$SDK/lib/amd64/"
  done
  docker rm "$c" >/dev/null
  log "Windows SDK (Wine's) in $SDK"
}

case $cmd in
  image) image ;;
  sdk) sdk ;;
  gil|ft)
    image
    dir=$CACHE/windows-$cmd
    "$ROOT/ci/windows-build.sh" "$cmd" arm64 "$dir" # with the prebuilt glue, as users get it
    # :1 runs asgi on the loop thread; +f with the fusion on (py.Config.FusedBytes).
    for run in py aio asgi asgi:1 wsgi workers py+f asgi+f asgi:1+f; do
      fusion=0
      case $run in *+f) fusion=1 run=${run%+f} ;; esac
      pkg=${run%%:*} loop=0
      [ "$run" = asgi:1 ] && loop=1
      log "$pkg tests$([ $loop = 1 ] && echo ', loop thread (Options.LoopThread)')$([ $fusion = 1 ] && echo ', fusion on'), $cmd windows/arm64 under Wine"
      docker run --rm -v "$dir/run:/w" -w /w -e PYTHAGO_ASGI_LOOP_THREAD=$loop -e PYTHAGO_TEST_FUSION=$fusion -e PYTHAGO_TEST_WHEELS=wheels "$IMAGE" wine "$pkg.test.exe" -test.timeout 15m
    done
    log "windows tier ($cmd) passed: arm64 under Wine"
    ;;
  *) echo "usage: ci/windows-wine.sh image|sdk|gil|ft" >&2; exit 2 ;;
esac
