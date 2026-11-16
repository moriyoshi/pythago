#!/usr/bin/env bash
# Glue reproducibility: rebuilds the Linux glue objects that the shared-object mode links
# (glue/prebuilt, from `pythago-dist -prebuild-glue`) with the pinned LLVM, against the
# ci/sysroot.sh sysroot of each architecture, and compares them with glue/prebuilt/SHA256SUMS.
# With Wine's SDK (PYTHAGO_WINDOWS_SDK, default ~/.cache/pythago-ci/windows-sdk when it
# exists: ci/windows-wine.sh sdk) the Windows glue DLLs too. The darwin objects are checked
# on a Mac: ci/darwin.sh glue.
#
#   ci/glue.sh
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
PYTHAGO_SYSROOT_ARM64=${PYTHAGO_SYSROOT_ARM64:-$(HOST_ARCH=none sysroot arm64)}
PYTHAGO_SYSROOT_AMD64=${PYTHAGO_SYSROOT_AMD64:-$(HOST_ARCH=none sysroot amd64)}
export PYTHAGO_SYSROOT_ARM64 PYTHAGO_SYSROOT_AMD64
# The Windows glue DLLs too, given Wine's SDK (ci/windows-wine.sh sdk).
oses=linux
if [ -z "${PYTHAGO_WINDOWS_SDK:-}" ] && [ -d "$CACHE/windows-sdk" ]; then
  export PYTHAGO_WINDOWS_SDK=$CACHE/windows-sdk
fi
[ -n "${PYTHAGO_WINDOWS_SDK:-}" ] && oses="linux|windows"
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
cp glue/prebuilt/SHA256SUMS "$out/" # the lines of OSes this prebuild does not build
"$CACHE/bin/pythago-dist" -prebuild-glue "$out"
if ! diff -u glue/prebuilt/SHA256SUMS "$out/SHA256SUMS"; then
  echo "glue/prebuilt differs from a rebuild: run pythago-dist -prebuild-glue glue/prebuilt (with both" >&2
  echo "PYTHAGO_SYSROOT_<ARCH> from ci/sysroot.sh) and commit the result" >&2
  exit 1
fi
# The rebuilt objects themselves, so that a prebuild that built nothing cannot pass.
grep -E -- "-($oses)_" glue/prebuilt/SHA256SUMS | (cd "$out" && sha256sum -c --quiet -)
(cd glue/prebuilt && sha256sum -c --quiet SHA256SUMS)
log "glue objects reproduce glue/prebuilt/SHA256SUMS"
