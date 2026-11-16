package aio_test

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// One benchmark operation is one completed message, not a round trip.
// Interpreter initialization, namespace compilation, and loop construction are
// outside the timer. ReportAllocs covers Go allocations, not the Python heap.
func benchmarkChannelPython(b *testing.B, attrs map[string]any, source string) {
	b.Helper()
	b.ReportAllocs()
	if err := py.Do(func() error {
		ns, err := py.From(attrs)
		if err != nil {
			return err
		}
		defer ns.Close()
		defer func() {
			// Functions reference their globals; clear the namespace to release
			// wrappers promptly instead of leaving cycles for a later benchmark.
			r, _ := ns.CallMethod("clear")
			r.Close()
		}()
		if err := py.ExecIn("import builtins\n__builtins__ = builtins.__dict__\nimport asyncio, goaio\nloop = goaio.GoEventLoop()\n"+source, ns); err != nil {
			return err
		}
		loop, err := ns.Item("loop")
		if err != nil {
			return err
		}
		defer loop.Close()
		defer func() {
			r, _ := loop.CallMethod("close")
			r.Close()
		}()
		fn, err := ns.Item("bench")
		if err != nil {
			return err
		}
		defer fn.Close()
		profilePath := os.Getenv("PYTHAGO_CHANNEL_PYPROFILE")
		if profilePath != "" {
			if err := ns.SetItem("_profile_path", profilePath); err != nil {
				return err
			}
			if err := py.ExecIn("import cProfile\n_profile = cProfile.Profile()\n_profile.enable()", ns); err != nil {
				return err
			}
		}
		b.ResetTimer()
		result, err := fn.Call(b.N)
		b.StopTimer()
		result.Close()
		if profilePath != "" {
			if profileErr := py.ExecIn("_profile.disable()\n_profile.dump_stats(_profile_path)", ns); profileErr != nil && err == nil {
				err = profileErr
			}
		}
		return err
	}); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msg/s")
}

const channelReceiveBenchmark = `
async def main(n):
    async def receive(index):
        last = expected
        for _ in range(index, n, workers):
            last = await channel.recv()
        if last != expected:
            raise AssertionError("incorrect channel payload")
    await asyncio.gather(*(receive(i) for i in range(workers)))

def bench(n):
    loop.run_until_complete(main(n))
`

const channelSendBenchmark = `
async def main(n):
    async def send(index):
        for _ in range(index, n, workers):
            await channel.send(expected)
    await asyncio.gather(*(send(i) for i in range(workers)))

def bench(n):
    loop.run_until_complete(main(n))
`

