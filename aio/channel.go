package aio

import (
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"github.com/moriyoshi/pythago/py"
)

//go:embed channel.py
var channelSource string

// Channel operations use concrete generic instantiations, never reflect.Select.
type channelAdapter interface {
	decodeObject(*py.Object) (any, error)
	prepare(send bool, value any) (channelResult, func(*channelOp) channelResult, error)
	close() error
}

type typedChannel[T any] struct {
	send         chan<- T
	recv         <-chan T
	canSend      bool
	canRecv      bool
	decode       func(any) (T, error)
	encode       func(T) any
	objectDecode func(*py.Object) (T, error)
}

func channelType[T any](v any, decode func(any) (T, error), encode func(T) any) (channelAdapter, bool) {
	ch := &typedChannel[T]{decode: decode, encode: encode}
	switch v := v.(type) {
	case chan T:
		ch.send, ch.recv, ch.canSend, ch.canRecv = v, v, true, true
	case chan<- T:
		ch.send, ch.canSend = v, true
	case <-chan T:
		ch.recv, ch.canRecv = v, true
	default:
		return nil, false
	}
	return ch, true
}

func (ch *typedChannel[T]) result(value T) any {
	if ch.encode != nil {
		return ch.encode(value)
	}
	return value
}

func (ch *typedChannel[T]) decodeObject(o *py.Object) (any, error) {
	if !ch.canSend {
		return nil, errors.New("aio: cannot send on a receive-only channel")
	}
	if ch.objectDecode != nil {
		return ch.objectDecode(o)
	}
	v, err := o.Go()
	if err != nil {
		return nil, err
	}
	defer closeChannelObjects(v)
	if !channelData(v) {
		return nil, errors.New("aio: channel values must be convertible Go data")
	}
	return ch.decode(v)
}

// A native nonblocking select is the fast path. Only an operation which would
// block returns a worker, so ready operations need no operation handle or Future.
func (ch *typedChannel[T]) prepare(send bool, value any) (channelResult, func(*channelOp) channelResult, error) {
	if send {
		if !ch.canSend {
			return channelResult{}, nil, errors.New("aio: cannot send on a receive-only channel")
		}
		var v T
		if value != nil {
			v = value.(T)
		}
		owned := true
		defer func() {
			if owned {
				discardChannelValue(v)
			}
		}()
		select {
		case ch.send <- v:
			owned = false
			return channelResult{}, nil, nil
		default:
		}
		owned = false // the worker now owns an unsent buffer lease
		return channelResult{}, func(op *channelOp) channelResult {
			sent := false
			defer func() {
				if !sent {
					discardChannelValue(v)
				}
			}()
			select {
			case ch.send <- v:
				sent = true
			case <-op.stop:
			}
			return channelResult{}
		}, nil
	}
	if !ch.canRecv {
		return channelResult{}, nil, errors.New("aio: cannot receive from a send-only channel")
	}

	select {
	case v, ok := <-ch.recv:
		return ch.receiveResult(v, ok), nil, nil
	default:
	}
	return channelResult{}, func(op *channelOp) channelResult {
		select {
		case v, ok := <-ch.recv:
			return ch.receiveResult(v, ok)
		case <-op.stop:
			return channelResult{}
		}
	}, nil
}

func (ch *typedChannel[T]) receiveResult(v T, ok bool) channelResult {
	status := channelReady
	if !ok {
		status = channelClosed
	}
	return channelResult{value: ch.result(v), status: status}
}

func (ch *typedChannel[T]) close() error {
	if !ch.canSend {
		return errors.New("aio: cannot close a receive-only channel")
	}
	close(ch.send)
	return nil
}

var channelTypes struct {
	sync.RWMutex
	converters []func(any) (channelAdapter, bool)
}

// RegisterChannelType enables direct conversion of chan T, <-chan T and chan<- T
// for an additional element type. Call before py.Init. Decode converts data from
// Object.Go to T; encode converts T to data accepted by py.From. Both functions
// must operate on Go data without Python API calls. A nil decode uses a type
// assertion to T; a nil encode passes T to py.From unchanged.
// Built-in variants take precedence over registrations.
func RegisterChannelType[T any](decode func(any) (T, error), encode func(T) any) {
	if decode == nil {
		decode = channelExact[T]
	}
	channelTypes.Lock()
	channelTypes.converters = append(channelTypes.converters, func(v any) (channelAdapter, bool) {
		return channelType(v, decode, encode)
	})
	channelTypes.Unlock()
}

