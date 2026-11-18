package git

import "os"

// openRootNoWait is os.OpenRoot on Windows. A folder there holds no FIFO, so
// the open does not wait (from the code).
func openRootNoWait(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}

// openInRootNoWait is parent.OpenRoot(name) on Windows.
func openInRootNoWait(parent *os.Root, name string) (*os.Root, error) {
	return parent.OpenRoot(name)
}
