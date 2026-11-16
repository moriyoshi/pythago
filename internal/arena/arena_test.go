package arena

import (
	"testing"
	"unsafe"
)

// Takes within one lifetime never overlap, also across a chunk change; what they return
// is zeroed, also when the lane reuses the memory of an earlier lifetime.
func TestTakesAreDistinctAndZeroed(t *testing.T) {
	var l Lane[int]
	for life := range 3 {
		var got [][]int
		for k := range 40 {
			s := l.Take(1 + k%7)
			for i := range s {
				if s[i] != 0 {
					t.Fatalf("lifetime %d, take %d: element %d is %d; want it zeroed", life, k, i, s[i])
				}
				s[i] = k + 1
			}
			got = append(got, s)
		}
		for k, s := range got {
			for i, v := range s {
				if v != k+1 {
					t.Fatalf("lifetime %d: take %d's element %d is %d; a later take overlapped it", life, k, i, v)
				}
			}
		}
		l.Reset()
	}
}

// A lane keeps what a lifetime took for the next, so that a lifetime of the same shape
// takes from one chunk without allocating, and trims a chunk past its bound back to its
// home chunk.
func TestLaneKeepsAndTrims(t *testing.T) {
	var home [4]string
	var l Lane[string]
	l.Init(home[:], false)
	l.Take(3)
	l.Take(10) // a second chunk
	l.Reset()
	if l.Cap() < 10 {
		t.Fatalf("after a lifetime of 13 the lane kept %d; want at least 10", l.Cap())
	}
	if n := testing.AllocsPerRun(100, func() { l.Take(3); l.Take(10); l.Reset() }); n != 0 {
		t.Errorf("a lifetime of the same shape allocates %v times; want 0", n)
	}
	huge := Keep/int(unsafe.Sizeof("")) + 1
	l.Take(huge)
	l.Reset()
	if !l.AtHome() {
		t.Errorf("after a lifetime of %d strings (past Keep) the lane kept %d; want its home chunk", huge, l.Cap())
	}
}

// A buffer built in place with Rest and Adopt is taken: a later take does not overlap
// it, whether append grew it in place or moved it.
func TestRestAdopt(t *testing.T) {
	var l Lane[byte]
	l.Take(1)[0] = 'x'
	b := append(l.Rest(), "hello, world, and more than eight bytes"...) // moves: past the chunk
	l.Adopt(b)
	c := l.Take(4)
	copy(c, "XXXX")
	if string(b) != "hello, world, and more than eight bytes" {
		t.Errorf("the adopted buffer reads %q after a later take", b)
	}
	l.Reset()
	l.Take(1)
	b = append(l.Rest(), "ab"...) // fits in place
	l.Adopt(b)
	copy(l.Take(2), "ZZ")
	if string(b) != "ab" {
		t.Errorf("the buffer adopted in place reads %q after a later take", b)
	}
}

// With noZero, Reset leaves what was taken as it was (its takers overwrite it); with
// ResetWith, the cleaning function sees every chunk the lifetime took from.
func TestNoZeroAndResetWith(t *testing.T) {
	var l Lane[int]
	l.Init(nil, true)
	l.Take(4)[0] = 7
	l.Reset()
	if l.Take(4)[0] != 7 {
		t.Error("a noZero lane zeroed what was taken")
	}
	l.Reset()
	var m Lane[int]
	m.Take(3)
	m.Take(20) // a second chunk
	seen := 0
	m.ResetWith(func(c []int) { seen += len(c) })
	if seen != 23 {
		t.Errorf("ResetWith saw %d elements; want the 23 taken", seen)
	}
}
