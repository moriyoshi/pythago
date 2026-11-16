package wsgi

import (
	"sync"
	"sync/atomic"

	"github.com/moriyoshi/pythago/py"
)

// CountDetachedWrites counts the response body writes made with the thread state detached
// (mid-request, in Python's call) until the returned function is called; it restores the
// writer and returns the count. Not for parallel tests.
func CountDetachedWrites() func() int64 {
	var n atomic.Int64
	orig := writeDetached
	writeDetached = func(fn func()) {
		n.Add(1)
		orig(fn)
	}
	return func() int64 {
		writeDetached = orig
		return n.Load()
	}
}

// HoldCall makes the first _pythago_wsgi call of function fn ("respond", "body",
// "sendfile", "readinto") that finds its request stop while it holds the request's
// reference, before it acts on it, with its thread state detached. held is closed once it
// stops. release lets it go on and reports the handle the call presented and the
// lifetime its request serves when it went on: they differ when the request was recycled
// (and taken by another) meanwhile; ok is false if no call stopped. Not for parallel
// tests.
func HoldCall(fn string) (held <-chan struct{}, release func() (presented, serving int64, ok bool)) {
	stopped := make(chan struct{})
	gate := make(chan struct{})
	report := make(chan [2]int64, 1)
	var armed atomic.Bool
	armed.Store(true)
	onAcquire = func(q *request, h int64, name string) {
		if name != fn || !armed.CompareAndSwap(true, false) {
			return
		}
		close(stopped)
		py.Release(func() { <-gate })
		report <- [2]int64{h, q.sc.ID()}
	}
	var once sync.Once
	var result [2]int64
	var ok bool
	return stopped, func() (int64, int64, bool) {
		once.Do(func() { // later calls report the same
			close(gate)
			select {
			case <-stopped:
				result, ok = <-report, true
			default:
			}
			onAcquire = nil
		})
		return result[0], result[1], ok
	}
}
