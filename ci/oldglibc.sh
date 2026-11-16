#!/usr/bin/env bash
# Unpack an old Debian release's glibc (libc6, libgcc1) for running binaries through its own
# loader, to check that they start on that glibc:
#
#   ci/oldglibc.sh jessie|buster amd64|arm64 DIR
#
# jessie has glibc 2.19 (the nearest archived release to pythago's 2.18 floor), buster 2.28
# (the extension shim's floor). Each package is checked against the SHA256 in the archive's
# Packages index (fetched over HTTPS; its Release signature is not checked).
set -euo pipefail
release=$1 arch=$2 dir=$3
mirror=https://archive.debian.org/debian
dl=$(mktemp -d)
trap 'rm -rf "$dl"' EXIT
curl -fsSL -o "$dl/Packages.gz" "$mirror/dists/$release/main/binary-$arch/Packages.gz"
mkdir -p "$dir"
for pkg in libc6 libgcc1; do
  read -r file sha < <(zcat "$dl/Packages.gz" | awk -v p="$pkg" '
    $1 == "Package:" { cur = ($2 == p) }
    cur && $1 == "Filename:" { f = $2 }
    cur && $1 == "SHA256:" { s = $2 }
    cur && $0 == "" && f != "" { print f, s; exit }')
  [ -n "$file" ] || { echo "no $pkg in $release/$arch" >&2; exit 1; }
  curl -fsSL -o "$dl/$pkg.deb" "$mirror/$file"
  echo "$sha  $dl/$pkg.deb" | sha256sum -c --quiet
  dpkg-deb -x "$dl/$pkg.deb" "$dir"
done
echo "glibc for $release/$arch in $dir" >&2
