# Image Build and Toolchain

## Summary
`cmd/pythago-dist` turns a python-build-standalone (PBS) release into a "dist package": a bao-translated CPython native image plus an embedded stdlib zip, registered with package `py` by importing the package. pythago ships source only. Consumers generate the dist package themselves with `go generate`, and in the default shared-object mode they need nothing but Go: the glue is prebuilt and checked in, bao runs in process, and no LLVM or native linker runs.

## Key Facts
- **No prebuilt native images.** User decision (2026-11-16): no `.syso` dist packages are distributed. Consumers run `pythago-dist`, typically from a `//go:generate` line.
- **Two modes.** `-mode shared` (default) translates PBS's `install_only_stripped` `libpython3.X[t].so.1.0`. `-mode lto` LTO-lowers the bitcode libpython of the PBS "full" archive and needs LLVM.
- **Shared mode needs only Go:** prebuilt glue objects in `glue/prebuilt`, bao as a Go library (`github.com/moriyoshi/bao/build`), and a single relocatable input, so no linker.
- **bao is pinned in `go.mod`: `github.com/moriyoshi/bao v0.0.8`** (`fcb42a6`). `-bao`/`PYTHAGO_BAO` still run an external bao executable.
- **LLVM:** `-llvm`, else `PYTHAGO_LLVM`, else LLVM 23.1.5 fetched and verified into the user cache (`toolchain.LLVMVersion`). Needed only for LTO mode and for compiling glue that has no prebuilt object.
- **The stdlib comes from an `fs.FS`**, served by a C importer (`_pythago_fs`) over an index Go builds at `Init`, so imports also work in forked children. The zip is stored (uncompressed) and read zero-copy from rodata.
- **Default PBS release:** `20261110` (`pbs.DefaultRelease`).
- A 3.13 image is about 23.5 MB of `.syso` (arm64, shared), and `examples/hello` is a 49 MB binary that runs from an empty directory under `env -i` and writes nothing to disk.

## Details

### How the consumer toolchain shrank to Go
1. **No prebuilt `.syso`** (user decision). With glue compiled on the consumer side too, a consumer needed Go, bao, a C compiler and, on the LTO path, LLVM. That raised the value of a path without LLVM and of `pythago-dist` provisioning its own tools.
2. **`pythago-dist` provisioned its toolchain:** LLVM fetched and verified, bao via `go install bao@<rev>`. Consumers needed only Go, but generation still fetched 721 MB of LLVM and ran a 50 s lowering per target.
3. **The user's direction "be free from any LLVM installation"** led to: glue shipped as prebuilt objects that are reproducible and checked by `SHA256SUMS` (rather than compiled per consumer); bao incorporated as a Go library instead of an installed executable ("directly incorporating bao into pythago"); and shared mode, which translates the already-linked `libpython.so` so no LTO lowering or native linker is needed. The `go install …@BaoRevision` path (`toolchain.Bao`) was then removed.
- LTO mode remains selectable (`-mode lto`, and a CI mode axis); it needs LLVM and glibc 2.30+.

### Product layout
- `glue/`: the product glue, embedded in the generator as package `glue`.
  - `pythago_glue.c`: interpreter setup (`pythago_config_new`, UTF-8 preinit) and runtime glue.
  - `pythago_py.c`: the Python-facing C (atfork child handler, `_pythago.restore_signal`, `_pythago_fs`).
  - `pythago_static.c`: LTO mode only. Definitions a static libpython gets from glibc 2.17's `libc_nonshared.a`: `atexit` (via `__cxa_atexit` with bao's `__dso_handle`) and the `stat64`/`fstat64`/`lstat64` wrappers. A shared libpython already has them resolved.
  - `prebuilt/`: `{cp313,cp314t}-{linux,darwin}_{arm64,amd64}.o`, `SHA256SUMS`, `README`. The darwin objects are pinned to the macOS 26.5 SDK (`darwinGlueSDK`), target macOS 11, and are built and checked on an arm64 Mac only (`ci/darwin.sh glue`, `glue-update`): Apple licenses the SDK for Apple hardware, and LLVM publishes macOS builds for arm64 only. `-prebuild-glue` builds the host OS's targets and keeps the other OS's `SHA256SUMS` lines.
