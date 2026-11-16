# glibc Floor and linux/amd64

## Summary
pythago images run on glibc 2.18 and newer (shared mode), on arm64 and amd64. The floor is enforced by capping every import's symbol version at bao time and proven by running binaries through old Debian loaders. Getting there took three bao fixes for the amd64 port (found under qemu-user) and three more for glibc below 2.34, none of which a version-number check could see.

## Key Facts
- **Floor: glibc 2.18.** User decision (2026-11-15): glibc < 2.18 is unsupported. `pythago-dist` caps symbol versions at the newer of 2.18 (`minGlibc` in `cmd/pythago-dist/build.go`) and the archive's `glibc-max-symbol-version` (2.17 for current PBS builds).
- **Why 2.18:** bao's native-TLS runtime needs `__cxa_thread_atexit_impl` (GLIBC_2.18). It binds that version explicitly, so older glibc refuses the binary at load with a clear message instead of dying at the first TLS access.
- **Binary verneed:** `GLIBC_2.17` and `GLIBC_2.18` only. Every other import binds ≤ 2.17 (547 of 557 versioned imports on arm64; amd64 `pthread_cond_init` binds `GLIBC_2.3.2`).
- **Allowed above the cap:** only the weak `getentropy` (libcrypto) and `sem_clockwait` (libpython). `ci/floor.sh` fails on anything else.
- **LTO-mode images need glibc 2.30+.** bao cannot bind weak *code* references from archive input, because Go's linker cannot import weakly. On the old-glibc runs, LTO images fail on jessie (`getentropy` missing) and on buster (`sem_clockwait` missing), so `ci/floor.sh` skips those runs when the manifest says `mode: lto`. Shared mode is unaffected.
- **Why jessie:** Debian jessie's glibc 2.19 is the nearest archived release to the 2.18 floor; buster (2.28) covers the extension-shim floor.
- **C extensions through the extension shim need glibc 2.28** (SHN_ABS dynsym entries; see [embedding-cpython.md](embedding-cpython.md)). `ci/oldglibc.sh` uses buster (2.28) for that reason.
- **amd64 is verified only under qemu-user** on an arm64 host. TLS was not a problem: the LTO-lowered amd64 libpython uses only local-dynamic TLS (TLSLD + DTPOFF32/64), which bao supports.
- **qemu-user 8.2.2 crashes when a guest reads `/proc/self/maps`.** `ci/qemu/nomaps.c` is preloaded to work around it.

## Details

### amd64 cross setup
- Cross-built on the arm64 host from PBS 20261110 x86_64 `pgo+lto-full` 3.13.18 and `freethreaded+pgo+lto-full` 3.14.10 (LTO mode; shared mode uses `install_only_stripped`), checked against the release digests.
- Sysroot: amd64 glibc 2.39 from Ubuntu noble `.deb`s checked against the archive index, assembled by `ci/sysroot.sh` (libc6, libc6-dev, linux-libc-dev, libgcc-s1, libgcc-13-dev, libcrypt1, libcrypt-dev, libstdc++6, zlib1g).
- Run under qemu-user 8.2.2 via binfmt with `QEMU_LD_PREFIX=<sysroot>`.
- Builds pass clang `--target`/`--sysroot`, `libgcc.a` from the sysroot as the builtins library, and bao `-sysroot`.
- One lowered object holds about 31k sections, as on arm64.

### The three amd64 bao gaps (fixed in bao #18–#20)
Reported to the bao agent with repros; all three merged and are in bao main `8673620e4cf0` and later.

| Gap | Symptom | Former pythago workaround (removed) |
|-----|---------|-------------------------------------|
| Import bindings had no symbol versions | ld.so binds unversioned references to the *oldest* definition: `pthread_cond_init@GLIBC_2.2.5`, the old ABI, rejects a CLOCK_MONOTONIC condattr: `create_gil: PyCOND_INIT(gil->cond) failed`. arm64 has no such compat versions. | `cmd/pythago-dist/pin.go` versioned each multi-version import at the newest version ≤ 2.17 (70–73 imports on amd64, 54–57 on arm64) |
| `SHN_COMMON` rejected | OpenSSL's `x86_64cpuid.s` has `.comm OPENSSL_ia32cap_P` | `glue/pythago_amd64.c` defined it |
| Missing-weak references in the `cmp $0, sym@GOTPCREL(%rip)` form (addend -5) rejected | libzstd's optional `ZSTD_trace_*` hooks; CPython's weak `sem_clockwait` | `pythago_amd64.c` defined the zstd hooks as "not tracing"; a `__wrap_sem_clockwait` in the glue |

