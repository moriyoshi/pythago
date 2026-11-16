# Environments and ML Stacks (PyTorch, vLLM)

## Summary
Third-party packages reach a pythago program through an on-disk, venv-like environment that a stock PBS Python of the same build creates and fills with pip; C extensions load through `py`'s extension shim. PyTorch+CUDA and vLLM, the heaviest real workloads, run unmodified on this path once `py` handled what embedding breaks: fork without exec, `sys.executable` re-exec, Go-owned signal handlers, and the locale encoding.

## Key Facts
- **Environments come from a stock PBS Python**, not a Go wheel installer. The same build has the same ABI and platform tags, so pip decides compatibility exactly and handles `*.data/scripts`, entry points, `RECORD` and dependency resolution.
- **`pythago-env`** (`cmd/pythago-env`, over package `env`) fetches the matching PBS `install_only_stripped` interpreter, runs `-m venv`, then pip: `pythago-env -python 3.13 -env ./venv install torch --index-url …`.
- **`py.Config{Environment: dir}`** activates it: `site.addsitedir` on `lib/pythonX.Y[t]/site-packages` (`.pth` works), `bin/` first on `PATH`, `sys.prefix`/`sys.exec_prefix` and `VIRTUAL_ENV` set. A `pyvenv.cfg` for another Python version is rejected.
- **Large native stacks stay on the dlopen-through-shim path.** Translating 5 GB of C++/CUDA libraries ahead of time with bao, with their own `dlopen`s, RPATH layouts and JIT-compiled extensions, is not a realistic target; embedding them in a Go binary is impractical too.
- **Programs must handle the "python command" case first.** `py.Run` does it (`IsPythonCommand()` → `os.Exit(Main(cfg, os.Args))`); programs that call `py.Init` directly must call `py.IsPythonCommand()` / `py.Main(cfg, os.Args)` first in `main`, or multiprocessing and `sys.executable` break.
- **Signal-installing code runs on Python's main thread** (`py.Main`, or `py.OnMain(fn)` from a running program), not inside `py.Do`.
- **UTF-8 mode is always on** (PEP 540), as it is by default from 3.15 (PEP 686).
- **vLLM is GIL-build only:** it ships only abi3 wheels, which free-threaded 3.14 cannot load (abi3t arrives in 3.15).
- Verified on an NVIDIA GB10 (compute capability 12.1, driver 580 / CUDA 13.0, arm64), cp313 and cp314t.

## Details

### Environments (`env`, `pythago-env`)
- `internal/pbs`: release metadata, digest-verified downloads, extraction (gzip in Go, zstd through an external tool). Shared with `pythago-dist`.
- `env.Interpreter` fetches `install_only_stripped` for the image's release, version and variant; `env.Create` runs `venv`; `env.Pip` runs pip; `env.Check` validates the `pyvenv.cfg` version and `lib/pythonX.Y[t]/site-packages`.
- `pythago-env` flags: `-env DIR` (created if missing), `-python`, `-freethreaded`, `-release`, `-interpreter` (a stock python3 instead of fetching). Arguments after the flags go to pip.
- `py.ImageInfo()` gives the image's version and variant before `Init`.
- An existing venv works if its Python version and ABI match the image.
- The environment must be a *real* environment, not just a `sys.path` entry: console scripts and binaries (e.g. `ninja`) live in wheels' `*.data/scripts` and must be on `PATH` for JIT builds; `purelib`/`platlib` must be merged. That is why pip installs into a venv.

| Measurement | Result |
|-------------|--------|
| First fetch, verify, extract and venv | about 20 s |
| torch venv | 30 distributions, 22 s |
| vLLM venv | 199 distributions, 54 s (with `torchaudio==2.11.0+cpu` pinned; `ninja` installed into `bin/` by pip) |
| markupsafe, offline install | its C extension imports from an embedded-stdlib program |

### PyTorch + CUDA
Setup: torch 2.14.1+cu130 with its CUDA 13 wheels, triton and numpy, cp313 and cp314t (3.0 GB of wheels, 5.1 GB unpacked). Originally a plain site directory (wheels unpacked keeping `nvidia/...` for the `$ORIGIN` RPATHs), later a `pythago-env` venv. No torch or CUDA file was modified.

