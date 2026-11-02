//go:build unix

package pathcanon

import "path/filepath"

// Canonical resolves p to the spelling git reports — symlinks resolved, e.g.
// macOS /tmp -> /private/tmp. A no-op for a symlink-free Linux path (so the frame
// battery stays byte-identical). Falls back to p when it cannot be resolved (e.g.
// the path does not exist).
func Canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// AsRecorded is the spelling of a worktree path that git.worktree_remove compares
// with the path git recorded in an entry: the symbolic links of its existing part
// resolved, so a remove named through macOS /tmp matches the recorded /private/tmp.
func AsRecorded(p string) string {
	return OfGone(p)
}

// FinalDir is Canonical. On Windows it also resolves junctions and 8.3 short
// names (see pathcanon_windows.go).
func FinalDir(p string) string {
	return Canonical(p)
}

// Same compares two Canonical results as plain strings.
func Same(a, b string) bool {
	return a == b
}
