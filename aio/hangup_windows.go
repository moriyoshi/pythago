package aio

import (
	"errors"
	"io"
	"syscall"
	"time"
)

// waitHangup waits for the reading end of a write pipe to close. A Windows pipe tells a
// writer that only through I/O:
//   - a subprocess's stdin is a duplex overlapped pipe (windows_utils.Popen), so a read on
//     the write end, served by Go's I/O completion port, ends with end of file once the
//     child closes its end, as asyncio's proactor learns it;
//   - a pipe that refuses reads (an os.pipe write end) is probed with a null write every
//     hangupProbe: it succeeds while a reader is there and fails with ERROR_NO_DATA (or
//     ERROR_BROKEN_PIPE) once it is gone. Control keeps the handle open for the probe, so
//     a closed and reused handle is never written; once the transport closed it, Control
//     fails and the wait ends.
func waitHangup(ac *aioConn, rc syscall.RawConn) bool {
	p, ok := ac.c.(pipeConn)
	if !ok {
		return false
	}
	var buf [512]byte
	for {
		_, err := p.Read(buf[:]) // data a duplex peer wrote back is not ours to read: dropped
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) || errors.Is(err, syscall.ERROR_BROKEN_PIPE) {
			return true
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, errorInvalidFunction) {
			return false // closed by the transport, or failed
		}
		break // not readable: probe instead
	}
	const errorNoData = syscall.Errno(232)
	for !ac.aborted.Load() {
		var werr error
		if err := rc.Control(func(h uintptr) {
			var n uint32
			werr = syscall.WriteFile(syscall.Handle(h), nil, &n, nil)
		}); err != nil {
			return false
		}
		if errors.Is(werr, errorNoData) || errors.Is(werr, syscall.ERROR_BROKEN_PIPE) {
			return true
		}
		if werr != nil {
			return false
		}
		time.Sleep(hangupProbe)
	}
	return false
}

const (
	hangupProbe          = 50 * time.Millisecond
	errorInvalidFunction = syscall.Errno(1)
)
