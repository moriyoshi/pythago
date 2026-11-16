//go:build !pythago_spike

package main

import (
	"errors"

	"github.com/moriyoshi/pythago/py"
)

// spikeCombiner stands in for py.SpikeCombiner without -tags pythago_spike.
type spikeCombiner struct{}

func (*spikeCombiner) Submit(...any) (int, []byte, []byte, string) { return 0, nil, nil, "" }

var comb *spikeCombiner

func newCombiner(*py.Object) (*spikeCombiner, error) {
	return nil, errors.New("-cres needs -tags pythago_spike")
}

func combStats() string { return "" }
