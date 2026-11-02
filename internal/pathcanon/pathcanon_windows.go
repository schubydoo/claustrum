//go:build windows

package pathcanon

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Canonical resolves p to the spelling git reports: symlinks/junctions
// resolved AND 8.3 short names expanded (git rev-parse reports the long path,
// e.g. C:\Users\RUNNER~1\... -> the full name). EvalSymlinks alone does not
// expand a short name, so GetLongPathName follows it. Falls back to the best
// spelling available when a step fails.
func Canonical(p string) string {
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

// AsRecorded is the spelling of a worktree path that git.worktree_remove compares
// with the path git recorded in an entry. On Windows it is the cleaned path as given.
// An 8.3 short name is not expanded, so a leaf sent as LONGWO~1 does not match the
// recorded long name, and the entry stays. Measured against f6010b97 on Windows.
// Letter case still does not matter (measured).
func AsRecorded(p string) string {
	return filepath.Clean(p)
}

// FinalDir is the path of the existing directory p with every junction, symbolic
// link and 8.3 short name resolved, as the file system reports it. It falls back to
// Canonical when p cannot be opened. git.worktree_remove uses it for the
// baseRepo part of a gone worktree's path, so a baseRepo sent in 8.3 form or through
// a junction still finds a locked registration. Measured against f6010b97 on
// Windows.
func FinalDir(p string) string {
	pp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return Canonical(p)
	}
	h, err := windows.CreateFile(pp, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return Canonical(p)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, 1024)
	// Flags 0 are FILE_NAME_NORMALIZED and VOLUME_NAME_DOS: a drive-letter path.
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || n == 0 || int(n) > len(buf) {
		return Canonical(p)
	}
	s := windows.UTF16ToString(buf[:n])
	if rest, ok := strings.CutPrefix(s, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(s, `\\?\`)
}

// Same compares two Canonical results without regard to slash
// direction or letter case. Canonical keeps a path's own spelling when the path
// no longer exists. Git writes its records with forward slashes (`C:/…/.git`). Go
// joins with backslashes (`C:\…\.git`).
func Same(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
