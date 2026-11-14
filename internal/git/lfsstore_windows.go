//go:build windows

package git

import (
	"errors"
	"os"
)

// storeInCheckout is false on Windows. 4c111e86 makes no `.checkout-<n>`
// folder there, and the read-tree call keeps the store of the git directory
// (measured on Windows).
const storeInCheckout = false

// linkIntoRoot is not used on Windows.
func linkIntoRoot(*os.Root, string, string) error {
	return errors.ErrUnsupported
}

// openObjectNoFollow is not used on Windows.
func openObjectNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

// removeFileAt is not used on Windows.
func removeFileAt(*os.Root, string) error {
	return errors.ErrUnsupported
}

// freeBytes is not used on Windows.
func freeBytes(string) (uint64, bool) {
	return 0, false
}
