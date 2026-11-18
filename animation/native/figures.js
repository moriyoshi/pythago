(() => {
  'use strict';

  const {label, card, note, arrow, board, codePanel, muted, palette} = window.PythagoFigureBoard;
  const column = [41, 409, 777];
  const lane = [41, 572];

  function row(y, boxes, {step, flow}, links = []) {
    return boxes.map(([title, lines, tone, active, mono], i) =>
      card(column[i], y, 282, 115, title, lines, {tone, active: active.includes(step), mono, size: mono ? 22 : 28})
    ).join('') + links.map(([cue, tone], i) =>
      arrow(column[i] + 291, y + 58, column[i + 1] - 9, y + 58, flow(cue, tone))
    ).join('');
  }

  function footer(notes, step, tone = 'teal') {
    return note(41, 568, ...notes[step], tone);
  }

  function overview(state) {
    return board('Three jobs in the native image', 'Follow a Python thread from a build-time reference to a runtime call.',
      row(145, [
        ['Object edit', ['Retarget a reference'], 'blue', [0, 1]],
        ['Runtime hook', ['Choose a thread backend'], 'teal', [1]],
        ['Thunk', ['Adapt a call or lookup'], 'gold', [1]],
      ], state)
      + label(41, 339, 'Example thread creation', {size: 24, weight: 720})
      + row(371, [
        ['CPython', ['Calls pthread_create'], 'gold', [0, 2]],
        ['C wrapper', ['Checks eligibility'], 'teal', [2]],
        ['Go or system', ['Starts an OS thread'], 'blue', [2]],
      ], state, [[2, 'gold'], [2, 'teal']])
      + footer([
        ['One familiar call', 'The Python program uses the normal threading API.'],
        ['Read the three contracts', 'Linker target, thread behavior, and native calling convention each have a role.'],
        ['Platform details matter', 'Linux and macOS wrap pthread calls in their images. Windows loads native DLLs.'],
      ], state.step));
  }

  function objects(state) {
    const {step} = state;
    return board('An object file carries instructions for the linker', 'The simplified call below measures its displacement from the next instruction.',
      row(139, [
        ['Sections', ['Machine code and data'], 'blue', [0]],
        ['Symbols', ['Locations and imports'], 'gold', [0, 1]],
        ['Relocations', ['Places and fixup rules'], 'teal', [1]],
      ], state)
      + label(41, 318, 'A relative call after placement', {size: 24, weight: 720})
      + row(350, [
        ['1,000', ['Next instruction'], 'blue', [2], true],
        ['1,000 bytes', ['Displacement written'], 'teal', [2], true],
        ['2,000', ['Address of the target'], 'gold', [2], true],
      ], state, [[2, 'blue'], [2, 'teal']])
      + footer([
        ['Definitions and imports', 'A defined symbol has a location. An undefined symbol asks another input to supply it.'],
        ['Relocation fields', 'The location, type, and optional symbol or addend tell the linker how to write the fixup.'],
        ['2,000 − 1,000 = 1,000', 'The linker writes the displacement using the relocation type’s encoding.'],
        ['Shared images have a layout', 'A loader may still need to rebase pointers or bind addresses from other libraries.'],
      ], step));
  }

  function edits({step, flow}) {
    function binding(y, title, before, after, cue) {
      return label(41, y - 15, title, {size: 23, weight: 720})
        + card(lane[0], y, 487, 103, before, ['Input reference'], {tone: 'gray', active: step === cue, mono: true, size: 23})
        + arrow(536, y + 52, 564, y + 52, flow(cue))
        + card(lane[1], y, 487, 103, after, ['Reference after wrapping'], {tone: 'teal', active: step === cue, mono: true, size: 23});
    }
    return board('The wrapper has two directions', 'Bao changes which symbol each reference will resolve to.',
      binding(150, 'Call from libpython', 'pthread_create', '__wrap_pthread_create', 0)
      + binding(307, 'Fallback from glue', '__real_pthread_create', 'pthread_create', 1)
      + label(41, 469, 'Related object edits', {size: 23, weight: 720})
      + label(41, 507, 'Rename · Prefix · Localize · Strip · Reindex archives', {size: 23, color: muted})
      + footer([
        ['The call now reaches Pythago glue', 'The edited reference resolves to the C wrapper with the matching pthread signature.'],
        ['Fallback can still reach libc', 'The original symbol remains available through the rewritten real-name reference.'],
        ['Rename or isolate a component', 'Prefixing changes the component’s own names and preserves its unrelated imports.'],
        ['Keep references consistent', 'A relocation or section group may still need a symbol. Archive lookup uses the updated index.'],
      ], step));
  }

  function image(state) {
    return board('Translate, link, then initialize', 'The shared build starts from an already linked native library.',
      row(143, [
        ['libpython', ['ELF or Mach-O image'], 'gold', [0, 1]],
        ['Bao', ['Merge glue and fixups'], 'teal', [0, 1]],
        ['dist package', ['Native objects and Go'], 'blue', [2]],
      ], state, [[0, 'gold'], [1, 'teal']])
      + label(41, 326, 'What the generated package supplies', {size: 24, weight: 720})
      + row(357, [
        ['.syso', ['The native load image'], 'blue', [2], true],
        ['Registration', ['Expose native symbols'], 'teal', [2]],
        ['Initialization', ['Imports and constructors'], 'gold', [2]],
      ], state, [[2, 'blue'], [2, 'teal']])
      + footer([
        ['Glue and shared library', 'The generator selects the native library for the target and passes the pthread wrap list.'],
        ['ELF import slots', 'A linkage stub loads or jumps through a slot whose binding now names the wrapper.'],
        ['The Go linker places the native image', 'System imports and native initialization still have work to do when the program runs.'],
        ['Linux LTO build', 'LLVM bitcode → native objects → lld partial link with wrapping → Bao translation.'],
      ], state.step));
  }

  function hook(state) {
    const {step, flow} = state;
    return board('The pthread wrapper chooses a path', 'Creation uses the original pthread signature and returns a pthread status.',
      row(137, [
        ['libpython', ['Native pthread call'], 'gold', [0], true],
        ['C wrapper', ['Default stack check'], 'teal', [0, 1, 2]],
        ['Spawn hook', ['Return status or decline'], 'blue', [1, 2]],
      ], state, [[0, 'gold'], [1, 'teal']])
      + label(41, 315, 'Possible outcomes', {size: 24, weight: 720})
      + row(346, [
        ['Success', ['Go-backed OS thread'], 'blue', [1]],
        ['Pthread error', ['Return a positive code'], 'gold', [1]],
        ['Use libc', ['Call the real pthread'], 'teal', [2, 3]],
      ], state)
      + arrow(918, 260, 918, 337, flow(2))
      + footer([
        ['The edited import selects the entry point', 'The C wrapper receives the same arguments as the original system function.'],
        ['Zero or a positive error', 'The caller receives either success or a pthread error such as the thread limit.'],
        ['Fallback conditions', 'A non-default stack or an unavailable Go spawn hook sends creation to libc.'],
        ['Lifecycle hooks', 'Join and detach look up the thread identity. The exit wrapper handles a separate path.'],
      ], step));
  }

  function lifecycle({step, flow}) {
    const left = 276, right = 823;
    const marker = (from, to, y, title, cue) =>
      label((from + to) / 2, y - 13, title, {size: 22, anchor: 'middle', weight: cue === step ? 730 : 500})
      + arrow(from, y, to, y, flow(cue));
    return board('Thread start and release handshake', 'The positions in each lane belong to one operating-system thread.',
      card(41, 128, 470, 94, 'Parent thread', ['Inside wrapped pthread_create'], {tone: 'gold', active: step < 2})
      + card(588, 128, 471, 94, 'Child thread', ['Locked goroutine runs C entry'], {tone: 'blue', active: step > 0})
      + `<path d="M${left} 222 V531 M${right} 222 V531" stroke="#b5c9ce" stroke-width="2"/>`
      + marker(left, right, 277, 'Assign record or start a thread', 0)
      + marker(right, left, 348, 'Publish pthread_self and started', 1)
      + card(699, 363, 248, 80, 'Run Python', ['Publish done on return'], {tone: 'blue', active: step === 2, size: 25})
      + marker(left, right, 491, 'Join or detach grants release', 3)
      + footer([
        ['Use a spare when possible', 'A spare is already locked to an OS thread and waits in C for an assignment.'],
        ['Creation waits for the handle', 'The registered handle is the child’s own pthread_t.'],
        ['Completion and release are separate', 'Normal return publishes done. Cleanup waits for release.'],
        ['Join waits for done', 'Detach records release immediately. Cleanup follows when the child is also done.'],
        ['Special exit path', 'The wrapped pthread_exit parks a Go-backed thread to avoid unwinding through Go.'],
      ], step, 'gold'));
  }

  function abi(state) {
    const {step, flow} = state;
    return board('Two directions across the C ABI', 'This outbound layout shows the fixed integer-slot thunk on amd64.',
      row(136, [
        ['Go call frame', ['Target · slots · result'], 'blue', [0, 2]],
        ['Call thunk', ['System stack'], 'teal', [1, 2]],
        ['Native target', ['C calling convention'], 'gold', [1, 2]],
      ], state, [[0, 'blue'], [1, 'teal']])
      + label(41, 298, 'Outbound locations', {size: 23, weight: 720})
      + label(41, 330, 'Slots 1–6: rdi, rsi, rdx, rcx, r8, r9', {size: 22, mono: true})
      + label(41, 361, 'Slots 7–8: system stack', {size: 22, mono: true})
      + card(41, 398, 400, 110, 'Go dispatcher', ['Selects the registered function'], {tone: 'blue', active: step === 3})
      + arrow(654, 454, 450, 454, flow(3, 'gold'))
      + card(663, 398, 396, 110, 'C trampoline', ['Python calls a built-in'], {tone: 'gold', active: step === 3})
      + footer([
        ['Go passes a frame', 'The frame groups the native target, fixed argument slots, and return value.'],
        ['Place arguments for the C callee', 'The runtime enters the adapter on the system stack. The adapter loads the C locations.'],
        ['The frame can move', 'A callback can grow the goroutine stack. The adapter corrects the frame address on return.'],
        ['The reverse direction has its own entry', 'The C trampoline packages Python arguments and the Go function index for the dispatcher.'],
      ], step));
  }

  function tls({step, flow}) {
    return board('A TLS descriptor serves every thread', 'Example addresses use the same variable offset in two separate thread blocks.',
      row(136, [
        ['Native access', ['Call the resolver'], 'gold', [0, 2]],
        ['Descriptor', ['Resolver + offset 0x18'], 'teal', [2, 3]],
        ['TLS thunk', ['Resolve this thread'], 'blue', [3, 4]],
      ], {step, flow}, [[2, 'gold'], [3, 'teal']])
      + lane.map((x, i) => {
        const [thread, block, slot] = i === 0 ? ['A', '0x7000', '0x7018'] : ['B', '0x9000', '0x9018'];
        const {fill, line} = palette[i === 0 ? 'blue' : 'gold'];
        return `<g data-card>
          <rect x="${x}" y="348" width="487" height="159" rx="16" fill="${fill}" stroke="${line}" stroke-width="${step === 1 || step === 3 ? 3 : 1.5}"/>
          ${label(x + 23, 386, 'OS thread ' + thread, {size: 28, display: true})}
          ${label(x + 23, 429, 'Block address  ' + block, {size: 22, mono: true})}
          ${label(x + 23, 471, 'Variable slot  ' + slot, {size: 22, mono: true, weight: 700})}
        </g>`;
      }).join('')
      + footer([
        ['The variable’s identity is shared', 'Each operating-system thread can have a separate address and value for the variable.'],
        ['A block starts from the TLS template', 'The two blocks have different addresses. The field is at offset 0x18 in either block.'],
        ['Two descriptor words', 'The first word calls the resolver. The second carries the variable’s template offset.'],
        ['The amd64 resolver returns a displacement', 'Block address + field offset − thread pointer. The native caller adds the thread pointer.'],
        ['The helper handles a cache miss', 'The descriptor convention dictates which registers the resolver must preserve.'],
      ], step, 'gold'));
  }

  function platforms({step}) {
    const entries = [
      ['Linux', 'ELF load image', 'GOT/PLT binding', 'Pthread wrappers', 'Linux TLS resolver'],
      ['macOS', 'Mach-O load image', 'Dylib bind fixups', 'Pthread wrappers', 'Mach-O TLV runtime'],
      ['Windows', 'Extracted DLLs', 'DLL export lookup', 'CPython threads', 'SyscallN + callback'],
    ];
    return board('Linux, macOS, and Windows', 'The third row uses the Windows loader for the bundled native libraries.',
      entries.map(([name, image, binding, threads, adapter], i) => {
        const y = 147 + i * 133;
        const {fill, line} = palette[['teal', 'blue', 'gold'][i]];
        const active = step === i || (i === 2 && step === 3);
        return `<g data-card>
          <rect x="41" y="${y}" width="1018" height="103" rx="14" fill="${fill}" stroke="${line}" stroke-width="${active ? 3 : 1}"/>
          ${label(62, y + 57, name, {size: 29, display: true})}
          ${label(280, y + 39, image, {size: 22, weight: 700})}
          ${label(280, y + 73, binding, {size: 21, color: muted})}
          ${label(690, y + 39, threads, {size: 22, weight: 700})}
          ${label(690, y + 73, adapter, {size: 21, color: muted})}
        </g>`;
      }).join('')
      + footer([
        ['Linux · translated ELF image', 'The image has native constructors, system library imports, and translated TLS fixups.'],
        ['macOS · translated Mach-O image', 'Bao binds the dylib and sets up native thread-local variable descriptors.'],
        ['Windows · native DLL loading', 'Pythago finds glue and Python exports in separately loaded libraries.'],
        ['Windows · platform call paths', 'CPython owns thread creation. SyscallN and a Go callback handle the native boundary.'],
      ], step));
  }

  function recap(state) {
    return board('One call, three contracts', 'Ask what each boundary receives and what it supplies.',
      label(41, 156, 'From build to runtime', {size: 24, weight: 720})
      + row(177, [
        ['Object edit', ['Symbol → wrapper'], 'blue', [0]],
        ['Pthread hook', ['Request → status'], 'teal', [1]],
        ['OS thread', ['Handle → lifecycle'], 'gold', [1]],
      ], state, [[0, 'blue'], [1, 'teal']])
      + label(41, 354, 'Adapters at the native boundary', {size: 24, weight: 720})
      + row(375, [
        ['C call thunk', ['Go frame → native call'], 'blue', [2]],
        ['C trampoline', ['Python args → Go Func'], 'gold', [2]],
        ['TLS resolver', ['Thread-relative offset'], 'teal', [2]],
      ], state)
      + footer([
        ['Build time', 'The wrapper reference and the real-name fallback reference resolve to distinct targets.'],
        ['Runtime', 'The wrapper returns a thread handle on success or a pthread error on failure.'],
        ['Adapter contracts', 'Arguments, dispatch, and thread-local addresses cross different boundaries.'],
      ], state.step));
  }

  const code = {
    editCode: [
      ['wrapUndefined', [74, 75], 'Undefined import', 'The wrapper name replaces the imported name.'],
      ['wrapDefined', [76, 77, 79, 82, 84], 'Existing definition', 'Bao keeps it and redirects runtime references to a wrapper entry.'],
      ['wrapReal', [85, 87, 93, 94], 'Fallback reference', 'An undefined real-name reference is renamed to the original symbol.'],
    ],
    imageCode: [
      ['buildOptions', [58, 59, 60, 62, 64], 'Choose the native format', 'Both shared build paths pass the glue object and wrap list to Bao.'],
      ['bindImport', [956, 960, 961, 962, 963], 'Match a shared-library import', 'The search skips definitions, local symbols, and imports with other names.'],
      ['bindImport', [965, 966, 968], 'Require the matching wrapper', 'Bao rejects a missing definition and binds the renamed import to existing glue.'],
      ['lto', [148, 149, 150, 153], 'Linux bitcode build', 'The partial link applies the wrap list to relocatable objects before Bao runs.'],
    ],
    hookCode: [
      ['install', [250, 251], 'Register the Go spawn callback', 'The dedicated main thread gives C an entry point for threadStart.'],
      ['create', [62, 63], 'Use the pthread status', 'The wrapper returns zero or a positive error from the Go-backed path.'],
      ['spawn', [353, 354, 356, 358], 'Negative means fall back', 'A missing spawn hook declines. The live-thread cap returns EAGAIN.'],
      ['thread', [82, 83, 86, 87, 89], 'The callback starts a locked goroutine', 'The goroutine enters C and runs or waits for the assigned thread record.'],
    ],
    abiCode: [
      ['registers', [21, 22, 23, 24, 25, 26, 27, 29, 31, 33], 'Apply the System V amd64 convention', 'Load the native target and use six integer registers and two stack slots.'],
      ['result', [34, 35, 36, 37, 38], 'Store through the adjusted frame address', 'The current goroutine stack top may differ after a callback.'],
      ['fastcall', [602, 603, 604, 605, 606, 607], 'Build the callback frame', 'The built-in context carries the index of the registered Go callable.'],
      ['dispatch', [622, 624, 626, 629, 630, 632], 'Enter Go and give Python the result', 'Windows invokes the callback directly. The Unix bridge uses crosscall2.'],
    ],
    tlsCode: [
      ['descriptor', [489, 492, 493, 494], 'Classify the native TLS fixup', 'Bao records the template offset and marks the descriptor case.'],
      ['lower', [780, 781, 786, 788], 'Fill the resolver and argument words', 'The first word points to executable code. The second word stores the offset.'],
      ['lookup', [34, 35, 36, 37, 38, 39, 40], 'Use the current thread’s cache entry', 'Verify the owner, then compute address minus thread pointer.'],
      ['lookup', [38, 39, 40, 47], 'Return under the TLS descriptor convention', 'The caller receives a displacement it can combine with the thread pointer.'],
    ],
  };

  const diagrams = {overview, objects, edits, image, hook, lifecycle, abi, tls, platforms, recap};
  window.drawPythagoNative = (scene, time, cue) => {
    const step = scene.cues.indexOf(cue);
    const elapsed = time - cue[0];
    const flow = (index, tone = 'teal') => ({active: step === index, tone, elapsed});
    const excerpt = code[scene.figure]?.[step];
    if (excerpt) {
      const [id, active, heading, detail] = excerpt;
      return codePanel(window.PYTHAGO_NATIVE_CODE[id], active, heading, detail);
    }
    return diagrams[scene.figure]({step, flow});
  };
})();
