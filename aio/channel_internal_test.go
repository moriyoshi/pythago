package aio

import (
	"testing"

	"github.com/moriyoshi/pythago/py"
)

func TestChannelShutdownReleasesQueuedLeases(t *testing.T) {
	l := &aioLoop{batch: newBatch[completion]()}
	first := py.NewOwnedBuffer([]byte{1}, true)
	op := &channelOp{loop: l}
	op.finish(channelResult{value: first})
	l.closeChannelQueue()
	if _, err := first.Bytes(); err == nil {
		t.Fatal("queued lease survived shutdown")
	}
	late := py.NewOwnedBuffer([]byte{2}, true)
	(&channelOp{loop: l}).finish(channelResult{value: late})
	if _, err := late.Bytes(); err == nil {
		t.Fatal("late completion leaked its lease")
	}
	cancelled := py.NewOwnedBuffer([]byte{3}, true)
	(&channelOp{loop: l, state: channelCancelled}).finish(channelResult{value: cancelled})
	if _, err := cancelled.Bytes(); err == nil {
		t.Fatal("cancelled completion leaked its lease")
	}
}

func TestChannelWorkerRetiresHandle(t *testing.T) {
	native := make(chan int64)
	adapter, _ := builtinChannel(native)
	_, work, err := adapter.prepare(false, nil)
	if err != nil || work == nil {
		t.Fatalf("expected pending receive: %v", err)
	}
	loop := &aioLoop{batch: newBatch[completion]()}
	op := &channelOp{loop: loop, stop: make(chan struct{}), done: make(chan struct{})}
	op.id = register(op)
	go op.run(work)
	native <- 42
	<-op.done
	if lookup[*channelOp](op.id) != nil {
		t.Fatal("completed operation still registered")
	}
	results := loop.take()
	if len(results) != 1 || results[0].val.(channelResult).value != int64(42) {
		t.Fatalf("completion: %v", results)
	}
}

func TestChannelShutdownReleasesUnicode(t *testing.T) {
	l := &aioLoop{batch: newBatch[completion]()}
	value, err := py.NewString("queued")
	if err != nil {
		t.Fatal(err)
	}
	(&channelOp{loop: l}).finish(channelResult{value: value})
	l.closeChannelQueue()
	if _, err := value.GoString(); err == nil {
		t.Fatal("queued Unicode survived shutdown")
	}
	value, err = py.NewString("cancelled")
	if err != nil {
		t.Fatal(err)
	}
	(&channelOp{loop: l, state: channelCancelled}).finish(channelResult{value: value})
	if _, err := value.GoString(); err == nil {
		t.Fatal("cancelled Unicode lease leaked")
	}
	value, err = py.NewString("late")
	if err != nil {
		t.Fatal(err)
	}
	(&channelOp{loop: l}).finish(channelResult{value: value})
	if _, err := value.GoString(); err == nil {
		t.Fatal("late Unicode lease leaked")
	}
}
