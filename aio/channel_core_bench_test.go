package aio

import (
	"fmt"
	"testing"
)

// Core isolates worker scheduling, typed channel operations, completion batching,
// and handle bookkeeping from the Python callbacks, conversions, and asyncio Tasks.
func BenchmarkChannelCore(b *testing.B) {
	for _, send := range []bool{false, true} {
		direction := "GoToPython"
		if send {
			direction = "PythonToGo"
		}
		for _, capacity := range []int{0, 64} {
			b.Run(fmt.Sprintf("%s/Cap%d", direction, capacity), func(b *testing.B) {
				native := make(chan int64, capacity)
				adapter, _ := builtinChannel(native)
				loop := &aioLoop{batch: newBatch[completion]()}
				channelID, loopID := register(adapter), register(loop)
				defer unregister(channelID)
				defer unregister(loopID)
				stop, peerDone := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(peerDone)
					for range b.N {
						if send {
							select {
							case <-native:
							case <-stop:
								return
							}
						} else {
							select {
							case native <- int64(1<<40 + 7):
							case <-stop:
								return
							}
						}
					}
				}()
				defer func() {
					close(stop)
					<-peerDone
				}()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					ch := lookup[channelAdapter](channelID)
					l := lookup[*aioLoop](loopID)
					_, work, err := ch.prepare(send, int64(1<<40+7))
					if err != nil {
						b.Fatal(err)
					}
					if work == nil {
						continue
					}
					op := &channelOp{
						stop: make(chan struct{}), done: make(chan struct{}), loop: l, token: 1,
					}
					op.id = register(op)
					go op.run(work)
					<-l.wake
					if len(l.take()) != 1 {
						b.Fatal("expected one completion")
					}
					<-op.done
				}
				b.StopTimer()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msg/s")
			})
		}
	}
}
