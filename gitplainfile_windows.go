//go:build windows

package main

import "os"

// readGitPlainFile reads the file at path. Windows has no FIFO in the file system,
// so the read is the plain one, as before.
func readGitPlainFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// readGitPlainFileIn is readGitPlainFile for name inside root.
func readGitPlainFileIn(root *os.Root, name string) ([]byte, error) {
	return root.ReadFile(name)
}

// openGitPlainFile opens the file at path for a read: the plain open, as before.
func openGitPlainFile(path string) (*os.File, error) {
	return os.Open(path)
}
