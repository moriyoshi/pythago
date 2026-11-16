# Pythago

<img src="./assets/pythago.webp" width="400" />

Pythago embeds CPython in Go programs built with `CGO_ENABLED=0`. It translates a
Python distribution into a native image linked into the Go executable, and can embed
the Python standard library and application code alongside it. Go can call Python,
and Python can call Go functions. Compatible third-party C extensions can also be
loaded from a Python environment on disk.

The project uses [python-build-standalone](https://github.com/astral-sh/python-build-standalone)
distributions and [bao](https://github.com/moriyoshi/bao) to generate the native image.
It supports Linux on amd64 and arm64, with GIL and free-threaded CPython variants.
The generated executable still depends on the system C library; it does not require a
separate libpython shared library.

## How it works

Pythago embeds the native CPython implementation, including its C runtime interfaces.
It uses bao to make that code linkable by the standard Go toolchain, and purego to
cross between Go and the native C ABI. The resulting executable still uses the host's
glibc and other system libraries; `CGO_ENABLED=0` does not mean that CPython has been
rewritten in Go or that the executable is fully static.

```text
python-build-standalone distribution + Pythago C glue
                    │
             pythago-dist / bao
                    │  translate image, edit symbols and relocations,
                    │  generate Go/assembly bindings and embedded stdlib
                    ▼
             generated dist package
                    │  Go linker
                    ▼
 Go application ── purego ── native CPython ── system libc
        ▲                         │
        └── callback trampoline ──┘
```

### Building the image

`pythago-dist` fetches and verifies a python-build-standalone distribution for the
chosen Python version, ABI, and target. In the default `shared` mode, bao takes the
distribution's `libpython` shared object and Pythago's glue as input. It incorporates
the native load image into Go-linkable `.syso` files and generates the Go and assembly
needed to register its symbols. The generated package also embeds the standard
library. Importing that package registers the image and stdlib with `py`.

Bao translates relocations and arranges initialization of the native image, including
its imports, constructors, and thread-local storage. System imports remain dynamically
resolved against the host libraries, with symbol versions selected for the target
glibc compatibility floor. The alternative `lto` mode starts from the distribution's
bitcode and uses LLVM before bao builds the image; its build requirements and glibc
floor can differ from the default mode.

The default build uses the stripped `libpython` from the distribution and prebuilt
glue for supported ABIs. Bao runs in process as a Go dependency. For LTO builds,
the generator reads the distribution's `PYTHON.json`, lowers the listed bitcode
objects with LLVM, generates the builtin-module table and symbol table, and passes
the resulting native objects and dependencies to bao. LLVM is also needed to rebuild
the glue or support an ABI without a prebuilt glue object. Downloads, toolchains, and
lowered objects are cached; generated native images belong to the consuming project.

For the default shared mode, the image targets glibc 2.18 or newer. The LTO mode
currently needs glibc 2.30 or newer because Go's linker cannot preserve weak dynamic
code references for some imports. The extension shim requires glibc 2.28 or newer.
The generator records the selected floor and bao records imports above the symbol
version cap in its manifest. Cross builds use a sysroot for the target architecture.

### Wrapping libc thread functions

CPython normally creates and manages threads through pthread functions. During image
generation, bao redirects the imported `pthread_create`, `pthread_join`,
`pthread_detach`, and `pthread_exit` symbols to Pythago's `__wrap_pthread_*` glue.
For the shared-object input, this includes binding libpython's GOT and PLT import
slots to those wrappers. The glue's `__real_pthread_*` references resolve to the
original libc functions, so fallback calls do not recurse through the wrappers.
Other libc imports continue to resolve to their system implementations.

The wrappers route eligible Python thread creation to Go, where a goroutine is locked
to an OS thread for the lifetime of the Python thread. The C glue coordinates the
start, join, detach, and exit handshake. Threads started before the hook is installed,
in a forked child, or with a non-default stack size fall back to libc. This preserves
CPython's OS-thread-based thread identity; it does not multiplex Python threads onto
a smaller set of OS threads. See [`glue/pythago_glue.c`](glue/pythago_glue.c) and
[`glue/pythago_py.c`](glue/pythago_py.c).

### Calls, thunks, and thread-local state

The image exposes a `pythago_symtab` containing addresses of CPython and glue symbols.
The `py` package uses purego's foreign-call machinery to invoke those addresses with
the C calling convention. C glue handles interfaces that need C-side structure handling,
such as `PyConfig` and `PyStatus`. In the other direction, a C callback trampoline
dispatches Python calls to Go functions. Python objects crossing into Go retain a
CPython reference, released by `Close`.

Native CPython and its dependencies also use thread-local storage. Bao translates
the native TLS relocations and supplies TLS lookup support, including descriptor thunks
where the input uses them. These thunks resolve the calling OS thread's native TLS;
they serve a different purpose from the callback trampoline and the pthread wrappers.
When a goroutine enters Python through `py.Do`, Pythago locks it to an OS thread and
attaches a CPython thread state. `py.Release` temporarily detaches that state around
blocking Go work. The runtime keeps at most one Python thread state per OS thread.
`Config.Slots` bounds concurrent Go entries, `Config.MaxThreads` limits live Python
threads, and `Config.SpareThreads` controls the pool of threads ready for new Python
threads. `py.Do` can be nested, including from Go callbacks invoked by Python.

### Imports and native extensions

Pythago installs an import finder that serves the embedded standard library and
application files from Go `fs.FS` values. For native extensions on disk, the executable
must also make the embedded CPython C API visible to the system dynamic loader. Pythago
builds a code-free ELF shared object containing absolute addresses for the exported
`Py*` symbols, writes it to a new temporary directory, loads it with `RTLD_GLOBAL`,
and removes the file. Compatible extensions can then resolve their Python API
references when loaded normally. `Config.ExtensionShimDir` chooses the directory
(default `$TMPDIR`, falling back to a memfd), and `Config.ExtensionShimMemfd` loads
from a memfd, for systems with no writable directory; gdb hangs attaching to a program
whose shim came from a memfd.
This mechanism requires glibc 2.28 or newer; see
[`py/internal/extshim`](py/internal/extshim/extshim.go).
The shim exposes Python API symbols rather than bundled dependencies such as OpenSSL.
Extensions and their dependent libraries stay on disk with their directory layout
intact, including libraries that use `$ORIGIN`-relative paths.

Some extensions remember the thread they were first used on. pyreqwest bundles
mimalloc, which takes that thread as its main thread; if the thread exits, a later
thread can share its heap and crash, under stock CPython too. Import such an extension
on a thread that outlives the others. Under CPython that is the main thread; in pythago,
a `py.Do` call, whose OS threads stay alive, rather than a Python thread or an ASGI
worker that may exit.

The importer builds an immutable index at initialization. Changes to `Config.Files`
after initialization are not reflected in imports; use search paths or an environment
on disk for files that must change while the interpreter is running. Python's isolated
configuration starts in UTF-8 mode. Embedded package resources should be accessed
through `importlib.resources` or `pkgutil.get_data`.

### Processes and signals

Python sees the Go executable as `sys.executable`. `py.Run` dispatches re-executed
Python children to `py.Main`, so Python command-line execution and multiprocessing
`spawn` and `forkserver` workers can use the same image. `Config.Modules`,
`Config.Environment`, the configured filesystems, and `Config.Setup` also apply to
those children; modules created only after initialization are not inherited by a
re-executed child. `py.Main` parses the command line as `python -I` does, so `PYTHON*`
environment variables such as `PYTHONTRACEMALLOC` are ignored, and `-X` options apply:
`-X dev` (with the memory allocators' debug hooks), `-X faulthandler`,
`-X tracemalloc[=N]`, `-X perf` and `-X perf_jit` (Linux), `-X int_max_str_digits=N`
and the rest.

The default multiprocessing start method is `forkserver`; set
`Config.MultiprocessingStart` to `"default"` to retain Python's default. **A child
created by `fork` without exec cannot call Go:** Go functions raise `RuntimeError`
there, and new threads use libc. Select `spawn` or `forkserver` when a child needs Go
callbacks.

Python's main thread is Pythago's dedicated initialization thread. Use `py.OnMain`
for operations that require it, such as installing Python signal handlers. Go retains
the process's signal handlers by default; `Config.PythonSignals` lets Python install
its own, including a SIGINT handler that raises `KeyboardInterrupt`.

## Quick start

Requirements: Go 1.26 (the module specifies toolchain 1.26.11), Linux on amd64 or
arm64, and network access to fetch the Python distribution and Go dependencies. The
default image requires glibc 2.18 or newer; loading C extensions requires glibc 2.28
or newer. On older glibc, set `py.Config.NoExtensions` to disable extension loading.

From a checkout of this repository:

```sh
go generate ./examples/hello
CGO_ENABLED=0 go build -o hello ./examples/hello
./hello
```

The first command generates the CPython image and embedded standard library in
`examples/hello/pydist`. Generated images are not distributed with the source and can
be large. The example embeds a Python package, exposes a Go function as `host.shout`,
and calls the Python function `hello.greet`. Its output resembles:

```json
{"banner": "hello from an embedded fs.FS", "greeting": ["GOPHER!", "PYTHON 3.13.18!"]}
```

The Python patch version depends on the downloaded distribution. See
[`examples/hello`](examples/hello) for the complete Go and Python code.

## Generating a Python image

Generate a package inside your own Go module, then import it for its registration
side effects:

```sh
go run github.com/moriyoshi/pythago/cmd/pythago-dist \
  -python 3.13 -target linux/amd64 -o ./pydist
```

```go
import _ "example.com/your-module/pydist"
```

Select `linux/arm64` for an arm64 target. Cross builds require `-sysroot` pointing to
a glibc sysroot for the target. On a Mac, select `darwin/arm64` or `darwin/amd64`
(shared mode only): the image is PBS's `libpython3.X.dylib` translated together with
the prebuilt glue. bao reads the macOS SDK, so the Command Line Tools must be installed;
`-sdk` names the SDK when it is not xcrun's. Use `-variant freethreaded` for a free-threaded distribution and `-mode lto`
for the alternative LTO build. The default `shared` mode uses bao in process and
prebuilt glue for supported ABIs; other cases may need LLVM, which the generator can
fetch. Run `go run ./cmd/pythago-dist -h` in this
checkout for all options, including local distribution and toolchain overrides.

For Windows, select `windows/arm64` or `windows/amd64` (the latter has not run outside
the Windows CI jobs). A Windows image is not translated by bao: it is a
set of files, PBS's `python3XY.dll` with its C runtime DLLs and the standard library's
extension modules (`DLLs\`), plus the prebuilt glue as `pythago_glue.dll`. The package
embeds them, and `py.Init` writes them once per image to the user's cache directory
(`%LOCALAPPDATA%\pythago\image-<digest>`) and loads them with the Windows loader. That
keeps `python3XY.dll` a module the loader knows by name, so extension modules, and the
DLLs they load, bind to the same interpreter, as they would under `python.exe`.
`Config.ImageCacheDir` chooses another directory; `Init` removes the images there that
no running process uses, such as those of earlier builds. On Windows, CPython starts its
own threads (none is goroutine-backed, and `Config.MaxThreads` does not apply),
multiprocessing has only `spawn`, and with `Config.PythonSignals` off `py` hands
`SIGINT` back so that Ctrl+C reaches Go. `aio`,
`asgi`, `wsgi` and `workers` work there too: subprocess pipes are overlapped and served by
Go's I/O completion port, and a worker accepts on the inherited socket with an overlapped
`AcceptEx` and serves each connection through the completion port.

Generated images for different targets can coexist in the same package. Generate
them locally or in CI, or choose to commit them in your own project. An image carries the
glue of the `pythago-dist` that generated it: when an upgrade of pythago changes the
glue's ABI, `py.Init` fails with "the native image's glue has ABI N, but this package
needs M". Regenerate the image with the matching `pythago-dist`; ABI 4 (2026-11-19)
decodes every argument of a Go function, where earlier images decoded at most 8.
`-bao` or `PYTHAGO_BAO` can select a bao executable instead of the bundled Go API;
`-llvm` or `PYTHAGO_LLVM` can select LLVM. `-print-toolchain` reports the selected
tools. The normal shared build uses the prebuilt glue; [`ci/glue.sh`](ci/glue.sh)
checks that those objects match the glue source.

## Using Python from Go

Configure the interpreter with `py.Config` and use `py.Run` to manage its lifetime.
Enter Python with `py.Do`; operations on Python objects belong inside `py.Do` or a Go
callback invoked by Python. A `*py.Object` owns a reference, so close it when finished.
An interpreter can be initialized only once per process.

`Config.Files` accepts an `fs.FS`, such as an `embed.FS`, for Python application
modules. These files have virtual paths: Python code should use
`importlib.resources` or `pkgutil.get_data` for package data. `Config.Modules` exposes
Go values and functions as Python modules. `Config.Slots` limits concurrent Go
entrants to Python; Python-created threads run on goroutines locked to OS threads.

For a complete example of initialization, imports, calls, conversion, and cleanup,
see [`examples/hello/main.go`](examples/hello/main.go) and the
[`py` package documentation](py/runtime.go).

The `py` API provides `Exec`, `Eval`, and `Import` for Python code; object methods
for attributes, items, calls, and conversions; and `Func` and `NewModule` for exposing
Go code. Python exceptions become Go errors, while errors and panics from Go callbacks
become Python exceptions. See the package documentation for ownership and threading
requirements.

A `Func` takes any number of positional and keyword arguments. They are borrowed: the
`[]*py.Object`, the `py.Kwargs` and every `*py.Object` in them are valid only until the
`Func` returns, and later calls reuse their memory. Keep an argument with `NewRef`, or keep
a converted value. Using a borrowed argument after its call has returned panics ("a
borrowed *Object was used after the call that lent it returned") until a later call reuses
its memory; from then on it reads that call's argument. `Config.CheckBorrows` never
reuses the memory, so such a use always panics. It costs an allocation per call, so use it
in tests and debugging.

### Sharing buffers

For data outside channel operations, `py.WithView[T]` borrows a Python buffer for the
duration of a callback. `Object.Buffer` and `py.View[T]` provide explicit lifetime
control; do not use exported slices after closing the buffer. Conversely,
`py.NewBuffer[T]` exposes a Go slice to Python and pins it until the Python object is
released. Typed views require C-contiguous memory and a compatible element format.
Neither side locks shared memory, so callers must synchronize concurrent access. See
[`py/buffer.go`](py/buffer.go) for the ownership and format requirements.

## Installing Python packages

For packages that live on disk, create an environment matching the image's Python
version and ABI, and optionally pass pip arguments after the tool's flags:

```sh
go run github.com/moriyoshi/pythago/cmd/pythago-env \
  -python 3.13 -env ./venv install requests
```

Set `py.Config{Environment: "./venv"}` when starting the interpreter. Use
`-freethreaded` when the native image is free-threaded. The tool can also create an
environment without installing packages; run `go run ./cmd/pythago-env -h` for its
options. Large native packages and their shared libraries remain on disk rather than
being embedded in the executable.

The environment tool uses a matching python-build-standalone interpreter to create
the environment and run pip, so pip resolves and installs wheels for that interpreter's
ABI. When activated, Pythago adds its site-packages using `site.addsitedir`, puts its
`bin` directory on `PATH`, and sets `VIRTUAL_ENV`. Existing environments can be used
when their Python version and ABI match. GIL and free-threaded wheels are not generally
interchangeable; some packages may also enable the GIL on import.

Workloads that compile native code at runtime, such as `torch.compile`, may need a
matching Python installation and headers on disk, supplied through `Config.Home` or
`PYTHONHOME`; the embedded stdlib alone does not contain those headers.

## Asyncio and ASGI

The `aio` package provides Go-backed asyncio event loops. Call `aio.Configure(&cfg)`
before initialization to make the Python `goaio` module available. `GoEventLoop`
uses goroutines for I/O and Go's network resolver, including pipes and asyncio
subprocesses. Its TLS is done by Go's `crypto/tls` when an `ssl.SSLContext` can be
mapped faithfully (certificates, trust anchors such as certifi's bundle, ALPN,
versions, verification), and by asyncio's own `SSLProtocol` otherwise (Python callbacks,
password-protected keys, CRL checks, `SSLContext` subclasses, and `start_tls`). Either
way, verification follows the context and failures raise the same
`ssl.SSLCertVerificationError`. `GoSelectorEventLoop` runs Python's selector loop over
Go's netpoller. `goaio.run(coro, selector=False)` starts a loop.
ASGI workers each run a Python event loop and their own application lifespan.

Native async libraries work on both loops when they complete futures through
`call_soon_threadsafe`. The tests run pyreqwest 0.15.1, whose futures tokio completes
from its own threads, on every loop, on several threads at once and inside ASGI apps
under load. Both loops have `add_reader` and `add_writer`: `GoEventLoop` watches the fd
on Go's netpoller as `GoSelectorEventLoop` does, and the readiness comes back with its
other completions. So `zmq.asyncio` (pyzmq 27.3.0, tested) works on both, including in
ASGI apps, whose workers run `GoEventLoop`. `GoEventLoop` still has no `sock_*` methods
(asyncio's `BaseEventLoop` raises `NotImplementedError` for them); use streams, or
`GoSelectorEventLoop`.


Go channels passed through `py.From`, function results, or `Config.Modules` become
Python awaitables after `aio.Configure`. Use ordinary Go channel operations on the
Go side; `make(chan T)` gives an unbuffered rendezvous, and `make(chan T, n)` gives
capacity `n` with Go FIFO, backpressure, and close behavior. For example, expose
two channels before initializing Python:

```go
requests := make(chan string)
replies := make(chan string, 8)
cfg := py.Config{Modules: map[string]map[string]any{
    "host": {"requests": requests, "replies": replies},
}}
aio.Configure(&cfg)
// Initialize Python with cfg, then start Go producers/consumers:
go func() {
    requests <- "hello"
    fmt.Println(<-replies) // HELLO
    close(requests)
}()
```

```python
import goaio
from host import requests, replies

async def main():
    async for message in requests:
        await replies.send(message.upper())

goaio.run(main())
```

Python can receive with `await channel` or `await channel.recv()`, send with
`await channel.send(value)`, and close with `channel.close()`. `recv_ok()` returns
`(value, ok)`, including the Go zero value after close; `recv()` instead raises
`goaio.ChannelClosed` after buffered values drain. Async iteration ends at close.
Sending to a closed channel and closing twice raise `RuntimeError`. Send-only and
receive-only Go channel directions are enforced. A nil channel waits until cancelled.

Channel awaits currently require `goaio.GoEventLoop`. Immediately ready operations
return directly without allocating a Future or worker. Each pending operation parks
a goroutine; cancellation and loop shutdown stop that wait without closing the Go
channel. As with a Go `select`, cancellation racing a transfer may leave the transfer
committed. Collecting a Python wrapper releases its handle without closing the channel.
Go-to-Python values use `py.From`; Python-to-Go values support data scalars, bytes,
lists/tuples, and dictionaries with checked integer conversions to the channel element
type. Arbitrary Python objects cannot be sent to Go channels.


Channel dispatch uses concrete type variants and generic native Go selects, without
reflection in channel operations or element conversion. Automatic variants cover
`bool`, `string`, `any`, signed and unsigned integer types, `uintptr`, `float32`,
`float64`, `[]byte`, `[]int`, `[]string`, `[]any`, `map[string]string`,
`map[string]any`, `map[any]any`, `*py.OwnedBuffer`, and `*py.String`, in all three
channel directions.
For custom element types, register typed conversions before initializing Python:

```go
aio.RegisterChannelType[MyType](decodeMyType, encodeMyType)
// decodeMyType: func(any) (MyType, error), receiving data from py.Object.Go
// encodeMyType: func(MyType) any, returning data accepted by py.From
```

These callbacks work on Go data without calling Python APIs. A nil decoder uses a
type assertion to `MyType`; a nil encoder passes values through to `py.From`.
Named channel types should be converted to `chan T`, `<-chan T`, or `chan<- T`
before being passed to Python.

For large binary messages, use `chan *py.OwnedBuffer` to share memory without a payload
copy. Python sends any C-contiguous buffer object (bytes, bytearray, or memoryview);
a Go receiver gets an owned lease and must release it after use:

```go
lease := <-buffers // buffers is a chan *py.OwnedBuffer passed to Python
func() {
    defer lease.Close() // may run on an ordinary goroutine; Python must be running
    data, err := lease.Bytes()
    if err != nil { panic(err) }
    consume(data) // data is valid while lease remains open
}()
```

In the other direction, send `py.NewOwnedBuffer(data, readOnly)` through the channel.
Python receives a `memoryview`, which it can release explicitly with `view.release()`
or by collection. Successful buffer sends transfer ownership: the sender must stop
using mutable source memory until the receiver releases it or acknowledges completion.
`ReadOnly()` tells Go receivers whether writes are forbidden. Buffer exports prevent
Python exporters from resizing until release. Cancelled, uncommitted sends release
their leases; a transfer racing cancellation may already have committed. Close Go
leases before finalizing Python. Ordinary `chan []byte` continues to copy payloads.

Use `chan *py.String` to retain Python Unicode directly. Python sends a `str`
and receives the same object when Go forwards it, preserving subclasses and even
unpaired surrogates without encoding to UTF-8. A Go receiver can forward the lease
or call `Close()`; `GoString()` produces an independent Go UTF-8 copy only when
requested. `Clone()` retains another lease over the same immutable Unicode object.
`Len()`, `Rune(index)`, `Runes()`, and `Slice(start, end)` operate on Unicode code
points, including surrogates. `Len`, `Rune`, and `Runes` read the native Unicode
representation directly in Go using pre-derived layouts; they do not enter Python
and do not cache object metadata. Indices are zero-based, and slices use Go-style
half-open bounds. `Runes()` performs one bulk
copy/widening into an independent Go rune slice, without an intermediate UTF-8 encoding.
Go creates a lease with `py.NewString(text)` after initializing Python; this
performs the initial Go-to-Python conversion once. These methods can run on
ordinary goroutines. Successful sends transfer ownership, and owned leases must
be closed before Python finalization. `None` maps to a nil `*py.String`, with
`recv_ok()` distinguishing it from a closed channel. Ordinary `chan string` retains
its UTF-8 copying behavior. Outside channels, `Object.Unicode()` and
`Object.OwnedBuffer()` take the same leases from a `str` or a buffer exporter, and
`TakeObject()` hands one back to Python. The `Object` accessors `UnicodeLen`,
`UnicodeRune`, and `UnicodeRunes` read a `str` natively in the same way.

The `asgi` package serves an ASGI application as a Go `http.Handler`. Call
`asgi.Configure(&cfg)` before initializing Python, then create a server with
`asgi.New("module:app", asgi.Options{})`. It supports ASGI lifespan, HTTP and
WebSockets; use its `Close` method during shutdown. An HTTP request runs on the
goroutine that serves it, as an eager task on its worker's event loop, and requests that
arrive meanwhile run in the same batch; a request that awaits real I/O continues on the
worker's loop thread. Application code therefore runs on whichever thread served the
batch, so rely on contextvars rather than `threading.local`, or set `Options.LoopThread`
to run every request on the loop thread (slower: 46.5k against 58.5k req/s on one core). To use
several cores with the GIL, serve from several processes with the `workers` package:
`workers.Spawn` listens once and supervises copies of the executable that inherit the
listener (`workers.InheritedListener`, called before `py.Init`) and shut down when
`workers.NotifyContext`'s context is done (SIGTERM; on Windows, which has none, a pipe the
supervisor closes), as uvicorn's `--workers` does; processes are spawned, not forked,
because the Go runtime does not survive `fork()`. It works for any `http.Handler`. See the package documentation in
[`aio`](aio/aio.go) and [`asgi`](asgi/asgi.go) for configuration and lifecycle details.


### Go-owned request bodies (opt-in)

`py.Config.FusedBytes` (0, off, by default) lets `asgi` read request bodies straight into
Python `bytes` objects: Go reads a body chunk into memory laid out as a `bytes` object,
and the conversion makes that memory the object, with no copy. When Python frees it, the
memory goes back to Go for the next body of the same size class. Nothing in an
application changes: `receive()` still yields ordinary `bytes`.

```go
cfg := py.Config{FusedBytes: 16 << 20} // up to 16 MiB of Go memory for request bodies
asgi.Configure(&cfg)
```

- **When it helps:** bodies of 16 KiB and more. On one core (linux/arm64, `asgiserve`
  with `GOMAXPROCS=1`, 64 connections), 16 KiB uploads gain 5-10% req/s and 64 KiB
  uploads 12-16%, on both CPython 3.13 and 3.14t; on the loop thread (`Options.LoopThread`)
  p99 latency for 64 KiB falls from about 6 ms to 2.4 ms. Bodies of 100 bytes and plain
  GETs are at parity. Bodies over 64 KiB per chunk (`asgi.Options.BodyChunk`), and bodies
  that find no room, are copied as before.
- **What it costs:**
  - It wraps Python's object allocator: every `PyObject_Free` pays a bounds check,
    about 1% on allocation-heavy Python code (0.6-2% on free-heavy loops, at most 1.5%
    on a request).
  - `py.Init` fails if the image's CPython is not one the design is verified on
    (3.13 with the GIL, 3.14 free-threaded), or if its startup check of CPython's
    lock-free readers fails. Opting in never silently does nothing.
- **Sizing:** the memory comes in 1 MiB chunks, made as size classes need them and given
  back to Go after they have been empty for a few seconds (two stay up to 20 seconds), so
  the cap costs nothing until it is used. Chunks in use count as live Go heap: they raise
  the GC pacer's heap goal and count against `GOMEMLIMIT`. 8-16 MiB is plenty for one
  process: 64 concurrent uploads used 2 to 6 MiB. A cap of 2 MiB or less under mixed body
  sizes falls off a cliff on free-threaded builds (memory changes size class only after a
  Python garbage collection, which the allocator then has to run every few milliseconds).
- **Free-threaded builds** give memory a new size class, or back to Go, only after a
  Python garbage collection has run since its last use. When none runs (a server's
  allocations and frees balance, so collections are rare) the allocator runs one itself,
  at most every 10 seconds and only while more than two empty chunks wait. With
  `gc.disable()` it cannot (`PyGC_Collect` does nothing then): once the cap is reached,
  bodies of other sizes are copied until the application calls `gc.collect()`.
- **Incompatible with tools that replace Python's object allocator** without wrapping the
  one installed (`PyMem_GetAllocator`, then chaining to it, as `tracemalloc` does).
- Only the main interpreter adopts the memory: in a subinterpreter the body is copied.

The design, its soundness argument and its tests are in `internal/fusion`'s package
documentation.

## WSGI

The `wsgi` package serves a WSGI application (PEP 3333: Flask, Django, any framework
with a WSGI entry point) as a Go `http.Handler`. After initializing Python, create a
server with `wsgi.New("module:app", wsgi.Options{})` and call its `Close` method during
shutdown. Each request runs the application on the goroutine net/http serves it on, so
the server is a thread pool whose size is `py.Config.Slots`; raise it for applications
that block in Python, such as database calls. Reading the request body and writing the
response release the GIL and the slot while the client is slow. Each chunk the
application yields is flushed before the next, as PEP 3333 requires; a list or tuple
result is sent with a Content-Length instead. `wsgi.file_wrapper` (Flask's `send_file`)
sends regular files with sendfile. Malformed statuses and headers, and hop-by-hop headers,
are the application's errors: a 500 before anything is sent, a dropped connection after.
Request headers with underscores are dropped, as nginx and gunicorn do, and each request
runs in a fresh `contextvars` context; `threading.local` values persist on a thread
across requests, as with any threaded WSGI server. `workers.Spawn` runs several
processes. Set `http.Server` timeouts (at least `ReadHeaderTimeout`) for either server.
See [`wsgi`](wsgi/wsgi.go) and `internal/tools/wsgiserve`, a gunicorn-like command.

## Type stubs

The Python modules pythago provides exist only inside a pythago program, so a type
checker cannot find them on its own. [`typing/`](typing) is a stub-only distribution,
`types-pythago`, covering `goaio`, `goaio_tls`, `pythago_asgi`, `pythago_wsgi` and the
Go modules behind them. Install it where the type checker runs (`pip install ./typing`
until it is published). `goaio.Channel[T]` types a Go channel's awaitable; it exists only
for type checking, as each channel has a class of its own at runtime. The tests check the
stubs against the live modules, and `ci/typecheck.sh` checks pythago's own annotated
Python sources against them with mypy.

## Server benchmarks

`bench/http/run.sh` serves the same small applications ([`bench/uvicorn/app.py`](bench/uvicorn/app.py)
and [`bench/wsgi/app.py`](bench/wsgi/app.py)) from `asgi` and `wsgi` (through
`internal/tools/asgiserve` and `wsgiserve`) and from uvicorn and gunicorn on stock
CPython, over plain HTTP and TLS. The WSGI servers each get 10 threads, the size of
a2wsgi's pool. "sleep" overlaps requests: 1 ms `asyncio.sleep` for ASGI, and 10 ms
`time.sleep` for WSGI, which caps a 10-thread server at about 1,000 req/s. "new
connection" opens a connection per request; over TLS each makes a full handshake. The
`GOMAXPROCS=2` columns are not pythago's default. Rerun with the `bench-http` skill
(`.agents/skills/bench-http`).

In the latest run, on one core:
- **`asgi` against uvicorn with uvloop and httptools:**
  - Keep-alive traffic: 0.79–0.97× over plain HTTP, 0.79–1.09× over TLS.
  - New connections: faster, 1.83× plain and 1.32× over TLS.
  - Weakest on 64 KiB responses (0.79×).
- **`asgi` against uvicorn on asyncio and h11:** 1.4–2.6×.
- **`wsgi`:** 2.2–4.9× uvicorn's WSGI mode, and 1.6–2.8× gunicorn's threaded worker.
  The sleep rows are capped by the 10 threads.
- **`GOMAXPROCS=2`:** slower for both servers on one core, most of all for 64 KiB WSGI
  responses.

<!-- bench-http:begin -->
CPython 3.13.18 (GIL), one server process pinned to one core (core 7, a Cortex-X925), load from cores 15-18 (`internal/tools/httpload`, HTTP/1.1 keep-alive). Median requests per second of 3 interleaved rounds of 5 s. uvicorn 0.54.0, uvloop 0.23.0, httptools 0.9.0, a2wsgi 1.10.10, gunicorn 27.0.1 on stock CPython (OpenSSL 3.5.9 29 Sep 2026); pythago c85fb11 with go1.26.11; Linux 7.0.0-1019-nvidia; 2026-11-17.

**ASGI**

| Workload | uvicorn uvloop+httptools | uvicorn asyncio+h11 | pythago asgi | pythago asgi GOMAXPROCS=2 | pythago asgi ÷ uvicorn uvloop+httptools |
|---|---|---|---|---|---|
| hello c1, plain | 38.5k | 21.0k | 35.4k | 31.2k | 0.92 |
| hello c64, plain | 68.8k | 23.8k | 56.2k | 50.2k | 0.82 |
| 64 KiB response c64, plain | 47.7k | 20.9k | 37.8k | 35.5k | 0.79 |
| 16 KiB upload c64, plain | 44.3k | 18.8k | 36.0k | 17.3k | 0.81 |
| sleep c64, plain | 32.4k | 16.7k | 31.4k | 28.6k | 0.97 |
| new connection c16, plain | 18.3k | 12.9k | 33.5k | 28.9k | 1.83 |
| hello c1, TLS | 33.8k | 17.8k | 34.8k | 29.8k | 1.03 |
| hello c64, TLS | 51.5k | 20.3k | 53.6k | 46.2k | 1.04 |
| 64 KiB response c64, TLS | 25.7k | 14.7k | 20.3k | 19.4k | 0.79 |
| 16 KiB upload c64, TLS | 37.2k | 15.5k | 33.3k | 22.1k | 0.90 |
| sleep c64, TLS | 28.0k | 15.6k | 30.5k | 28.4k | 1.09 |
| new connection c16, TLS | 4.1k | 3.0k | 5.5k | 4.7k | 1.32 |

**WSGI**

| Workload | uvicorn a2wsgi uvloop+httptools | gunicorn gthread | pythago wsgi | pythago wsgi GOMAXPROCS=2 | pythago wsgi ÷ uvicorn a2wsgi uvloop+httptools |
|---|---|---|---|---|---|
| hello c1, plain | 11.5k | 21.1k | 40.7k | 35.3k | 3.55 |
| hello c64, plain | 14.3k | 23.8k | 64.2k | 55.6k | 4.49 |
| 64 KiB response c64, plain | 12.9k | 20.4k | 41.2k | 9.4k | 3.18 |
| 16 KiB upload c64, plain | 11.3k | 20.8k | 36.5k | 18.7k | 3.24 |
| sleep c64, plain | 912 | 952 | 960 | 868 † | 1.05 |
| new connection c16, plain | 10.0k | 14.3k | 35.4k | 29.9k | 3.54 |
| hello c1, TLS | 10.6k | 19.2k | 38.0k | 31.7k | 3.60 |
| hello c64, TLS | 12.4k | 21.4k | 60.3k | 54.3k | 4.86 |
| 64 KiB response c64, TLS | 9.5k | 13.5k | 21.4k | 7.5k | 2.25 |
| 16 KiB upload c64, TLS | 9.1k | 17.4k | 34.0k | 21.0k | 3.73 |
| sleep c64, TLS | 914 | 948 | 958 | 836 | 1.05 |
| new connection c16, TLS | 2.7k | 2.1k | 5.3k | 4.3k | 1.96 |

† half the rounds or more were suspect, so the median may be off. 13 of 288 runs were suspect: 13 had other processes on over 5% of the server's core. A cell's median comes from its other rounds unless marked †; `bench/http/summarize.py` lists the runs.

TLS 1.3 cipher suites negotiated: TLS_AES_256_GCM_SHA384 (gunicorn gthread, uvicorn a2wsgi uvloop+httptools, uvicorn asyncio+h11, uvicorn uvloop+httptools); TLS_AES_128_GCM_SHA256 (pythago asgi GOMAXPROCS=2, pythago asgi, pythago wsgi GOMAXPROCS=2, pythago wsgi).
<!-- bench-http:end -->

## Comparison with other Go–Python projects

Go–Python projects make different choices about where Python runs, how values cross
the boundary, and how applications are distributed. The comparison below describes
their documented approaches; it is not a performance benchmark. Follow the links
for each project's current platform and version support.

| Project | Runtime and deployment | Relationship to Pythago |
| --- | --- | --- |
| **Pythago** | Native CPython image linked into a `CGO_ENABLED=0` executable; the system C library is required. Compatible native extensions load from disk. | Bidirectional in-process calls, embedded Python files, Go-backed asyncio, awaitable Go channels, and ASGI and WSGI integration. |
| [goccy/go-python](https://github.com/goccy/go-python) | CPython is compiled to WebAssembly and transpiled into Go. The interpreter and a trimmed standard library are bundled without cgo or a native Python installation. | Provides isolated interpreter instances and configurable host access. Its Wasm-derived runtime has a different ABI, so ordinary native Linux wheels cannot be assumed to work unchanged. |
| [go-python/cpy3](https://github.com/go-python/cpy3) | Go bindings to the native CPython C API using cgo. | Exposes CPython functions and macros; applications arrange the native Python build and deployment. Pythago also generates the interpreter image and supplies higher-level runtime integration. |
| [kluctl/go-embed-python](https://github.com/kluctl/go-embed-python) | Embeds a Python distribution, extracts it, and executes Python in a separate process without cgo. | Bundles Python for deployment and provides process separation. Pythago offers in-process Python objects and callbacks; applications using a subprocess exchange data through IPC. |
| [i2y/pyffi](https://github.com/i2y/pyffi) | Calls an installed native CPython through purego without cgo. | Provides object conversion, callbacks, asyncio support, and generated bindings. Pythago also links the interpreter image into the executable and integrates Go channels and networking with asyncio. |
| [go-python/gopy](https://github.com/go-python/gopy) | Generates CPython extension modules and Python wrappers for Go packages. | Primarily makes Go packages callable from Python; Pythago primarily embeds Python in Go applications. gopy documents that Python callbacks passed into Go must be called before the Go function returns. |
| [Pygolo](https://gitlab.com/pygolo/py) and [py4go](https://github.com/tliron/py4go) | Embed or extend native Python with Go, with APIs for calls and value conversion. py4go uses cgo. | Prior work on bidirectional interoperability and object lifetime. Pythago additionally packages its CPython image and integrates asyncio and ASGI. |

Pythago's native image and extension loader aim to combine a cgo-free Go build with
the native Python ecosystem. The loader exports CPython symbols through a generated
ELF shim, allowing compatible extensions from an environment on disk to resolve them.
Compatibility still depends on the Python version, ABI, architecture, system libraries,
and extension. The project's notes record tests with NumPy, cffi, PyTorch, and vLLM;
it does not imply that every wheel works. See
[`.agents/docs/LTM/environments-and-ml-stacks.md`](.agents/docs/LTM/environments-and-ml-stacks.md)
and [`ci/gpu.sh`](ci/gpu.sh) for the recorded work and test coverage.

Pythago also connects Python concurrency to Go: `goaio` uses Go networking and
exposes typed Go channels as awaitables, while `asgi` and `wsgi` serve Python
applications as Go HTTP handlers. Python-created threads are backed by goroutines locked to OS
threads; each still occupies an OS thread. The GIL and free-threaded variants are
separate images, and extensions must match the selected ABI.

These choices have limits. Pythago currently supports Linux and macOS, amd64 and arm64,
and Windows arm64 and amd64 (arm64 checked under Wine; both run natively in the Windows
CI jobs),
requires the consumer to generate an image, initializes one interpreter per process, and
runs native Python code with the host process's privileges. The default Linux image
requires glibc 2.18 or newer; loading C extensions requires glibc 2.28 or newer. On
macOS, the extension shim loads (about 0.3-0.5 s, once per process) at the first
extension import, `dlopen` of a library not loaded yet, C API lookup through ctypes
(a C API name through `ctypes.pythonapi` or `CDLL(None)`), subinterpreter, or fork;
programs that do none of these start in about 0.045 s, `import ctypes` included.
Subinterpreters (`concurrent.interpreters`) import from the embedded standard library and
the application's file systems like the main interpreter, but cannot call Go functions.
Goroutine-backed Python
threads get 8 MiB C stacks (Go's threads) where stock CPython asks for 16 MiB. A separate
process can provide a process boundary, while goccy/go-python documents independent
interpreter instances and host capability controls. Pythago does not provide those
isolation properties.

Related interpreter projects include [gpython](https://github.com/go-python/gpython),
a partial Python 3.4 implementation in Go with multiple interpreter contexts, and
[Grumpy](https://github.com/google/grumpy), an archived Python 2.7-to-Go compiler and
runtime that does not support CPython C extensions.

## License

Pythago is licensed under the [MIT License](LICENSE).

### Licensing generated applications

A Pythago-generated executable embeds CPython and may include code from other
libraries in the selected [python-build-standalone distribution](https://docs.astral.sh/python-build-standalone/).
Those components retain their own licenses even when the application is licensed
under MIT. CPython is generally covered by the [Python Software Foundation License
Version 2](https://docs.python.org/3/license.html): retain its license and copyright
notice when distributing Python, and describe changes if distributing a derivative
version of Python.

Review the licenses and notices for the exact PBS release, platform, build mode,
and components included in your application. In particular, PBS releases before
2023 may include GPL-licensed readline or GDBM; current PBS builds avoid those
dependencies. See the [PBS licensing guidance](https://docs.astral.sh/python-build-standalone/running/#licensing)
and the full distribution's `PYTHON.json` metadata. Missing license metadata does
not establish that a component has no license obligations.

`pythago-dist` copies available PBS license files into the generated package's
`licenses` directory. When distributing an executable, include the applicable
license texts and notices with the distribution; do not assume those files are
embedded in the executable. Also review the terms of any other bundled Go, Python,
or native dependencies.

## Development

The unit CI script generates test images and runs the checks for one Python variant:

```sh
ci/unit.sh gil  # CPython 3.13
ci/unit.sh ft   # free-threaded CPython 3.14
```

These checks can download distributions and toolchains, and also build the other
architecture's image and tests, which they run under qemu-user when it is available
(`PYTHAGO_OTHER_ARCH=run|build|none` overrides that; CI uses `build`, since it has native
runners for both). `ci/floor.sh` checks glibc requirements and runs against older
glibc loaders. `ci/gpu.sh gil|ft` exercises PyTorch and, on the GIL build, vLLM on an
NVIDIA GPU, comparing applicable checks with the environment's stock Python.
`ci/typecheck.sh` type-checks the stubs and the annotated Python sources with a pinned
mypy (it needs uv). `ci/darwin.sh gil|ft [arm64] [amd64]` syncs the tree to a Mac per architecture over ssh
(`PYTHAGO_DARWIN_ARM64_HOST`, `PYTHAGO_DARWIN_AMD64_HOST`) and runs the py, aio, asgi
and wsgi tests there against a darwin image. `ci/darwin.sh glue` checks the darwin glue
objects on the arm64 Mac, as `ci/glue.sh` does the Linux ones; in GitHub Actions,
`ci/darwin-gha.sh` runs the same tiers on the macOS runner itself. `ci/windows-wine.sh
gil|ft` runs the py, aio, asgi, wsgi and workers tests for windows/arm64 under Wine 11 in
a container on an arm64 Linux host (`image` builds the container, `sdk` copies out the headers and import libraries the
Windows glue DLL is built with). The `windows` CI jobs cross-build the same test binaries
on Linux (`ci/windows-build.sh`) and run them on Windows (`ci/windows-run.ps1`): arm64
and amd64 natively, and amd64 (gil) in a Windows Server Core container. `PYTHAGO_MODE` selects the shared or LTO image for CI. See [`ci/`](ci) for the scripts
and [`.agents/docs/LTM/INDEX.md`](.agents/docs/LTM/INDEX.md) for recorded results and
implementation history.

The image generator and glue depend on CPython initialization, thread-state, signal,
and multiprocessing behavior that must be checked when adopting a new Python minor
version. The TLS and extension-loading paths also depend on bao and glibc behavior;
native amd64 Linux and arm64 kernels with larger pages require their own verification.
Embedded-wheel provisioning,
and ahead-of-time
linking of additional native extensions are future work.

Channel bridge benchmarks and before/after measurements are recorded in
[`bench/channels/optimization-results.txt`](bench/channels/optimization-results.txt).
Native string and buffer access measurements are in
[`bench/channels/native-views-results.txt`](bench/channels/native-views-results.txt).

Native Unicode layouts are derived ahead of time from matching CPython headers;
see [`internal/layout/README.txt`](internal/layout/README.txt) for supported builds
and regeneration. Python-backed buffer accessors read the active `Py_buffer`
record directly. `py.Buffer.Shape()` and `Strides()` are read-only native views
valid until export close; metadata accessors return zero values after close.