- The VT and sthreads hooks are not in the product `__wrap_pthread_*`. The frozen spikes keep their own copy in `.agents/attic/spike/glue` and `.agents/attic/spike/s1/build.sh` (see `.agents/attic/spike/README.md`).
- `internal/ghrelease`: generic GitHub-release assets with digest verification. `internal/pbs` (release metadata, downloads, extraction) is built on it and shared by `pythago-dist` and `env`.
- `internal/toolchain`: LLVM resolution and fetching.

### `pythago-dist` flags

| Flag | Meaning |
|------|---------|
| `-python 3.13` | major.minor |
| `-variant gil\|freethreaded` | free-threaded uses the `freethreaded-*` PBS flavor and ABI tag `t` |
| `-target goos/goarch` | default: the host |
| `-mode shared\|lto` | default `shared` |
| `-o DIR`, `-package NAME` | output package |
| `-sysroot DIR` | glibc sysroot for the target (default `/` for native builds) |
| `-llvm DIR`, `-bao PATH` | toolchain overrides (also `PYTHAGO_LLVM`, `PYTHAGO_BAO`) |
| `-print-toolchain` | resolve (and fetch) the toolchain, print `llvm=DIR` and `bao=PATH` (empty: built in), exit |
| `-prebuild-glue DIR` | maintainers: build every glue object into DIR (`glue/prebuilt`), exit |
| `-static-tls` | experimental: bao `-linux-static-tls`; the package's `TLSStatic()` reports whether it took effect |
| `-pyc-python PATH` | Python used to compile `.pyc` files (`none` skips) |
| `-release`, `-pbs DIR`, `-work`, `-cache`, `-skip-lto` | release tag, pre-extracted PBS, work and download dirs, reuse the lowered object |

