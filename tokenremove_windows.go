package main

import (
	"os"
	"syscall"
)

// removeRefusedTokenFile removes the -token-file of a serve run whose place was
// refused, unless it is a folder. 13047a51 removes a regular file (measured on
// Windows). The other kinds follow the Linux rows (not measured on Windows). A
// missing path or a failed Lstat removes nothing. One DeleteFile by the path
// that the flag names: it never removes a folder, also one that took the name
// after the Lstat, and it removes a file link as a link. Its error is ignored
// (claustrum's own, not measured).
func removeRefusedTokenFile(path string) {
	fi, err := os.Lstat(path)
	if err != nil || fi.IsDir() {
		return
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_ = syscall.DeleteFile(p)
}
