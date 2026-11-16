package asgi_test

import "errors"

// mkfifo: Windows has no FIFOs in the file system.
func mkfifo(string) error { return errors.ErrUnsupported }
