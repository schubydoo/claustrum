//go:build windows

package main

import (
	"os"
	"os/exec"
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

// The x rows of the Windows VM: a worktree at T\.claude\worktrees\s0, so path lies
// inside baseRepo.
func TestGitStatusWindowsInsidePathRows(t *testing.T) {
	type fx struct {
		statusFixture
		s0, s0entry string
	}
	build := func(t *testing.T) fx {
		f := newStatusFixture(t)
		s0 := filepath.Join(f.T, ".claude", "worktrees", "s0")
		runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", s0)
		return fx{f, s0, filepath.Join(f.T, ".git", "worktrees", "s0")}
	}
	upper := func(p string) string { return strings.Replace(p, "worktrees", "WORKTREES", 1) }
	cases := []struct {
		name string
		edit func(t *testing.T, f fx) (path string)
		want string
	}{
		{"control", func(_ *testing.T, f fx) string { return f.s0 }, statusClean},
		// Rows x1 and x2: the last component ends in a dot or a space.
		{"x1 trailing dot", func(_ *testing.T, f fx) string { return f.s0 + "." }, statusNotRepo},
		{"x2 trailing space", func(_ *testing.T, f fx) string { return f.s0 + " " }, statusNotRepo},
		// Row x3: a colon.
		{"x3 colon", func(_ *testing.T, f fx) string { return f.s0 + ":x" }, statusNotRepo},
		// Rows x4 and x4b: a `..` component, without and with the folder before it.
		{"x4 dot dot, no folder", func(_ *testing.T, f fx) string {
			return filepath.Dir(f.s0) + `\q\..\s0`
		}, statusNotRepo},
		{"x4b dot dot, folder there", func(t *testing.T, f fx) string {
			if err := os.Mkdir(filepath.Join(filepath.Dir(f.s0), "q"), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Dir(f.s0) + `\q\..\s0`
		}, statusNotRepo},
		// Row x6: one folder name of the entry gitdir in another letter case.
		{"x6 gitdir with WORKTREES", func(t *testing.T, f fx) string {
			writeFile(t, filepath.Join(f.s0entry, "gitdir"), upper(filepath.ToSlash(filepath.Join(f.s0, ".git")))+"\n", 0o644)
			return f.s0
		}, statusClean},
		// Row x7: the gitdir ends in .GIT.
		{"x7 gitdir ends in .GIT", func(t *testing.T, f fx) string {
			writeFile(t, filepath.Join(f.s0entry, "gitdir"), filepath.ToSlash(filepath.Join(f.s0, ".GIT"))+"\n", 0o644)
			return f.s0
		}, statusNotRepo},
		// Row x8: one folder name of path in another letter case.
		{"x8 path with WORKTREES", func(_ *testing.T, f fx) string { return upper(f.s0) }, statusClean},
		// Row x9: one folder name of an absolute commondir in another letter case.
		{"x9 commondir in another case", func(t *testing.T, f fx) string {
			v := filepath.ToSlash(filepath.Join(f.T, ".git"))
			writeFile(t, filepath.Join(f.s0entry, "commondir"), strings.Replace(v, "/T/", "/t/", 1)+"\n", 0o644)
			return f.s0
		}, statusNotRepo},
		// Row x10: a junction as the last component.
		{"x10 junction to s0", func(t *testing.T, f fx) string {
			j := filepath.Join(filepath.Dir(f.s0), "j0")
			makeJunction(t, j, f.s0)
			return j
		}, statusNotRepo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := build(t)
			path := c.edit(t, f)
			if got := statusFrame(t, path, f.T); got != c.want {
				t.Errorf("git.status(%s) = %s\nwant %s", path, got, c.want)
			}
		})
	}
	// Row x5: a relative path that lies inside baseRepo goes to git as sent, the
	// rev-parse fails, and the answer is isRepo:false.
	t.Run("x5 relative path", func(t *testing.T) {
		f := build(t)
		t.Chdir(f.root)
		if got := statusFrame(t, `T\.claude\worktrees\s0`, f.T); got != statusNotRepo {
			t.Errorf("git.status = %s\nwant %s", got, statusNotRepo)
		}
	})
}

// Rows n11-j and x11: a baseRepo that is a dangling junction answers isRepo:false
// with no git call after the read of the excludes.
func TestGitStatusWindowsDanglingJunctionBaseRepo(t *testing.T) {
	f := newStatusFixture(t)
	target := filepath.Join(f.root, "nothing")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(f.root, "DL")
	makeJunction(t, dl, target)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	userExcludesFile() // the read of the excludes is not counted below
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	if got := statusFrame(t, f.W, dl); got != statusNotRepo {
		t.Errorf("git.status = %s\nwant %s", got, statusNotRepo)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("git calls = %q, want none", b)
	}
}
