//go:build !windows

package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A socket path longer than the socket address of the system allows (maxSocketPath
// bytes) still serves and still answers a dial. `be1c1ca5` serves at a 120-byte
// path on Linux and at a 110-byte path on macOS, and its -bridge and -stop reach
// it there (measured). The routes, in order:
//
//   - A path that fits is used as it is.
//   - A last element over the limit fails with socketTooLong.
//   - Linux: the name /proc/self/fd/<n>/<last element> of a handle of the socket
//     folder (folderHandleName). `be1c1ca5` binds and dials by that name, with no
//     change of folder (measured on Linux, strace).
//   - Else the process changes its working folder to the socket folder, uses the
//     last element, and changes back (inSocketFolder). `be1c1ca5` does that on
//     Linux with no usable /proc, and macOS has no /proc route (measured).
//
// The change of folder acts on the whole process. So it runs only in a short-lived
// process (the launcher, -bridge, -stop) and in the bind of a -serve start, before
// any request is served. A running daemon dials with dialSocketInDaemon, which
// never changes folder: a request resolves a relative path against the working
// folder (wipesHomeDir).

// folderChange serializes the routes that change the working folder of the process.
var folderChange sync.Mutex

// viaShortSocketName runs use on a name of path that fits the socket address, by
// the routes above. With mayChangeFolder false it never changes folder and fails
// with socketTooLong where only that route is left.
func viaShortSocketName[T io.Closer](op, path string, mayChangeFolder bool, use func(name string) (T, error)) (T, error) {
	var zero T
	if len(path) <= maxSocketPath {
		return use(path)
	}
	dir, base := filepath.Dir(path), filepath.Base(path)
	if len(base) > maxSocketPath {
		return zero, socketTooLong(op, path, maxSocketPath)
	}
	if v, ok, err := folderHandleName(dir, base, use); ok {
		return v, err
	}
	if !mayChangeFolder {
		return zero, socketTooLong(op, path, maxSocketPath)
	}
	return inSocketFolder(dir, base, use)
}

// socketTooLong builds the error of op ("listen" or "dial") on path, in the form
// "<op> unix <path>: the socket path is longer than ...".
func socketTooLong(op, path string, limit int) error {
	return &net.OpError{Op: op, Net: "unix", Addr: &net.UnixAddr{Name: path, Net: "unix"}, Err: &socketTooLongError{limit: limit}}
}

// changeBackError is the error of a change of folder that could not return to the
// old working folder. Its text is claustrum's own. The line of `be1c1ca5` after a
// failed change back is not measured.
type changeBackError struct{ err error }

func (e *changeBackError) Error() string {
	return "change back to the working folder: " + e.err.Error()
}

func (e *changeBackError) Unwrap() error { return e.err }

// changeBack returns to the folder of back. A var so a test can make it fail.
var changeBack = func(back *os.File) error { return back.Chdir() }

// inSocketFolder changes the working folder to dir, runs use on base and changes
// back through a handle of the old folder. If the change back fails, it closes the
// result and returns a *changeBackError.
func inSocketFolder[T io.Closer](dir, base string, use func(name string) (T, error)) (T, error) {
	var zero T
	folderChange.Lock()
	defer folderChange.Unlock()
	back, err := openWorkingFolder()
	if err != nil {
		return zero, err
	}
	defer back.Close()
	if err := os.Chdir(dir); err != nil {
		return zero, err
	}
	v, err := use(base)
	if cerr := changeBack(back); cerr != nil {
		if err == nil {
			_ = v.Close()
		}
		return zero, &changeBackError{err: cerr}
	}
	return v, err
}

// listenSocket binds the unix socket at path. A listener bound by another name
// never removes that name at its close: the name of a folder handle can lead to
// another folder once the handle is closed. The caller removes the real path
// (unlinkOwnSocket).
func listenSocket(path string) (net.Listener, error) {
	return viaShortSocketName("listen", path, true, func(name string) (net.Listener, error) {
		ln, err := net.Listen("unix", name)
		if err == nil && name != path {
			keepSocketFileAtClose(ln)
		}
		return ln, err
	})
}

// dialUnixPath dials the unix socket at path. It can change the working folder, so
// only a short-lived process calls it.
func dialUnixPath(path string, timeout time.Duration) (net.Conn, error) {
	return viaShortSocketName("dial", path, true, dialName(timeout))
}

// dialSocketInDaemon dials the unix socket at path and never changes the working
// folder. Where only that route is left, it fails with socketTooLong.
func dialSocketInDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return viaShortSocketName("dial", path, false, dialName(timeout))
}

func dialName(timeout time.Duration) func(name string) (net.Conn, error) {
	return func(name string) (net.Conn, error) { return net.DialTimeout("unix", name, timeout) }
}