// The wrapper owns a handle, not the channel: collecting it must not close a
// channel still in use by Go. Buffer leases keep exporters alive explicitly.
func channelObject(v any) (*py.Object, bool, error) {
	ch, found := builtinChannel(v)
	if !found {
		channelTypes.RLock()
		for _, convert := range channelTypes.converters {
			if ch, found = convert(v); found {
				break
			}
		}
		channelTypes.RUnlock()
	}
	if !found {
		return nil, false, nil
	}
	h := register(ch)
	ns, err := py.From(map[string]any{"_handle": h, "_release": py.Func(channelRelease)})
	if err != nil {
		unregister(h)
		return nil, true, err
	}
	defer ns.Close()
	if err := py.ExecIn(channelSource, ns); err != nil {
		unregister(h)
		return nil, true, err
	}
	o, err := ns.Item("_channel")
	if err != nil {
		unregister(h)
		return nil, true, err
	}
	// Remove the namespace reference so the wrapper can be collected promptly.
	if err := py.ExecIn("del _channel", ns); err != nil {
		o.Close()
		unregister(h)
		return nil, true, err
	}
	return o, true, nil
}

func channelRelease(a []*py.Object, _ py.Kwargs) (any, error) {
	unregister(intArg(a, 0))
	return nil, nil
}

const (
	channelReady = iota
	channelClosed
	channelPending
	channelError
)

type channelState uint8

const (
	channelWaiting channelState = iota
	channelCompleted
	channelCancelled
)

type channelOp struct {
	mu    sync.Mutex
	stop  chan struct{}
	done  chan struct{}
	loop  *aioLoop
	token int64
	id    int64
	state channelState
}

// channelLease carries owned Python or shared-memory payloads across a channel:
// *py.String and *py.OwnedBuffer.
type channelLease interface {
	TakeObject() (*py.Object, error)
	Close() error
}

type channelResult struct {
	value  any
	status int
}

// Common values go straight into the outer result tuple, without allocating an
// intermediate owned Object. Complex/custom values retain per-message errors.
func (r channelResult) python() any {
	if b, ok := r.value.(channelLease); ok {
		o, err := b.TakeObject()
		if err != nil {
			return py.Tuple{channelError, err.Error()}
		}
		defer o.Close()
		out, err := py.From(py.Tuple{r.status, o})
		if err != nil {
			return py.Tuple{channelError, err.Error()}
		}
		return out
	}
	switch r.value.(type) {
	case nil, bool, string, []byte, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr, float32, float64:
		return py.Tuple{r.status, r.value}
	}
	o, err := py.From(r.value)
	if err != nil {
		return py.Tuple{channelError, err.Error()}
	}
	defer o.Close()
	out, err := py.From(py.Tuple{r.status, o})
	if err != nil {
		return py.Tuple{channelError, err.Error()}
	}
	return out
}

func discardChannelValue(value any) {
	if b, ok := value.(channelLease); ok {
		_ = b.Close()
	}
}

// finish serializes publication with cancellation. The loop's batch mutex also
// serializes publication with shutdown. Queueing never waits for backpressure.
func (op *channelOp) finish(result channelResult) {
	op.mu.Lock()
	if op.state == channelCancelled {
		op.mu.Unlock()
		discardChannelValue(result.value)
		return
	}
	op.state = channelCompleted
	b := op.loop.batch
	b.mu.Lock()
	closed := op.loop.channelClosed
	if !closed {
		b.items = append(b.items, completion{token: op.token, kind: evGo, val: result})
	}
	b.mu.Unlock()
	op.mu.Unlock()
	if closed {
		discardChannelValue(result.value)
	} else {
		kick(b.wake)
	}
}

