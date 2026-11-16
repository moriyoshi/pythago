package py_test

import (
	"runtime"
	"sync"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// TestThreadStateKeyNotNativeTLS: the glue's pthread key for the thread state Do caches on
// an OS thread exists before anything enters Python. Init enters Python on other OS
// threads before it marks the runtime started (the fusion's startup check, free-threaded
// with the fusion on). The key used to be created only then, so those threads stored
// their thread state under key 0, the native image's TLS key (bao): the thread's TLS
// record became a PyThreadState, and once bao's lookup cache missed, its TLS reads and
// writes went to the thread state's memory (garbage pointers, another thread's values:
// SIGSEGV in TestMixedThreads under qemu-x86_64). Such a thread goes back to Go's pool,
// so the test locks many goroutines at once, which takes the idle OS threads, and checks
// that none has its cached thread state under key 0.
func TestThreadStateKeyNotNativeTLS(t *testing.T) {
	if !py.FusionSelfCheckRan() {
		t.Skip("Init entered Python on no other OS thread (free-threaded with the fusion on: PYTHAGO_TEST_FUSION=1)")
	}
	const n = 128
	var ready, done sync.WaitGroup
	release := make(chan struct{})
	var mu sync.Mutex
	withTS, clobbered := 0, 0
	ready.Add(n)
	for range n {
		done.Go(func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ts, key0 := py.ThreadKeyProbe()
			mu.Lock()
			if ts != 0 {
				withTS++
				if key0 == ts {
					clobbered++
				}
			}
			mu.Unlock()
			ready.Done()
			<-release // hold this OS thread until every goroutine has one
		})
	}
	ready.Wait()
	close(release)
	done.Wait()
	if clobbered != 0 {
		t.Fatalf("%d of %d OS threads with a cached thread state hold it under pthread key 0, the native image's TLS key", clobbered, withTS)
	}
	if withTS == 0 {
		t.Fatal("no OS thread with a cached thread state was observed: the check saw nothing")
	}
	t.Logf("%d OS threads with a cached thread state; none under key 0", withTS)
}
