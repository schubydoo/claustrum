package git

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestGitParamsExpandPathsCoversEveryPathField is the gitParams half of
// TestExpandPathsCoversEveryPathField. gitParams can implement ExpandPaths and
// still miss a path field. The compile-time interface cannot catch that.
func TestGitParamsExpandPathsCoversEveryPathField(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Not because Windows is unmeasured — it is, see internal/pathcanon/expandpath.go. The
		// expectations below are written in Unix spelling ("~/f.txt" -> home +
		// "/f.txt"), and the reference rewrites "/" to "\" there, so this table
		// pins the Unix form only. TestExpandPathBareTildeIsNotCleaned covers
		// the one row whose behaviour is spelling-independent.
		t.Skip("this table is written in Unix path spelling; Windows is pinned in internal/pathcanon/expandpath.go")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	under := func(rest string) string { return filepath.Join(home, rest) }

	gp := &gitParams{Path: "~/g", BaseRepo: "~/b", WorktreePath: "~/w", WorktreeRoot: "~/r", BranchName: "~keep", SourceBranch: "~keep2"}
	gp.ExpandPaths()
	if gp.Path != under("g") || gp.BaseRepo != under("b") || gp.WorktreePath != under("w") || gp.WorktreeRoot != under("r") {
		t.Errorf("gitParams paths = %+v", gp)
	}
	// Branch names are refs, not paths: a branch legitimately called "~keep"
	// must survive untouched.
	if gp.BranchName != "~keep" || gp.SourceBranch != "~keep2" {
		t.Errorf("gitParams expanded a ref: BranchName=%q SourceBranch=%q", gp.BranchName, gp.SourceBranch)
	}
}
