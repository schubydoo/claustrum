//go:build unix

package git

import (
	"strings"
	"testing"
)

// TestStatusLFSPlaceOfWindows runs the place of Windows on this system: the
// lfs.storage item of the `status` call comes right after --work-tree
// (4c111e86, measured on Windows).
func TestStatusLFSPlaceOfWindows(t *testing.T) {
	old := statusLFSAfterWorkTree
	statusLFSAfterWorkTree = true
	t.Cleanup(func() { statusLFSAfterWorkTree = old })
	f := newSeededShadowFixture(t)
	raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.status = %s", raw)
	}
	checkStatusLFSPlace(t, calls)
}
