package wsgi

import (
	"os"
	"syscall"
)

// On Windows file_wrapper passes the file's handle (msvcrt.get_osfhandle), not its C
// runtime descriptor.

// regularSize returns the size of the regular file h; ok is false for anything else
// (pipes, consoles, directories).
func regularSize(h int64) (size int64, ok bool) {
	if t, err := syscall.GetFileType(syscall.Handle(h)); err != nil || t != syscall.FILE_TYPE_DISK {
		return 0, false
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(h), &info); err != nil ||
		info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return 0, false
	}
	return int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow), true
}

// openDuplicate returns a duplicate of h as a file.
func openDuplicate(h int64) (*os.File, error) {
	p, _ := syscall.GetCurrentProcess()
	var dup syscall.Handle
	if err := syscall.DuplicateHandle(p, syscall.Handle(h), p, &dup, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(dup), "wsgi.file_wrapper"), nil
}