### Shared mode (default)
- Input: PBS `install_only_stripped` (or `freethreaded-install_only_stripped`), file `python/lib/libpython3.X[t].so.1.0`.
- bao options: `-linux-shared-object <so>`, `-wrap pthread_{create,join,detach,exit}` (wraps the library's imports to the glue; bao PR #30), `-linux-max-symbol-version GLIBC_2.18`.
- The glue is the only relocatable input, so no partial link and no native linker run.
- The glue object comes from `glue.Prebuilt(glue.Key(pyver, goos, goarch))`. Without a prebuilt object for the target, it is compiled with LLVM.
- `register.go` also calls `py.RegisterExports(SharedObjectExports(), SharedObjectExport)`, which merges the library's dynamic exports into the symtab lookups.
- `.pyc` files are compiled with the target PBS `python` when it runs on this host, else with the host's stock `install_only_stripped` Python of the same version.

### LTO mode
Pipeline (the Go port of the spikes' `build.sh`):
1. Fetch the PBS "full" archive with the release digest (GitHub API, cached), extract.
2. Read `PYTHON.json`. An ar parser with indexed member names (the `.a` has duplicate member names). Keep the core `pytime.o`, add the `_hacl` objects, regenerate the inittab.
3. LTO-lower the bitcode with `ld.lld`, then wrap (`--wrap=pthread_*`). The glue is wrapped too, so both modes have the same semantics.
4. Build the symtab from `debug/elf`; add glue; run bao with `-linux-max-symbol-version GLIBC_<floor>`. The cross combine uses `Options.Linker = ld.lld` (it once needed a `PATH` wrapper script for `x86_64-linux-gnu-ld`).
- The lowered libpython is cached, keyed by the archive, inittab, `PYTHON.json`, `_hacl` objects, drop lists, triple and `clang --version`. `-skip-lto` reuses it regardless.
- Background on bitcode lowering: [building-cpython-for-bao.md](building-cpython-for-bao.md).

### Outputs
- Per target: `register_<os>_<arch>.go`, the bao `.syso`/`.go`/`.s` files, `pythago-dist_<os>_<arch>.json` (manifest: `glibc_floor`, `mode`, ...), `bao-manifest_linux_<arch>.json` (bao's, including `linux_imports_above_version_cap`), and the PBS license notices.
- The stdlib zip, with `.pyc` files from `compileall --invalidation-mode unchecked-hash` by a same-version host Python.
- Several targets share one package directory. bao writes to a scratch directory and only that target's files move in, so targets accumulate per GOARCH.

### Importer (`py/fsimport.go`)
- API: `Config.Stdlib`, `Config.Files`, `RegisterStdlib(fs.FS)`, `RegisterStdlibZip([]byte)`. Mounted under `/pythago-fs`.
- A path hook with a path-entry finder, so CPython's `PathFinder` provides namespace packages and `pkgutil.iter_modules`.
- The loader prefers `__pycache__/<name>.<cache_tag>.pyc` when its magic number matches.
- `importlib.resources` gets a Traversable. A disk stdlib combined with an app `fs.FS` also works.
- `_pythago_fs` is C over an immutable sorted index (path → pointer and size) built by Go at `Init`. It replaced a first version that called Go for stat/read/listdir: in forked children every embedded-stdlib import raised "Go functions are not available in a forked child process" and the test run hung.
- The stdlib zip is *stored*, so index entries point into embedded rodata (19 MB instead of 7 MB compressed). Other file systems are read once into retained memory.

### Prebuilt glue
- Built by `pythago-dist -prebuild-glue DIR` with the pinned LLVM and both `PYTHAGO_SYSROOT_ARM64` / `PYTHAGO_SYSROOT_AMD64` from `ci/sysroot.sh`.
- Reproducible: the build uses relative paths in a fixed work directory `$cache/pythago-dist/glue-build/<key>` with `include` symlinked, so objects are byte-identical across runs.
- `PYTHAGO_SYSROOT_<ARCH>` applies to the host architecture too, so every host compiles against the same glibc headers. (arm64 against `/`, Ubuntu glibc 2.39, and against the noble sysroot gave identical bytes anyway.)
- `ci/glue.sh` rebuilds and compares with `SHA256SUMS`; it has its own workflow job. Rebuild and commit whenever `glue/*.c` changes. Changing a Go↔C shared structure also means bumping `PYTHAGO_PY_ABI` and `glueABI` (`py/capi.go`).

### Toolchain provisioning
- LLVM fetch: release `llvmorg-23.1.5`, asset `LLVM-23.1.5-Linux-<arch>`. Only the needed binaries (`clang`, `lld`, `ld.lld`, `llvm-ar`, `llvm-nm`, `llvm-readobj`, `llvm-readelf`) and `lib/clang` are extracted: 721 MB of 13 GB. The archive is deleted afterwards.
- `.tar.zst` is decompressed with `zstd -d --long=31`: LLVM's archives use a 1 GiB window, which plain `tar --zstd` refuses ("Frame requires too much memory"). Extraction is selective by member path.
- bao in process: bao PR #31 added package `github.com/moriyoshi/bao/build` (`build.Run(ctx, build.Options{...})`, the same code path as `bao build`). An external bao is run through `Options.CommandLine()`.
- In-process output was byte-identical to the external bao's for every `.syso`, `.go` and `.s` file of the 3.13 arm64 and amd64 images.

### bao adoption history
| Pin | What it brought |
|-----|-----------------|
| main `8673620e4cf0` (`go install …/cmd/bao@main`) | #18, #19, #20: `-linux-max-symbol-version` and the amd64 fixes; replaced pythago's own version pinning (`cmd/pythago-dist/pin.go`) and `glue/pythago_amd64.c` |
| main `ad1667c00f3a` | #30: `-wrap` on a Linux shared object's imports (enables shared mode) |
| PR branch `fb6c077c9620`, then main `345ad90cc2ec` | #31: the in-process `build` package |
| unmerged #32–#36 | pinned only while verifying them |
| tag **v0.0.7** (`3f53b8c`) | #32–#36 merged (TLS speedups, pre-2.34 glibc fixes) |
| tag **v0.0.8** (`fcb42a6`) | #41 `-darwin-dylib` (`build.Options.Darwin.Dylib`, `-wrap` on the dylib's binds), #42 `-windows-dll`, #43 docs: a pthread key destructor running after bao's per-package key sees glue TLS at its initial value, so pass state through `pthread_setspecific` |

Adopting new bao flags as defaults waits for the user (see [process-lessons.md](process-lessons.md)). The glibc-related fixes are in [glibc-floor-and-amd64.md](glibc-floor-and-amd64.md).

### Consumer workflow
`examples/hello/gen.go`:
```go
//go:generate env -u GOOS -u GOARCH go run github.com/moriyoshi/pythago/cmd/pythago-dist -python 3.13 -target $GOOS/$GOARCH -sysroot=$PYTHAGO_SYSROOT -o pydist
```
- `pydist/` is generated and git-ignored.
- The program imports the package for its side effect and calls `py.Run(cfg, fn)`.

### Measurements
| What | Result | Setup |
|------|--------|-------|
| 3.13 `.syso` total, arm64 | 23.5 MB shared, 35.7 MB LTO | stripped input |
| 3.13 `.syso` total, amd64 | 23.8 MB shared, 34.6 MB LTO | |
| Generation, shared | 0.7 s | `PYTHAGO_LLVM=/nonexistent go generate ./examples/hello`, PBS archive cached |
| Generation, LTO | about 50 s per target | fetched LLVM, cold lowering cache |
| Generation, LTO | about 2.5 s | lowered object reused |
| `examples/hello` binary | 49,442,475 bytes | 3.13 shared; Go + Python, app `embed.FS`, a Go module, threads, json, `importlib.resources` |
| Startup, whole process | 26 ms embedded stdlib, 20 ms disk stdlib | 3.13 |

## Files
- `cmd/pythago-dist/main.go`: flags, mode dispatch, package doc.
- `cmd/pythago-dist/shared.go`: shared mode (`runShared`, `sharedGlue`).
- `cmd/pythago-dist/build.go`: LTO mode, lowering cache, `minGlibc`, bao options (`MaxSymbolVersions`, `StaticTLS`).
- `cmd/pythago-dist/prebuild.go`: `-prebuild-glue`.
- `cmd/pythago-dist/stdlib.go`, `emit.go`, `fetch.go`: stdlib zip and `.pyc`, register file and manifest, downloads.
- `glue/glue.go`, `glue/*.c`, `glue/prebuilt/`: glue sources and prebuilt objects.
- `internal/toolchain/toolchain.go`: LLVM resolution and fetch.
- `internal/ghrelease/ghrelease.go`, `internal/pbs/pbs.go`: verified downloads and PBS metadata.
- `py/fsimport.go`: the `fs.FS` importer.
- `examples/hello/gen.go`, `examples/hello/main.go`: the consumer pattern.

## Test Coverage
- `py` tests `TestFSImport` (embedded stdlib importer) and `TestSQLiteFile` (stat wrappers, LTO mode); the whole `py` suite runs against the embedded stdlib, and passes with the disk stdlib too.
- `ci/unit.sh gil` generates `examples/hello` with `go generate` and runs it from an empty directory under `env -i` on each architecture.
- `ci/glue.sh` checks that `glue/prebuilt`'s Linux objects reproduce `SHA256SUMS`; `ci/darwin.sh glue` does the darwin ones on a Mac. Both verify the rebuilt files against the committed sums, not only the merged sums file.
- CI runs both modes (`PYTHAGO_MODE=shared|lto`). See [ci-and-test-image.md](ci-and-test-image.md).

## Pitfalls
- **`go generate` passes its GOOS/GOARCH to `go run`.** With a cross GOARCH, `go run` built pythago-dist *for the target*; it ran under qemu, believed it was on an amd64 host, and skipped the cross linker setup. Strip them with `env -u GOOS -u GOARCH` and pass the target as `-target $GOOS/$GOARCH`, which go generate expands.
- **`-wrap` applies to the glue too.** Glue fallbacks that call `pthread_create` are redirected to `__wrap_pthread_create`, i.e. themselves; `TestForkChild` crashed. Fallbacks call `__real_pthread_*`.
- **No compiler-rt in shared mode:** arm64 glue builds with `-mno-outline-atomics`, or outline-atomics helpers are missing.
- **Anything an import needs must work without Go** (forked children cannot run Go); hence the C importer.
- **`torch.compile` and vLLM need Python's headers.** They do not exist in the embedded stdlib; point `PYTHONHOME` at a stock prefix (as `ci/gpu.sh` does) or use the disk stdlib.
- The first switch to shared mode also surfaced `TestNestedUnderLoad` failing about 1 run in 45 (amd64, qemu): a Go wrapper without `//go:uintptrescapes`. See [embedding-cpython.md](embedding-cpython.md).
