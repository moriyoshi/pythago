//go:build pythago_spike

package main

import (
	"fmt"
	"time"

	"github.com/moriyoshi/pythago/py"
)

var comb *py.SpikeCombiner

func newCombiner(fn *py.Object) (*py.SpikeCombiner, error) {
	return py.NewSpikeCombiner(fn, *completers)
}

func combStats() string {
	st := comb.Stats()
	return fmt.Sprintf("batches %d jobs %d (%.1f/batch) sleeps %d asleep %v build %v fn %v handoff %v",
		st[0], st[1], float64(st[1])/float64(max(st[0], 1)), st[2], time.Duration(st[3]), time.Duration(st[4]),
		time.Duration(st[5]), time.Duration(st[6]))
}
