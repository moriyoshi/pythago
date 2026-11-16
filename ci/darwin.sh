#!/usr/bin/env bash
# darwin tier for one variant (gil: CPython 3.13, ft: 3.14t), run from any host: for each
# architecture, sync the tree to a macOS host over ssh and run ci/darwin-host.sh there,
# which generates a darwin test image and runs py's, aio's, asgi's and wsgi's tests natively
# (py's and asgi's again with the fusion on).
# glue checks the darwin glue objects in glue/prebuilt on the arm64 host (LLVM publishes
# macOS builds for arm64 only); glue-update rebuilds them there and copies them back.
# GitHub Actions runs the same tiers on its macOS runners without ssh (ci/darwin-gha.sh).
#
#   PYTHAGO_DARWIN_ARM64_HOST  ssh host for darwin/arm64 (default: dengaku.local)
#   PYTHAGO_DARWIN_AMD64_HOST  ssh host for darwin/amd64 (default: macbook-pro-2.local)
#   PYTHAGO_DARWIN_DIR         directory on the hosts (default: pythago-work, under $HOME)
#   PYTHAGO_DARWIN_BAO         bao executable on the hosts, passed as PYTHAGO_BAO (default:
#                              the bao built into pythago-dist)
#
#   ci/darwin.sh gil|ft [arm64] [amd64]
#   ci/darwin.sh glue|glue-update
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
variant=${1:?usage: ci/darwin.sh gil|ft|glue|glue-update [arm64] [amd64]}
shift
arches=("$@")
case $variant in
  glue|glue-update) arches=(arm64) ;;
  *) [ ${#arches[@]} -gt 0 ] || arches=(arm64 amd64) ;;
esac
dir=${PYTHAGO_DARWIN_DIR:-pythago-work}

for arch in "${arches[@]}"; do
  case $arch in
    arm64) host=${PYTHAGO_DARWIN_ARM64_HOST:-dengaku.local} ;;
    amd64) host=${PYTHAGO_DARWIN_AMD64_HOST:-macbook-pro-2.local} ;;
    *) echo "unknown architecture $arch" >&2; exit 1 ;;
  esac
  printf '== darwin/%s on %s: sync\n' "$arch" "$host" >&2
  # The host keeps its own .work (test image) and caches; the tree is otherwise mirrored.
  rsync -a --delete --exclude .git --exclude /.work --exclude /.agents-workspace \
    --exclude /examples/hello/pydist "$ROOT/" "$host:$dir/src/"
  ssh -o BatchMode=yes "$host" "${PYTHAGO_DARWIN_BAO:+PYTHAGO_BAO=$PYTHAGO_DARWIN_BAO }bash $dir/src/ci/darwin-host.sh $variant $arch"
  if [ "$variant" = glue-update ]; then
    rsync -a --include '*-darwin_*.o' --include SHA256SUMS --exclude '*' "$host:$dir/ci/glue/" "$ROOT/glue/prebuilt/"
    printf '== glue/prebuilt: darwin objects and SHA256SUMS updated from %s\n' "$host" >&2
    exit 0
  fi
done
printf '== darwin tier (%s) passed: %s\n' "$variant" "${arches[*]}" >&2
