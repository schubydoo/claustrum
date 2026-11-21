//go:build windows

package git

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStatusProbeOfAJunctionPath: a `path` outside baseRepo through a junction
// gets no `rev-parse --show-toplevel` call with the junction in its
// --work-tree. On 4c111e86 a `path` through a junction gives one
// `rev-parse --show-toplevel` with the real path, and the `status` call with the
// path as sent (measured on Windows).
func TestStatusProbeOfAJunctionPath(t *testing.T) {
	f := newSeededShadowFixture(t)
	jw := filepath.Join(t.TempDir(), "JW")
	makeJunction(t, jw, f.wt)
	if !junctionInPath(jw) || junctionInPath(f.wt) {
		t.Fatalf("junctionInPath: %v for the junction, %v for the worktree, want true and false", junctionInPath(jw), junctionInPath(f.wt))
	}
	raw, calls := f.call(t, "git.status", map[string]any{"path": jw, "baseRepo": f.repo})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.status = %s", raw)
	}
	var probe, status []string
	for _, c := range calls {
		var tree string
		for _, a := range c.argv {
			if v, ok := strings.CutPrefix(a, "--work-tree="); ok {
				tree = v
			}
		}
		switch {
		case slices.Contains(c.argv, "--show-toplevel"):
			probe = append(probe, tree)
		case slices.Contains(c.argv, "status"):
			status = append(status, tree)
		}
	}
	if len(probe) != 1 || !strings.EqualFold(probe[0], resolvedDir(f.wt)) {
		t.Errorf("rev-parse --show-toplevel work trees %q, want only the real path %q", probe, resolvedDir(f.wt))
	}
	if len(status) != 1 || status[0] != jw {
		t.Errorf("status work trees %q, want only the path as sent %q", status, jw)
	}
}
