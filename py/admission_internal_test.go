package py

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// The admission tests take slots directly (slots <- struct{}{}), as a Do outside Python
// would, and give them back with putSlot, as Do and Release do: "fake holders" that keep
// Python free while every slot is taken.

// holdSlots takes k slots for the test goroutine, blocking like Do.
func holdSlots(k int) {
	for range k {
		slots <- struct{}{}
	}
}

// waitFor polls cond for up to d and reports whether it became true.
func waitFor(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

func next(t *testing.T, events <-chan string, what string) string {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("no Do was admitted for %s within 5 s", what)
		return ""
	}
}

// startReturns starts m goroutines that enter Do and wait in Release until proceed is
// closed; once back they report "return" on events. It returns when all m are inside
// Release (their slots given back).
func startReturns(t *testing.T, m int, proceed <-chan struct{}, events chan<- string, wg *sync.WaitGroup) {
	t.Helper()
	released := make(chan struct{}, m)
	for range m {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Do(func() error {
				Release(func() {
					released <- struct{}{}
					<-proceed
				})
				events <- "return"
				return nil
			})
			if err != nil {
				events <- "error: " + err.Error()
			}
		}()
	}
	for range m {
		select {
		case <-released:
		case <-time.After(5 * time.Second):
			t.Fatal("the Do calls did not reach Release within 5 s")
		}
	}
}

// startArrivals starts k goroutines entering Do, which report "arrival" once admitted.
func startArrivals(k int, events chan<- string, wg *sync.WaitGroup) {
	for range k {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Do(func() error { events <- "arrival"; return nil }); err != nil {
				events <- "error: " + err.Error()
			}
		}()
	}
}

// TestReleaseRegainsSlotBeforeArrivals: a Do coming back from Release, with every slot
// taken and goroutines already waiting in Do, gets the next free slot.
func TestReleaseRegainsSlotBeforeArrivals(t *testing.T) {
	n := cap(slots)
	const k = 3
	events := make(chan string, k+1)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()

	holdSlots(n - 1)
	held := n - 1
	defer func() {
		for range held {
			putSlot()
		}
	}()
	startReturns(t, 1, proceed, events, &wg) // took the last slot and gave it back
	holdSlots(1)                             // every slot is taken again
	held++
	startArrivals(k, events, &wg)
	time.Sleep(50 * time.Millisecond) // the arrivals block in Do's send first
	close(proceed)
	if !waitFor(2*time.Second, func() bool { return admission.returning.Load() == 1 }) {
		t.Log("the return did not queue in getSlotBack")
	}

	putSlot() // one slot comes free: only one goroutine can be admitted
	held--
	if e := next(t, events, "the freed slot"); e != "return" {
		t.Errorf("the freed slot went to %q, not to the Do returning from Release (queued behind %d new arrivals)", e, k)
	}
	for range held {
		putSlot()
	}
	held = 0
	for range k {
		next(t, events, "the remaining arrivals")
	}
	wg.Wait()
	if l, r := len(slots), admission.returning.Load(); l != 0 || r != 0 {
		t.Errorf("after the test %d slots are taken and %d returns wait", l, r)
	}
}

// TestArrivalsNotShutOutByReturns: while returns keep queuing, a goroutine waiting in Do
// still gets one of the first five slots that come free (Release's documentation).
func TestArrivalsNotShutOutByReturns(t *testing.T) {
	n := cap(slots)
	const m, bound = 10, 5 // returns waiting, more than bound
	events := make(chan string, m+1)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()

	startReturns(t, m, proceed, events, &wg)
	holdSlots(n)
	held := n
	defer func() {
		for range held {
			putSlot()
		}
	}()
	startArrivals(1, events, &wg)
	time.Sleep(50 * time.Millisecond) // the arrival blocks in Do's send
	close(proceed)
	if !waitFor(2*time.Second, func() bool { return admission.returning.Load() == m }) {
		t.Fatalf("%d of %d returns queued in getSlotBack", admission.returning.Load(), m)
	}

	putSlot() // one slot comes free; each admitted Do passes it on as it leaves
	held--
	var order []string
	for range m + 1 {
		order = append(order, next(t, events, fmt.Sprintf("slot %d", len(order)+1)))
	}
	if order[0] != "return" {
		t.Errorf("the first free slot went to %q, not to a return", order[0])
	}
	at := 0
	for i, e := range order {
		if e == "arrival" {
			at = i + 1
		}
	}
	if at == 0 || at > bound {
		t.Errorf("the arrival got slot %d of %d (%v); it must get one of the first %d", at, m+1, order, bound)
	}
	for range held {
		putSlot()
	}
	held = 0
	wg.Wait()
	if l, r := len(slots), admission.returning.Load(); l != 0 || r != 0 {
		t.Errorf("after the test %d slots are taken and %d returns wait", l, r)
	}
}

// TestReturnQueuedInPutSlotWindow: a return that finds every slot taken and queues just
// after putSlot found no returns, but before its receive, still gets the slot. Nothing
// else gives a slot back, so a lost wake-up leaves it queued with a slot free.
func TestReturnQueuedInPutSlotWindow(t *testing.T) {
	n := cap(slots)
	events := make(chan string, 1)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()

	holdSlots(n - 1)
	startReturns(t, 1, proceed, events, &wg)
	holdSlots(1)
	held := n
	defer func() {
		for range held {
			putSlot()
		}
	}()
	var once sync.Once
	letGo := func() { once.Do(func() { close(proceed) }) }
	putSlotWindow = func() {
		putSlotWindow = nil
		letGo()
		if !waitFor(2*time.Second, func() bool { return admission.returning.Load() == 1 }) {
			t.Error("the return did not queue in getSlotBack")
		}
	}
	defer func() { putSlotWindow = nil }()
	putSlot()
	held--
	select {
	case e := <-events:
		if e != "return" {
			t.Errorf("got %q", e)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("the return that queued in putSlot's window was not admitted (%d slots taken of %d, %d returns waiting)",
			len(slots), n, admission.returning.Load())
		// Unstick it for the other tests: hand it the free slot.
		letGo()
		admission.mu.Lock()
		admission.fill()
		admission.mu.Unlock()
		next(t, events, "the stuck return")
	}
	for range held {
		putSlot()
	}
	held = 0
	wg.Wait()
	if l, r := len(slots), admission.returning.Load(); l != 0 || r != 0 {
		t.Errorf("after the test %d slots are taken and %d returns wait", l, r)
	}
}

// TestAdmissionUnderLoad: many goroutines entering Do, Releasing (with Do nested inside
// Release and Release nested inside a Go function called from Python) all finish, and
// leave every slot free.
func TestAdmissionUnderLoad(t *testing.T) {
	const g, iters = 64, 500
	var fn *Object
	if err := Do(func() error {
		var err error
		fn, err = NewFunc("release", func(_ []*Object, _ Kwargs) (any, error) {
			Release(func() {})
			return nil, nil
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer Do(func() error { fn.Close(); return nil })
	var wg sync.WaitGroup
	errs := make(chan error, g)
	for i := range g {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range iters {
				err := Do(func() error {
					Release(func() {
						if (i+j)%3 == 0 {
							Do(func() error { return nil })
						}
					})
					if (i+j)%5 == 0 {
						r, err := fn.Call()
						if err != nil {
							return err
						}
						r.Close()
					}
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("Do and Release did not finish within 60 s (%d slots taken, %d returns waiting)",
			len(slots), admission.returning.Load())
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if l, r := len(slots), admission.returning.Load(); l != 0 || r != 0 {
		t.Errorf("after the test %d slots are taken and %d returns wait", l, r)
	}
}
