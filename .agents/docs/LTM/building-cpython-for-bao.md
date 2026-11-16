# Building CPython for bao

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- The libpython members in PBS glibc/darwin full archives are **LLVM bitcode**.
  - Lowering them needs LLVM ≥ the PBS clang (23.1.x) and `ld.lld -r --lto-O2`.
  - Any symbol-level editing, including `--wrap`, has to happen at that LTO link, because a
    bitcode member's undefined references only exist there.
- Assemble from `PYTHON.json` objs, not from `libpythonX.Y.a`. The `.a` is missing
  `Hacl_Hash_SHA2.o` and has duplicate member names, which makes `ar x` lossy.
- `PYTHON.json` `static_lib` omits the ABI tag `t`; derive the path from `abi_tag`.
- `//go:embed dir` skips `_`-prefixed files. Use `all:dir` for Python packages.
- **High `.text` alignment** (libffi's 16 KiB) made Go's linker misplace runtime.text. Every
  method dispatch through a runtime-built itab then went to the wrong method, silently.
  Always pass `-linux-max-text-alignment 4096`.
- bao refuses to overwrite an existing output dir: `rm -rf` the pyembed dir before rebuilding.
- Pin `GOTOOLCHAIN=go1.26.11`.
- **Translating the shipped shared library beats re-lowering bitcode.** PBS's
  `libpython3.X.so` is itself PGO+LTO-built. Translating it removed the LLVM pipeline, and
  the image came out about a third smaller (stripped input).
- **Symbol wrapping applies to your own glue too.** With bao `-wrap` (as with `ld --wrap`),
  the glue's fallback `pthread_create` call is redirected to `__wrap_pthread_create`
  itself, so it recurses. Fallbacks call `__real_*`.
- **No compiler runtime is linked in shared mode,** so arm64 glue builds with
  `-mno-outline-atomics`.
- **Reproducible objects need a fixed layout:** relative paths, a fixed work directory and
  a symlinked include directory. Then check them byte for byte in CI; the host's glibc
  headers did not change the output.
- **An LD_PRELOAD interposer must define versioned symbols** when the program binds imports
  with `dlvsym(name, version)`, as bao's shared-object binder does. Unversioned
  definitions are skipped.
