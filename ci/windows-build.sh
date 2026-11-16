#!/usr/bin/env bash
# Builds the Windows test binaries on Linux: a windows/<arch> test image for the variant in
# DIR/testimage (its own module, joined to this one by DIR/go.work, as .work/testimage is
# for the Linux tiers) and the py, aio, asgi, wsgi and workers test binaries in DIR/run.
# Each binary embeds the image and is its own sys.executable, and DIR/run/wheels holds the
# binary wheels aio's and asgi's tests load (ci/test-wheels.txt, for the variant and
# windows/<arch>; PYTHAGO_TEST_WHEELS), so DIR/run is all a Windows machine needs
# (ci/windows-run.ps1 runs them). The glue DLL is glue/prebuilt's, or with
# PYTHAGO_WINDOWS_SDK compiled.
#
#   ci/windows-build.sh gil|ft arm64|amd64 DIR
. "$(dirname "$0")/lib.sh"
variant=${1:?usage: ci/windows-build.sh gil|ft arm64|amd64 DIR}
arch=${2:?usage: ci/windows-build.sh gil|ft arm64|amd64 DIR}
dir=${3:?usage: ci/windows-build.sh gil|ft arm64|amd64 DIR}
rm -rf "$dir" && mkdir -p "$dir/testimage" "$dir/run"
dir=$(cd "$dir" && pwd)
log "pythago-dist $variant windows/$arch"
# shellcheck disable=SC2046
"$CACHE/bin/pythago-dist" $(python_args "$variant") -target "windows/$arch" \
  ${PYTHAGO_WINDOWS_SDK:+-windows-sdk "$PYTHAGO_WINDOWS_SDK"} -o "$dir/testimage" -package testimage
purego=$(cd "$ROOT" && GOWORK=off go list -m -f '{{.Version}}' github.com/ebitengine/purego)
printf '%s\n' "module github.com/moriyoshi/pythago/internal/testimage" "" "go 1.26.0" "" \
  "require github.com/ebitengine/purego $purego" >"$dir/testimage/go.mod"
printf '%s\n' "go 1.26.0" "" "use (" "	$ROOT" "	./testimage" ")" >"$dir/go.work"
for pkg in py aio asgi wsgi workers; do
  log "$pkg test binary, $variant windows/$arch"
  (cd "$ROOT" && GOWORK=$dir/go.work GOOS=windows GOARCH=$arch go test -c -o "$dir/run/$pkg.test.exe" "./$pkg")
done
log "test wheels, $variant windows/$arch"
mkdir -p "$dir/run/wheels"
cp "$(test_wheels "$variant" windows "$arch")"/* "$dir/run/wheels/"
