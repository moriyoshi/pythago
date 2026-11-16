Native CPython layouts
=====================

These generated definitions cover CPython 3.13.18 with the GIL and 3.14.10 with
or without the GIL, on Linux, darwin and Windows, arm64 and amd64.
darwin shares the Linux definitions: generating from the darwin arm64 PBS headers
gives identical output (3.13.18 and 3.14.10t), since these structs depend only on
the LP64 data layout. Each was derived from the matching
python-build-standalone 20261110 distribution headers. The 3.14.10 GIL offsets,
sizes, and masks match 3.13.18 GIL; they have separate version registrations.
Each definition is selected by exact PY_VERSION_HEX, GIL variant, and target
architecture. Do not assume that different patch releases have identical layouts.

Windows has definitions of its own, generated from the PBS Windows
headers: an MSVC-ABI COFF probe compiled by clang against Wine's C runtime and Windows
headers (ci/windows-wine.sh sdk). They are not interchangeable with the Linux ones: in
3.14t, PyASCIIObject.state starts with an unsigned char and continues with unsigned
int bitfields, which GCC/Itanium packs at byte 49 and MSVC at byte 52 (a new 4-byte
unit). The 3.13 and 3.14 GIL layouts equal Linux's. Windows' pyconfig.h leaves
Py_GIL_DISABLED to the compiler, so 3.14t needs -DPy_GIL_DISABLED=1 in -cflags:

  SDK=~/.cache/pythago-ci/windows-sdk
  GOTOOLCHAIN=go1.26.11 go run ./internal/tools/nativelayout \
    -clang /path/to/clang -include /path/to/windows/python/include \
    -target aarch64-pc-windows-msvc -arch arm64 -build windows \
    -cflags "-nostdinc -isystem $(clang -print-resource-dir)/include -isystem $SDK/msvcrt \
      -isystem $SDK/windows -D_UCRT -D__WINE_PE_BUILD -fms-extensions [-DPy_GIL_DISABLED=1]" \
    -o internal/layout/unicode_cp314t_windows_arm64_generated.go

Generate definitions before using a different Python build. Run from the repo
root with the headers of that build and a matching target libc sysroot:

  GOTOOLCHAIN=go1.26.11 go run ./internal/tools/nativelayout \
    -clang /path/to/clang \
    -include /path/to/python/include/python3.14t \
    -target aarch64-unknown-linux-gnu -arch arm64 \
    -sysroot /path/to/target/sysroot \
    -o internal/layout/unicode_cp314t_arm64_generated.go

For 3.14 GIL, use its python3.14 include directory (without the t suffix) and
unicode_cp314_arm64_generated.go. Use the actual GIL distribution headers;
the variant is detected from pyconfig.h, without overriding build macros.

For amd64, use -target x86_64-unknown-linux-gnu -arch amd64 and the corresponding
headers and sysroot. Use a distinct output filename when retaining definitions
for multiple patch releases. The generator supports CPython 3.12+ header shapes;
only the checked-in builds above have been validated.

The generator compiles a small C probe and reads its ELF (COFF for Windows)
symbols without running target code. offsetof/sizeof supply field offsets and
object sizes. Initialized
bitfield probes supply the actual byte positions and masks for kind, compact,
and ASCII flags; these are not inferred from assumed C bitfield packing. The
generator checks the ELF target architecture, word size, and byte order.

At interpreter startup, py selects one immutable definition and checks it against
ASCII, Latin-1, BMP, non-BMP, and noncompact subclass objects. It also verifies the
Py_buffer layout against the Go declaration. Unknown builds or failed checks
disable native Unicode views with an error; ordinary Python API access remains
available. Python refcounts continue to be changed through the Python API.

There is no per-object metadata cache. py.String retains only its owned
Python reference. Len/Rune/Runes read length, representation flags, and data
pointers directly on each access without entering Python. Runes returns an
independent Go copy. py.String serializes reads and ownership changes with its
Go mutex; operations creating or releasing Python objects still enter Python.
Object.UnicodeLen/UnicodeRune/UnicodeRunes use the same reads for a str after
one type check per Object; other objects and out-of-range indexes use the C API.

Python-backed py.Buffer retains an active Py_buffer export, the canonical buffer
protocol record, and reads that record on every access. It does not duplicate
length, pointer, format, shape, strides, or readonly metadata. An export pins the
exporter's storage until Close. Shape/Strides are borrowed read-only native
slices, just as Bytes is borrowed data: keep the Buffer reachable and open, and
synchronize access with Close or re-export. Native reads do not synchronize
concurrent mutation of the exported data by Python.
