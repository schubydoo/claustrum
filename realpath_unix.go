//go:build unix

package main

import (
	"path/filepath"
	"syscall"
)

// isLinkLoopErrno reports the errno of a link loop or a chain over the kernel
// limit: 40 on Linux and 62 on macOS (build 683cc6a9).
func isLinkLoopErrno(en syscall.Errno) bool { return en == syscall.ELOOP }

// realpathPathRefusal returns the -32602 text for a path that is not absolute.
func realpathPathRefusal(path string) string {
	if !filepath.IsAbs(path) {
		return realpathNeedAbsolute
	}
	return ""
}
