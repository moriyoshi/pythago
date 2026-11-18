window.PYTHAGO_INTERNALS_STORY = [
  {
    start: 0, end: 25, kicker: 'Inside the runtime', title: 'Asyncio inside\nPythago',
    sub: 'Follow a DNS lookup across the event loop, thread state, and callback bridge.',
    footer: 'Runtime overview', diagram: 'intro', figure: 'overview', cues: [
      [0, 8, 'A Python coroutine needs a network address. It awaits a lookup, and the Pythago process can keep doing other work.'],
      [8, 16, 'The event loop schedules tasks. Thread state lets a thread enter Python. A trampoline carries calls into Go.'],
      [16, 25, 'All three happen inside one process. Follow the lookup in order, from a suspended coroutine to a result that lets it resume.'],
    ],
  },
  {
    start: 25, end: 67, kicker: 'Asyncio scheduling', title: 'One loop,\nmany tasks',
    sub: 'The event loop runs ready work and gives suspended tasks another turn when their futures finish.',
    footer: 'The event loop', diagram: 'build', figure: 'schedule', cues: [
      [25, 36, 'An asyncio event loop runs callbacks and steps tasks on its loop thread. Its ready queue holds work that can run now. A timer holds work until its deadline.'],
      [36, 46, 'Task A starts a lookup. Its future is still pending, so await suspends the task. The event loop can now give task B a turn.'],
      [46, 56, 'When the lookup finishes, setting the future result schedules the waiting task. The loop takes it from the ready queue and continues after the await.'],
      [56, 67, 'This is cooperative scheduling. A long synchronous callback occupies the loop thread. The loop can only run the next callback when the current one returns or the task yields.'],
    ],
  },
  {
    start: 67, end: 111, kicker: 'Go-backed I/O', title: 'Who waits\nfor the socket?',
    sub: 'One backend delivers completed operations. The other reports descriptors that are ready.',
    footer: 'Completion and readiness', diagram: 'bridge', figure: 'backends', cues: [
      [67, 78, 'Pythago provides two backends through goaio. Both preserve Python’s task and timer scheduling, but they hand different parts of the I/O work to Go.'],
      [78, 89, 'In GoEventLoop, goroutines do operations such as dialing sockets and resolving DNS. They put completed results in a batch and wake the Python loop.'],
      [89, 100, 'GoSelectorEventLoop goes through the standard selector loop. Go watches descriptor readiness. Once a descriptor is ready, Python makes the socket call.'],
      [100, 111, 'A timer can bound how long the loop waits. A thread-safe callback can wake it even when the I/O batch is empty. The loop then checks its ready queue.'],
    ],
  },
  {
    start: 111, end: 149, kicker: 'Thread identity', title: 'Entering\nCPython',
    sub: 'A goroutine entering Python needs an attached state on a specific operating system thread.',
    footer: 'Goroutines and OS threads', diagram: 'packages', figure: 'threads', cues: [
      [111, 121, 'A goroutine can normally move between operating system threads. CPython stores an attached thread state locally on the operating system thread that is using it.'],
      [121, 130, 'An outer py.Do takes a slot, pins the goroutine to its current OS thread, and attaches that thread’s Python state.'],
      [130, 140, 'On the pthread path, eligible Python-created threads use a Go goroutine locked to an operating system thread for their lifetime. Python keeps its thread identity.'],
      [140, 149, 'GIL builds also need the lock. Free-threaded builds still attach state. Config.Slots separately limits how many outer Go calls can enter.'],
    ],
  },
  {
    start: 149, end: 185, kicker: 'Waiting for Go', title: 'Let other\nthreads run',
    sub: 'The loop releases its Python thread state during a blocking wait and restores it before dispatching results.',
    footer: 'Detach and reattach', diagram: 'threads', figure: 'release', cues: [
      [149, 158, 'If a poll already has work, it returns straight away. If it has to wait, Pythago uses py.Release around the wait.'],
      [158, 167, 'Release detaches the thread state and returns any admission slot it held. Detaching also gives up the GIL on builds that use it.'],
      [167, 176, 'While the loop waits for a wakeup or deadline, other goroutines can enter Python. The blocking section needs an attached state to access Python objects.'],
      [176, 185, 'Before going back to Python, Release reacquires any slot it returned and restores the thread state. Then the loop can dispatch the results on its own thread.'],
    ],
  },
  {
    start: 185, end: 241, kicker: 'Crossing the call boundary', title: 'The callback\ntrampoline',
    sub: 'A shared C entry point adapts Python’s calling convention and dispatches to the registered Go function.',
    footer: 'The trampoline', diagram: 'request', figure: 'trampoline', cues: [
      [185, 195, 'A trampoline is a short adapter at a call boundary. Go can call a CPython symbol using the C calling convention. Now follow a call in the other direction.'],
      [195, 205, 'With py.NewFunc, a Go function becomes a Python builtin. These builtins share a C trampoline. Each carries the index of its Go function.'],
      [205, 215, 'The trampoline packs the function index, positional arguments, and keywords into a call frame. It enters Go through crosscall2, or a Windows callback.'],
      [215, 224, 'Go dispatches to the registered function on the calling Python thread, with its state attached. The call is synchronous, even from a coroutine.'],
      [224, 232, 'Go returns a Python value or raises an exception. Borrowed arguments expire when the call returns unless explicitly retained.'],
      [232, 241, 'A callback can start I/O and return immediately. If the work finishes later, the event loop handles that completion separately.'],
    ],
  },
  {
    start: 241, end: 280, kicker: 'Putting it together', title: 'Trace one\nDNS lookup',
    sub: 'The lookup crosses into Go synchronously. Its completion returns later through the loop.',
    footer: 'One lookup end to end', diagram: 'deploy', figure: 'lookup', cues: [
      [241, 251, 'The coroutine calls getaddrinfo. The loop creates a future and registers a handler under a token. It calls Go to resolve the address, then awaits the future.'],
      [251, 261, 'The Go call starts a lookup on a goroutine. If the loop has no ready work, poll can wait with the Python thread state detached.'],
      [261, 271, 'Go posts the token and result to the completion queue. The wakeup brings the loop back. It reattaches its state, reads the batch, and finds the handler for that token.'],
      [271, 280, 'The handler completes the future with a result or exception. The waiting task goes back in the ready queue. Python resumes it after await.'],
    ],
  },
  {
    start: 280, end: 342, kicker: 'Read the Python source', title: 'From token\nto future',
    sub: 'Trace the matching pair of calls in the asyncio adapter.',
    footer: 'Asyncio adapter code', diagram: 'bridge', figure: 'code', code: 'pythonLoop', cues: [
      [280, 290, 'Start in getaddrinfo. The loop creates a future, registers a completion handler, and passes the handler token to the native resolve function.'],
      [290, 300, 'Await suspends the coroutine if the future is pending. The handler eventually removes its own registration and sets the result or exception on that same future.'],
      [300, 310, 'The token is a key in the current loop’s handler table. Each operation gets a new key, so several requests can be outstanding on the same loop.'],
      [310, 320, 'GoEventLoop inherits Python’s base event loop. Its proactor adapter implements select by calling native poll with the timeout chosen by the loop.'],
      [320, 330, 'Each returned item names a token and event kind. Process events looks up the matching handler and calls it if it is still registered.'],
      [330, 342, 'The selector backend looks different. It returns ready file descriptors and an event mask. The standard selector loop can then perform the socket operation itself.'],
    ],
  },
  {
    start: 342, end: 404, kicker: 'Read the Go source', title: 'Queue, wake,\nand drain',
    sub: 'A Go worker returns an event through a queue owned by the loop.',
    footer: 'I/O and wakeup code', diagram: 'build', figure: 'code', code: 'goQueue', cues: [
      [342, 352, 'In Go, resolve reads the arguments before it starts a goroutine. The callback returns immediately, while the worker calls the Go network resolver.'],
      [352, 362, 'The worker posts a success or error with the original token. The queue holds Go data, which the loop later converts for Python.'],
      [362, 372, 'Posting takes the batch mutex. A full batch makes producers wait. When the batch changes from empty to nonempty, the producer sends a wakeup.'],
      [372, 382, 'The wake channel has room for one signal. Kick sends without blocking. If a signal is already pending, it leaves that signal in place.'],
      [382, 392, 'Poll first tries to take queued work. If it found anything or the timeout is zero, it can return without waiting.'],
      [392, 404, 'Otherwise, the loop releases its Python state and waits for a wakeup or a timer. It drains the batch again after the wait, even if the wakeup carried no work.'],
    ],
  },
  {
    start: 404, end: 466, kicker: 'Read the thread bridge', title: 'Attach, run,\nand restore',
    sub: 'The Go entry point and the C glue coordinate which thread can access Python.',
    footer: 'Thread-state code', diagram: 'threads', figure: 'code', code: 'threadState', cues: [
      [404, 414, 'An outer py.Do reserves an admission slot before locking the goroutine to its current operating system thread. The deferred cleanup reverses that setup.'],
      [414, 424, 'If the same thread has already entered Python, a nested Do calls enter directly. It does not wait for another slot or repin the goroutine.'],
      [424, 434, 'The Go enter helper asks C to attach. The C result distinguishes an attachment it just made from one it can reuse.'],
      [434, 444, 'The C glue looks for a cached state on this thread. It creates one if needed, then restores it before Python code runs.'],
      [444, 455, 'Release asks C to detach and remembers the reported slot. It returns that slot before calling the blocking function. Python access in that section needs a nested Do.'],
      [455, 466, 'Deferred cleanup reacquires the slot if there was one, then restores the saved thread state. The original thread can continue using Python.'],
    ],
  },
  {
    start: 466, end: 522, kicker: 'Thread layout', title: 'Who owns\nwhich thread?',
    sub: 'Compare a Go caller, the pthread wrapper, and a native Windows Python thread.',
    footer: 'Thread layout', diagram: 'threads', figure: 'threadLayout', cues: [
      [466, 475, 'There can be several operating system threads in one process. Each row shows who starts the work and which thread owns the Python state.'],
      [475, 484, 'A Go caller entering through py.Do is pinned to its current operating system thread. Pythago attaches a Python state on that thread.'],
      [484, 493, 'On the pthread wrapper path, a locked goroutine runs Python’s native start routine. A spare can already be waiting for that assignment.'],
      [493, 502, 'Before calling the start routine, the wrapper drops any Python state cached by py.Do on that thread. CPython can then bind its own state.'],
      [502, 512, 'On Windows, CPython creates its own native thread. The callback can still enter Go on that same thread while its Python state is attached.'],
      [512, 522, 'A Go I/O worker can run without attaching Python state. The loop thread detaches its own state while it waits for work.'],
    ],
  },
  {
    start: 522, end: 586, kicker: 'Stack layout', title: 'One OS thread,\ntwo stacks',
    sub: 'Follow the active frames during a nested call from Go into CPython and back into Go.',
    footer: 'Stack layout', diagram: 'bridge', figure: 'stackLayout', cues: [
      [522, 532, 'A Go goroutine has its own growable stack. On this call path, native code runs on the operating system thread’s system stack.'],
      [532, 542, 'On Linux and macOS, the caller prepares a C call frame. The runtime switches to the system stack before entering the assembly thunk.'],
      [542, 553, 'The thunk places arguments where the platform calling convention expects them. On amd64, the last two of its eight integer slots go on the stack.'],
      [553, 564, 'When CPython calls the C trampoline, crosscall2 enters the Go callback. The native call is still pending on the same operating system thread.'],
      [564, 574, 'The callback can grow and move the goroutine stack. The thunk compares the old and new stack tops before storing the C return value.'],
      [574, 586, 'Windows takes a separate route through SyscallN. The runtime keeps that outbound call frame with the operating system thread, outside the movable goroutine stack.'],
    ],
  },
  {
    start: 586, end: 650, kicker: 'Read the C trampoline', title: 'One entry point,\nmany functions',
    sub: 'An index identifies the Go function while the native adapter stays the same.',
    footer: 'Callback trampoline code', diagram: 'request', figure: 'code', code: 'callFrame', cues: [
      [586, 596, 'NewFunc adds the Go function to a registry under a lock. Its index is the length of the registry before the append.'],
      [596, 607, 'The native method definition points at the shared fast-call entry. The new Python builtin carries the function index as its self object.'],
      [607, 618, 'When Python calls the builtin, the C trampoline writes the argument pointers, counts, keyword names, and the index into a call frame.'],
      [618, 629, 'C decodes a small prefix of arguments and keyword names. Go can use these cached representations for common values while it still has the original objects.'],
      [629, 640, 'The trampoline tracks callback depth while it calls the Go dispatcher. Windows uses a native callback pointer. Other targets here enter through crosscall2.'],
      [640, 650, 'On the way back, a borrowed result needs a new Python reference. The trampoline increments its reference count before returning it to the interpreter.'],
    ],
  },
  {
    start: 650, end: 712, kicker: 'Read the Go dispatcher', title: 'Arguments\nand returns',
    sub: 'The dispatcher separates argument lifetime, callback execution, and the result sent to Python.',
    footer: 'Dispatch and ownership code', diagram: 'bridge', figure: 'code', code: 'dispatch', cues: [
      [650, 660, 'The Go entry reads the same call frame that C allocated. It writes the result and ownership flag into that frame for C to read.'],
      [660, 670, 'The dispatcher uses the index to read the Go function from the registry. It drops the read lock before invoking the function.'],
      [670, 680, 'The argument pointers are borrowed. Their wrappers can use the values during the call. A Go function must retain a reference to keep an object longer.'],
      [680, 690, 'The positional slice and keyword map point to those wrappers. Keyword names map to slots after the positional arguments. The final call uses both collections.'],
      [690, 700, 'An ordinary Go error becomes a Python exception. The deferred recover path also converts a Go panic and leaves a null result for Python.'],
      [700, 712, 'The return path distinguishes an owned object from a borrowed object. An owned reference transfers to Python. A borrowed reference is marked so C can take a new reference.'],
    ],
  },
  {
    start: 712, end: 740, kicker: 'Follow the result', title: 'Back to\nthe loop',
    sub: 'The same token survives the round trip through Go and back to the Python handler.',
    footer: 'Completion conversion code', diagram: 'deploy', figure: 'code', code: 'roundTrip', cues: [
      [712, 722, 'Poll calls wait and gets the batch after the thread state is attached again. This is where Go can safely turn event data into Python values.'],
      [722, 731, 'The poll response is a list of tuples. Each tuple preserves the original token, the event kind, and the data the Python handler expects.'],
      [731, 740, 'The lookup handler completes the future. Its waiter returns to the ready queue, and the coroutine picks up where it left off.'],
    ],
  },
  {
    start: 740, end: 760, kicker: 'Recap', title: 'Three jobs,\none process',
    sub: 'Scheduling, Python thread state, and call dispatch each solve a different part of the same trip.',
    footer: 'Pythago internals', diagram: 'outro', figure: 'recap', cues: [
      [740, 749, 'The loop decides what resumes. The operating system thread carries Python’s attached state. The trampoline adapts the synchronous call into Go.'],
      [749, 760, 'When the next coroutine awaits a network result, follow the same path. Go finishes the operation, wakes the loop, and Python resumes the waiting task.'],
    ],
  },
];
