// Package testwatch bounds the parts of a test binary's TestMain that -test.timeout does
// not cover: the testing package's alarm runs only inside m.Run, so a py.Init before it or
// a server Close and py.Finalize after it that never return hang the binary forever (a
// darwin/amd64 3.14t asgi run once sat in py.Finalize for 25 hours).
//
// TestMain arms a watchdog around such work:
//
//	code := m.Run()
//	testwatch.Arm("Close and py.Finalize") // left armed through os.Exit, which is
//	...                                    // libc's exit on darwin and can hang too
//	os.Exit(code)
//
// and disarms it with the function Arm returns where the process goes on.
//
// When the bound passes first, the watchdog prints what it can about the process to
// stderr (the native threads' state, every goroutine) and exits with ExitCode.
//
// The bound is PYTHAGO_TEST_WATCHDOG (a time.Duration; 0 disables the watchdog) or,
// without it, a quarter of -test.timeout clamped to [MinBound, MaxBound] (MaxBound when
// -test.timeout is 0), which scales with the larger timeouts slow runs (qemu-user) use.
package testwatch

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Env names the environment variable that overrides the bound.
const Env = "PYTHAGO_TEST_WATCHDOG"

// ExitCode is the status the process exits with when the watchdog fires.
const ExitCode = 3

// The bounds derived from -test.timeout.
const (
	MinBound = 3 * time.Minute
	MaxBound = 10 * time.Minute
)

// Bound reports the watchdog's bound for an environment value env (as from
// os.Getenv(Env)) and a -test.timeout value; 0 means disabled.
func Bound(env string, testTimeout time.Duration) (time.Duration, error) {
	if env != "" {
		d, err := time.ParseDuration(env)
		if err != nil && strings.Trim(env, "0123456789") == "" {
			d, err = time.ParseDuration(env + "s") // a bare number of seconds
		}
		if err != nil || d < 0 {
			return 0, fmt.Errorf("testwatch: %s=%q is not a non-negative duration", Env, env)
		}
		return d, nil
	}
	if testTimeout <= 0 {
		return MaxBound, nil
	}
	return min(max(testTimeout/4, MinBound), MaxBound), nil
}

// testTimeout reads -test.timeout, parsing the command line first if m.Run has not
// (testing.MainStart registered the flags before TestMain runs).
func testTimeout() time.Duration {
	if !flag.Parsed() {
		flag.Parse()
	}
	f := flag.Lookup("test.timeout")
	if f == nil {
		return 0
	}
	if g, ok := f.Value.(flag.Getter); ok {
		if d, ok := g.Get().(time.Duration); ok {
			return d
		}
	}
	return 0
}

// Arm starts a watchdog for the work named what and returns the function that stops it.
// An invalid PYTHAGO_TEST_WATCHDOG fails the process at once rather than run unguarded.
func Arm(what string) (disarm func()) {
	d, err := Bound(os.Getenv(Env), testTimeout())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitCode)
	}
	if d == 0 {
		return func() {}
	}
	t := time.AfterFunc(d, func() { fire(what, d) })
	return func() { t.Stop() }
}

var firing sync.Mutex

// fire reports the hang and ends the process.
func fire(what string, d time.Duration) {
	firing.Lock() // two watchdogs at once: one report
	w := os.Stderr
	fmt.Fprintf(w, "\ntestwatch: %s did not return within %v (pid %d); %s bounds it\n", what, d, os.Getpid(), Env)
	nativeThreads(w)
	fmt.Fprintln(w, "testwatch: goroutines:")
	w.Write(goroutines())
	fmt.Fprintf(w, "FAIL\ttestwatch: %s hung\n", what)
	// os.Exit is libc's exit on darwin, which runs atexit handlers that may wait on the
	// threads that hung: kill the process if it does not exit promptly.
	time.AfterFunc(10*time.Second, func() {
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			p.Kill()
		}
	})
	os.Exit(ExitCode)
}

// goroutines returns every goroutine's stack.
func goroutines() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= 64<<20 {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// nativeThreads writes the state of the process's native threads, where that is cheap:
// /proc on Linux, sample(1) on macOS.
func nativeThreads(w io.Writer) {
	switch runtime.GOOS {
	case "linux":
		procThreads(w)
	case "darwin":
		sample(w)
	}
}

// procThreads writes each thread's name, scheduler state, wait channel and current
// system call (number and arguments) from /proc/self/task.
func procThreads(w io.Writer) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		fmt.Fprintln(w, "testwatch: native threads:", err)
		return
	}
	fmt.Fprintf(w, "testwatch: %d native threads (tid comm state wchan syscall):\n", len(tasks))
	read := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "?"
		}
		return strings.TrimSpace(string(b))
	}
	for _, t := range tasks {
		dir := filepath.Join("/proc/self/task", t.Name())
		state := "?"
		if stat := read(dir, "stat"); stat != "?" {
			if i := strings.LastIndexByte(stat, ')'); i >= 0 {
				if f := strings.Fields(stat[i+1:]); len(f) > 0 {
					state = f[0]
				}
			}
		}
		fmt.Fprintf(w, "  %s %q %s %s %s\n", t.Name(), read(dir, "comm"), state, read(dir, "wchan"), read(dir, "syscall"))
	}
}

// sample writes a few seconds of sample(1)'s call graph of this process.
func sample(w io.Writer) {
	path, err := exec.LookPath("sample")
	if err != nil {
		fmt.Fprintln(w, "testwatch: native threads: no sample(1):", err)
		return
	}
	f, err := os.CreateTemp("", "testwatch-*.sample.txt")
	if err != nil {
		fmt.Fprintln(w, "testwatch: native threads:", err)
		return
	}
	f.Close()
	defer os.Remove(f.Name())
	cmd := exec.Command(path, fmt.Sprint(os.Getpid()), "3", "-mayDie", "-file", f.Name())
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(w, "testwatch: native threads: sample:", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(2 * time.Minute):
		cmd.Process.Kill()
		err = fmt.Errorf("sample did not finish: %w", <-done)
	}
	if err != nil {
		fmt.Fprintln(w, "testwatch: native threads: sample:", err)
	}
	if b, rerr := os.ReadFile(f.Name()); rerr == nil && len(b) > 0 {
		fmt.Fprintln(w, "testwatch: native threads (sample):")
		w.Write(b)
	}
}
