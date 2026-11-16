#!/usr/bin/env bash
# glibc floor tier, on the current test image, .work/testimage (run after ci/unit.sh): for
# each target, a program built on it (internal/tools/pyrun)
#   - may need no glibc symbol version newer than the floor recorded by pythago-dist, and
#     the only imports bao left above the cap may be optional (weak) ones;
#   - must start and run the standard library's native modules on old glibc releases
#     (ci/oldglibc.sh: Debian jessie 2.19 and buster 2.28), through their own loader with
#     an empty environment. Version numbers alone miss imports bound to the wrong library,
#     e.g. pthread_* that moved into libc.so.6 in glibc 2.34.
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
OTHER_ARCH_MODE=$(other_arch_mode)
allowed="getentropy sem_clockwait" # weak references in libcrypto and libpython
status=0
for manifest in .work/testimage/pythago-dist_linux_*.json; do
  arch=${manifest##*_}; arch=${arch%.json}
  floor=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["glibc_floor"])' "$manifest")
  above=$(python3 -I -c 'import json,sys; print(" ".join(s.split("@")[0] for s in json.load(open(sys.argv[1])).get("linux_imports_above_version_cap") or []))' ".work/testimage/bao-manifest_linux_$arch.json")
  for s in $above; do
    case " $allowed " in *" $s "*) ;; *) echo "linux/$arch: $s is above the glibc $floor cap"; status=1 ;; esac
  done
  GOARCH=$arch go build -o "$CACHE/pyrun-floor-$arch" ./internal/tools/pyrun
  newest=$("$PYTHAGO_LLVM/llvm-readelf" -V "$CACHE/pyrun-floor-$arch" | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -V | tail -1)
  if [ "$(printf '%s\n%s\n' "$newest" "$floor" | sort -V | tail -1)" != "$floor" ]; then
    echo "linux/$arch: needs GLIBC_$newest, above the $floor floor"; status=1
  fi
  log "linux/$arch: floor $floor, newest needed GLIBC_$newest, above cap: ${above:-none}"

  runs_arch "$arch" || { log "linux/$arch: old glibc runs skipped (PYTHAGO_OTHER_ARCH=$OTHER_ARCH_MODE)"; continue; }
  mode=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("mode", "lto"))' "$manifest")
  if [ "$mode" = lto ]; then
    # bao binds a static archive's weak code references (getentropy, sem_clockwait) through
    # Go's linker, which cannot import them weakly: LTO images need glibc 2.30+.
    log "linux/$arch: LTO image, old glibc runs skipped (needs glibc 2.30+)"
    continue
  fi
  for release in jessie buster; do
    root=$CACHE/glibc-$release-$arch
    [ -d "$root/lib" ] || "$ROOT/ci/oldglibc.sh" "$release" "$arch" "$root"
    ld=$(echo "$root"/lib/*-linux-gnu/ld-2.*.so)
    libs=$(dirname "$ld")
    qemu=()
    if [ "$arch" != "$HOST_ARCH" ]; then # qemu-user needs the /proc/self/maps shim
      [ -f "$root/nomaps.so" ] || nomaps_shim "$arch" "$root/nomaps.so" "$libs"
      qemu=(QEMU_SET_ENV=LD_PRELOAD=/nomaps.so)
    fi
    if out=$(cd "$CACHE" && env -i QEMU_LD_PREFIX="$root" "${qemu[@]}" "$ld" --library-path "$libs" \
      "$CACHE/pyrun-floor-$arch" "$ROOT/ci/floor_check.py" 2>&1 | grep -v '^nomaps:'); then
      log "linux/$arch: runs on $release ($(basename "$ld")): $(echo "$out" | grep '^floor ok')"
    else
      echo "linux/$arch: fails on $release ($(basename "$ld")):"; echo "$out" | head -5; status=1
    fi
  done
done
exit $status
