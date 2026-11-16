package workers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/workers"
)

// exitAtOnce makes this binary exit with the given status as soon as it starts.
const exitAtOnce = "WORKERS_TEST_EXIT_AT_ONCE"

// info is what a worker reports about itself.
type info struct {
	Pid    int
	Index  int
	Marker string // the worker marker as the worker's own children would see it
}

// TestMain serves as a worker when TestSpawn started this binary as one: a plain Go
// handler on the inherited listener until asked to stop (NotifyContext: SIGTERM, or the
// stop pipe on Windows), as a program's main would.
func TestMain(m *testing.M) {
	if code := os.Getenv(exitAtOnce); code != "" { // a worker that cannot start
		n, _ := strconv.Atoi(code)
		os.Exit(n)
	}
	index := workers.Index()
	l, err := workers.InheritedListener()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if l == nil {
		os.Exit(m.Run())
	}
	me := info{Pid: os.Getpid(), Index: index, Marker: os.Getenv("PYTHAGO_WORKER")}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(me)
	})}
	ctx, stop := workers.NotifyContext(context.Background())
	defer stop()
	go func() {
		<-ctx.Done()
		hs.Shutdown(context.Background())
	}()
	if err := hs.Serve(l); err != nil && err != http.ErrServerClosed {
		fmt.Println("worker Serve:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestSpawn runs two worker processes on one listener: both serve, a killed worker is
// replaced while the other keeps serving, and cancelling stops every worker.
func TestSpawn(t *testing.T) {
	if workers.Index() != -1 {
		t.Fatal("the test process looks like a worker")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// A long grace: shutting down well within it proves the workers got SIGTERM, not
	// the SIGKILL that follows it.
	const grace = 60 * time.Second
	go func() { done <- workers.Spawn(ctx, l, 2, workers.Options{Grace: grace}) }()
	defer func() {
		cancel()
		<-done
	}()

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
	ask := func() (info, error) {
		var w info
		resp, err := client.Get("http://" + l.Addr().String() + "/")
		if err != nil {
			return w, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return w, err
		}
		return w, json.Unmarshal(b, &w)
	}
	// serving collects the workers that answer within d; every request must succeed.
	serving := func(d time.Duration, want func(map[int]info) bool) map[int]info {
		seen := map[int]info{}
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) && !want(seen) {
			var mu sync.Mutex
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					w, err := ask()
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						t.Errorf("request: %v", err)
						return
					}
					seen[w.Pid] = w
				})
			}
			wg.Wait()
			if t.Failed() {
				t.FailNow()
			}
		}
		return seen
	}
	first := serving(30*time.Second, func(s map[int]info) bool { return len(s) == 2 })
	if len(first) != 2 {
		t.Fatalf("workers serving: %v, want 2", first)
	}
	indexes := map[int]bool{}
	for _, w := range first {
		indexes[w.Index] = true
		if w.Marker != "" {
			t.Errorf("worker %d still has the marker %q after taking the listener", w.Pid, w.Marker)
		}
	}
	if !indexes[0] || !indexes[1] {
		t.Errorf("worker indexes %v, want 0 and 1", indexes)
	}
	var victim info
	for _, w := range first {
		victim = w
		break
	}
	if p, err := os.FindProcess(victim.Pid); err != nil {
		t.Fatal(err)
	} else if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	// Kill returns before the worker is gone (TerminateProcess is asynchronous): until
	// then its pending accept (an AcceptEx on Windows) can still take a connection from
	// the shared queue, and its exit resets it ("An existing connection was forcibly
	// closed by the remote host", windows/arm64 CI). Requests must all succeed only once
	// it is gone; on Unix that is once Spawn has reaped it.
	for deadline := time.Now().Add(10 * time.Second); alive(victim.Pid); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("killed worker %d still running after 10 s", victim.Pid)
		}
	}
	// The other worker keeps serving, and a replacement with the victim's index joins.
	replaced := serving(30*time.Second, func(s map[int]info) bool {
		for p := range s {
			if _, ok := first[p]; !ok {
				return true
			}
		}
		return false
	})
	var fresh info
	for p, w := range replaced {
		if p == victim.Pid {
			t.Errorf("killed worker %d still served", victim.Pid)
		}
		if _, ok := first[p]; !ok {
			fresh = w
		}
	}
	if fresh.Pid == 0 {
		t.Fatalf("no replacement worker served within 30 s: %v", replaced)
	}
	if fresh.Index != victim.Index {
		t.Errorf("replacement has index %d, the killed worker had %d", fresh.Index, victim.Index)
	}

	cancel()
	stopping := time.Now()
	select {
	case err := <-done:
		done <- err // for the deferred receive
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(grace + 30*time.Second):
		t.Fatal("Spawn did not return after cancel")
	}
	if d := time.Since(stopping); d > grace/4 {
		t.Errorf("Spawn took %v to stop its workers: they did not stop when asked (SIGTERM, or the stop pipe)", d.Round(time.Millisecond))
	}
	for p := range replaced {
		if alive(p) {
			t.Errorf("worker %d still running after Spawn returned", p)
		}
	}
}

// TestSpawnGivesUp checks that workers that cannot start (here, a command that exits at
// once) make Spawn stop and report it instead of restarting them forever.
func TestSpawnGivesUp(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		done <- workers.Spawn(context.Background(), l, 2, workers.Options{
			MaxEarlyExits: 3,
			Command: func(int) *exec.Cmd {
				exe, _ := os.Executable()
				cmd := exec.Command(exe)
				cmd.Env = append(os.Environ(), exitAtOnce+"=3")
				return cmd
			},
		})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "giving up") {
			t.Fatalf("Spawn returned %v, want a giving-up error", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Spawn kept restarting workers that cannot start")
	}
}

func TestSpawnErrors(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := workers.Spawn(context.Background(), l, 0); err == nil {
		t.Error("Spawn with 0 workers succeeded")
	}
	var noFile struct{ net.Listener }
	noFile.Listener = l
	if err := workers.Spawn(context.Background(), noFile, 1); err == nil || !strings.Contains(err.Error(), "no file descriptor") {
		t.Errorf("Spawn with a listener without File(): %v", err)
	}
}
