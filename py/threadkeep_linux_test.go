package py_test

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/py"
)

// TestPythonThreadsAvoidDoThreads: a Python thread never runs on an OS thread that ran
// Python through Do, the Do threads stay alive, and each Python thread's own OS thread
// exits once it is joined (py/thread.go runThread).
//   - Native code may keep the identity of the thread it was loaded or first used on:
//     mimalloc (in Rust extensions such as pyreqwest) takes that thread as its main thread,
//     by thread pointer, and a new thread on an exited thread's cached stack gets the same
//     pointer and mimalloc's main heap; two threads then used it and pyreqwest crashed
//     (SIGSEGV, addr=0x18). Goroutine-backed Python threads used to exit their OS thread
//     at the end even when Go had started them on a Do thread.
//   - A Python thread's own OS thread exits at its end, as a libc-created thread does
//     (the design, not a known requirement: see runThread).
func TestPythonThreadsAvoidDoThreads(t *testing.T) {
	before := py.ReadStats()
	for range 10 {
		// OS threads that run Python through Do, then go idle in Go's pool.
		var mu sync.Mutex
		doTIDs := map[int]bool{}
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				var tid int64
				if err := py.Do(func() error {
					v, err := py.Eval("__import__('threading').get_native_id()")
					if err != nil {
						return err
					}
					defer v.Close()
					tid, err = v.Int64()
					return err
				}); err != nil {
					t.Error(err)
				}
				mu.Lock()
				doTIDs[int(tid)] = true
				mu.Unlock()
			})
		}
		wg.Wait()
		// Python threads, which Go would start on idle OS threads, these among them.
		var ids []int
		do(t, func() error {
			v, err := py.Eval(`(lambda threading: (lambda ids, ts: ([t.start() for t in ts], [t.join() for t in ts], ids)[2])(
    ids := [], [threading.Thread(target=lambda: ids.append(threading.get_native_id())) for _ in range(64)]))(__import__('threading'))`)
			if err != nil {
				return err
			}
			defer v.Close()
			n, err := v.Len()
			if err != nil {
				return err
			}
			for i := range n {
				it, err := v.Item(i)
				if err != nil {
					return err
				}
				id, err := it.Int64()
				it.Close()
				if err != nil {
					return err
				}
				ids = append(ids, int(id))
			}
			return nil
		})
		for _, id := range ids {
			if doTIDs[id] {
				t.Fatalf("a Python thread ran on OS thread %d, which had run Do", id)
			}
		}
		waitFor(t, "the Python threads to finish", func() bool { return py.ReadStats().Threads == before.Threads })
		for id := range doTIDs {
			if _, err := os.Stat(fmt.Sprintf("/proc/self/task/%d", id)); err != nil {
				t.Fatalf("OS thread %d ran Do and exited", id)
			}
		}
		// The Python threads' own OS threads exit once joined: none is used again.
		deadline := time.Now().Add(5 * time.Second)
		for _, id := range ids {
			for {
				if _, err := os.Stat(fmt.Sprintf("/proc/self/task/%d", id)); err != nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("OS thread %d ran a Python thread, which was joined, and is still alive", id)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
}
