//go:build windows

package supervisor

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// resolveTestRoot gives a t.TempDir() root with its links resolved and its long
// name, as the daemon reports a path below it. On the GitHub Windows runners TEMP
// holds an 8.3 short name.
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

// The signal modes of the helper are Unix-only. Windows has no POSIX signals, so
// these do nothing.
func ignoreSigterm()     {}
func ignoreSighup()      {}
func exitZeroOnSigterm() {}
