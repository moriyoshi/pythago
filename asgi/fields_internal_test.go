package asgi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/internal/arena"
)

// Response fields parsed for a request take their pairs from its lane, however many
// there are (there was a fixed array of 8): the only allocation left is the one copy of
// the packed fields the strings share. Without a lane the pairs are made.
func TestFieldsFromLane(t *testing.T) {
	var b strings.Builder
	const n = 20
	for i := range n {
		if i > 0 {
			b.WriteByte(0)
		}
		fmt.Fprintf(&b, "x-h%d\x00v%d", i, i)
	}
	packed := []byte(b.String())
	var lane arena.SharedLane[[2]string]
	pairs, err := parseFields(&lane, packed, n, "headers")
	if err != nil {
		t.Fatal(err)
	}
	for i, kv := range pairs {
		if kv != [2]string{fmt.Sprintf("x-h%d", i), fmt.Sprintf("v%d", i)} {
			t.Fatalf("pair %d is %q", i, kv)
		}
	}
	lane.Reset()
	withLane := testing.AllocsPerRun(200, func() {
		if _, err := parseFields(&lane, packed, n, "headers"); err != nil {
			t.Fatal(err)
		}
		lane.Reset()
	})
	without := testing.AllocsPerRun(200, func() {
		if _, err := parseFields(nil, packed, n, "headers"); err != nil {
			t.Fatal(err)
		}
	})
	if withLane != 1 || without != 2 {
		t.Errorf("parsing %d fields allocates %v times from a request's lane and %v without; want 1 and 2", n, withLane, without)
	}
}