Works unchanged on 3.13 and 3.14t:
- `import torch` (`torch._C` → `libtorch_python` → libtorch, CUDA, cuDNN and NCCL from the wheels, `libcuda` from the system);
- device detection, GPU matmul against CPU, autograd training (loss 1.04 → 0.0014), bf16 autocast;
- C++ exceptions surfacing as `RuntimeError`;
- libtorch's OpenMP pool (20 threads); 8–16 Python threads driving the GPU on their own streams;
- DataLoader with `num_workers=0`, and with workers under `fork`, `spawn` and the default (forkserver on 3.14) after the fixes below;
- `torch.compile` on CUDA (triton) and CPU (inductor C++). Both compile and `dlopen` their generated extensions through the shim.
- 3.14t: the GIL stays off; 8 Python threads run torch CPU work 5.4× faster than one.
- 16 goroutines calling torch (CPU plus GPU) through `py.Do`: 0.39 s, correct.
- Importing triton re-enables the GIL with a warning. That is the triton wheel, not free-threading-ready; stock CPython does the same.

### vLLM
Setup: vLLM 0.32.0 (`cp38-abi3-manylinux_2_28_aarch64`) with torch 2.13.0+cu132 on cp313, 198 wheels, 7.6 GB unpacked; the same 199 distributions as a working stock-Python venv on the machine.

Works:
- **Offline generation** (`LLM.generate`, Qwen2.5-0.5B-Instruct) with vLLM's defaults: the V1 engine in a separate process, weights from the Hugging Face Hub, `torch.compile` with the AOT compile cache, CUDA graph capture, a 28 GiB KV cache. Correct ("Paris") and deterministic, 72 tokens in 0.14 s.
- **Embeddings** (`runner="pooling"`, multilingual-e5-small): correct similarity ordering.
- **OpenAI-compatible server** (`-m vllm.entrypoints.openai.api_server`) started through `py.Main`: `/health`, `/v1/models`, `/v1/completions`, `/v1/chat/completions`; SIGTERM exits 0 with no leftover processes. It needs the `py.Main` path because the server installs signal handlers.

Packaging notes:
- torchaudio must be the `+cpu` build; the PyPI CUDA 13.0 build fails torchaudio's CUDA-version check against torch cu132.
- `flashinfer-cubin` comes from flashinfer's own index, not PyPI (generation passes without it).
- flashinfer's JIT top-k/top-p sampler fails in nvcc ("CUDA compiler and CUDA toolkit headers are incompatible") in this package set under stock Python too. Run with `VLLM_USE_FLASHINFER_SAMPLER=0`. The pythago side of JIT builds (spawning ninja and nvcc) works.

### The `py` fixes these workloads required
These are general multiprocessing and embedding gaps, not torch-specific.

1. **fork without exec hung** (DataLoader `fork` workers, the 3.13 default).
   - The child has only the forking thread but inherits `py`'s C state. Its first new thread (a multiprocessing queue feeder) went to a spare thread that exists only in the parent, and waited forever.
   - Fix: a child handler registered with `__register_atfork(NULL, NULL, forked_child, &__dso_handle)` in `glue/pythago_py.c`. glibc's `pthread_atfork` lives in `libc_nonshared.a`, so the glue calls the exported function with `__dso_handle`, as for `atexit`. The handler forgets the parent's spares and thread records; threads then start with libc.
   - Go functions called in a forked child raise `RuntimeError` ("Go functions are not available in a forked child process") instead of entering a Go runtime that cannot run there.
2. **Re-exec of `sys.executable` failed** (DataLoader `spawn` workers; forkserver, the 3.14 default; `subprocess.run([sys.executable, ...])`).
   - `sys.executable` is the Go binary, which treated Python's arguments (`-I -c ...`) as its own.
   - Fix: `py.Init` sets `sys.executable` explicitly and marks the environment with `PYTHAGO_PYTHON_EXECUTABLE=<path>`. `py.IsPythonCommand()` compares it with `os.Executable()`. `py.Main(cfg, argv)` runs CPython's command-line semantics (`PyConfig_SetBytesArgv` with `parse_argv`, then `Py_RunMain`) with the same configuration.
   - `Config.Setup`, `Config.Modules` and the environment apply in both, so children get the same `sys.path` and shim.
