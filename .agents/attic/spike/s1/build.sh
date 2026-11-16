#!/usr/bin/env bash
# Spike S1: turn a python-build-standalone "full" archive into a bao package.
#
#   build.sh <extracted-pbs-python-dir> <llvm-bin-dir> <bao> <work-dir> <out-pkg-dir>
#
# GOARCH (default: the host's) selects the target; cross builds need SYSROOT, a glibc sysroot
# for that target (headers, libc.so.6, libgcc.a), used by clang and by bao.
# SKIP_LTO=1 reuses <work-dir>/libpython.o from a previous run. NO_VT=1 leaves virtual threads
# (glue/pythago_vt*.c) and their wrappers out; package py needs only the thread hooks. BAO_FLAGS adds options to
# `bao build`. Requires bao with -linux-max-text-alignment (bao PR #7): libffi's AArch64 sysv.o
# aligns .text to 16 KiB, which the Go linker cannot honor without breaking method offsets.
#
# The pgo+lto full archives ship libpython members as LLVM bitcode, so they are
# LTO-lowered into one native relocatable object with ld.lld -r before bao sees them.
set -euo pipefail

PBS=$(realpath "$1")
LLVM=$(realpath "$2")
BAO=$(realpath "$3")
WORK=$(realpath -m "$4")
OUT=$(realpath -m "$5")
SPIKE=$(realpath "$(dirname "$0")/..")

# PYVER is e.g. 3.13, or 3.13t for free-threaded builds (whose PYTHON.json static_lib path
# omits the ABI tag).
GOARCH=${GOARCH:-$(go env GOARCH)}
case $GOARCH in
  arm64) CTRIPLE=aarch64-unknown-linux-gnu ;;
  amd64) CTRIPLE=x86_64-unknown-linux-gnu ;;
  *) echo "unsupported GOARCH $GOARCH" >&2; exit 1 ;;
esac
CFLAGS_TARGET=(--target="$CTRIPLE")
BAO_SYSROOT=/
if [ -n "${SYSROOT:-}" ]; then
  SYSROOT=$(realpath "$SYSROOT")
  CFLAGS_TARGET+=(--sysroot="$SYSROOT")
  BAO_SYSROOT=$SYSROOT
fi

PYVER=$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["python_major_minor_version"] + d.get("python_abi_tag", ""))' "$PBS/PYTHON.json")
CONFIG_DIR=$(echo "$PBS"/install/lib/python$PYVER/config-$PYVER-*)
INCLUDE=$PBS/install/include/python$PYVER

# Modules left out of the inittab: interactive terminal, GUI/dbm, and test-only modules.
DROP_MODULES="readline _curses _curses_panel xxsubtype _xxtestfuzz _testinternalcapi"
# Archive members that belong to dropped modules or to the python executable.
DROP_MEMBERS="python.o config.o readline.o _cursesmodule.o _curses_panel.o xxsubtype.o
  _xxtestfuzz.o fuzzer.o _testinternalcapi.o test_lock.o test_critical_sections.o
  _tkinter.o tkappinit.o _dbmmodule.o"
# Static dependency archives still referenced after the drops.
DEPS="ssl crypto ffi sqlite3 z bz2 lzma mpdec expat uuid"
# 3.14+: _zstd.
[ -f "$PBS/build/lib/libzstd.a" ] && DEPS="$DEPS zstd"

# defines OBJ SYM: whether OBJ (ELF or bitcode) defines SYM. grep reads all input so
# llvm-nm never hits SIGPIPE under pipefail.
defines() {
  "$LLVM/llvm-nm" --defined-only --format=just-symbols "$1" | grep -x -- "$2" >/dev/null
}

if [ "${SKIP_LTO:-}" = 1 ] && [ -f "$WORK/libpython.o" ]; then
  echo "reusing $WORK/libpython.o"
else
rm -rf "$WORK"
mkdir -p "$WORK/members"
cd "$WORK/members"
# Member names are not unique (Python/pytime.o vs Modules/_testinternalcapi/pytime.o),
# so extract each member under an index prefix instead of using `ar x`.
python3 -I - "$CONFIG_DIR/libpython$PYVER.a" "$WORK/members" $DROP_MEMBERS <<'EOF'
import os, sys
path, dst, *drop = sys.argv[1:]
data = open(path, "rb").read()
assert data[:8] == b"!<arch>\n"
off, idx, names = 8, 0, b""
while off < len(data):
    hdr = data[off:off + 60]
    name, size = hdr[:16].decode().strip(), int(hdr[48:58])
    body = data[off + 60:off + 60 + size]
    off += 60 + size + (size & 1)
    if name == "//":
        names = body
        continue
    if name.startswith("/") and name[1:].isdigit():
        start = int(name[1:])
        name = names[start:names.index(b"/\n", start)].decode()
    else:
        name = name.rstrip("/")
    if not name or name in drop:
        continue
    open(os.path.join(dst, "%03d_%s" % (idx, name)), "wb").write(body)
    idx += 1