- The replacement is bao `-linux-max-symbol-version GLIBC_<floor>`, plus bao's manifest field `linux_imports_above_version_cap`.
- Pinning to the newest default version instead also "works", but raises the floor to 2.34–2.38.

### The stat64 floor break
- `linux_imports_above_version_cap` exposed a break the old pinning had left silently unversioned: `stat64`, `fstat64` and `lstat64` @GLIBC_2.33, referenced directly by the PBS sqlite.
- PBS links against glibc 2.17, where those were static wrappers in `libc_nonshared.a`.
- Fix: `glue/pythago_static.c` defines the same wrappers, calling `__xstat64`/`__fxstat64`/`__lxstat64` with `_STAT_VER` 1 on x86_64 and 0 on aarch64. They bind at 2.17 (aarch64) / 2.2.5 (x86_64).
- Only the LTO build includes that file; a shared libpython already has them resolved. Test: `TestSQLiteFile`.

### `__cxa_thread_atexit_impl` and dropping 2.17
- The bao native-TLS runtime calls it the first time a thread touches native TLS, and aborts if it fails (confirmed by the bao agent).
- It is bound lazily, so on 2.17 a binary would load and then die at the first TLS access ("symbol lookup error"). This was inferred, not run.
- A 2.17 fallback (weak lookup plus a pthread key destructor) would change destructor ordering. The user chose to drop glibc < 2.18 instead, and the bao agent was told the fallback is not needed.

### Below glibc 2.34: three bao bugs
No pythago binary had started below glibc 2.34 until these were fixed; the documented 2.18 floor had never held for 2.18–2.33.

```text
$ llvm-readelf -V hello | …
  File: libc.so.6  Cnt: 2   Name: GLIBC_2.17  Name: GLIBC_2.18
  pthread_create@GLIBC_2.17  pthread_key_create@GLIBC_2.17
$ env -i buster/lib/aarch64-linux-gnu/ld-2.28.so --library-path … ./hello
./hello: relocation error: ./hello: symbol pthread_key_create version GLIBC_2.17 not defined in file libc.so.6 with link time reference
```

| # | Failure | Cause | bao fix |
|---|---------|-------|---------|
| 1 | relocation error above | capped imports of symbols that glibc 2.34 moved into `libc.so.6` still named `libc.so.6` | #34 (`28adac1`): names the pre-2.34 library (libpthread, libdl, librt, libutil, libanl) for the 210 moved symbols |
| 2 | `panic: …/pyrun-floor-arm64: undefined symbol: sem_clockwait` | the generated binder had no weak marker (`{"sem_clockwait", "", 0}`) | #35 (`54648d6`): weak imports bind 0 |
| 3 | ``Inconsistency detected by ld.so: dl-minimal.c: 126: realloc: Assertion `ptr == alloc_last_block' failed!`` (arm64 only) | DT_NEEDED order | #36 (`15b3ac4`): libc first, the dynamic linker last, on every arch |

Bug 3 in detail:
- It hit in a real `debian:buster` container too, not only through the explicit loader.
- `LD_DEBUG=all` placed it at the **second** `RTLD_GLOBAL` dlopen (the extension shim), where `add_to_global` reallocates the scope list.
- The generated directives were alphabetical, so `//go:cgo_import_dynamic _ _ "ld-linux-aarch64.so.1"` (for `__stack_chk_guard`) came first and DT_NEEDED started with ld-linux.
- Before glibc 2.32, rtld rebinds its malloc family by a global-scope lookup. It found its own exported minimal allocator first and kept it.
- A plain purego binary (no ld-linux in DT_NEEDED) survived three `RTLD_GLOBAL` dlopens on buster; moving the directive last by hand made pyrun pass. amd64 imports nothing from ld.so and was never affected.

Verified with bao #36: Debian jessie (2.19) and buster (2.28) pass for 3.13 shared and 3.14t shared with static TLS, on arm64 natively and amd64 under qemu; `debian:buster` natively with all 20 cores: 3/3.