func (op *channelOp) cancel() {
	op.mu.Lock()
	if op.state == channelWaiting {
		op.state = channelCancelled
		close(op.stop)
	}
	op.mu.Unlock()
	// Also join completed workers: no transferred resource is left in flight.
	select {
	case <-op.done:
	default:
		py.Release(func() { <-op.done })
	}
}

func startChannel(ch channelAdapter, loopID, tokenObject *py.Object, send bool, value *py.Object) (any, error) {
	var decoded any
	if send {
		var err error
		decoded, err = ch.decodeObject(value)
		if err != nil {
			return nil, err
		}
	}
	result, work, err := ch.prepare(send, decoded)
	if err != nil {
		discardChannelValue(decoded)
		return nil, err
	}
	if work == nil {
		return result.python(), nil
	}
	// Ready operations do not need to parse loop/token integers or look up a loop.
	// Keep ownership of an unsent lease until the pending worker is launched.
	owned := true
	defer func() {
		if owned {
			discardChannelValue(decoded)
		}
	}()
	l := lookup[*aioLoop](intArg([]*py.Object{loopID}, 0))
	if l == nil {
		return nil, errors.New("aio: closed event loop")
	}
	token := intArg([]*py.Object{tokenObject}, 0)
	op := &channelOp{stop: make(chan struct{}), done: make(chan struct{}), loop: l, token: token}
	op.id = register(op)
	go op.run(work)
	owned = false
	return py.Tuple{channelPending, op.id}, nil
}

func channelModule(attrs map[string]any) {
	for _, send := range []bool{false, true} {
		name := "channel_recv"
		if send {
			name = "channel_send"
		}
		attrs[name] = py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			want := 3
			if send {
				want++
			}
			if len(a) != want {
				return nil, errors.New("aio: invalid channel arguments")
			}
			ch := lookup[channelAdapter](intArg(a, 0))
			if ch == nil {
				return nil, errors.New("aio: invalid channel or closed event loop")
			}
			var value *py.Object
			if send {
				value = a[3]
			}
			return startChannel(ch, a[1], a[2], send, value)
		})
	}
	attrs["channel_cancel"] = py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
		if op := lookup[*channelOp](intArg(a, 0)); op != nil {
			op.cancel()
		}
		return nil, nil
	})
	attrs["channel_close"] = py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
		ch := lookup[channelAdapter](intArg(a, 0))
		if ch == nil {
			return nil, errors.New("aio: released channel")
		}
		return nil, ch.close()
	})
}

func (op *channelOp) run(work func(*channelOp) channelResult) {
	defer close(op.done)
	defer unregister(op.id)
	// Always invoke work, even after cancellation: it owns any decoded lease.
	result := func() (r channelResult) {
		defer func() {
			if p := recover(); p != nil {
				r = channelResult{status: channelError, value: fmt.Sprint(p)}
			}
		}()
		return work(op)
	}()
	op.finish(result)
}

// closeChannelQueue runs on the loop thread after its pending operations have
// been cancelled. A racing completed worker sees channelClosed under batch.mu.
func (l *aioLoop) closeChannelQueue() {
	l.batch.mu.Lock()
	l.channelClosed = true
	items := l.items
	l.items = nil
	l.cond.Broadcast()
	l.batch.mu.Unlock()
	for _, item := range items {
		if item.pool != nil {
			readBufs.Put(item.pool)
		}
		if r, ok := item.val.(channelResult); ok {
			discardChannelValue(r.value)
		}
	}
}

// Object.Go returns owned references for objects without a data conversion. Reject
// these recursively, and release every such reference on the calling Python thread.
func closeChannelObjects(v any) {
	switch v := v.(type) {
	case *py.Object:
		v.Close()
	case []any:
		for _, item := range v {
			closeChannelObjects(item)
		}
	case map[any]any:
		for key, item := range v {
			closeChannelObjects(key)
			closeChannelObjects(item)
		}
	}
}

func channelData(v any) bool {
	switch v := v.(type) {
	case *py.Object:
		return false
	case []any:
		for _, item := range v {
			if !channelData(item) {
				return false
			}
		}
	case map[any]any:
		for key, item := range v {
			if !channelData(key) || !channelData(item) {
				return false
			}
		}
	}
	return true
}
