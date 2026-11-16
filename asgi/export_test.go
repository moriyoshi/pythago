package asgi

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// WaitIdle waits until no one holds any of s's workers: each loop thread waits for
// events and no inline request runs or is queued. A request that arrives while one is
// held runs on the holder's thread (flat combining), which may be the loop thread, still
// finishing a request that continued there or the startup; only a request that finds
// its worker free is guaranteed to run inline, on its own goroutine.
func WaitIdle(s *Server, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for i, w := range s.workers {
		for {
			w.mu.Lock()
			idle := !w.busy && len(w.pending) == 0
			w.mu.Unlock()
			if idle {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("worker %d still held after %v", i, timeout)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}
	return nil
}

// HoldCall makes the first _pythago_asgi function (start, body, respond, ...) that finds
// its request stop while it holds the request's reference, before it acts on it. held is
// closed once it stops. release lets it go on, and reports the request's handle when the
// call stopped and when it went on: they differ when the request was recycled (and taken
// by another) meanwhile; ok is false if no call stopped. Call it before s serves.
func HoldCall(s *Server) (held <-chan struct{}, release func() (before, after int64, ok bool)) {
	stopped := make(chan struct{})
	gate := make(chan struct{})
	report := make(chan [2]int64, 1)
	var armed atomic.Bool
	armed.Store(true)
	s.onCall = func(q *request) {
		if !armed.CompareAndSwap(true, false) {
			return
		}
		h := q.h
		close(stopped)
		<-gate
		report <- [2]int64{h, q.h}
	}
	var once sync.Once
	return stopped, func() (int64, int64, bool) {
		once.Do(func() { close(gate) })
		select {
		case <-stopped:
		default:
			return 0, 0, false
		}
		r, ok := <-report
		if ok {
			close(report)
		}
		return r[0], r[1], ok
	}
}

// Requests is how many requests s has registered and not finished.
func Requests(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// Queued is how many inline requests wait in s's workers' queues.
func Queued(s *Server) int {
	n := 0
	for _, w := range s.workers {
		w.mu.Lock()
		n += len(w.pending)
		w.mu.Unlock()
	}
	return n
}
