//go:build unix

package main

import (
	"os"
	"syscall"
)

// removeRefusedTokenFile removes the -token-file of a serve run whose place was
// refused, unless it is a folder. 13047a51 removes a regular file, a link (the
// target stays) and a FIFO, and keeps an empty folder (measured on Linux). It
// removes a regular file on macOS (measured). A
// missing path or a failed Lstat removes nothing. One unlink by the path that
// the flag names: unlink never removes a folder, also one that took the name
// after the Lstat, and it removes a link as a link. Its error is ignored
// (claustrum's own, not measured).
func removeRefusedTokenFile(path string) {
	fi, err := os.Lstat(path)
	if err != nil || fi.IsDir() {
		return
	}
	_ = syscall.Unlink(path)
}