### The qemu `/proc/self/maps` shim
- qemu-user 8.2.2 crashes ("QEMU internal SIGSEGV", addr 0x20) whenever a guest reads `/proc/self/maps`, even a plain Go program. Test environment only.
- Readers in pythago: CPython's path configuration (`_PyConfig_InitPathConfig`), and glibc's `pthread_getattr_np` on the main thread (3.14's `_Py_InitializeRecursionLimits`).
- `ci/qemu/nomaps.c`, preloaded with `QEMU_SET_ENV=LD_PRELOAD=/nomaps.so`, fails that open and answers the main thread's stack from `__libc_stack_end` and `RLIMIT_STACK`. It overrides `open`, `open64`, `openat`, `fopen`, `fopen64` and `pthread_getattr_np`.
- **It must define versioned symbols.** bao binds a shared object's imports with `dlvsym(RTLD_DEFAULT, name, version)`, which skips unversioned definitions. `nomaps_shim` in `ci/lib.sh` links it with a version script: `GLIBC_2.2.5` on x86_64, `GLIBC_2.17` on aarch64.
- For old-glibc runs it is linked against that release's `libc.so.6`, `libdl.so.2` and `libpthread.so.0`. The noble-built shim needs `GLIBC_2.34` (`dlsym`, `pthread_attr_setstack`).
- Its messages start with `nomaps:`; CI filters them out of compared output.

### Results under qemu (amd64)
- 3.13: `py` tests; S1 (json, sqlite, ssl, decimal, sha256, threads); S2/S3 (1784-entry symtab, callbacks from goroutines and Python threads); S4 (fs.FS importer); sthreads and saio (GoEventLoop, GoSelector, default loop); S5 (markupsafe, cffi with callbacks, numpy 2.5.4 including threaded matmul).
- 3.14t: `py` tests; S1, S2/S3, S4, sthreads; S5 with cp314t wheels. The GIL stays off; 4 CPU-bound threads give a 4.0× speedup.
- Today `ci/unit.sh` runs the `py`, `aio` and `asgi` tests and `examples/hello` under qemu for the other architecture (see [ci-and-test-image.md](ci-and-test-image.md)).
- Not covered by emulation: performance and amd64 hardware specifics. (The 16/64 KiB page sizes are an arm64 concern.)

## Files
- `cmd/pythago-dist/build.go`: `minGlibc = "2.18"`, floor computation, `bo.Linux.MaxSymbolVersions`.
- `cmd/pythago-dist/shared.go`: shared mode uses `glibcFloor: minGlibc` directly.
- `glue/pythago_static.c`: `stat64` family wrappers and `atexit` (LTO only).
- `ci/floor.sh`, `ci/floor_check.py`, `ci/oldglibc.sh`: the floor tier.
- `ci/sysroot.sh`: noble sysroots for cross builds and qemu.
- `ci/qemu/nomaps.c`, `ci/lib.sh` (`nomaps_shim`, `run_arch`, `run_clean`): the qemu shim.
- `.agents/attic/spike/`: the S1–S5, sthreads and saio spikes used for the first amd64 verification.

## Test Coverage
- `ci/floor.sh` (after `ci/unit.sh`): version cap check plus runs on jessie 2.19 and buster 2.28 through their own loader. Details in [ci-and-test-image.md](ci-and-test-image.md).
- `TestSQLiteFile` covers the stat wrappers.
- The full unit tier runs amd64 under qemu on an arm64 host.

## Pitfalls
- **A floor is proven only by running on it.** `ci/floor.sh` once compared only the newest `GLIBC_*` number in verneed against the floor. It never looked at which library a version was bound to, and all three pre-2.34 bugs were invisible to it.
- **An old `ld.so` run directly** (`ld-2.28.so --library-path … ./prog` under `env -i`) reproduces old-system failures exactly as a container does, at a fraction of the cost. `LD_DEBUG=libs,versions` or `all` shows where loading dies. See [debugging-techniques.md](debugging-techniques.md).
- **Unversioned dynamic imports silently bind compat ABIs on x86_64 glibc** (`pthread_cond_init@GLIBC_2.2.5`). arm64 hides this because it has no compat versions.
- **Make the tool report what it could not do.** `linux_imports_above_version_cap` is what exposed `stat64`; keep `floor.sh`'s allowlist short and explicit.
- **Isolate emulator bugs with a minimal guest.** A plain Go program reading `/proc/self/maps` crashed qemu the same way, which ruled out pythago and bao in one step. Always run cross binaries through `ci/lib.sh`'s `run_arch`, never qemu directly.
- A cross-architecture port surfaces linker-semantics gaps (version nodes, common symbols, weak references), not codegen ones. See [design-lessons.md](design-lessons.md).
