# Design

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- **Prefer the design that matches the embedded runtime's assumptions over one that
  emulates them.** VTs were correct only where we had found and wrapped every assumption,
  and finding them took a long chain of hangs. Before building a scheduler under someone
  else's runtime, list the assumptions it would break: ownership keyed by OS thread, lock
  wake order, which stacks callbacks may run on.
- **The one-thread-state-per-OS-thread rule holds for 1:1 designs too.** Any cache of
  thread states per OS thread must give way when CPython binds its own thread state to that
  thread; otherwise the VT refcount race comes back.
- **Admission must cover blocking.** A slot held across blocking Go code starves the
  other callers; whatever detaches the thread state should also release the slot.
- **Time the stages before redesigning a slow path.** Moving the thread handshake into C
  rested on a guess (the channel) and gained 15%. A three-timestamp breakdown then showed
  that 80% of the time was Go creating the OS thread, and spare threads fixed that.
- **Benchmark free-threaded CPython against stock, using distributions.** Lock convoys make
  a single run vary 4–10×. Compare medians from the same machine before calling a gap a
  regression.
- **Compare against the plain baseline early.** 1:1 OS threads were within about 1.5× of
  VTs on the benchmark VTs were built for. That should have bounded the effort spent on
  VT edge cases.

- **A cross-architecture port surfaces linker-semantics gaps, not codegen ones.** The
  amd64 issues were all about symbol resolution: version nodes, common symbols, weak
  references. The risk the plan expected (TLS models) was fine. Unversioned dynamic
  imports silently bind compat ABIs on x86_64 glibc.
- **Isolate emulator bugs with a minimal guest.** A plain Go program reading
  `/proc/self/maps` crashed qemu the same way, which ruled out pythago and bao in one
  step.

- **Embedding breaks the assumption that sys.executable is a Python interpreter.**
  multiprocessing (spawn, forkserver), `subprocess.run([sys.executable, ...])` and
  fork-without-exec all need explicit support from the host, and so does signal
  ownership. Test a real heavy workload (PyTorch DataLoader) early: it uses all of them.

- **Validate failures against a stock-interpreter control with the identical package
  set.** The flashinfer JIT failure looked like it could be ours until stock CPython
  failed the same way.

- **Anything Python needs to import must work without Go.** Forked children (fork
  start, every forkserver worker) cannot run Go, so import machinery, signal restore and
  similar infrastructure belong in C, over data Go prepares up front.

- **Reuse the ecosystem's tools where the interpreter can run them.** A stock
  interpreter from the same build has exactly our ABI, so venv and pip replace a
  hand-written installer, and their compatibility checks are correct by construction.

- **Make the tool report what it could not do.** bao's
  `linux_imports_above_version_cap` turned "silently unversioned" into a visible list, and
  that showed sqlite's `stat64` breaking the glibc floor. That had been there unnoticed
  all along.

- **A stock-interpreter control catches test bugs as well as product bugs.** Each
  failure that differs between pythago and stock Python, in either direction, points at
  a fidelity gap or a broken test.

- **`go generate` passes its GOOS/GOARCH to the commands it runs.** A cross-target
  generate step that uses `go run` must strip them, or the generator itself is built for
  the target.

- **A compatibility floor is only proven by running on it.** Version-number checks passed
  while no binary could start below glibc 2.34; running on old releases found three
  separate bugs in a row.

- **Batch boundary crossings, but keep the semantics.** Combining `http.response.start`
  with the first body into one call, and packing headers into one bytes object, cut calls
  without changing ASGI behaviour, because Python holds the start until a body exists.

- **Follow the protocol's own extension points before inventing one.** ASGI's
  `http.response.trailers`, `http.response.pathsend` and `websocket.http.response`, and
  asyncio's `BufferedProtocol`, gave zero-copy and sendfile paths that existing frameworks
  use unmodified.
