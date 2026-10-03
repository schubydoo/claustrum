//go:build windows

package main

import (
	"path/filepath"
	"strings"
)

// windowsPathSpellingHazard reports whether any component of p (below the volume
// name) ends in "." or " " or contains ":". Windows silently strips trailing dots
// and spaces from a name and treats ":" as an alternate-data-stream / drive
// separator, so such a component names a DIFFERENT file than written — a
// path-confusion hazard. The reference (7d193f89) refuses a worktree path with such a
// component on Windows (measured — byte-identical create/remove refusal frames on the
// VM); unix has no such hazard (spelling_other.go returns false). The volume name is
// stripped first so a drive letter's ":" (C:) is not flagged, and both "\" and "/" are
// treated as separators (Go path semantics; the reference's backslash refusals were
// measured byte-for-byte on the VM). A var so a test on the linux coverage cell can
// stub it to exercise worktreePathRefusal's spelling branch (cf. homeguard.go's
// pathsAreCaseInsensitive) — the real check needs Windows filepath semantics.
var windowsPathSpellingHazard = func(p string) bool {
	rest := p[len(filepath.VolumeName(p)):]
	for _, comp := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		if comp == "." || comp == ".." {
			continue
		}
		if strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ") || strings.Contains(comp, ":") {
			return true
		}
	}
	return false
}
