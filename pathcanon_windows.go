//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// canonicalPath resolves p to the spelling git reports: symlinks/junctions
// resolved AND 8.3 short names expanded (git rev-parse reports the long path,
// e.g. C:\Users\RUNNER~1\... -> the full name). EvalSymlinks alone does not
// expand a short name, so GetLongPathName follows it. Falls back to the best
// spelling available when a step fails.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
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
