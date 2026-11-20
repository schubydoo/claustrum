//go:build windows

package git

import (
	"errors"
	"os"
)

// storeInCheckout is false on Windows. The `.checkout-<n>` folder of 4c111e86
// holds no LFS object there, and the read-tree call keeps the store of the git
// directory (measured on Windows).
const storeInCheckout = false

// linkIntoRoot is not used on Windows.
func linkIntoRoot(*os.Root, string, string) error {
	return errors.ErrUnsupported
}

// hardLinks is 1 on Windows, where the seed does not run.
func hardLinks(os.FileInfo) uint64 {
	return 1
}

// createSeedFile is not used on Windows.
func createSeedFile(*os.Root, string, os.FileMode) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

// linkAcrossRoots is not used on Windows.
func linkAcrossRoots(*os.Root, string, *os.Root, string) error {
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
