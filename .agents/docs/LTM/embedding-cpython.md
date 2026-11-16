# Embedding CPython

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- Avoid variadic C APIs entirely. A fastcall PyCFunction signature is directly
  purego-compatible, so one Go dispatcher plus a PyLong index in `self` replaces all
  trampolines.
- Expose data symbols through a glue symtab; Go never hard-codes struct layouts or refcount
  fields.
- Only **SHN_ABS** dynsym entries can make static data addresses (`PyLong_Type`,
  `_Py_NoneStruct`) visible to dlopen'ed extensions, and that requires glibc ≥ 2.28. Export
  only `_?Py*`, so that vendored OpenSSL/zlib copies in wheels never bind to ours.
- Python memory cannot move to the Go heap on free-threaded builds. The GC walks mimalloc heaps
  that belong to each thread state.
- **Every Go wrapper that forwards `uintptr` arguments to C must be `//go:uintptrescapes`**,
  as `purego.SyscallN` is. Without it, `call(fn, uintptr(unsafe.Pointer(&n)))` leaves `n`
  on the goroutine stack. A stack copy on the way into SyscallN then moves `n`, C writes
  the out-parameter to the old location, and Go reads zero. (2026-11-16)
- **C must never see goroutine-stack memory.** The pragma only covers pointers converted
  in the call expression itself. A pointer that reaches C another way (inside an
  `append`, in a variable) leaves its target free to stay on the stack: Go 1.25+ puts
  small non-escaping `make` buffers there. Check with `-gcflags=-m` that every buffer
  handed to C escapes; `cString` is `//go:noinline` for this reason. (2026-11-17)
- **A variadic `//go:uintptrescapes` function heap-allocates its argument array.** Hot
  paths use the fixed-arity `call0`…`call8`. (2026-11-17)
- **Structures shared with the glue are versioned.** `PYTHAGO_PY_ABI` and `glueABI` must
  change together, so a stale native image fails at Init instead of misreading memory.
  (2026-11-17)
- **Ownership across two memory managers is about liveness, not allocation.** Allocating
  Python's blocks from the Go heap would not help:
  - Python's refcount still decides when a block is reused;
  - Go's GC does not scan raw arena bytes.

  What makes sharing safe is a protocol at the boundary, plus an API that scopes it
  (`WithView`):
  - Python → Go: hold the export while the slice is in use;
  - Go → Python: pin until Python frees the object, and unpin from a queue, never from
    the deallocator.
- **Never call Go from a CPython deallocator.** It can run on foreign threads, during
  finalization, or in a forked child. Push a token to a lock-free list and let Go drain it
  on its next entry.
- **A delimiter-packed format needs a count.** Packing header pairs as `name\0value\0…`
  saved many calls, but a NUL inside a value would shift the pairs and inject a field.
  Sending the pair count with it makes any embedded delimiter a detectable mismatch.
- **The isolated config ignores `PYTHONMALLOC`** and other environment variables, so
  "allocator A against B" experiments through the environment measure nothing.
- **Each Go→Python element conversion that registers a GC cleanup costs real time.**
  Build nested values as raw references and wrap only the outermost one.
