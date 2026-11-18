(() => {
  'use strict';

  const {label, card, note, arrow, board, codePanel, ink, muted, palette} = window.PythagoFigureBoard;

  function message(x1, x2, y, words, state) {
    return label((x1 + x2) / 2, y - 13, words, {size: 20, anchor: 'middle', color: state.active ? ink : muted})
      + arrow(x1, y, x2, y, state);
  }

  function overview({step, flow}) {
    return board('The path of an await', 'Python and Go in the same process',
      card(41, 142, 305, 162, 'Event loop', ['Ready tasks and timers', 'A turn for each callback'], {tone: 'teal', active: step === 1})
      + card(398, 142, 305, 162, 'Thread state', ['Attached to an OS thread', 'Required for Python access'], {tone: 'gold', active: step === 1})
      + card(755, 142, 304, 162, 'Trampoline', ['A shared C entry point', 'Dispatch to a Go function'], {tone: 'blue', active: step === 1})
      + label(41, 363, 'Example lookup', {size: 24, weight: 720})
      + card(41, 394, 281, 112, 'Coroutine', ['Await an address'], {tone: 'teal', active: step === 0})
      + arrow(330, 451, 401, 451, flow(2))
      + card(409, 394, 281, 112, 'Go lookup', ['Do I/O on a goroutine'], {tone: 'blue', active: step === 2})
      + arrow(698, 451, 769, 451, flow(2))
      + card(777, 394, 282, 112, 'Resume', ['Ready after the result'], {tone: 'teal', active: step === 2})
      + note(41, 556, 'One operation, two kinds of control flow', 'The call into Go returns synchronously. The lookup completion can arrive later.'));
  }

  function schedule({step, flow}) {
    function bar(x, y, width, title, tone, active) {
      const {fill, line} = palette[tone];
      return `<rect x="${x}" y="${y}" width="${width}" height="43" rx="8" fill="${fill}" stroke="${line}"
        stroke-width="${active ? 3 : 1.5}" ${tone === 'gray' ? 'stroke-dasharray="6 5"' : ''}/>
        ${label(x + width / 2, y + 28, title, {size: 19, anchor: 'middle'})}`;
    }
    return board('Ready work and suspended tasks', 'A task steps until it returns or yields to the loop.',
      card(41, 131, 281, 112, 'Due timers', ['Release ready callbacks'], {tone: 'gold', active: step === 0})
      + arrow(330, 185, 401, 185, flow(0, 'gold'))
      + card(409, 131, 281, 112, 'Ready queue', ['Callbacks and task steps'], {tone: 'teal', active: step === 0 || step === 2})
      + arrow(698, 185, 769, 185, flow(0))
      + card(777, 131, 282, 112, 'Loop thread', ['Runs one step at a time'], {tone: 'blue', active: step === 3})
      + label(41, 299, 'Example task timeline', {size: 24, weight: 720})
      + label(1050, 299, 'Time →', {size: 20, color: muted, anchor: 'end'})
      + label(41, 371, 'Task A', {weight: 730})
      + bar(169, 343, 209, 'Starts lookup', 'teal', step === 1)
      + bar(389, 343, 410, 'Await: Future pending', 'gray', step === 1)
      + bar(810, 343, 249, 'Resumes', 'teal', step === 2)
      + label(41, 439, 'Task B', {weight: 730})
      + bar(406, 411, 374, 'Runs while A waits', 'blue', step === 1)
      + message(760, 944, 517, 'Future completes', flow(2))
      + note(41, 558, 'Cooperative scheduling', 'A synchronous callback holds the loop thread until it returns.', step === 3 ? 'gold' : 'gray'));
  }

  function backends({step, flow}) {
    return board('Completion or readiness?', 'Both backends leave Tasks, Futures, timers, and ready work with asyncio.',
      label(41, 147, 'GoEventLoop  ·  completion', {size: 26, weight: 720})
      + card(41, 172, 285, 115, 'Go I/O workers', ['Dial, read, resolve'], {tone: 'blue', active: step === 1})
      + arrow(333, 230, 397, 230, flow(1, 'blue'))
      + card(405, 172, 289, 115, 'Batch + wake', ['Token and result'], {tone: 'gold', active: step === 1})
      + arrow(702, 230, 763, 230, flow(1))
      + card(771, 172, 288, 115, 'Python handler', ['Process completed I/O'], {tone: 'teal', active: step === 1})
      + label(41, 354, 'GoSelectorEventLoop  ·  readiness', {size: 26, weight: 720})
      + card(41, 379, 285, 115, 'Go netpoller', ['Watch descriptors'], {tone: 'blue', active: step === 2})
      + arrow(333, 437, 397, 437, flow(2, 'blue'))
      + card(405, 379, 289, 115, 'Ready events', ['Readable or writable'], {tone: 'gold', active: step === 2})
      + arrow(702, 437, 763, 437, flow(2))
      + card(771, 379, 288, 115, 'Python socket', ['Perform the operation'], {tone: 'teal', active: step === 2})
      + note(41, 551, 'Wake faster than the timer', 'call_soon_threadsafe queues a callback and wakes the loop, even with an empty I/O batch.', step === 3 ? 'teal' : 'gray'));
  }

  function threads({step, flow}) {
    return board('Where CPython thread state lives', 'A goroutine and an operating system thread have different identities.',
      label(41, 148, 'Go thread entry', {size: 25, weight: 720})
      + card(41, 173, 285, 115, 'py.Do', ['Take an admission slot'], {tone: 'blue', active: step === 1, mono: true, size: 26})
      + arrow(333, 231, 397, 231, flow(1, 'blue'))
      + card(405, 173, 289, 115, 'OS thread', ['Pin the calling goroutine'], {tone: 'gold', active: step === 0 || step === 1})
      + arrow(702, 231, 763, 231, flow(1, 'gold'))
      + card(771, 173, 288, 115, 'CPython state', ['Attach this thread’s state'], {tone: 'teal', active: step === 0 || step === 1})
      + label(41, 353, 'Eligible Python-created pthread', {size: 25, weight: 720})
      + card(41, 378, 285, 115, 'pthread wrapper', ['Hand creation to Go'], {tone: 'blue', active: step === 2})
      + arrow(333, 436, 397, 436, flow(2, 'blue'))
      + card(405, 378, 289, 115, 'Locked goroutine', ['Pinned for its lifetime'], {tone: 'gold', active: step === 2, size: 28})
      + arrow(702, 436, 763, 436, flow(2, 'gold'))
      + card(771, 378, 288, 115, 'CPython state', ['Keep thread identity'], {tone: 'teal', active: step === 2})
      + note(41, 551, 'Separate constraints', 'GIL builds require the lock. Free-threaded builds still attach state. Config.Slots limits outer Go entries.', step === 3 ? 'gold' : 'gray'));
  }

  function release({step, flow}) {
    return board('What happens during a blocking poll', 'The loop thread retains its identity while its Python thread state is detached.',
      label(41, 154, 'Loop thread', {size: 25, weight: 720})
      + card(41, 182, 229, 133, 'Attached', ['Check for results', 'Return if ready'], {tone: 'teal', active: step === 0})
      + arrow(275, 250, 297, 250, flow(1))
      + card(302, 182, 229, 133, 'Detach', ['py.Release', 'Return a held slot'], {tone: 'gold', active: step === 1})
      + arrow(536, 250, 558, 250, flow(2, 'gold'))
      + card(563, 182, 229, 133, 'Wait in Go', ['Wakeup or timer', 'State detached'], {tone: 'blue', active: step === 2})
      + arrow(797, 250, 819, 250, flow(3, 'blue'))
      + card(824, 182, 235, 133, 'Reattach', ['Regain slot if held', 'Restore thread state'], {tone: 'teal', active: step === 3})
      + `<path d="M563 352 V367 H792 V352" fill="none" stroke="#475e96" stroke-width="2.5"/>`
      + label(677, 404, 'Other goroutines can enter Python here', {anchor: 'middle', size: 25, weight: 690})
      + label(677, 442, 'A GIL build releases the lock while detached.', {anchor: 'middle', size: 21, color: muted})
      + note(41, 540, 'Python objects require attachment', 'The blocking Go section can reenter Python through a nested py.Do if it needs Python access.', step === 2 ? 'blue' : 'gray'));
  }

  function trampoline({step, flow}) {
    return board('An immediate Python-to-Go call', 'The callback runs on the calling Python thread with its state attached.',
      card(41, 174, 290, 149, 'Python builtin', ['py.NewFunc exposes it', 'Carries a function index'], {tone: 'teal', active: step === 0 || step === 1})
      + arrow(338, 235, 390, 235, flow(2))
      + card(397, 174, 306, 149, 'C trampoline', ['pythago_py_fastcall', 'Build the call frame'], {tone: 'gold', active: step === 1 || step === 2})
      + arrow(710, 235, 762, 235, flow(2, 'gold'))
      + card(769, 174, 290, 149, 'Go dispatcher', ['Look up the index', 'Invoke the registered Func'], {tone: 'blue', active: step === 3})
      + `<path d="M41 358 V373 H1059 V358" fill="none" stroke="#92a9b2" stroke-width="2"/>`
      + label(550, 405, 'Same OS thread  ·  attached Python state', {size: 23, anchor: 'middle', weight: 720})
      + label(550, 439, 'crosscall2 on Unix-like systems  ·  syscall.NewCallback on Windows', {size: 19, anchor: 'middle', color: muted})
      + message(999, 102, 491, 'Return a Python value or raise an exception', flow(4, 'blue'))
      + note(41, 553, step === 5 ? 'Completion can arrive later' : 'Borrowed arguments last for the call',
        step === 5 ? 'A callback can start an operation and return. The event loop delivers any later completion.'
          : 'Retain a reference if you need an argument after the return.', step === 5 ? 'teal' : 'gray'));
  }

  function lookup({step, flow}) {
    const columns = [164, 550, 936];
    return board('One lookup, end to end', 'Time runs down. The dashed line shows the poll waiting without Python state.',
      card(41, 129, 246, 94, 'Python loop', ['Task and Future'], {tone: 'teal', size: 26, active: step === 0 || step === 3})
      + card(427, 129, 246, 94, 'Go bridge', ['Resolver and queue'], {tone: 'gold', size: 26, active: step === 1 || step === 2})
      + card(813, 129, 246, 94, 'Go worker', ['DNS operation'], {tone: 'blue', size: 26, active: step === 1})
      + columns.map(x => `<path d="M${x} 225 V562" stroke="#bdced2" stroke-width="2"/>`).join('')
      + message(164, 550, 260, 'resolve(loop, token, host)', flow(0))
      + message(550, 936, 321, 'Submit DNS lookup', flow(1, 'blue'))
      + `<path d="M164 278 V462" stroke="#147982" stroke-width="3" stroke-dasharray="7 6"/>`
      + label(182, 369, 'poll waits', {size: 19, color: muted})
      + label(182, 394, 'state detached', {size: 19, color: muted})
      + message(936, 550, 426, 'Queue result + wake loop', flow(2, 'blue'))
      + message(550, 164, 499, 'poll returns the token and result', flow(2, 'gold'))
      + note(41, 566, 'The loop resumes the task', 'Reattach → find the handler → complete the Future → put the task back in the ready queue.', step === 3 ? 'teal' : 'gray'));
  }

  function code({scene, step}) {
    const {chapters, snippets} = window.PYTHAGO_INTERNALS_CODE;
    const [id, active, heading, detail] = chapters[scene.code][step];
    return codePanel(snippets[id], active, heading, detail);
  }

  function threadLayout({step, flow}) {
    function row(y, title, blocks, cue) {
      const tones = ['blue', 'gold', 'teal'];
      const rowActive = step === 0 || cue.includes(step);
      return label(41, y - 16, title, {size: 23, weight: 710})
        + blocks.map(([name, desc], i) => card(41 + i * 357, y, 304, 94, name, [desc],
          {tone: tones[i], size: 27, active: rowActive})).join('')
        + arrow(352, y + 49, 390, y + 49, flow(cue[0], 'blue'))
        + arrow(709, y + 49, 747, y + 49, flow(cue.at(-1), 'gold'));
    }
    const explanation = step === 5 ? ['Go I/O can run independently', 'A worker can do Go I/O without attaching Python state.']
      : step === 3 ? ['One Python state per OS thread', 'The wrapper drops a cached state before CPython binds the new thread’s state.']
        : ['Thread identity follows the OS thread', 'The loop can detach its state while waiting without moving to another OS thread.'];
    return board('Thread ownership inside one process', 'Each row is a different way execution reaches a Python thread state.',
      row(146, 'Go caller', [['Goroutine', 'Enters through py.Do'], ['Locked OS thread', 'Pinned during the entry'], ['CPython state', 'Attach this thread’s state']], [1])
      + row(295, 'Eligible pthread wrapper', [['Python start', 'Wrapped pthread_create'], ['Locked Go spare', 'Runs the native entry'], ['CPython state', 'CPython binds its own']], [2, 3])
      + row(444, 'Windows Python thread', [['Python start', 'CPython creates the thread'], ['Native OS thread', 'Callback stays on that thread'], ['CPython state', 'Attached when Go is called']], [4])
      + note(41, 576, ...explanation, 'gold'));
  }

  function stackLayout({step, flow}) {
    function stack(x, title, detail, frames, tone) {
      const {fill, line} = palette[tone];
      return `<g><rect x="${x}" y="129" width="447" height="395" rx="16" fill="${fill}" stroke="${line}" stroke-opacity=".65"/>
        ${label(x + 22, 171, title, {size: 29, display: true})}
        ${label(x + 22, 199, detail, {size: 18, color: muted})}
        ${frames.map(([name, text, activeSteps], i) => {
          const y = 228 + i * 69, active = activeSteps.includes(step);
          return `<rect x="${x + 20}" y="${y}" width="407" height="57" rx="8" fill="${active ? line : '#fff'}" stroke="${line}" stroke-opacity="${active ? 1 : .35}"/>
            ${label(x + 34, y + 24, name, {size: 20, mono: true, color: active ? '#fff' : ink})}
            ${label(x + 34, y + 46, text, {size: 17, color: active ? '#fff' : muted})}`;
        }).join('')}</g>`;
    }
    const notes = [
      ['Two stack regions, one OS thread', 'Native frames use the system stack. Go callback code uses a goroutine stack.'],
      ['Switch for the native call', 'runtime.cgocall enters the C thunk on the system stack.'],
      ['Argument placement depends on the ABI', 'This thunk passes eight integer slots. On arm64 they fit in registers.'],
      ['A nested call can reenter Go', 'The native frames remain pending while the Go dispatcher runs.'],
      ['The Go stack can relocate', 'The thunk measures the change in stack top before storing the return value.'],
      ['Windows uses SyscallN', 'The runtime keeps its call frame with the OS thread. Windows callbacks use syscall.NewCallback.'],
    ];
    return board('Frames during a Go → C → Go call', 'Conceptual view of the Linux and macOS call path on one OS thread.',
      stack(41, 'Goroutine stack', 'Go-managed and growable', [
        ['Go caller', 'Makes the outbound call', [0, 1]],
        ['cframe', 'Function, arguments, result', [1, 2, 4]],
        ['dispatchFrame', 'Go callback from CPython', [3, 4]],
        ['Go Func', 'Runs the registered callback', [3, 4]],
      ], 'blue')
      + stack(612, 'System stack', 'Native call frames on that OS thread', [
        ['pythago_cthunk', 'Applies the C calling convention', [1, 2, 4]],
        ['CPython C call', 'Invokes the Python runtime', [2]],
        ['pythago_py_fastcall', 'Builds the inbound call frame', [3]],
        ['crosscall2', 'Enters the Go callback', [3]],
      ], 'gold')
      + arrow(495, 274, 604, 274, flow(1, 'blue'))
      + arrow(604, 417, 495, 417, flow(3, 'gold'))
      + note(41, 576, ...notes[step], step === 5 ? 'gold' : 'blue'));
  }

  function recap({step, flow}) {
    return board('What each part does', 'Three jobs in one process',
      card(41, 149, 305, 164, 'Event loop', ['Schedules ready work', 'Resumes waiting tasks'], {tone: 'teal', active: step === 0})
      + card(398, 149, 305, 164, 'Thread state', ['Attaches to an OS thread', 'Makes Python access valid'], {tone: 'gold', active: step === 0})
      + card(755, 149, 304, 164, 'Trampoline', ['Adapts a synchronous call', 'Dispatches to a Go Func'], {tone: 'blue', active: step === 0})
      + label(41, 368, 'When Go delivers a result', {size: 24, weight: 720})
      + card(41, 393, 281, 113, 'Complete', ['Go posts a result'], {tone: 'blue', active: step === 1})
      + arrow(330, 450, 401, 450, flow(1, 'blue'))
      + card(409, 393, 281, 113, 'Wake', ['The loop reattaches'], {tone: 'gold', active: step === 1})
      + arrow(698, 450, 769, 450, flow(1, 'gold'))
      + card(777, 393, 282, 113, 'Resume', ['Python steps the task'], {tone: 'teal', active: step === 1}));
  }

  const figures = {overview, schedule, backends, threads, release, trampoline, lookup, code, threadLayout, stackLayout, recap};
  window.drawPythagoInternals = (scene, time, cue) => {
    const step = scene.cues.indexOf(cue);
    const elapsed = time - cue[0];
    const flow = (cueIndex, tone = 'teal') => ({active: step === cueIndex, tone, elapsed});
    return figures[scene.figure]({scene, step, flow});
  };
})();
