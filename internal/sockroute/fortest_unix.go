//go:build !windows

package sockroute

import "os"

// SetChangeBackForTest sets the change back of a change of folder and returns the
// one before it, so a test of another package can make the change back fail.
func SetChangeBackForTest(f func(back *os.File) error) (old func(back *os.File) error) {
	old, changeBack = changeBack, f
	return old
}
