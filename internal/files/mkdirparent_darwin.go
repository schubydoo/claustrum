package files

import (
	"bytes"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openParentPath asks fcntl F_GETPATH for the path of the handle. A failed call
// gives parent_unverified. The call and the failure are claustrum's own, not
// measured: the macOS rows show only that a link at or above the parent gives
// parent_elsewhere.
func openParentPath(fd int) (string, error) {
	buf := make([]byte, unix.PathMax)
	for {
		_, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return "", e
		}
		break
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf), nil
}