func benchmarkChannelBridge[T any](b *testing.B, payload T, capacity, workers int, send bool) {
	ch := make(chan T, capacity)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for range b.N {
			if send {
				select {
				case <-ch:
				case <-stop:
					return
				}
			} else {
				select {
				case ch <- payload:
				case <-stop:
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()
	source := channelReceiveBenchmark
	if send {
		source = channelSendBenchmark
	}
	benchmarkChannelPython(b, map[string]any{
		"channel": ch, "expected": payload, "workers": workers,
	}, source)
}

func BenchmarkChannelBridge(b *testing.B) {
	b.Logf("go=%s GOMAXPROCS=%d free_threaded=%t", runtime.Version(), runtime.GOMAXPROCS(0), py.FreeThreaded())
	for _, direction := range []string{"GoToPython", "PythonToGo"} {
		for _, capacity := range []int{0, 1, 64, 1024} {
			for _, workers := range []int{1, 32} {
				name := fmt.Sprintf("%s/Cap%d/Workers%d", direction, capacity, workers)
				b.Run(name+"/Int64", func(b *testing.B) {
					benchmarkChannelBridge(b, int64(1<<40+7), capacity, workers, direction == "PythonToGo")
				})
				for _, size := range []int{64, 4096, 65536, 1048576} {
					b.Run(fmt.Sprintf("%s/Bytes%d", name, size), func(b *testing.B) {
						b.SetBytes(int64(size))
						benchmarkChannelBridge(b, make([]byte, size), capacity, workers, direction == "PythonToGo")
					})
				}
			}
		}
	}
}

func benchmarkNativeChannel[T any](b *testing.B, payload T, capacity int, send bool) {
	ch := make(chan T, capacity)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range b.N {
			if send {
				<-ch
			} else {
				ch <- payload
			}
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if send {
			ch <- payload
		} else {
			<-ch
		}
	}
	b.StopTimer()
	<-done
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msg/s")
}

func BenchmarkNativeChannel(b *testing.B) {
	for _, direction := range []string{"Send", "Receive"} {
		for _, capacity := range []int{0, 1, 64, 1024} {
			b.Run(fmt.Sprintf("%s/Cap%d/Int64", direction, capacity), func(b *testing.B) {
				benchmarkNativeChannel(b, int64(1<<40+7), capacity, direction == "Send")
			})
			b.Run(fmt.Sprintf("%s/Cap%d/Bytes4096", direction, capacity), func(b *testing.B) {
				b.SetBytes(4096)
				benchmarkNativeChannel(b, make([]byte, 4096), capacity, direction == "Send")
			})
		}
	}
}

func BenchmarkAsyncioQueue(b *testing.B) {
	for _, capacity := range []int{1, 64, 1024} {
		b.Run(fmt.Sprintf("Cap%d", capacity), func(b *testing.B) {
			benchmarkChannelPython(b, map[string]any{"capacity": capacity}, `
async def main(n):
    queue = asyncio.Queue(capacity)
    async def producer():
        for _ in range(n):
            await queue.put(42)
    producer_task = asyncio.create_task(producer())
    last = 42
    for _ in range(n):
        last = await queue.get()
    await producer_task
    if last != 42:
        raise AssertionError("incorrect queue payload")

def bench(n):
    loop.run_until_complete(main(n))
`)
		})
	}
}

// Isolate the Python-to-Go callback and scalar conversion cost without awaiting.
func BenchmarkPythonGoCall(b *testing.B) {
	benchmarkChannelPython(b, map[string]any{
		"expected": int64(1<<40 + 7),
		"echo": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			n, err := a[0].Int64()
			return n, err
		}),
	}, `
def bench(n):
    value = expected
    for _ in range(n):
        value = echo(expected)
    if value != expected:
        raise AssertionError("incorrect callback result")
`)
}

// Ready-path control: alternating send/receive on a capacity-one channel. One
// benchmark operation remains one message, with no peer goroutine scheduling.
func BenchmarkChannelReady(b *testing.B) {
	benchmarkChannelPython(b, map[string]any{"channel": make(chan int64, 1)}, `
async def main(n):
    for _ in range(n // 2):
        await channel.send(1099511627783)
        await channel.recv()
    if n % 2:
        await channel.send(1099511627783)
def bench(n):
    loop.run_until_complete(main(n))
`)
}

func BenchmarkChannelBuffer(b *testing.B) {
	for _, direction := range []string{"GoToPython", "PythonToGo"} {
		for _, workers := range []int{1, 32} {
			for _, size := range []int{4096, 65536, 1048576} {
				b.Run(fmt.Sprintf("%s/Cap64/Workers%d/Bytes%d", direction, workers, size), func(b *testing.B) {
					payload := make([]byte, size)
					ch := make(chan *py.OwnedBuffer, 64)
					stop, done := make(chan struct{}), make(chan error, 1)
					send := direction == "PythonToGo"
					go func() {
						for range b.N {
							if send {
								select {
								case lease := <-ch:
									if err := lease.Close(); err != nil {
										done <- err
										return
									}
								case <-stop:
									done <- nil
									return
								}
							} else {
								lease := py.NewOwnedBuffer(payload, true)
								select {
								case ch <- lease:
								case <-stop:
									_ = lease.Close()
									done <- nil
									return
								}
							}
						}
						done <- nil
					}()
					defer func() {
						close(stop)
						if err := <-done; err != nil {
							b.Error(err)
						}
					}()
					source := channelReceiveBenchmark
					if send {
						source = channelSendBenchmark
					}
					b.SetBytes(int64(size))
					benchmarkChannelPython(b, map[string]any{"channel": ch, "expected": payload, "workers": workers}, source)
				})
			}
		}
	}
}

// Compare ordinary UTF-8 copying with retaining PyUnicode itself. Python-to-Go
// consumers release each reference without requesting a Go UTF-8 copy.
func BenchmarkChannelString(b *testing.B) {
	for _, size := range []int{64, 65536, 1048576} {
		payload := strings.Repeat("x", size)
		b.Run(fmt.Sprintf("Copy/Bytes%d", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			benchmarkChannelBridge(b, payload, 64, 32, true)
		})
		b.Run(fmt.Sprintf("UnicodeRef/Bytes%d", size), func(b *testing.B) {
			ch := make(chan *py.String, 64)
			stop, done := make(chan struct{}), make(chan error, 1)
			go func() {
				for range b.N {
					select {
					case value := <-ch:
						if err := value.Close(); err != nil {
							done <- err
							return
						}
					case <-stop:
						done <- nil
						return
					}
				}
				done <- nil
			}()
			defer func() {
				close(stop)
				if err := <-done; err != nil {
					b.Error(err)
				}
			}()
			b.SetBytes(int64(size))
			benchmarkChannelPython(b, map[string]any{"channel": ch, "expected": payload, "workers": 32}, channelSendBenchmark)
		})
	}
}
