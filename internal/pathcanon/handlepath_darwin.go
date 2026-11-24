package pathcanon

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// PathOfOpenFolder is the path of the open folder f, from fcntl F_GETPATH. A rename of
// the folder or of a parent shows in the answer.
func PathOfOpenFolder(f *os.File) (string, error) {
	buf := make([]byte, unix.PathMax)
	for {
		_, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
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
