package aio

import "github.com/moriyoshi/pythago/py"

// Readers and writers on GoEventLoop (add_reader, add_writer): the loop keeps a readiness
// selector (GoSelector's watchers, selector_unix.go and selector_windows.go) whose reports
// go into the loop's completion queue as evReady instead of the selector's own, so the
// loop waits for both in one poll. As with GoSelector, a watcher that reported waits to be
// re-armed, by the next poll (after the callbacks ran), then re-checks the level.

// newSelector is sel_new([loop]): a selector for GoSelector, or, given a loop, for that
// loop's readers and writers.
func newSelector(a []*py.Object) any {
	s := &selector{batch: newBatch[selEvent](), fds: map[int]*selFD{}}
	if len(a) > 0 { // the loop is open: _add_reader and _add_writer check
		s.loop = lookup[*aioLoop](intArg(a, 0))
	}
	return register(s)
}

// notify reports that e.w is ready in direction e.dir.
func (s *selector) notify(e selEvent) {
	if s.loop != nil {
		s.loop.post(completion{kind: evReady, val: e})
		return
	}
	s.post(e)
}
