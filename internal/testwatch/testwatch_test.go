package testwatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/internal/testwatch"
)

// mode makes this binary a child that arms a watchdog in TestMain, as the packages'
// TestMains do after m.Run, and then: "hang" blocks forever in a read nobody answers;
// "disarm" stops the watchdog, outlives the bound and exits 0; "exit" exits 0 at once
// with the watchdog armed.
const mode = "TESTWATCH_TEST_MODE"

func TestMain(m *testing.M) {
	switch os.Getenv(mode) {
	case "":
		os.Exit(m.Run())
	case "hang":
		testwatch.Arm("hang-sim")
		r, _, err := os.Pipe()
		if err == nil {
			_, err = r.Read(make([]byte, 1))
		}
		fmt.Println("read returned:", err)
		os.Exit(0)
	case "disarm":
		disarm := testwatch.Arm("disarm-sim")
		disarm()
		time.Sleep(3 * time.Second)
		os.Exit(0)
	case "exit":
		testwatch.Arm("exit-sim")
		os.Exit(0)
	}
	os.Exit(2)
}

// child runs this binary in the given mode with the watchdog's bound env, killing it
// after limit; it returns the combined output, the exit status (-1: killed) and the
// time it ran.
func child(t *testing.T, m, env string, limit time.Duration) (string, int, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.timeout=1h")
	cmd.Env = append(os.Environ(), mode+"="+m, testwatch.Env+"="+env)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0, took
	case ctx.Err() != nil:
		return out.String(), -1, took
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode(), took
	}
	t.Fatal(err)
	return "", 0, 0
}

// TestFires: a TestMain that hangs after arming the watchdog fails within the bound,
// with ExitCode, naming what hung, the native threads (Linux) and every goroutine,
// including the one blocked in the read.
func TestFires(t *testing.T) {
	out, code, took := child(t, "hang", "2s", 3*time.Minute) // sample(1) takes a while on macOS
	if code != testwatch.ExitCode {
		t.Fatalf("exit status %d after %v, want %d; output:\n%s", code, took, testwatch.ExitCode, out)
	}
	if took < 2*time.Second {
		t.Errorf("fired after %v, before the 2s bound", took)
	}
	for _, want := range []string{
		"testwatch: hang-sim did not return within 2s",
		"testwatch: goroutines:",
		"goroutine 1 [",
		"testwatch_test.TestMain",
		"FAIL\ttestwatch: hang-sim hung",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if runtime.GOOS == "linux" && !strings.Contains(out, "native threads (tid comm state wchan syscall)") {
		t.Errorf("output lacks the native threads:\n%s", out)
	}
	if strings.Contains(out, "read returned") {
		t.Errorf("the hung read returned:\n%s", out)
	}
}

// TestDisabled is the control for TestFires: with the watchdog off the same child hangs
// until something outside kills it.
func TestDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the hang")
	}
	out, code, took := child(t, "hang", "0", 8*time.Second)
	if code != -1 {
		t.Fatalf("exit status %d after %v, want a hang until killed; output:\n%s", code, took, out)
	}
	if strings.Contains(out, "testwatch:") {
		t.Errorf("disabled watchdog reported:\n%s", out)
	}
}

// TestDisarm: a stopped watchdog never fires, and an armed one does not get in the way
// of an exit.
func TestDisarm(t *testing.T) {
	for _, m := range []string{"disarm", "exit"} {
		out, code, took := child(t, m, "1s", time.Minute)
		if code != 0 || strings.Contains(out, "testwatch:") {
			t.Errorf("%s: exit status %d after %v, want 0 and no report; output:\n%s", m, code, took, out)
		}
	}
}

func TestBound(t *testing.T) {
	for _, c := range []struct {
		env     string
		timeout time.Duration
		want    time.Duration
		err     bool
	}{
		{"", 0, testwatch.MaxBound, false},
		{"", 600 * time.Second, testwatch.MinBound, false}, // 150s, raised to the minimum
		{"", 20 * time.Minute, 5 * time.Minute, false},
		{"", 2400 * time.Second, testwatch.MaxBound, false},
		{"", 10 * time.Hour, testwatch.MaxBound, false},
		{"90s", 600 * time.Second, 90 * time.Second, false},
		{"45", 0, 45 * time.Second, false},
		{"0", 600 * time.Second, 0, false},
		{"-1s", 0, 0, true},
		{"soon", 0, 0, true},
	} {
		got, err := testwatch.Bound(c.env, c.timeout)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("Bound(%q, %v) = %v, %v; want %v, error %v", c.env, c.timeout, got, err, c.want, c.err)
		}
	}
}
