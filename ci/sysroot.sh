#!/usr/bin/env bash
# Assemble a glibc sysroot for a cross target from Ubuntu noble packages:
#
#   ci/sysroot.sh amd64|arm64 DIR
#
# Each package is checked against the SHA256 in the archive's Packages index (the index
# itself is fetched over HTTPS from the Ubuntu mirror; its Release signature is not
# checked). Contents: libc6 (+dev), linux-libc-dev, libgcc (+dev), libstdc++6, zlib1g:
# enough for bao, clang, and running test binaries and wheels under qemu-user.
set -euo pipefail
arch=$1 dir=$2
case $arch in
  amd64) mirror=http://archive.ubuntu.com/ubuntu ;;
  arm64) mirror=http://ports.ubuntu.com/ubuntu-ports ;;
  *) echo "unsupported arch $arch" >&2; exit 2 ;;
esac
dl=$(mktemp -d)
trap 'rm -rf "$dl"' EXIT
for suite in noble noble-updates; do
  curl -fsSL -o "$dl/Packages-$suite.gz" "$mirror/dists/$suite/main/binary-$arch/Packages.gz"
done
python3 -I - "$dl" > "$dl/want" <<'PY'
import gzip, os, sys
want = {"libc6", "libc6-dev", "linux-libc-dev", "libgcc-s1", "libgcc-13-dev", "libcrypt1",
        "libcrypt-dev", "libstdc++6", "zlib1g"}
best = {}
for suite in ("noble", "noble-updates"):  # updates win
    for stanza in gzip.open(os.path.join(sys.argv[1], "Packages-%s.gz" % suite), "rt").read().split("\n\n"):
        f = dict(l.split(": ", 1) for l in stanza.splitlines() if ": " in l and not l.startswith(" "))
        if f.get("Package") in want:
            best[f["Package"]] = (f["Filename"], f["SHA256"])
missing = want - best.keys()
if missing:
    sys.exit("missing packages: %s" % sorted(missing))
for name, (path, sha) in sorted(best.items()):
    print(path, sha)
PY
mkdir -p "$dir"
while read -r path sha; do
  f="$dl/$(basename "$path")"
  curl -fsSL -o "$f" "$mirror/$path"
  echo "$sha  $f" | sha256sum -c --quiet
  dpkg-deb -x "$f" "$dir"
done < "$dl/want"
# Merged-/usr layout links and the ELF interpreter path qemu-user looks up.
ln -sfn usr/lib "$dir/lib"
mkdir -p "$dir/usr/lib64"
ln -sfn usr/lib64 "$dir/lib64"
case $arch in
  amd64) ln -sfn ../lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 "$dir/usr/lib64/ld-linux-x86-64.so.2" ;;
  arm64) ln -sfn aarch64-linux-gnu/ld-linux-aarch64.so.1 "$dir/usr/lib/ld-linux-aarch64.so.1" ;;
esac
echo "sysroot for $arch in $dir"
