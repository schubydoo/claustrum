//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows rows of the git.status gate. Each row is from pass X of the probe of
// 89cb6289 on a Windows VM, where the daemon has a user excludes file. The frames of
// claustrum are the same with no such file (D16).
func TestGitStatusWindowsSpellings(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, f statusFixture) (path string)
		want string
	}{
		// Row D8: path in upper case.
		{"D8 path in upper case", func(_ *testing.T, f statusFixture) string {
			return strings.ToUpper(f.W)
		}, statusClean},
		// Row D15: forward slashes.
		{"D15 forward slashes", func(_ *testing.T, f statusFixture) string {
			return filepath.ToSlash(f.W)
		}, statusClean},
		// Rows D14dot and D14sp: a trailing dot or space on a path outside baseRepo.
		{"D14dot trailing dot", func(_ *testing.T, f statusFixture) string { return f.W + "." }, statusClean},
		{"D14sp trailing space", func(_ *testing.T, f statusFixture) string { return f.W + " " }, statusClean},
		// Row D16: a \\?\ path.
		{"D16 long path prefix", func(_ *testing.T, f statusFixture) string { return `\\?\` + f.W }, statusClean},
		// Row D6: path is a junction to the worktree.
		{"D6 junction to the worktree", func(t *testing.T, f statusFixture) string {
			jw := filepath.Join(f.root, "JW")
			makeJunction(t, jw, f.W)
			return jw
		}, statusClean},
		// Rows D11f and D11b: an absolute commondir with forward or back slashes.
		{"D11f commondir with forward slashes", func(t *testing.T, f statusFixture) string {
			f.write(t, "commondir", filepath.ToSlash(filepath.Join(f.T, ".git"))+"\n")
			return f.W
		}, statusClean},
		{"D11b commondir with back slashes", func(t *testing.T, f statusFixture) string {
			f.write(t, "commondir", filepath.Join(f.T, ".git")+"\n")
			return f.W
		}, statusClean},
		// Rows D10 and W04: a commondir in upper case.
		{"D10 commondir in upper case", func(t *testing.T, f statusFixture) string {
			f.write(t, "commondir", strings.ToUpper(filepath.ToSlash(filepath.Join(f.T, ".git")))+"\n")
			return f.W
		}, statusNotRepo},
		// Row W05: a commondir with the \\?\ prefix.
		{"W05 commondir with the long path prefix", func(t *testing.T, f statusFixture) string {
			f.write(t, "commondir", `\\?\`+filepath.Join(f.T, ".git")+"\n")
			return f.W
		}, statusNotRepo},
		// Row W06: a commondir with no drive letter.
		{"W06 commondir with no drive letter", func(t *testing.T, f statusFixture) string {
			f.write(t, "commondir", filepath.ToSlash(strings.TrimPrefix(filepath.Join(f.T, ".git"), filepath.VolumeName(f.T)))+"\n")
			return f.W
		}, statusNotRepo},
		// Row D9: the gitdir value in upper case, .git included.
		{"D9 gitdir in upper case", func(t *testing.T, f statusFixture) string {
			f.write(t, "gitdir", strings.ToUpper(filepath.ToSlash(filepath.Join(f.W, ".git")))+"\n")
			return f.W
		}, statusNotRepo},
		// Row n07-j: path lies inside baseRepo and .claude is a junction.
		{"n07-j junction inside baseRepo", func(t *testing.T, f statusFixture) string {
			elsewhere := filepath.Join(f.root, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o755); err != nil {
				t.Fatal(err)
			}
			makeJunction(t, filepath.Join(f.T, ".claude"), elsewhere)
			s0 := filepath.Join(f.T, ".claude", "worktrees", "s0")
			runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", s0)
			writeFile(t, filepath.Join(f.T, ".git", "worktrees", "s0", "gitdir"), filepath.ToSlash(filepath.Join(s0, ".git"))+"\n", 0o644)
			return s0
		}, statusNotRepo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusFixture(t)
			path := c.edit(t, f)
			if got := statusFrame(t, path, f.T); got != c.want {
				t.Errorf("git.status(%s) = %s\nwant %s", path, got, c.want)
			}
		})
	}
}

// Row D5: baseRepo is a junction to the repository.
func TestGitStatusWindowsBaseRepoJunction(t *testing.T) {
	f := newStatusFixture(t)
	jt := filepath.Join(f.root, "JT")
	makeJunction(t, jt, f.T)
	if got := statusFrame(t, f.W, jt); got != statusClean {
		t.Errorf("D5 git.status = %s\nwant %s", got, statusClean)
	}
}
