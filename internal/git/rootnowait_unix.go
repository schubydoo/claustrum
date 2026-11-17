//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// openRootNoWait is os.OpenRoot for a folder of the daemon. os.OpenRoot opens
// with no O_NONBLOCK, so a FIFO at path waits for a writer. A first open with
// O_NONBLOCK finds a FIFO at once. It then gets the error that os.OpenRoot
// gives for a plain file. Any other result goes on to os.OpenRoot, so its
// errors stay as they are. A FIFO put in place between the two opens still
// waits (claustrum's own, not measured).
func openRootNoWait(path string) (*os.Root, error) {
	if f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		if isFIFO(f) {
			return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("not a directory")}
		}
	}
	return os.OpenRoot(path)
}

// openInRootNoWait is parent.OpenRoot(name) with the FIFO test of
// openRootNoWait (claustrum's own, not measured).
func openInRootNoWait(parent *os.Root, name string) (*os.Root, error) {
	if f, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		if isFIFO(f) {
			return nil, &os.PathError{Op: "open", Path: parent.Name() + "/" + name, Err: errors.New("not a directory")}
		}
	}
	return parent.OpenRoot(name)
}

// isFIFO reports whether the open file f is a FIFO, and closes f.
func isFIFO(f *os.File) bool {
	fi, err := f.Stat()
	_ = f.Close()
	return err == nil && fi.Mode().Type() == fs.ModeNamedPipe
}
