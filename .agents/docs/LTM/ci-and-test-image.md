# CI Tiers and the Test Image

## Summary
pythago's verification is a set of bash tiers under `ci/`: `unit.sh` (every package natively and the other architecture under qemu-user), `floor.sh` (the glibc floor, including runs on old glibc), `gpu.sh` (torch and vLLM against a stock-Python control) and `glue.sh` (prebuilt glue reproducibility). They all share `ci/lib.sh`, which builds `pythago-dist`, resolves its toolchain and generates the test image in `.work/testimage`, a separate module that the tests import through the workspace `.work/go.work`.

## Key Facts
- **Standard gate:** `ci/unit.sh gil && ci/unit.sh ft` before declaring a change complete. `gil` is CPython 3.13, `ft` is 3.14t.
- **The test image is generated, never checked in:** `.work/testimage` (module path `github.com/moriyoshi/pythago/internal/testimage`) plus `.work/go.work`. Tests import the image package by that path and need `GOWORK=$PWD/.work/go.work`.
- **It holds whichever variant ran last.** Regenerate the variant you need before building a tool against it; a 3.13 environment with a 3.14t image fails at `Init`.
- **Every `go` command runs with `GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0`** (exported by `ci/lib.sh`).
- **Cross architecture under qemu-user** only when binfmt for it is enabled (`have_qemu`); otherwise the tier runs native only and logs it.
- **`PYTHAGO_MODE`** selects `pythago-dist -mode` for test images: `shared` (default) or `lto`. The CI workflow runs both.
- **GPU checks run twice**, under pythago and under the environment's stock Python. A check fails the tier only when it fails under pythago while the control passes (or has no control).
- CI caches live in `~/.cache/pythago-ci` (`PYTHAGO_CI_CACHE`), `~/.cache/pythago-dist` and `~/.cache/pythago/pbs`.

## Details

### `ci/lib.sh` (sourced by every tier)
Environment knobs:

| Variable | Meaning |
|----------|---------|
| `PYTHAGO_LLVM` | LLVM bin dir (default: fetched by `pythago-dist`) |
| `PYTHAGO_BAO` | external bao executable (default: the bao built into `pythago-dist` from `go.mod`) |
| `PYTHAGO_SYSROOT_<ARCH>` | glibc sysroot for cross targets, e.g. `PYTHAGO_SYSROOT_AMD64` (default: built by `ci/sysroot.sh` into the cache) |
| `PYTHAGO_CI_CACHE` | cache root (default `~/.cache/pythago-ci`) |
| `PYTHAGO_MODE` | `shared` (default) or `lto` |
| `PYTHAGO_DIST_ARGS` | further `pythago-dist` arguments for test images (e.g. `-static-tls`) |

On load it:
- exports `GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0`, and `GOWORK=.work/go.work` if it exists, else `GOWORK=off`;
- builds `$CACHE/bin/pythago-dist` and evals its `-print-toolchain` output into `PYTHAGO_LLVM`/`PYTHAGO_BAO`.

