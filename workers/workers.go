// Package workers serves one listener from several processes of the same program, as
// gunicorn and uvicorn's --workers do. With the GIL, one process runs Python on one core
// at a time, so this is how a GIL build uses several cores; it works for any
// http.Handler (asgi.Server, wsgi.Server, or plain Go) and does not start Python itself.
//
// The processes must be spawned (fork, then exec), not forked: a forked child has only
// the forking thread, and the Go runtime, net/http and the Go event loop cannot run there.
//
// A supervisor (Spawn) listens once and starts n copies of its own executable, with the
// same arguments. Each inherits the listening socket (as file descriptor 3, exec.Cmd
// ExtraFiles; on Windows as a handle) and finds it with InheritedListener; all of them
// accept from the one kernel queue. A program serves either way with a main like:
//
//	cfg := py.Config{...}
//	if py.IsPythonCommand() { os.Exit(py.Main(cfg, os.Args)) }
//	l, err := workers.InheritedListener()
//	if l == nil && processes > 1 {
//		l, err = net.Listen("tcp", addr)
//		err = workers.Spawn(ctx, l, processes) // supervises; Python is never started here
//		return
//	}
//	if l == nil { l, err = net.Listen("tcp", addr) }
//	py.Init(cfg); srv, err := wsgi.New("app:app", opts)
//	hs := &http.Server{Handler: srv}
//	ctx, stop := workers.NotifyContext(context.Background())
//	defer stop()
//	go func() { <-ctx.Done(); hs.Shutdown(context.Background()); srv.Close(context.Background()) }()
//	hs.Serve(l)
//
// Workers must shut down when NotifyContext's context is done (http.Server.Shutdown,
// then the server's Close): on SIGTERM, which a worker also gets when the supervisor dies
// (on Linux from the parent-death signal, on darwin from a watch on the parent that
// InheritedListener starts). Windows has no SIGTERM to send: there a worker inherits the
// listening socket and the read end of a pipe that only the supervisor can write, and
// the pipe's closing (Spawn asks it to stop, or the supervisor exits) is the stop request.
package workers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// workerEnv marks a worker process; its value is the worker's index.
const workerEnv = "PYTHAGO_WORKER"

// InheritedListener returns the listener this process inherited as a worker started by
// Spawn, or nil (and no error) when it is not one. It takes the listener once: later
// calls return nil, and processes this one starts do not see themselves as workers.
//
// Call it before py.Init. Python copies the environment when it initializes, so after
// Init the marker would stay in os.environ, and a pythago program the application starts
// would take its own descriptor 3 for a listener.
func InheritedListener() (net.Listener, error) {
	if _, ok := os.LookupEnv(workerEnv); !ok {
		return nil, nil
	}
	os.Unsetenv(workerEnv)
	l, err := inheritedListener()
	if err != nil {
		return nil, fmt.Errorf("workers: inherited listener: %w", err)
	}
	return l, nil
}

// NotifyContext returns a context that is done when this process is asked to stop: on
// SIGINT or SIGTERM, which Spawn sends its workers, and in a worker on Windows, which has
// no SIGTERM to send, when Spawn asks it to stop or exits. A worker's main uses it to
// know when to shut down (http.Server.Shutdown, then the server's Close).
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-stopRequested:
			stop()
		case <-ctx.Done():
		}
	}()
	return ctx, stop
}

// stopRequested is closed when the supervisor asks this worker to stop by other means
// than a signal (Windows).
var stopRequested = make(chan struct{})

