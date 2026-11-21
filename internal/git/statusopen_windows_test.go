//go:build windows

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStatusPathThatCannotBeOpened: a git.status `path` with the reparse tag
// 0x80000008 passes os.Stat, and its open fails. There 4c111e86 answers isRepo
// false with no `rev-parse --show-toplevel` in the git directory (measured on
// Windows). A plain folder in the same place still gets that call. Mutation:
// drop the statusPathOpenFails test in gitStatus (the first row makes the call).
func TestStatusPathThatCannotBeOpened(t *testing.T) {
	for _, row := range []struct {
		name   string
		tagged bool
	}{
		{"tag 0x80000008", true},
		{"plain folder", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			sub := filepath.Join(f.wt, "sub")
			if row.tagged {
				tagFolder(t, sub, ioReparseTagWIM)
				if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
					t.Fatalf("os.Stat of the tagged folder: %v, %v", fi, err)
				}
				if !statusPathOpenFails(sub) {
					skipOutsideCI(t, "the folder with the tag %#x opens", ioReparseTagWIM)
				}
			} else if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			raw, calls := f.call(t, "git.status", map[string]any{"path": sub, "baseRepo": f.repo})
			probe := false
			for _, c := range calls {
				if slices.Contains(c.argv, "--show-toplevel") {
					probe = true
				}
			}
			if row.tagged {
				if !strings.Contains(raw, `"isRepo":false`) {
					t.Errorf("git.status = %s, want isRepo false", raw)
				}
				if probe {
					t.Error("the tagged folder got a rev-parse --show-toplevel call")
				}
			} else if !probe {
				t.Errorf("the plain folder got no rev-parse --show-toplevel call (reply %s)", raw)
			}
		})
	}
}