EOF
# Keep only the core pytime.o (the one defining PyTime_Time).
for m in *_pytime.o; do
  defines "$m" PyTime_Time || rm "$m"
done

# libpython.a omits some objects that PYTHON.json lists for built-in extensions
# (3.13: Modules/_hacl/Hacl_Hash_SHA2.o). Add any _hacl object not already present.
for o in "$PBS"/build/Modules/_hacl/*.o; do
  n=$(basename "$o")
  compgen -G "./*_$n" >/dev/null || cp "$o" "./extra_$n"
done

# Regenerate the inittab without the dropped modules.
python3 -I - "$CONFIG_DIR/config.c" "$WORK/config.c" $DROP_MODULES <<'EOF'
import re, sys
src, dst, *drop = sys.argv[1:]
out = []
for line in open(src):
    m = re.match(r'\s*extern PyObject\* PyInit_(\w+)\(void\);', line) or \
        re.match(r'\s*\{"(\w+)", PyInit_\w+\},', line)
    if m and m.group(1) in drop:
        continue
    out.append(line)
open(dst, "w").write("".join(out))
EOF
"$LLVM/clang" "${CFLAGS_TARGET[@]}" -c -O2 -fPIC -ftls-model=global-dynamic -I"$INCLUDE" -DPy_BUILD_CORE \
  -o "$WORK/config.o" "$WORK/config.c"

# LTO-lower all bitcode members (plus the new inittab) into one native object.
"$LLVM/ld.lld" -r --lto-O2 -o "$WORK/libpython.o" "$WORK/config.o" "$WORK"/members/*.o
fi

# Route libpython's thread creation through the glue (__wrap_pthread_*) so Go can own Python
# threads. Only libpython is wrapped; threads started by deps (sqlite) stay plain pthreads.
WRAP=()
# Virtual threads (pythago_vt.c) also wrap the blocking calls they suspend in, and the
# thread identity calls that must follow the VT.
# pythago_vtio.c makes semaphores (PyMutex parking) and sockets yield too.
WRAPPED="pthread_create pthread_join pthread_detach pthread_exit"
GLUE_FLAGS=()
if [ "${NO_VT:-}" = 1 ]; then
  GLUE_FLAGS+=(-DPYTHAGO_NO_VT)
else
  WRAPPED="$WRAPPED clock_nanosleep pthread_self pthread_getattr_np
    sem_init sem_destroy sem_post sem_wait sem_timedwait sem_clockwait sem_trywait sem_getvalue
    recv send accept accept4 connect poll pause
    pthread_setname_np pthread_getname_np pthread_getcpuclockid pthread_kill"
fi
for f in $WRAPPED; do WRAP+=(--wrap="$f"); done
"$LLVM/ld.lld" -r "${WRAP[@]}" -o "$WORK/libpython-wrapped.o" "$WORK/libpython.o"

# pythago_symtab: {name, address} of every default-visibility Py*/_Py* function and data
# object, NULL-terminated. Go reads all C API addresses through this one exported symbol.
"$LLVM/llvm-readelf" -sW "$WORK/libpython-wrapped.o" |
  awk '($5=="GLOBAL" || $5=="WEAK") && $6=="DEFAULT" && $7!="UND" && ($4=="FUNC" || $4=="OBJECT") && $8 ~ /^_?Py/ {print $8}' |
  sort -u > "$WORK/symtab.txt"
# C glue (PyStatus/PyConfig wrappers, atexit, thread hooks, virtual threads); its pythago_*
# functions join the symtab.
GLUE_OBJS=()
for c in "$SPIKE"/glue/*.c; do
  o="$WORK/$(basename "${c%.c}").o"
  "$LLVM/clang" "${CFLAGS_TARGET[@]}" -c -O2 -fPIC "${GLUE_FLAGS[@]}" -I"$INCLUDE" -I"$INCLUDE/internal" -o "$o" "$c"
  GLUE_OBJS+=("$o")
  "$LLVM/llvm-readelf" -sW "$o" |
    awk '$5=="GLOBAL" && $7!="UND" && $4=="FUNC" && $8 ~ /^pythago_/ {print $8}' >> "$WORK/symtab.txt"
done
python3 -I - "$WORK/symtab.txt" "$WORK/symtab.c" <<'PYEOF'
import sys
names = open(sys.argv[1]).read().split()
with open(sys.argv[2], "w") as f:
    f.write("/* Generated by build.sh. */\n")
    for n in names:
        f.write("extern char %s[];\n" % n)
    f.write("const struct { const char *name; void *addr; } pythago_symtab[] = {\n")
    for n in names:
        f.write('  {"%s", %s},\n' % (n, n))
    f.write("  {0, 0},\n};\n")
PYEOF
"$LLVM/clang" "${CFLAGS_TARGET[@]}" -c -O2 -fPIC -w -o "$WORK/symtab.o" "$WORK/symtab.c"
echo "pythago_symtab: $(wc -l < "$WORK/symtab.txt") symbols"
rm -f "$WORK/libpython.a"
"$LLVM/llvm-ar" rcs "$WORK/libpython.a" "$WORK/libpython-wrapped.o" "${GLUE_OBJS[@]}" "$WORK/symtab.o"