// Index returns this worker's index (0 to n-1) as Spawn numbered it, or -1 when this
// process is not a worker. Call it before InheritedListener.
func Index() int {
	v, ok := os.LookupEnv(workerEnv)
	if !ok {
		return -1
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return i
}

// Options tune Spawn. Unless the environment sets GOMAXPROCS, each worker runs with
// GOMAXPROCS set to its share of the CPUs this process may use (at least 1).
type Options struct {
	// Grace is how long workers get to exit after SIGTERM before they are killed
	// (default 30 s).
	Grace time.Duration
	// Command, if set, makes a worker's command instead of this executable with its
	// arguments (os.Args); Spawn adds the listener and the worker marker.
	Command func(index int) *exec.Cmd
	// MaxEarlyExits is how many times in a row a worker may exit within 5 s of starting
	// before Spawn gives up: it stops the other workers and returns an error, rather than
	// restart a worker that cannot start (a bad configuration, an import error) forever
	// (default 5).
	MaxEarlyExits int
}

// earlyExit is how soon after starting a worker's exit counts towards MaxEarlyExits.
const earlyExit = 5 * time.Second

// Spawn supervises n worker processes serving l, until ctx is done or the process gets
// SIGINT or SIGTERM; then it stops them (SIGTERM, then SIGKILL after Grace) and returns.
// A worker that exits on its own is restarted, with a delay that grows while workers
// keep dying soon after they start. Workers get SIGTERM if the supervisor dies.
func Spawn(ctx context.Context, l net.Listener, n int, opts ...Options) error {
	if n < 1 {
		return errors.New("workers: Spawn needs at least one worker")
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Grace <= 0 {
		o.Grace = 30 * time.Second
	}
	if o.MaxEarlyExits <= 0 {
		o.MaxEarlyExits = 5
	}
	lf, err := listenerFile(l) // a duplicate, for the children
	if err != nil {
		return err
	}
	defer lf.Close()
	stops := newStopper()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Workers are started from one locked thread: Pdeathsig fires when the thread that
	// started a child exits, and Go may otherwise retire threads.
	starts := make(chan startReq)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread lives as long as the process
		for r := range starts {
			r.done <- r.cmd.Start()
		}
	}()
	defer close(starts)

	var mu sync.Mutex
	procs := make([]*os.Process, n)
	exits := make(chan workerExit, n)
	start := func(i int) error {
		var cmd *exec.Cmd
		if o.Command != nil {
			cmd = o.Command(i)
		} else {
			cmd = exec.Command(exe, os.Args[1:]...)
		}
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, workerEnv+"="+strconv.Itoa(i))
		if !hasEnv(cmd.Env, "GOMAXPROCS") {
			// Each worker's Go runtime gets its share of the CPUs: n runtimes each sized
			// for all of them oversubscribe the cores (4 workers on 4 cores: 162k req/s
			// with GOMAXPROCS=4 each, 234k with 1).
			cmd.Env = append(cmd.Env, "GOMAXPROCS="+strconv.Itoa(max(1, runtime.NumCPU()/n)))
		}
		if cmd.Stdout == nil {
			cmd.Stdout = os.Stdout
		}
		if cmd.Stderr == nil {
			cmd.Stderr = os.Stderr
		}
		started, err := stops.prepare(cmd, lf) // the listener, and how to stop the worker
		if err != nil {
			return err
		}
		r := startReq{cmd: cmd, done: make(chan error)}
		starts <- r
		err = <-r.done
		started(cmd.Process, err)
		if err != nil {
			return err
		}
		mu.Lock()
		procs[i] = cmd.Process
		mu.Unlock()
		began := time.Now()
		go func() {
			err := cmd.Wait()
			stops.forget(cmd.Process)
			exits <- workerExit{i, err, time.Since(began)}
		}()
		return nil
	}
	for i := range n {
		if err := start(i); err != nil {
			killAll(&mu, procs)
			return fmt.Errorf("workers: starting worker %d: %w", i, err)
		}
	}

	running := n
	backoff := make([]time.Duration, n) // per worker: grows while it keeps dying early
	early := make([]int, n)             // per worker: exits in a row soon after starting
	restart := make(chan int, n)
	for {
		select {
		case <-ctx.Done():
			return shutdown(&mu, procs, stops, exits, running, o.Grace)
		case e := <-exits:
			running--
			mu.Lock()
			procs[e.index] = nil
			mu.Unlock()
			if ctx.Err() != nil {
				continue
			}
			if e.lived < earlyExit {
				early[e.index]++
				backoff[e.index] = min(max(2*backoff[e.index], 100*time.Millisecond), 30*time.Second)
			} else {
				early[e.index], backoff[e.index] = 0, 0
			}
			if early[e.index] >= o.MaxEarlyExits {
				err := fmt.Errorf("workers: worker %d exited %d times in a row within %v of starting (last: %v); giving up",
					e.index, early[e.index], earlyExit, e.err)
				log.Print(err)
				shutdown(&mu, procs, stops, exits, running, o.Grace)
				return err
			}
			log.Printf("workers: worker %d exited (%v) after %v; restarting", e.index, e.err, e.lived.Round(time.Millisecond))
			i, d := e.index, backoff[e.index]
			time.AfterFunc(d, func() { restart <- i })
		case i := <-restart:
			if ctx.Err() != nil {
				continue
			}
			if err := start(i); err != nil {
				log.Printf("workers: restarting worker %d: %v", i, err)
				backoff[i] = min(max(2*backoff[i], 100*time.Millisecond), 30*time.Second)
				d := backoff[i]
				time.AfterFunc(d, func() { restart <- i })
				continue
			}
			running++
		}
	}
}

func hasEnv(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

type startReq struct {
	cmd  *exec.Cmd
	done chan error
}

type workerExit struct {
	index int
	err   error
	lived time.Duration
}

// shutdown asks the running workers to stop (SIGTERM, or on Windows the stop pipe) and
// waits for them, killing those still running after grace.
func shutdown(mu *sync.Mutex, procs []*os.Process, stops *stopper, exits chan workerExit, running int, grace time.Duration) error {
	mu.Lock()
	for _, p := range procs {
		if p != nil {
			stops.stop(p)
		}
	}
	mu.Unlock()
	kill := time.NewTimer(grace)
	defer kill.Stop()
	for running > 0 {
		select {
		case e := <-exits:
			running--
			mu.Lock()
			procs[e.index] = nil
			mu.Unlock()
		case <-kill.C:
			mu.Lock()
			var left []string
			for i, p := range procs {
				if p != nil {
					left = append(left, fmt.Sprintf("%d (pid %d)", i, p.Pid))
				}
			}
			mu.Unlock()
			log.Printf("workers: %d workers still running after %v (%s); killing them", running, grace, strings.Join(left, ", "))
			killAll(mu, procs)
		}
	}
	return nil
}

func killAll(mu *sync.Mutex, procs []*os.Process) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range procs {
		if p != nil {
			p.Kill()
		}
	}
}
