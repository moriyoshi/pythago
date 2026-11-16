//go:build !windows

package wsgi

import (
	"os"
	"syscall"
)

// regularSize returns the size of the regular file fd; ok is false for anything else.
func regularSize(fd int64) (size int64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(fd), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return 0, false
	}
	return st.Size, true
}

// openDuplicate returns a duplicate of fd as a file.
func openDuplicate(fd int64) (*os.File, error) {
	dup, err := syscall.Dup(int(fd))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(dup)
	return os.NewFile(uintptr(dup), "wsgi.file_wrapper"), nil
}
