//go:build pythago_spike

package py

// Experimental (.agents/attic/spike/sinline): the Go side of the C-resident combiner in the
// glue (pythago_comb_*). Built only with -tags pythago_spike.

import (
	"encoding/binary"
	"errors"
	"unsafe"
)

// SpikeCombiner runs fn(*arguments of a batch of submitted calls) on one Python thread
// that stays in C; fn returns the batch's responses as records (<HII status, header
// length, body length, then the header and the body).
type SpikeCombiner struct{ c uintptr }

// cjob is glue struct pythago_job, followed by Go-only fields.
type cjob struct {
	next   uintptr
	items  uintptr
	nitems int

	vals   []pyValue
	keep   []any // the payloads the values point into
	status int
	hdr    []byte
	body   []byte
	err    string
	done   chan struct{}
}

// cbatch is glue struct pythago_batch.
type cbatch struct {
	next, jobs, result, data uintptr
	len                      int
}

// NewSpikeCombiner starts the combiner thread and completers completer goroutines, to
// which finished batches go round-robin. Inside Do.
func NewSpikeCombiner(fn *Object, completers int) (*SpikeCombiner, error) {
	for _, name := range []string{"pythago_comb_new", "pythago_comb_run", "pythago_comb_take",
		"pythago_comb_done", "pythago_comb_push", "pythago_comb_stats"} {
		if symbols[name] == 0 {
			return nil, errors.New("py: the native image's glue lacks " + name)
		}
	}
	c := call2(symbols["pythago_comb_new"], fn.p, uintptr(completers))
	if c == 0 {
		return nil, errors.New("pythago_comb_new")
	}
	go Do(func() error { // takes one admission slot for good
		call1(symbols["pythago_comb_run"], c)
		return nil
	})
	for k := range max(1, min(completers, 16)) {
		go func() {
			take, done := symbols["pythago_comb_take"], symbols["pythago_comb_done"]
			for {
				first := call2(take, c, uintptr(k)) // waits in C: no GIL, no admission slot
				for b := first; b != 0; b = (*cbatch)(unsafe.Pointer(b)).next {
					complete((*cbatch)(unsafe.Pointer(b)))
				}
				call2(done, c, first)
			}
		}()
	}
	return &SpikeCombiner{c: c}, nil
}

func complete(b *cbatch) {
	data := unsafe.Slice((*byte)(unsafe.Pointer(b.data)), b.len)
	for p := b.jobs; p != 0; {
		j := (*cjob)(unsafe.Pointer(p))
		p = j.next
		if len(data) < 10 {
			j.err = "the batch function failed"
		} else {
			st := int(binary.LittleEndian.Uint16(data))
			hl := int(binary.LittleEndian.Uint32(data[2:]))
			bl := int(binary.LittleEndian.Uint32(data[6:]))
			data = data[10:]
			j.hdr = append([]byte(nil), data[:hl]...)
			j.body = append([]byte(nil), data[hl:hl+bl]...)
			data = data[hl+bl:]
			if j.status = st; st == 0 {
				j.err = string(j.body)
			}
		}
		close(j.done)
	}
}

// Submit queues a call with args (strings, []byte, ints, bools and nil only) and waits
// for its response record.
func (s *SpikeCombiner) Submit(args ...any) (status int, hdr, body []byte, err string) {
	j := &cjob{vals: make([]pyValue, len(args)), keep: args, done: make(chan struct{})}
	for i, a := range args {
		v := &j.vals[i]
		switch a := a.(type) {
		case nil:
			v.tag = pvNone
		case bool:
			v.tag = pvFalse
			if a {
				v.tag = pvTrue
			}
		case int:
			*v = pyValue{tag: pvInt, a: uint64(a)}
		case int64:
			*v = pyValue{tag: pvInt, a: uint64(a)}
		case string:
			*v = pyValue{tag: pvStr, a: uint64(uintptr(unsafe.Pointer(unsafe.StringData(a)))), b: uint64(len(a))}
		case []byte:
			*v = pyValue{tag: pvBytes, a: uint64(uintptr(unsafe.Pointer(unsafe.SliceData(a)))), b: uint64(len(a))}
		default:
			return 0, nil, nil, "unsupported argument type"
		}
	}
	j.items, j.nitems = uintptr(unsafe.Pointer(unsafe.SliceData(j.vals))), len(args)
	call2(symbols["pythago_comb_push"], s.c, uintptr(unsafe.Pointer(j)))
	<-j.done
	return j.status, j.hdr, j.body, j.err
}

// Stats returns the combiner's counters: batches, jobs, sleeps, and nanoseconds asleep,
// building arguments, in the batch function, and handing off.
func (s *SpikeCombiner) Stats() (st [7]uint64) {
	call2(symbols["pythago_comb_stats"], s.c, uintptr(unsafe.Pointer(&st)))
	return st
}
