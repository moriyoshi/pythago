#!/bin/bash
# ci/darwin-host.sh on a GitHub Actions macOS runner (the darwin jobs of
# .github/workflows/ci.yml), from the checkout: no ssh, no ~/pythago-work layout. Go comes
# from actions/setup-go (1.26.11, the GOTOOLCHAIN darwin-host.sh sets), pythago-dist's
# downloads and image work from ~/Library/Caches/pythago-dist (the job caches it), and
# GITHUB_TOKEN authenticates its GitHub API lookups. The built tools and test binaries go
# to $RUNNER_TEMP/pythago-ci (PYTHAGO_DARWIN_CACHE; outside a runner, darwin-host.sh's
# default, ../ci next to the tree).
#
# glue needs the macOS SDK the darwin glue objects are pinned to (darwinGlueSDK in
# cmd/pythago-dist/prebuild.go): when the active developer directory has none, this picks
# the first /Applications/Xcode*.app that does (DEVELOPER_DIR).
#
#   bash ci/darwin-gha.sh gil|ft|glue ARCH
set -euo pipefail
variant=${1:?usage: darwin-gha.sh gil|ft|glue ARCH}
arch=${2:?usage: darwin-gha.sh gil|ft|glue ARCH}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
log() { printf '== %s\n' "$*" >&2; }

if [ -n "${RUNNER_TEMP:-}" ]; then export PYTHAGO_DARWIN_CACHE=${PYTHAGO_DARWIN_CACHE:-$RUNNER_TEMP/pythago-ci}; fi

if [ "$variant" = glue ]; then
  sdk=$(sed -n 's/^const darwinGlueSDK = "\(.*\)"$/\1/p' "$ROOT/cmd/pythago-dist/prebuild.go")
  [ -n "$sdk" ] || { echo "no darwinGlueSDK in cmd/pythago-dist/prebuild.go" >&2; exit 1; }
  if ! xcrun --sdk "macosx$sdk" --show-sdk-path >/dev/null 2>&1; then
    for x in /Applications/Xcode*.app; do
      if DEVELOPER_DIR=$x/Contents/Developer xcrun --sdk "macosx$sdk" --show-sdk-path >/dev/null 2>&1; then
        export DEVELOPER_DIR=$x/Contents/Developer
        break
      fi
    done
  fi
  log "macOS $sdk SDK: $(xcrun --sdk "macosx$sdk" --show-sdk-path 2>/dev/null || echo 'not found (pythago-dist tries the Command Line Tools)')"
fi

log "runner: macOS $(sw_vers -productVersion), $(uname -m), $(sysctl -n hw.ncpu) CPUs, $(($(sysctl -n hw.memsize) / 1048576)) MiB"
log "developer dir $(xcode-select -p)${DEVELOPER_DIR:+ (DEVELOPER_DIR $DEVELOPER_DIR)}, SDK $(xcrun --show-sdk-version)"
log "$(GOTOOLCHAIN=go1.26.11 go version)"
exec bash "$ROOT/ci/darwin-host.sh" "$variant" "$arch"
