//go:build !windows

package workers_test

import (
	"context"
	"net"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/workers"
	"golang.org/x/sys/unix"
)

// alive reports whether process pid is running.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestSpawnKeepsListenerNonblocking: starting a worker leaves the shared listening
// socket non-blocking. O_NONBLOCK belongs to the open file description, which the
// supervisor and every worker share; os/exec's Fd() on a net listener's File() made it
// blocking at each start, and a worker could then block in accept(2) past its SIGTERM.
// The worker here (sleep) never sets the flag back, as a real worker's FileListener did,
// which hid the problem except in races.
func TestSpawnKeepsListenerNonblocking(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- workers.Spawn(ctx, l, 1, workers.Options{Grace: 5 * time.Second,
			Command: func(int) *exec.Cmd { return exec.Command("/bin/sleep", "30") }})
	}()
	time.Sleep(500 * time.Millisecond) // the worker has started
	rc, err := l.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var nonblocking bool
	rc.Control(func(fd uintptr) {
		var flags int
		flags, err = unix.FcntlInt(fd, unix.F_GETFL, 0)
		nonblocking = flags&unix.O_NONBLOCK != 0
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !nonblocking {
		t.Error("the listener is in blocking mode after a worker started")
	}
}
