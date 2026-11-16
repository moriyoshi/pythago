//go:build !windows

package workers

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenerFD is the inherited listener's descriptor in a worker (ExtraFiles[0]).
const listenerFD = 3

// inheritedListener takes the listener on descriptor 3, and has the worker get SIGTERM
// when its parent exits where the kernel does not do that itself (watchParent).
func inheritedListener() (net.Listener, error) {
	watchParent()
	f := os.NewFile(listenerFD, "workers-listener")
	defer f.Close() // FileListener holds its own descriptor
	return net.FileListener(f)
}

// listenerFile duplicates l's descriptor for the children. Not with l.File(): that
// *os.File is one Go put into non-blocking mode, so its Fd(), which os.StartProcess calls
// for every ExtraFiles entry, switches the descriptor to blocking mode. O_NONBLOCK belongs
// to the open file description, shared by the listener here and in every worker, so each
// start made the workers' listener blocking: a worker could then block inside accept(2),
// where Close cannot reach it, and ignore SIGTERM (1 TestSpawn run in about 40). An
// os.NewFile of a plain duplicate is left as it is by Fd.
func listenerFile(l net.Listener) (*os.File, error) {
	sc, ok := l.(syscall.Conn)
	if !ok {
		return nil, fmt.Errorf("workers: Spawn: %T has no file descriptor to pass on", l)
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil, err
	}
	dup := -1
	cerr := rc.Control(func(fd uintptr) {
		dup, err = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	})
	if cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(dup), "workers-listener"), nil
}

// stopper asks workers to stop with SIGTERM.
type stopper struct{}

func newStopper() *stopper { return &stopper{} }

// prepare passes the listener as descriptor 3 and sets the worker's attributes (its
// parent-death signal); started is to be called after cmd.Start.
func (*stopper) prepare(cmd *exec.Cmd, lf *os.File) (started func(*os.Process, error), err error) {
	cmd.ExtraFiles = []*os.File{lf} // descriptor 3 in the child
	cmd.SysProcAttr = workerAttr()
	return func(*os.Process, error) {}, nil
}

func (*stopper) stop(p *os.Process) { p.Signal(syscall.SIGTERM) }

func (*stopper) forget(*os.Process) {}
