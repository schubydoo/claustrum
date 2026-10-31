//go:build unix

package main

import (
	"path/filepath"
	"strings"
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

// realpathClean drops the trailing separators, but keeps a lone "/". With one,
// the kernel follows a final link, so Lstat, Stat and the split must all see the
// same text. It cleans nothing else: the kernel applies a ".." after a link to
// the link target.
func realpathClean(p string) string {
	if t := strings.TrimRight(p, "/"); t != "" {
		return t
	}
	return p
}

// realpathJoin reads a relative link target from the folder of the link.
func realpathJoin(dir, target string) string { return dir + "/" + target }

// realpathEval resolves every link of p.
func realpathEval(p string) (string, error) { return filepath.EvalSymlinks(p) }
