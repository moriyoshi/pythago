// Package arena is borrowed memory with a lifetime: a Lane hands out slices of one
// element type during a lifetime (a call, a request) and takes them all back at once
// when it ends, keeping its memory for the next lifetime up to a bound.
//
// A lane grows to fit whatever one lifetime takes, so its users have no fixed limit (on
// arguments, fields, bytes), and keeps up to Keep bytes for the next lifetime, so that a
// typical lifetime allocates nothing while one huge one does not pin its memory.
//
// A Lane is not safe for concurrent use: one goroutine at a time takes from it and
// resets it.
package arena

import "unsafe"

// Keep is how many bytes of its memory a lane keeps between lifetimes.
const Keep = 64 << 10

// A Lane is memory of element type T for one lifetime at a time: Take hands out slices
// of it, valid until Reset ends the lifetime. The zero Lane is ready to use.
type Lane[T any] struct {
	buf    []T   // the chunk takes come from
	used   int   // how much of buf this lifetime took
	full   [][]T // chunks filled earlier in this lifetime, still in use
	home   []T   // the chunk a trimmed lane returns to (Init's, or none)
	keep   int   // most elements kept between lifetimes (0: Keep bytes' worth)
	noZero bool
}

// Init gives the lane a first chunk, home (memory the lane's owner holds, such as an
// array in the same struct), which it returns to whenever it trims. With noZero, Reset
// does not zero what was taken: every taker overwrites what it takes, and what the old
// contents point to is kept alive anyway.
func (l *Lane[T]) Init(home []T, noZero bool) {
	l.buf, l.home, l.noZero = home, home, noZero
	l.keep = max(Keep/max(int(unsafe.Sizeof(*new(T))), 1), len(home))
}

// Take returns n elements, as a full slice (an append copies), valid until Reset. They
// are zeroed unless the lane was made with noZero.
func (l *Lane[T]) Take(n int) []T {
	if n <= len(l.buf)-l.used {
		s := l.buf[l.used : l.used+n : l.used+n]
		l.used += n
		return s
	}
	return l.grow(n)
}

// grow starts a new chunk for a take that does not fit: the earlier one stays valid, and
// the new one is twice as large (or n), so that a lifetime of the same shape fits in one
// chunk next time.
func (l *Lane[T]) grow(n int) []T {
	if l.used > 0 {
		l.full = append(l.full, l.buf[:l.used])
	}
	l.buf = make([]T, max(n, 2*len(l.buf), 8))
	l.used = n
	return l.buf[:n:n]
}

// Rest returns the unused part of the current chunk as an empty slice to append to (a
// buffer built in place); Adopt gives what was appended back to the lane.
func (l *Lane[T]) Rest() []T { return l.buf[l.used:l.used] }

// Adopt takes s, appended to the slice Rest returned, as taken: in place, or, if append
// moved it to a larger array, as the lane's new chunk.
func (l *Lane[T]) Adopt(s []T) {
	if len(s) == 0 {
		return
	}
	if unsafe.SliceData(s) == unsafe.SliceData(l.buf[l.used:cap(l.buf)]) {
		l.used += len(s)
		return
	}
	if l.used > 0 {
		l.full = append(l.full, l.buf[:l.used])
	}
	l.buf, l.used = s[:cap(s)], len(s)
}

// Touched reports whether this lifetime took from the lane.
func (l *Lane[T]) Touched() bool { return l.used > 0 || len(l.full) > 0 }

// Reset ends the lifetime: what was taken is zeroed (unless noZero) and may be handed
// out again, and the lane drops the chunks filled earlier and, past its bound, the
// current one.
func (l *Lane[T]) Reset() {
	if !l.noZero {
		clear(l.buf[:l.used])
	}
	l.trim()
}

// ResetWith is Reset passing each chunk this lifetime took from to clean instead of
// zeroing it (to poison it, say).
func (l *Lane[T]) ResetWith(clean func([]T)) {
	clean(l.buf[:l.used])
	for _, c := range l.full {
		clean(c)
	}
	l.trim()
}

func (l *Lane[T]) trim() {
	clear(l.full)
	l.full = l.full[:0]
	if l.keep == 0 {
		l.keep = max(Keep/max(int(unsafe.Sizeof(*new(T))), 1), len(l.home))
	}
	if len(l.buf) > l.keep {
		l.buf = l.home
	}
	l.used = 0
}

// Cap returns the size of the lane's current chunk (for tests).
func (l *Lane[T]) Cap() int { return len(l.buf) }

// AtHome reports whether the lane's current chunk is its home chunk (for tests).
func (l *Lane[T]) AtHome() bool {
	return len(l.buf) == len(l.home) && unsafe.SliceData(l.buf) == unsafe.SliceData(l.home)
}
