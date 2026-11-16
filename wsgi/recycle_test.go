package wsgi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/py"
	"github.com/moriyoshi/pythago/wsgi"
)

// lateResults removes and returns testapp.late_results.
func lateResults(t *testing.T) []string {
	t.Helper()
	var out []string
	if err := py.Do(func() error {
		o, err := py.Eval("__import__('testapp').take_late()")
		if err != nil {
			return err
		}
		defer o.Close()
		v, err := o.Go()
		if err != nil {
			return err
		}
		for _, s := range v.([]any) {
			out = append(out, s.(string))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// A _pythago_wsgi call that found its request before the response ended keeps that
// request: a thread's late write() holds request A while A's handler returns and
// another request runs, and must then see A (over: the client is gone), never the
// request that would take A's memory if it were recycled under the call. GOMAXPROCS is
// 1 meanwhile, so that B's handler takes from the pool what A's would have put back.
// The call's handle is compared with the lifetime its request serves when it goes on:
// a recycled request can still produce correct responses.
func TestRecycleHeldRequest(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	lateResults(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	held, release := wsgi.HoldCall("body")
	defer release()
	// A's application waits (late_gate) until its late write() is held, then returns.
	answerA := make(chan string, 1)
	go func() {
		resp, err := http.Get(ts.URL + "/late?id=A&wait=1")
		if err != nil {
			answerA <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		answerA <- string(b)
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the late write() never reached Go")
	}
	get(t, ts.URL+"/open-late")
	if a := <-answerA; a != "A" {
		t.Fatalf("request A answered %q; want %q", a, "A")
	}
	_, bodyB := get(t, ts.URL+"/hello")
	if string(bodyB) != "Hello, world!" {
		t.Errorf("request B answered %q", bodyB)
	}
	presented, serving, ok := release()
	if !ok {
		t.Fatal("no call stopped")
	}
	if presented != serving {
		t.Errorf("the request a _pythago_wsgi call held (handle %d) was recycled under it: it went on with lifetime %d", presented, serving)
	}
	deadline := time.Now().Add(10 * time.Second)
	var got []string
	for len(got) == 0 && time.Now().Before(deadline) {
		got = lateResults(t)
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 || got[0] != "gone A" {
		t.Errorf("the late write() of request A: %q; want [\"gone A\"]", got)
	}
}

// Many requests, each with a thread that writes after (or, sometimes, before) its
// response ends: every response holds its own id, and at most its own late write,
// never another request's.
func TestRecycleNoCrossTalk(t *testing.T) {
	lateResults(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	const clients, rounds = 16, 40
	var wg sync.WaitGroup
	errs := make(chan string, clients)
	for c := range clients {
		wg.Go(func() {
			for r := range rounds {
				id := fmt.Sprintf("c%dr%d", c, r)
				sleep := []string{"0", "0.0005", "0.002"}[(c+r)%3]
				req, _ := http.NewRequestWithContext(context.Background(), "GET", ts.URL+"/late?id="+id+"&sleep="+sleep, nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					errs <- err.Error()
					return
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if body := string(b); body != id && body != id+"LATE"+id {
					errs <- fmt.Sprintf("request %s answered %q", id, body)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// Every late write found its own request (or none): no "sent" for another id is
	// possible to see from here, but each id reports once.
	deadline := time.Now().Add(10 * time.Second)
	seen := map[string]int{}
	for n := 0; n < clients*rounds && time.Now().Before(deadline); {
		for _, s := range lateResults(t) {
			seen[s[strings.IndexByte(s, ' ')+1:]]++
			n++
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(seen) != clients*rounds {
		t.Errorf("%d of %d late writes reported", len(seen), clients*rounds)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("request %s's late write reported %d times", id, n)
		}
	}
}