3. **forkserver workers died in `signal.signal(sig, None)`.**
   - Go owns the signal handlers (`install_signal_handlers = 0`), so `signal.signal()` returns `None` ("not installed by Python"). forkserver saves those values and restores them in each child, and CPython rejects `None`.
   - Fix: the glue saves every signal's handler at startup; the C builtin `_pythago.restore_signal` reinstalls it, or leaves `SIG_DFL` in a forked child. `py` wraps `signal.signal` so that `None` means "restore the handler from before Python".
   - It is C, not Go, because forked children must use it too.
4. **The locale encoding was ASCII** (vLLM: torch inductor's UTF-8 templates).
   - CPython's isolated configuration never sets the locale from the environment, so `open()` without an encoding decoded as ASCII.
   - Fix: `pythago_config_new` (`glue/pythago_glue.c`) preinitializes with `PyPreConfig_InitIsolatedConfig`, `utf8_mode = 1` and `Py_PreInitialize`.

Related current `py.Config` knobs: `MultiprocessingStart` (default `"forkserver"`, so workers get a fresh process where Go works; `"default"` keeps Python's), `PythonSignals` (let Python install its own handlers; SIGINT then no longer reaches Go), `NoExtensions` (skip the extension shim, which needs glibc 2.28+).

### Embedded stdlib vs disk stdlib
- torch (3.13 and 3.14t: basic, free threading, DataLoader default/fork/spawn) passes with the embedded stdlib.
- `torch.compile` and vLLM need Python's C headers, which the embedded stdlib lacks. `ci/gpu.sh` sets `PYTHONHOME` to the venv's stock interpreter prefix (from `pyvenv.cfg` `home`).

## Files
- `env/env.go`: `Interpreter`, `Create`, `Pip`, `Check`.
- `cmd/pythago-env/main.go`: the CLI.
- `internal/pbs/pbs.go`: PBS release metadata and downloads.
- `py/process.go`: `Run`, `RegisterExports`, `ImageInfo`, environment activation, extension shim loading, start method.
- `py/runtime.go`: `Config` (`Environment`, `PythonSignals`, `MultiprocessingStart`, `NoExtensions`, `Setup`), `Init`, `IsPythonCommand`, `Main`, the `signal.signal` shim.
- `glue/pythago_py.c`: atfork child handler, `_pythago.restore_signal`, forked-child guard for Go functions.
- `glue/pythago_glue.c`: `pythago_config_new` (UTF-8 preinit).
- `py/internal/extshim`: the extension shim that exposes the C API to dlopen'ed extensions.
- `ci/gpu.sh`, `ci/gpu/*.py`: the torch and vLLM checks.

## Test Coverage
- `py` tests: `sys.executable` subprocess, spawn pool, forkserver pool, signal restore, fork child (`TestForkChild`), `TestUTF8Mode`.
- `env` tests (a Go-built wheel with a console script): `PYTHAGO_TEST_FETCH=1 go test ./env`, run by `ci/unit.sh`.
- `ci/gpu.sh gil|ft`: torch and vLLM under pythago and stock Python. See [ci-and-test-image.md](ci-and-test-image.md).

## Pitfalls
- **`signal.signal` works only on Python's main thread,** which is `py`'s init thread, not `Do`'s. Servers that install handlers (vLLM's OpenAI server) must run under `py.Main` or through `py.OnMain`.
- **Scripts need a `__main__` guard** for spawn and forkserver, which re-import `__main__` from its file. A runner that does not set `__file__` hides the bug.
- **Anything Python needs in a forked child must be C.** Go cannot run there (import machinery, signal restore).
- **Validate failures against a stock-Python control with the identical package set.** The flashinfer JIT failure looked like ours until stock CPython failed the same way. See [design-lessons.md](design-lessons.md).
- **vLLM's startup memory profiling asserts when free GPU memory grows.** On the GB10, GPU memory is unified with system RAM shared with other processes; rerun on that flake.
- A 3.13 venv against a 3.14t image fails at `Init`; activation rejects mismatched `pyvenv.cfg` versions.
