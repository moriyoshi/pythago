package py

import (
	"sync"

	"github.com/moriyoshi/pythago/internal/arena"
)

// A callScope is the borrowed memory of one call: a Python→Go call of a Func (its
// arguments, positional slice and keyword names) or a Go→C build or call (the values it
// describes and their payloads). One goroutine uses it, from the call to its return; each
// active call has its own (calls nest on one OS thread, Python → Go → Python → Go, and run
// concurrently on many), and release puts it back into callScopes for a later call.
//
// The lanes grow to fit any call, so no call is limited in its number of arguments or
// keywords, and keep up to arena.Keep bytes each for the next call. A call that fits the
// inline arrays uses them directly, without the lanes' bookkeeping, which costs a
// one-argument call about 5% (2026-11-19: 94 against 99 ns).
type callScope struct {
	objs  arena.Lane[Object] // borrowed arguments: poisoned on release
	ptrs  arena.Lane[*Object]
	strs  arena.Lane[string]
	vals  arena.Lane[pyValue]
	bytes arena.Lane[byte]

	// inline is how many of objsIn a call used directly (-1: none).
	inline int
	objsIn [8]Object
	ptrsIn [8]*Object
	strsIn [8]string
	valsIn [8]pyValue
	n      int64 // kwName's scratch
}

var callScopes = sync.Pool{New: func() any {
	c := &callScope{inline: -1}
	c.objs.Init(c.objsIn[:], false)
	// dispatch and build write every element they take: their lanes need no zeroing.
	// What the old contents point to is the scope's own memory or kept anyway (cached
	// keyword names).
	c.ptrs.Init(c.ptrsIn[:], true)
	c.strs.Init(c.strsIn[:], true)
	c.vals.Init(c.valsIn[:], true)
	c.bytes.Init(nil, true)
	return c
}}

// checkBorrows is Config.CheckBorrows: released call scopes are never reused.
var checkBorrows bool

// releasedValue marks the borrowed *Object values of a released call: their accessors
// panic with errReleased (live) instead of reading another call's argument, or freed
// memory.
var releasedValue = pyValue{tag: pvReleased}

const errReleased = "py: a borrowed *Object was used after the call that lent it returned (keep a NewRef instead)"

func acquireCall() *callScope { return callScopes.Get().(*callScope) }

// release ends the call: it poisons the borrowed Objects the call handed out, resets the
// lanes it took from and puts the scope back (with Config.CheckBorrows, it never does:
// the poison stays).
func (c *callScope) release() {
	if c.inline >= 0 {
		poison(c.objsIn[:c.inline])
		c.inline = -1
	} else {
		if c.objs.Touched() {
			c.objs.ResetWith(poison)
		}
		if c.ptrs.Touched() {
			c.ptrs.Reset()
		}
		if c.strs.Touched() {
			c.strs.Reset()
		}
		if c.vals.Touched() {
			c.vals.Reset()
		}
		if c.bytes.Touched() {
			c.bytes.Reset()
		}
	}
	if !checkBorrows {
		callScopes.Put(c)
	}
}

// poison marks objs released: p and pre, which every accessor reads first (live); the
// next taker writes whole Objects.
func poison(objs []Object) {
	for i := range objs {
		objs[i].p, objs[i].pre = 0, &releasedValue
	}
}
