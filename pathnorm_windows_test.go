//go:build windows

package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// resolveTestRoot canonicalizes a t.TempDir() root to the spelling the daemon
// will report for paths under it. On the GitHub Windows runners TEMP uses an
// 8.3 short name (C:\Users\RUNNER~1\...), while git rev-parse --show-toplevel
// reports the expanded long path — so expand it here before building fixtures.
func resolveTestRoot(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	pp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 512)
	n, err := windows.GetLongPathName(pp, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}
