// Each cue specifies its duration. Chapter and cue boundaries share one timeline.
window.PYTHAGO_NATIVE_STORY = (() => {
  let time = 0;
  function chapter(footer, title, sub, diagram, figure, narration) {
    const start = time;
    const cues = narration.map(([length, text]) => {
      const cue = [time, time + length, text];
      time += length;
      return cue;
    });
    return {start, end: time, kicker: footer, title, sub, footer, diagram, figure, cues};
  }

  return [
    chapter('Overview', 'Inside the\nnative image',
      'Follow a Python thread call from the object file to its operating-system thread.',
      'intro', 'overview', [
        [9, 'When Python starts a thread inside Pythago, how does its native call reach Go? The answer starts before the program runs.'],
        [11, 'Bao edits the binary references. A pthread wrapper chooses who starts the thread. Small thunks handle calling conventions and thread-local addresses.'],
        [11, 'We will follow a pthread_create call through those layers, then see which parts are shared by Linux, macOS, and Windows.'],
      ]),
    chapter('Object files', 'Bytes, names,\nand fixups',
      'A relocation identifies a place to fix up and the rule for computing its value.',
      'build', 'objects', [
        [12, 'A relocatable object groups machine instructions and data into sections. A symbol defines a location in one section, or names something another input must supply.'],
        [12, 'A relocation tells the linker where to apply a fixup, what kind of calculation to use, and which symbol it refers to when one is needed.'],
        [13, 'In this simplified relative call, the symbol is at address two thousand and the next instruction is at one thousand. The displacement is one thousand bytes.'],
        [12, 'A shared library already has a layout and may carry dynamic fixups. Bao can translate the load image and carry forward the fixups it still needs.'],
      ]),
    chapter('Symbol editing', 'Change the\nreference',
      'Wrapping redirects selected references. The fallback keeps a route to the original function.',
      'bridge', 'edits', [
        [12, 'Think of a call as a reference to a symbol. Wrapping changes that reference from pthread_create to the glue function named __wrap_pthread_create.'],
        [12, 'Inside the wrapper, the reference named __real_pthread_create becomes a reference to pthread_create. It can then resolve to the system implementation.'],
        [13, 'A rename changes a spelling. A prefix groups a component’s own definitions and references under distinct names. Unrelated imports keep their names.'],
        [13, 'Localization makes a definition local to its object. Stripping can remove unused names, but references must stay valid. An edited archive needs an updated symbol index.'],
      ]),
    chapter('Bao source', 'Follow the\nsymbol edit',
      'Three cases in the pinned Bao implementation explain what happens to the before and after references.',
      'build', 'editCode', [
        [12, 'For an undefined global symbol on the wrap list, Bao changes the name to the wrapper name. The relocation still has a symbol entry to refer to.'],
        [13, 'If the same object defines the wrapped symbol, Bao keeps that definition. It adds or reuses a wrapper symbol and retargets runtime relocations.'],
        [13, 'The real-name case goes the other way. An undefined __real_ reference is renamed to the original name. It resolves to the original definition at link time.'],
      ]),
    chapter('Image generation', 'From libpython\nto a Go package',
      'The shared build translates the native image and links it with a relocatable C glue object.',
      'packages', 'image', [
        [12, 'The normal shared build starts with the target’s libpython and a C glue object. The generator gives Bao the four pthread symbols that it wants to wrap.'],
        [14, 'On Linux, a call can go through a procedure linkage table stub and its target pointer, in the global offset table. Bao binds that import slot to the wrapper.'],
        [13, 'The generated dist package contains Go-linkable native objects and registration code. The Go linker places the image. Initialization resolves remaining imports and runs native constructors.'],
        [13, 'The Linux LTO route starts from bitcode. LLVM lowers it to native objects and lld wraps the relocatable references before Bao translates the resulting archive.'],
      ]),
    chapter('Build source', 'Two stages\nof binding',
      'Pythago chooses the inputs. Bao checks that references redirected to the wrapper have a definition.',
      'build', 'imageCode', [
        [12, 'The generator sets the glue object and wrap list together. It selects a dylib on macOS and a shared object on Linux, then calls the Bao build.'],
        [12, 'Bao finds the undefined global imports with the requested name. The example shows the ELF shared-object path after Bao has read the image’s relocations.'],
        [13, 'The edit requires matching glue. Bao rejects a missing wrapper instead of leaving an unresolved hook. Otherwise it gives the imported symbol the wrapper name.'],
        [12, 'In the LTO route, the partial link uses the same wrap list. The link step makes a relocatable object, which Pythago packages with the symbol table for Bao.'],
      ]),
    chapter('Runtime hooks', 'Who starts\nthe thread?',
      'The wrapper chooses between the Go-backed path, a pthread error, and system pthreads.',
      'threads', 'hook', [
        [12, 'At runtime, CPython calls its usual pthread function. The edited reference sends it to C glue. The wrapper still has the original pthread calling convention.'],
        [13, 'The wrapper checks for a default stack and asks the spawn hook to handle the call. Success and positive pthread errors are returned to the caller.'],
        [13, 'If the hook is unavailable or the stack size needs libc, the wrapper calls the real pthread function. The forked child disables the Go spawn hook.'],
        [13, 'The C shim tracks the pthread identities it owns. Join and detach use that registry, and ordinary threads fall back to libc. Exit needs its own wrapper.'],
      ]),
    chapter('Hook source', 'Install, call,\nand fall back',
      'Installing the runtime callback is separate from binding the wrapper during the build.',
      'threads', 'hookCode', [
        [12, 'On the dedicated initialization thread, Go registers a C-callable callback for threadStart. C glue keeps the pointer and the target number of spare threads.'],
        [13, 'In the wrapper, the greater-than-or-equal-to-zero test is deliberate. Zero is success and a positive error, such as EAGAIN at the cap, stays an error.'],
        [12, 'If no hook is installed, the C spawn function returns negative one. That is a private signal to use the real pthread implementation.'],
        [12, 'threadStart starts a goroutine and returns. Inside the goroutine, Go locks to the operating-system thread and enters the C thread main function.'],
      ]),
    chapter('Thread lifecycle', 'A real thread\nwith a Go owner',
      'The parent receives the child’s pthread identity before returning from creation.',
      'threads', 'lifecycle', [
        [12, 'Creation first assigns a thread record to a waiting spare if one is available. Otherwise C calls the Go spawn hook to start a locked goroutine.'],
        [13, 'The child publishes pthread_self and enters the registry. It signals started, so the parent can return the handle before the Python function has finished.'],
        [13, 'The C entry point runs CPython’s thread function on that operating-system thread. When the function returns, C publishes done and waits for release.'],
        [13, 'Join waits for done and releases the record. Detach grants release without waiting for completion. This preserves the thread’s identity and lifetime.'],
        [13, 'On the special pthread_exit path, the Go-backed thread parks. Calling the system exit there would force an unwind across Go frames. Ordinary threads still use libc.'],
      ]),
    chapter('Call thunks', 'Cross the\ncalling convention',
      'A small adapter arranges a call so its callee sees the registers and stack it expects.',
      'bridge', 'abi', [
        [12, 'A thunk is a small piece of executable code that adapts one interface to another. In a Go-to-C call, Pythago packs the target and arguments into a frame.'],
        [13, 'The Go runtime enters the C call thunk on the system stack. On Linux and macOS using amd64, six integer slots go in registers and two go on the stack.'],
        [13, 'The native callee can call back into Go and cause the goroutine stack to move. The thunk adjusts the saved frame pointer before writing the return value.'],
        [13, 'The reverse path starts at a C trampoline. It builds a call frame, identifies the registered Go function, and enters the dispatcher through the platform callback bridge.'],
      ]),
    chapter('Call source', 'Track the\ncall frame',
      'The outbound thunk adapts a fixed calling convention. The inbound trampoline dispatches a registered callable.',
      'bridge', 'abiCode', [
        [12, 'On amd64 the thunk reads the function address and the first six integer arguments from the frame. It puts the last two integer slots on the system stack.'],
        [13, 'After the native call, the thunk saves the result. It compares the goroutine stack top with its earlier value, then stores the result in the corrected frame.'],
        [13, 'In the other direction, the C fastcall entry builds a frame from the Python arguments and the function index. That index selects the Go callable.'],
        [13, 'The trampoline enters Go through crosscall2 on Linux and macOS, and through a Go callback on Windows. On return it hands Python the result or the recorded exception.'],
      ]),
    chapter('Thread-local storage', 'One name,\none value per thread',
      'Native TLS belongs to the operating-system thread. A descriptor provides a way to find its slot.',
      'threads', 'tls', [
        [13, 'A native thread-local variable has the same name in the code on every thread. Its address can be different on each operating-system thread.'],
        [13, 'The translated Linux image has a TLS template. Bao’s runtime finds or creates an instance for the calling thread. The variable’s offset within the instance is shared.'],
        [13, 'For the Linux descriptor example, Bao builds a pair: the resolver entry point and the variable’s offset in the template. Native code calls the resolver.'],
        [13, 'On amd64, the thunk returns an offset from the current thread pointer. The caller combines the two to reach that thread’s variable.'],
        [13, 'A cached instance can be found quickly. A miss calls the TLS helper that can allocate a fresh instance. Register preservation follows the descriptor convention.'],
      ]),
    chapter('TLS source', 'Bind a TLS\ndescriptor',
      'The build supplies the resolver and offset. The thunk finds the running thread’s instance.',
      'build', 'tlsCode', [
        [13, 'Bao classifies supported native TLS relocations as it reads the image. In the descriptor case, the dynamic fixup becomes an internal descriptor relocation.'],
        [13, 'When Bao lowers the descriptor, it stores the template offset in the second word. A relocation in the first word points to the resolver thunk.'],
        [13, 'The amd64 fast path checks that the cached record belongs to the calling thread. It adds the template offset to the block address, then subtracts the thread pointer.'],
        [13, 'This is a separate use of a thunk. It resolves a storage location under the TLS descriptor convention. A C call thunk moves arguments, and a callback trampoline dispatches code.'],
      ]),
    chapter('Platform comparison', 'What differs\nby target?',
      'The native packaging, thread hooks, and ABI adapters follow each target’s loader and conventions.',
      'deploy', 'platforms', [
        [12, 'On Linux, Bao translates the ELF image from libpython. System imports still bind to shared libraries, and Bao supplies native TLS support for the translated image.'],
        [13, 'On macOS, Bao translates a Mach-O dylib. Its bind fixups can target the glue wrappers. Thread-local variables use Mach-O descriptors and Bao’s corresponding runtime.'],
        [13, 'On Windows, the dist package embeds the CPython and glue DLLs. Pythago extracts and loads them, and finds exported addresses with GetProcAddress.'],
        [13, 'Windows keeps CPython’s own thread creation. Go uses SyscallN and a Windows callback for ABI crossing. The pthread and ELF descriptor paths are specific to other targets.'],
      ]),
    chapter('End-to-end recap', 'Read the path\nfrom both ends',
      'Trace the target of the reference, the owner of the thread, and the contract of the adapter.',
      'outro', 'recap', [
        [12, 'At build time, a pthread reference moves to the glue wrapper. A real-name reference in the wrapper keeps a path to the system callable.'],
        [12, 'At runtime, the wrapper chooses a locked Go-backed thread or the system implementation, or returns a pthread error. The child publishes its real thread identity.'],
        [13, 'The call thunk, callback trampoline, and TLS resolver each adapt a different interface. Follow the input and the result at each boundary to see which job is being done.'],
      ]),
  ];
})();