# Report TLS relocations and unresolved references so bao rejections are easy to triage.
"$LLVM/llvm-readelf" -rW "$WORK/libpython-wrapped.o" | grep -oE 'R_AARCH64_TLS[A-Z0-9_]+|R_X86_64_(TPOFF|GOTTPOFF|TLSGD|TLSLD|DTPOFF)[A-Z0-9_]*' | sort | uniq -c || true

LINK_ARGS=()
for d in $DEPS; do
  LINK_ARGS+=(-link-archive "$PBS/build/lib/lib$d.a")
done
# Compiler support routines: outline-atomics helpers (__aarch64_cas*, __aarch64_ldadd*) from
# compiler-rt on arm64; libgcc from the sysroot on amd64 (__cpu_model, 128-bit division).
if [ "$GOARCH" = arm64 ]; then
  LINK_ARGS+=(-link-archive "$(echo "$LLVM"/../lib/clang/*/lib/aarch64-*linux-gnu/libclang_rt.builtins.a)")
else
  LINK_ARGS+=(-link-archive "$(echo "${SYSROOT:-}"/usr/lib/gcc/x86_64-linux-gnu/*/libgcc.a)")
fi

# bao runs <triple>-ld for its partial link; ld.lld does the job when binutils for the target
# are not installed.
if [ "$GOARCH" = amd64 ] && ! command -v x86_64-linux-gnu-ld >/dev/null; then
  mkdir -p "$WORK/bin"
  printf '#!/bin/sh\nexec "%s" "$@"\n' "$LLVM/ld.lld" > "$WORK/bin/x86_64-linux-gnu-ld"
  chmod +x "$WORK/bin/x86_64-linux-gnu-ld"
  export PATH="$WORK/bin:$PATH"
fi

GOTOOLCHAIN=go1.26.11 "$BAO" build -target linux/"$GOARCH" -sysroot "$BAO_SYSROOT" \
  -package pyembed -symbol pythago_symtab -linux-max-text-alignment 4096 ${BAO_FLAGS:-} \
  "${LINK_ARGS[@]}" -o "$OUT" "$WORK/libpython.a"

# Workaround for a bao gap (reported): import bindings carry no symbol versions, so ld.so binds
# them to the oldest definition. On x86_64 that is e.g. pthread_cond_init@GLIBC_2.2.5, the
# old ABI, which rejects CPython's CLOCK_MONOTONIC condattr. Pin every import that has
# several versions in its sysroot library to the newest GLIBC version not above the
# archive's floor (PYTHON.json glibc-max-symbol-version), as the PBS binaries bind. Imports
# with no version at or below the floor stay unversioned, so the floor does not rise.
GLIBC_FLOOR=$(python3 -I -c 'import json,sys; print(next((f.split(":")[1] for f in json.load(open(sys.argv[1])).get("crt_features", []) if f.startswith("glibc-max-symbol-version:")), "2.17"))' "$PBS/PYTHON.json")
VERSIONS="$WORK/versions"
rm -rf "$VERSIONS"
mkdir -p "$VERSIONS"
for lib in libc.so.6 libm.so.6 libgcc_s.so.1; do
  f=$(find "$BAO_SYSROOT"/lib* "$BAO_SYSROOT"/usr/lib* -name "$lib" 2>/dev/null | head -1)
  [ -n "$f" ] && "$LLVM/llvm-readelf" --dyn-syms -W "$f" | awk '$7!="UND" && $8 ~ /@/ {print $8}' > "$VERSIONS/$lib.txt"
done
for g in "$OUT"/symbols_linux_*.go; do
python3 -I - "$g" "$VERSIONS" "$GLIBC_FLOOR" <<'PYEOF'
import collections, os, re, sys
path, vdir, floor = sys.argv[1:]
def glibc(v):
    return tuple(int(x) for x in v[len("GLIBC_"):].split(".")) if v.startswith("GLIBC_") else None
floor = tuple(int(x) for x in floor.split("."))
vers = {}
for lib in os.listdir(vdir):
    names = collections.defaultdict(list)
    for line in open(os.path.join(vdir, lib)):
        n, _, v = line.strip().partition("@")
        names[n].append(v)
    vers[lib[:-len(".txt")]] = names
out, n = [], 0
for line in open(path):
    m = re.match(r'(//go:cgo_import_dynamic \S+ )(\S+)( "(\S+)")$', line.rstrip("\n"))
    if m and "#" not in m.group(2):
        vs = [v.lstrip("@") for v in vers.get(m.group(4), {}).get(m.group(2), [])]
        old = [v for v in vs if glibc(v) is not None and glibc(v) <= floor]
        if len(vs) > 1 and old:
            line = "%s%s#%s%s\n" % (m.group(1), m.group(2), max(old, key=glibc), m.group(3))
            n += 1
    out.append(line)
open(path, "w").write("".join(out))
print("%s: pinned %d imports to symbol versions <= GLIBC_%s" % (os.path.basename(path), n, ".".join(map(str, floor))))
PYEOF
done
