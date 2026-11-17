//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// A marker above the spelling of baseRepo refuses it, also when baseRepo is a
// symbolic link to a repository with no marker above it. Markers 1 and 4 levels up
// were measured (b3081b51 and be1c1ca5, measured on Linux).
func TestBaseRepoSpelledBelowMarkerRefused(t *testing.T) {
	requireGit(t)
	for name, sub := range map[string]string{"1up": "", "4up": filepath.Join("a", "b", "c")} {
		t.Run(name, func(t *testing.T) {
			w := t.TempDir()
			m := filepath.Join(w, "mk")
			if err := os.MkdirAll(filepath.Join(m, sub), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(m, managedWorktreesMarker), "", 0o644)
			r := filepath.Join(w, "out", "R")
			markerLayoutRepo(t, r)
			base := filepath.Join(m, sub, "sl")
			symlinkForTest(t, r, base)
			markerLayoutFrames(t, base, r)
		})
	}
}

// A marker that is a link with no target refuses a baseRepo below it, as a marker
// file does. The create is 4c111e86, measured on Linux. The remove and list_branches
// answers are claustrum's own, not measured.
func TestBaseRepoBelowDanglingMarkerLinkRefused(t *testing.T) {
	requireGit(t)
	m := t.TempDir()
	symlinkForTest(t, "nowhere-marker", filepath.Join(m, managedWorktreesMarker))
	r := filepath.Join(m, "R")
	markerLayoutRepo(t, r)
	markerLayoutFrames(t, r, r)
}
