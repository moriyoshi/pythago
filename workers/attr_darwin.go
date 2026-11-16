package workers

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// workerAttr has no parent-death signal to set on darwin; the worker watches its
// parent itself (watchParent).
func workerAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

// watchParent sends this worker SIGTERM when its parent, the supervisor, exits, as
// Linux's parent-death signal does. It waits for the parent's exit with a kqueue
// NOTE_EXIT filter; a parent already gone (reparented to launchd, or ESRCH) counts.
func watchParent() {
	ppid := os.Getppid()
	kq, err := unix.Kqueue()
	if err != nil {
		return
	}
	unix.CloseOnExec(kq)
	ev := []unix.Kevent_t{{Ident: uint64(ppid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	if _, err := unix.Kevent(kq, ev, nil, nil); err != nil || ppid == 1 {
		unix.Close(kq)
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
		return
	}
	go func() {
		defer unix.Close(kq)
		out := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(kq, nil, out, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil || n > 0 {
				break
			}
		}
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
}
