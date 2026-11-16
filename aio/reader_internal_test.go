package aio

import (
	"net"
	"testing"
	"time"
)

// TestReaderWaitsForPoll: while the loop thread does not poll, a connection's reader
// queues at most dataInFlight EV_DATA events, however much the peer sends; handing back
// a credit, as poll does after copying an event out, lets it post exactly one more. A
// reader that kept reading until Python's pause_reading arrived queued megabytes while
// the loop thread could not run.
func TestReaderWaitsForPoll(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		chunk := make([]byte, 1<<20)
		for {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	l := &aioLoop{batch: newBatch[completion]()}
	h := newConn(l, c)
	handleMu.Lock()
	ac := handles[h].(*aioConn)
	handleMu.Unlock()
	defer func() {
		ac.aborted.Store(true)
		c.Close()
		kick(ac.wake)
		kick(ac.resume)
		unregister(h)
		<-ac.readerDone
	}()
	ac.token.Store(1)
	ac.readerOn.Store(true)
	go ac.reader()

	queued := func() (evs []completion) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, e := range l.items {
			if e.kind == evData {
				evs = append(evs, e)
			}
		}
		return evs
	}
	time.Sleep(300 * time.Millisecond) // the loop thread is busy: nothing polls
	evs := queued()
	if len(evs) != dataInFlight {
		t.Fatalf("%d EV_DATA events queued while the loop did not poll, want %d", len(evs), dataInFlight)
	}

	// What poll does: take the events, copy them out, hand back the credits.
	for _, e := range l.take() {
		if e.kind == evData {
			readBufs.Put(e.pool)
			<-e.held
		}
	}
	time.Sleep(300 * time.Millisecond)
	if evs := queued(); len(evs) != dataInFlight {
		t.Fatalf("%d EV_DATA events queued after one poll, want %d", len(evs), dataInFlight)
	}
}