Helpers:
- `other_arch`: the cross architecture (`arm64` ↔ `amd64`).
- `python_args gil|ft`: `-python 3.13` or `-python 3.14 -variant freethreaded`.
- `sysroot ARCH`: empty for the host, else `PYTHAGO_SYSROOT_<ARCH>` or a cached `ci/sysroot.sh` build.
- `gen_image VARIANT ARCH...`: removes `.work/testimage`, `.work/go.work` and `go.work.sum`; runs `pythago-dist -mode $MODE $PYTHAGO_DIST_ARGS <python_args> -target linux/$arch [-sysroot …] -o .work/testimage` per arch with `GOWORK=off`; then `write_workspace`.
- `write_workspace`: writes `.work/testimage/go.mod` (module `github.com/moriyoshi/pythago/internal/testimage`, `go 1.26.0`, requiring only `github.com/ebitengine/purego` at the main module's version) and `.work/go.work` (`use ( .. ./testimage )`), and exports `GOWORK`.
- `run_arch ARCH CMD...`: native, or under qemu with `QEMU_LD_PREFIX=<sysroot>` and `QEMU_SET_ENV=LD_PRELOAD=/nomaps.so`, rebuilding the shim when `ci/qemu/nomaps.c` is newer.
- `nomaps_shim ARCH OUT [LIBDIR]`: builds the `/proc/self/maps` shim with a version script (`GLIBC_2.2.5` x86_64, `GLIBC_2.17` aarch64), optionally linked against an old release's libraries. See [glibc-floor-and-amd64.md](glibc-floor-and-amd64.md).
- `have_qemu ARCH`: checks `/proc/sys/fs/binfmt_misc/qemu-<x86_64|aarch64>` is enabled.
- `run_clean ARCH CMD...`: like `run_arch` but with `env -i` (qemu settings kept).

### `ci/unit.sh gil|ft`
1. `gen_image` for the host arch plus the other arch if qemu can run it.
2. `gil` only: `go generate ./examples/hello` and add `./examples/...` to the package list.
3. `go vet` over `./py/... ./aio ./asgi ./env ./cmd/... ./internal/pbs ./internal/ghrelease ./internal/tools/... ./glue` (ignoring "possible misuse of unsafe.Pointer"), and `gofmt -l` over the same trees; any output fails.
4. `PYTHAGO_TEST_FETCH=1 go test -count=1 ./env` (fetches a stock interpreter).
5. Per arch: `go test -c` for `py`, `aio`, `asgi` with `GOARCH=$arch`, then run each through `run_arch` in its package dir with `PYTHONHOME` unset (timeouts 2400 s for `py`, 600 s otherwise). `asgi` runs twice: inline, then `PYTHAGO_ASGI_LOOP_THREAD=1` (`Options.LoopThread`).
6. `gil` only, per arch: generate `examples/hello` for the cross arch (`GOARCH=$arch PYTHAGO_SYSROOT=…`), build, copy into an empty temp dir and run it with `run_clean`. Output must contain `"greeting": ["GOPHER!", "PYTHON 3.`.

### `ci/floor.sh` (after `unit.sh`, on the current test image)
For each `.work/testimage/pythago-dist_linux_<arch>.json`:
1. Read `glibc_floor`; read `linux_imports_above_version_cap` from `bao-manifest_linux_<arch>.json`. Anything except `getentropy` and `sem_clockwait` fails.
2. Build `internal/tools/pyrun` for the arch; the newest `GLIBC_*` in `llvm-readelf -V` must not exceed the floor.
3. If qemu can run the arch and the manifest's `mode` is not `lto`: for `jessie` (2.19) and `buster` (2.28), unpack glibc with `ci/oldglibc.sh` into `$CACHE/glibc-<release>-<arch>`, then run `env -i QEMU_LD_PREFIX=<root> [LD_PRELOAD shim] <root>/lib/*-linux-gnu/ld-2.*.so --library-path <libs> pyrun-floor-<arch> ci/floor_check.py`. Success prints `floor ok <version> <OpenSSL version>`.
- LTO images skip the old-glibc runs: they need glibc 2.30+.
- `ci/floor_check.py` exercises threads, sqlite3, hashlib, zlib, lzma, bz2, ssl, ctypes (and imports decimal, json).
- `ci/oldglibc.sh jessie|buster amd64|arm64 DIR` unpacks `libc6` and `libgcc1` from `archive.debian.org`, each checked against the SHA256 in the `Packages` index.

### `ci/gpu.sh gil|ft` (needs an NVIDIA GPU)
- `gen_image` for the host only; builds `pyrun-<variant>` and `pythago-env`.
- Provisions venvs with `pythago-env` (`$CACHE/venv-<name>-<variant>`): torch `torch==2.14.1+cu130` from the cu130 index; vLLM (gil only) `vllm==0.32.0 torch==2.13.0+cu132 torchaudio==2.11.0+cpu`. `PYTHAGO_GPU_WHEELS` switches to offline `--no-index --find-links`.
- `check NAME VENV SCRIPT [ENV=VALUE]`: runs `ci/gpu/<script>` with `PYRUN_ENV=<venv>` and `PYTHONHOME=<stock prefix>` (headers for `torch.compile` and JIT kernels), then with `<venv>/bin/python`. Timeout 1800 s each.
- Checks: `torch_basic`, `torch_workers`, `torch_compile`, `torch_threads`, `torch_goroutines` (pythago only, `PYRUN_GO=16`); gil adds `vllm_generate`, `vllm_embed` (both with `VLLM_USE_FLASHINFER_SAMPLER=0`) and `vllm_server` (OpenAI server: health, a chat completion answering "blue", SIGTERM exit 0).
- Verdicts: `ok`, `FAIL (pythago)` (sets exit 1), `both fail (environment)`. `PYTHAGO_GPU_VLLM=0` skips vLLM.
- Logs: `$CACHE/gpu-<name>.{pythago,stock}.log`.

### `ci/glue.sh`, `ci/sysroot.sh`
- `glue.sh`: `pythago-dist -prebuild-glue` with both noble sysroots, diffed against `glue/prebuilt/SHA256SUMS` (the Linux objects; `ci/darwin.sh glue` checks the darwin ones on the arm64 Mac). See [image-build-and-toolchain.md](image-build-and-toolchain.md).
- `sysroot.sh amd64|arm64 DIR`: Ubuntu noble (+ noble-updates) packages, each checked against the `Packages` index SHA256.

### Workflows
- `.github/workflows/ci.yml`: job `unit` over runner (`ubuntu-24.04`, `ubuntu-24.04-arm`) × variant (`gil`, `ft`) × mode (`lto`, `shared`), installing `qemu-user-binfmt zstd`, then `ci/unit.sh` and `ci/floor.sh`. Job `glue` runs `ci/glue.sh`. Caches are keyed on `cmd/pythago-dist/**`, `glue/**`, `internal/pbs/**`, `internal/toolchain/**`, `ci/**`.
- `.github/workflows/gpu-nightly.yml`: self-hosted runner labels `self-hosted, linux, gpu`, variants gil/ft, `PYTHAGO_GPU_VLLM: "0"`, uploads `~/.cache/pythago-ci/gpu-*.log`.
- Both workflows were YAML-validated but have never run (no remote yet); every tier result recorded here comes from running the `ci/` scripts locally (arm64 host, amd64 under qemu).

### `internal/tools/pyrun`
- The runner used by `floor.sh` and `gpu.sh`. Scripts run as `python` runs them: `__file__`, `sys.argv`, code objects named by path. `--python` runs a python command line (`-m …`).
- `PYRUN_ENV=dir` sets `Config.Environment`; `PYRUN_GO=n` also runs the script's `outer(g)` from n goroutines.

### The test image as a separate module
- It sits outside the published tree (git-ignored) so the generated image is never committed, and the module path stays `github.com/moriyoshi/pythago/internal/testimage` so no import changed.
- Its `go.mod` must **not** require `github.com/moriyoshi/pythago v0.0.0`: go then looks that version up (`unknown revision v0.0.0`) instead of using the workspace module. It needs only purego.
- Without the workspace, test packages fail with "no required module provides package" (also what happens when the image was never generated).

### What the control harness caught
- `torch_workers` failed under stock Python only: spawn and forkserver re-import `__main__` from its file, and the script lacked a main guard. pyrun had hidden that by not setting `__file__`. Fixed the script and made pyrun python-like.
- `torch_threads` failed under pythago only: its `SystemExit(0)` (skipping on the GIL build) became a failure. `py.Run` now exits with a `SystemExit`'s status, through `(*Error).SystemExit()`.

### Recorded results (all tiers, both modes)
- unit gil and ft: pass on arm64 native and amd64 under qemu.
- floor: 2.18; above the cap only `getentropy` and `sem_clockwait`; jessie and buster pass (shared).
- GPU (GB10, shared mode): gil 8/8 (torch basic, workers, compile, threads, goroutines; vLLM generate, embed, server), ft 5/5, the stock control agreeing everywhere.

## Files
- `ci/lib.sh`: shared settings, `gen_image`, `write_workspace`, qemu helpers.
- `ci/unit.sh`, `ci/floor.sh`, `ci/gpu.sh`, `ci/glue.sh`: the tiers.
- `ci/floor_check.py`, `ci/gpu/*.py`: scripts run by the tiers.
- `ci/oldglibc.sh`, `ci/sysroot.sh`: verified Debian/Ubuntu glibc downloads.
- `ci/qemu/nomaps.c`: the qemu `/proc/self/maps` shim.
- `.github/workflows/ci.yml`, `.github/workflows/gpu-nightly.yml`.
- `internal/tools/pyrun/`: the script runner.
- `.work/testimage/`, `.work/go.work`: generated test image and workspace.

## Test Coverage
```sh
ci/unit.sh gil && ci/unit.sh ft     # standard gate
ci/floor.sh                         # after unit.sh; uses the image unit.sh left
ci/gpu.sh gil                       # GPU host only; PYTHAGO_GPU_VLLM=0 to skip vLLM
ci/glue.sh                          # after editing glue/*.c
PYTHAGO_MODE=lto ci/unit.sh gil     # LTO mode
GOWORK=$PWD/.work/go.work go test ./py   # quick run against the current image
```

## Pitfalls
- **The image is per variant.** `floor.sh` checks whatever `unit.sh` generated last; `gpu.sh` regenerates for the host only, which drops the cross-arch image.
- **Bash:** `local a=$1 b=$a` expands every word before any assignment, so `$a` is unset there.
- **Bash under `set -euo pipefail`:** `grep -c` exits 1 on a zero count; append `|| true`.
- **Never invoke qemu directly;** use `run_arch`/`run_clean`, which preload the shim. Shim messages start with `nomaps:` and are filtered from compared output.
- **Generate `examples/hello` before `go vet`,** or the import of its `pydist` package is missing.
- **A stock control catches test bugs too:** a failure that differs between pythago and stock Python, in either direction, points at a fidelity gap or a broken test. See [design-lessons.md](design-lessons.md).
- `py/py_test.go` comments (lines 24 and 84) still say `internal/testimage`; the image is in `.work/testimage`.
