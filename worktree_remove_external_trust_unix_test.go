//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests cover git.worktree_remove with worktreeRoot on a baseRepo whose git
// directory the trust check refuses, or in which there is no repository, plus the
// linked-worktree baseRepo. Each row name is the row of the side-by-side run against
// f6010b97 on a Linux VM (rows WR* and K*). worktreeRoot is unix-only.

// wrFixture is the layout of those rows. T is a repository and TW its linked
// worktree. X is a second repository. The worktree location is R. e0 is a worktree
// that T added at R/proj/e0, and e1 one that TW added at R/proj/e1. p0 is a plain
// folder at R/proj/p0.
type wrFixture struct {
	base, T, TW, eTW, X, R, e0, e1, p0 string
}

func newWRFixture(t *testing.T) *wrFixture {
	t.Helper()
	requireGit(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", base)
	f := &wrFixture{
		base: base,
		T:    filepath.Join(base, "T"),
		TW:   filepath.Join(base, "T_wt"),
		X:    filepath.Join(base, "X"),
		R:    filepath.Join(base, "R"),
	}
	f.eTW = filepath.Join(f.T, ".git", "worktrees", "T_wt")
	f.e0 = filepath.Join(f.R, "proj", "e0")
	f.e1 = filepath.Join(f.R, "proj", "e1")
	f.p0 = filepath.Join(f.R, "proj", "p0")
	for _, d := range []string{f.T, f.X, f.p0} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrWrite(t, filepath.Join(f.p0, "keep.txt"), "k\n")
	for _, r := range []string{f.T, f.X} {
		runGit(t, r, "init", "-q")
		runGit(t, r, "commit", "-q", "--allow-empty", "-m", "init")
	}
	runGit(t, f.T, "worktree", "add", "-q", f.TW, "-b", "lw")
	runGit(t, f.T, "worktree", "add", "-q", f.e0, "-b", "e0")
	runGit(t, f.TW, "worktree", "add", "-q", f.e1, "-b", "e1")
	return f
}

func wrWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// remove sends git.worktree_remove with worktreeRoot R and returns the frame.
func (f *wrFixture) remove(t *testing.T, baseRepo, target, branch string) string {
	t.Helper()
	s := newTestServer(t)
	return dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": baseRepo, "worktreePath": target, "branchName": branch, "worktreeRoot": f.R}))
}

// kept fails the test when the worktree, its entry or its branch is gone.
func (f *wrFixture) kept(t *testing.T, target, branch string) {
	t.Helper()
	for _, p := range []string{target, filepath.Join(f.T, ".git", "worktrees", branch),
		filepath.Join(f.T, ".git", "refs", "heads", branch)} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s is gone (%v), but a refusal deletes nothing", p, err)
		}
	}
}

// removed fails the test when the worktree, its entry or its branch is still there.
func (f *wrFixture) removed(t *testing.T, target, branch string) {
	t.Helper()
	for _, p := range []string{target, filepath.Join(f.T, ".git", "worktrees", branch),
		filepath.Join(f.T, ".git", "refs", "heads", branch)} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s is still there, but f6010b97 removes it", p)
		}
	}
}

func refusalFrame(t *testing.T, msg string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + jsonEscape(t, msg) + `"}}`
}

const (
	wtUnknown = "failed to remove worktree: cannot determine the repository's work tree: "
	trustText = "the repository's git directory could not be trusted; git not run: " +
		"commondir not as git writes it: "
)

// strayText is the reason for a stray commondir of baseRepo. want is the trust text
// (wantTR or wantTP).
func strayText(want string) string {
	return wtUnknown + want
}

func foreignText(cd, repo string) string {
	return wtUnknown + trustText + `"` + cd + `" does not name the entry's own repository, "` +
		repo + `"; restore the file to read ../.. or remove that worktree entry and add the worktree again`
}

// Rows WR01 to WR06 and WR08: a refused git directory of baseRepo answers the prefix
// and the trust refusal text. Nothing is deleted.
func TestWorktreeRemoveExternalRefusedGitDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		// plant damages the fixture. It returns baseRepo, the target, the branch and
		// the expected error text.
		plant func(t *testing.T, f *wrFixture) (base, target, branch, want string)
	}{
		// On f6010b97 WR01 planted "." and was refused. 89cb6289 trusts "." (row T01),
		// so the row plants "x" here.
		{"WR01_stray_commondir_x", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			wrWrite(t, cd, "x\n")
			return f.T, f.e0, "e0", strayText(wantTR(cd, "x"))
		}},
		{"WR02_stray_commondir_own_abs", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			wrWrite(t, cd, filepath.Join(f.T, ".git")+"\n")
			return f.T, f.e0, "e0", strayText(wantTR(cd, filepath.Join(f.T, ".git")))
		}},
		{"WR03_stray_commondir_is_dir", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			if err := os.MkdirAll(cd, 0o755); err != nil {
				t.Fatal(err)
			}
			return f.T, f.e0, "e0", strayText(wantTP(cd, "commondir is not a regular file"))
		}},
		{"WR04_linked_base_commondir_other_repo", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.eTW, "commondir")
			wrWrite(t, cd, filepath.Join(f.X, ".git")+"\n")
			return f.TW, f.e1, "e1", foreignText(cd, filepath.Join(f.T, ".git"))
		}},
		{"WR05_linked_base_commondir_removed", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			if err := os.Remove(filepath.Join(f.eTW, "commondir")); err != nil {
				t.Fatal(err)
			}
			return f.TW, f.e1, "e1", wtUnknown + trustText + `worktree entry "` + f.eTW +
				`" has none (git always writes one there, containing ../..); the entry is damaged ` +
				"or was not made by git — recreate the worktree with git worktree add, or remove the entry"
		}},
		{"WR06_linked_base_commondir_nonexistent", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.eTW, "commondir")
			wrWrite(t, cd, filepath.Join(f.base, "nope", ".git")+"\n")
			return f.TW, f.e1, "e1", foreignText(cd, filepath.Join(f.T, ".git"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWRFixture(t)
			base, target, branch, want := tc.plant(t, f)
			if got := f.remove(t, base, target, branch); got != refusalFrame(t, want) {
				t.Errorf("got  %s\nwant %s", got, refusalFrame(t, want))
			}
			f.kept(t, target, branch)
		})
	}

	// WR08: a plain folder as the target gets the trust text too, not the
	// "not a worktree" refusal of the registration check.
	t.Run("WR08_stray_commondir_plain_folder", func(t *testing.T) {
		f := newWRFixture(t)
		cd := filepath.Join(f.T, ".git", "commondir")
		wrWrite(t, cd, "x\n")
		if got, want := f.remove(t, f.T, f.p0, "p0"), refusalFrame(t, strayText(wantTR(cd, "x"))); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
			t.Errorf("the plain folder was touched: %v", err)
		}
	})
}

// Rows WR10 to WR13: a baseRepo with no repository answers the prefix and
// "exit status 128". Nothing is deleted.
func TestWorktreeRemoveExternalNoRepositoryBase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, f *wrFixture) (base, target, branch string)
	}{
		{"WR10_nested_empty_dotgit", func(t *testing.T, f *wrFixture) (string, string, string) {
			a := filepath.Join(f.base, "A")
			if err := os.MkdirAll(filepath.Join(a, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			return a, f.e0, "e0"
		}},
		{"WR11_plain_dir", func(t *testing.T, f *wrFixture) (string, string, string) {
			a := filepath.Join(f.base, "A")
			if err := os.MkdirAll(a, 0o755); err != nil {
				t.Fatal(err)
			}
			return a, f.e0, "e0"
		}},
		{"WR12_dotgit_file_garbage", func(t *testing.T, f *wrFixture) (string, string, string) {
			a := filepath.Join(f.base, "A")
			wrWrite(t, filepath.Join(a, ".git"), "junk\n")
			return a, f.e0, "e0"
		}},
		{"WR13_linked_base_entry_removed", func(t *testing.T, f *wrFixture) (string, string, string) {
			if err := os.RemoveAll(f.eTW); err != nil {
				t.Fatal(err)
			}
			return f.TW, f.e1, "e1"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWRFixture(t)
			base, target, branch := tc.plant(t, f)
			want := refusalFrame(t, wtUnknown+"exit status 128")
			if got := f.remove(t, base, target, branch); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			f.kept(t, target, branch)
		})
	}
}

// Row WR15: a configuration that cannot be listed answers the prefix and the hooks
// refusal text. Nothing is deleted.
func TestWorktreeRemoveExternalHooksRefusal(t *testing.T) {
	f := newWRFixture(t)
	cfg := filepath.Join(f.T, ".git", "config")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b) + "[core\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	line := strings.Count(body, "\n")
	want := refusalFrame(t, wtUnknown+"config-defined hooks could not be pinned off; git not run: "+
		"listing the configuration in force: exit status 128: fatal: bad config line "+
		strconv.Itoa(line)+" in file "+cfg)
	if got := f.remove(t, f.T, f.e0, "e0"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := os.Stat(f.e0); err != nil {
		t.Errorf("the worktree is gone: %v", err)
	}
}

// Rows WR07, WR14, K02 and K09: the removal resolves baseRepo to its repository git
// directory and removes the worktree, its entry and its branch.
func TestWorktreeRemoveExternalResolvesBaseRepository(t *testing.T) {
	// WR07: a linked worktree as baseRepo.
	t.Run("WR07_linked_base", func(t *testing.T) {
		f := newWRFixture(t)
		if got, want := f.remove(t, f.TW, f.e1, "e1"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.removed(t, f.e1, "e1")
	})
	// WR14: the main repository carries a stray commondir. The check judges the git
	// directory of baseRepo, which is the entry T/.git/worktrees/T_wt. T/.git is only
	// the pin, and the check does not judge it again.
	t.Run("WR14_stray_commondir_in_main_linked_base", func(t *testing.T) {
		f := newWRFixture(t)
		wrWrite(t, filepath.Join(f.T, ".git", "commondir"), ".\n")
		if got, want := f.remove(t, f.TW, f.e1, "e1"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.removed(t, f.e1, "e1")
	})
	// K02: a subdirectory of the repository as baseRepo.
	t.Run("K02_subdir_base", func(t *testing.T) {
		f := newWRFixture(t)
		sub := filepath.Join(f.T, "p", "q")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, want := f.remove(t, sub, f.e0, "e0"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.removed(t, f.e0, "e0")
	})
	// K09: a submodule as baseRepo. Its `.git` file names S/.git/modules/sub.
	t.Run("K09_submodule_base", func(t *testing.T) {
		f := newWRFixture(t)
		sub := filepath.Join(f.base, "S", "sub")
		mods := filepath.Join(f.base, "S", ".git", "modules", "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, filepath.Dir(sub), "init", "-q")
		if err := os.MkdirAll(filepath.Dir(mods), 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, sub, "init", "-q", "--separate-git-dir", mods)
		runGit(t, sub, "commit", "-q", "--allow-empty", "-m", "init")
		m0 := filepath.Join(f.R, "proj", "m0")
		runGit(t, sub, "worktree", "add", "-q", m0, "-b", "m0")
		if got, want := f.remove(t, sub, m0, "m0"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		for _, p := range []string{m0, filepath.Join(mods, "worktrees", "m0"), filepath.Join(mods, "refs", "heads", "m0")} {
			if _, err := os.Lstat(p); err == nil {
				t.Errorf("%s is still there, but f6010b97 removes it", p)
			}
		}
	})
}

// Rows K07 and K25: a baseRepo that is a git directory answers the prefix and
// "exit status 128". Nothing is deleted.
func TestWorktreeRemoveExternalBaseIsGitDir(t *testing.T) {
	t.Run("K07_bare_repo", func(t *testing.T) {
		f := newWRFixture(t)
		bare := filepath.Join(f.base, "B.git")
		runGit(t, f.base, "clone", "-q", "--bare", f.T, bare)
		b0 := filepath.Join(f.R, "proj", "b0")
		runGit(t, bare, "worktree", "add", "-q", b0, "-b", "b0")
		if got, want := f.remove(t, bare, b0, "b0"), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		for _, p := range []string{b0, filepath.Join(bare, "worktrees", "b0"), filepath.Join(bare, "refs", "heads", "b0")} {
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("%s is gone (%v), but a refusal deletes nothing", p, err)
			}
		}
	})
	t.Run("K25_base_is_dotgit", func(t *testing.T) {
		f := newWRFixture(t)
		if got, want := f.remove(t, filepath.Join(f.T, ".git"), f.e0, "e0"), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
}

// Rows K12 and K15, a daemon GIT_DIR. K12: it names nothing, and git's repository
// check fails with "exit status 128". K15: it names the repository X, so the
// registration check reads X/.git/worktrees, which does not exist.
func TestWorktreeRemoveExternalDaemonGitDir(t *testing.T) {
	t.Run("K12_names_nothing", func(t *testing.T) {
		f := newWRFixture(t)
		t.Setenv("GIT_DIR", filepath.Join(f.base, "nope.git"))
		if got, want := f.remove(t, f.T, f.e0, "e0"), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
	t.Run("K15_names_other_repo", func(t *testing.T) {
		f := newWRFixture(t)
		t.Setenv("GIT_DIR", filepath.Join(f.X, ".git"))
		want := refusalFrame(t, "failed to remove worktree: could not verify that "+f.e0+
			" is a worktree of "+f.T+" (open "+filepath.Join(f.X, ".git", "worktrees")+
			": no such file or directory); retry")
		if got := f.remove(t, f.T, f.e0, "e0"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
}

// When the repository git directory is <baseRepo>/.git, the transient text keeps the
// spelling of baseRepo as given, here a symlink, as before the pin was used.
func TestWorktreeRemoveExternalVerifyKeepsBaseSpelling(t *testing.T) {
	f := newWRFixture(t)
	link := filepath.Join(f.base, "XL")
	if err := os.Symlink(f.X, link); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(f.R, "proj", "f0")
	wrWrite(t, filepath.Join(wt, ".git"), "gitdir: /foreign/.git/worktrees/x\n")
	want := refusalFrame(t, "failed to remove worktree: could not verify that "+wt+
		" is a worktree of "+link+" (open "+filepath.Join(link, ".git", "worktrees")+
		": no such file or directory); retry")
	if got := f.remove(t, link, wt, ""); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree is gone: %v", err)
	}
}

// Row K03 with a target that is already gone: the no-repository answer comes before
// the "already gone" success.
func TestWorktreeRemoveExternalNoRepositoryGoneTarget(t *testing.T) {
	f := newWRFixture(t)
	plain := filepath.Join(f.base, "N")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(f.R, "proj", "gone")
	if got, want := f.remove(t, plain, gone, ""), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A linked baseRepo changes only which worktrees directory the registration check
// reads. It does not widen what the delete can reach. Each case is refused and
// nothing is deleted:
//   - a home directory that is a genuine worktree of the repository (wipesHomeDir),
//   - a relative worktreePath (the location check),
//   - a genuine worktree of another repository, through an honest linked baseRepo,
//   - the same worktree, through a linked baseRepo whose entry names that repository.
func TestWorktreeRemoveExternalLinkedBaseCannotRedirect(t *testing.T) {
	t.Run("home", func(t *testing.T) {
		f := newWRFixture(t)
		home := filepath.Join(f.R, "proj", "h")
		runGit(t, f.T, "worktree", "add", "-q", home, "-b", "h")
		t.Setenv("HOME", home)
		want := refusalFrame(t, `worktreePath must not be or contain the home directory: "`+home+`"`)
		if got := f.remove(t, f.TW, home, "h"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, home, "h")
	})
	t.Run("relative", func(t *testing.T) {
		f := newWRFixture(t)
		t.Chdir(f.base)
		rel := filepath.Join("R", "proj", "e1")
		want := refusalFrame(t, "refusing to remove worktree: "+rel+" is a relative path; "+
			`choose the session folder by its absolute path, without ".."`)
		if got := f.remove(t, f.TW, rel, "e1"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e1, "e1")
	})
	t.Run("other_repository", func(t *testing.T) {
		f := newWRFixture(t)
		x0 := filepath.Join(f.R, "proj", "x0")
		runGit(t, f.X, "worktree", "add", "-q", x0, "-b", "x0")
		want := refusalFrame(t, "refusing to remove worktree: "+x0+" is not a worktree of "+f.TW+
			" ("+x0+" carries a .git file that does not name this repository's own worktree admin "+
			"directory), so it is left in place; remove it by hand if it is a leftover")
		if got := f.remove(t, f.TW, x0, "x0"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		if _, err := os.Stat(filepath.Join(x0, ".git")); err != nil {
			t.Errorf("the worktree of X is gone: %v", err)
		}
	})
	t.Run("entry_names_other_repository", func(t *testing.T) {
		f := newWRFixture(t)
		x0 := filepath.Join(f.R, "proj", "x0")
		runGit(t, f.X, "worktree", "add", "-q", x0, "-b", "x0")
		cd := filepath.Join(f.eTW, "commondir")
		wrWrite(t, cd, filepath.Join(f.X, ".git")+"\n")
		want := refusalFrame(t, foreignText(cd, filepath.Join(f.T, ".git")))
		if got := f.remove(t, f.TW, x0, "x0"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		if _, err := os.Stat(filepath.Join(x0, ".git")); err != nil {
			t.Errorf("the worktree of X is gone: %v", err)
		}
	})
}

// With worktreeRoot, a D5 (git-timeout) kill of the repository check answers the
// work-tree refusal with the exec error. Nothing is deleted, and no further git
// command runs. `rev-parse --show-toplevel` answers at once, so that its own kill
// refusal does not come first.
func TestWorktreeRemoveExternalRepositoryCheckTimeoutRefuses(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(bin, "ran")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in config) exit 0 ;; --show-toplevel) exit 0 ;; " +
		"--absolute-git-dir) exec sleep 30 ;; esac; done\n" +
		"echo \"$*\" >> '" + ran + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := gitTimeout
	gitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "T")
	shapeAsGitRepo(t, repo)
	root := filepath.Join(base, "R")
	target := filepath.Join(root, "proj", "e0")
	keep := filepath.Join(target, "KEEP.txt")
	wrWrite(t, keep, "must survive")

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": target, "worktreeRoot": root}))
	if want := refusalFrame(t, wtUnknown+"signal: killed"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("worktreePath was deleted: %v", err)
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Errorf("git ran after the killed repository check: %s", b)
	}
}

// Rows K10, K11 and K14: a baseRepo that git cannot start in answers the work-tree
// refusal with the hooks refusal and the start error of the configuration listing.
// The error names the git that PATH resolves. Nothing is deleted.
func TestWorktreeRemoveExternalUnenterableBase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test depends on")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	hooks := wtUnknown + "config-defined hooks could not be pinned off; git not run: " +
		"listing the configuration in force: fork/exec " + gitPath + ": "
	t.Run("K10_mode0600_plain", func(t *testing.T) {
		f := newWRFixture(t)
		n := filepath.Join(f.base, "N")
		if err := os.MkdirAll(n, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(n, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(n, 0o755) })
		for _, target := range []string{f.p0, filepath.Join(f.R, "proj", "gone")} {
			if got, want := f.remove(t, n, target, ""), refusalFrame(t, hooks+"permission denied"); got != want {
				t.Errorf("%s:\ngot  %s\nwant %s", target, got, want)
			}
		}
		if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
			t.Errorf("the plain folder was touched: %v", err)
		}
	})
	t.Run("K11_mode0600_repo", func(t *testing.T) {
		f := newWRFixture(t)
		if err := os.Chmod(f.T, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(f.T, 0o755) })
		got := f.remove(t, f.T, f.e0, "e0")
		if err := os.Chmod(f.T, 0o755); err != nil {
			t.Fatal(err)
		}
		if want := refusalFrame(t, hooks+"permission denied"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
	t.Run("K14_base_is_regular_file", func(t *testing.T) {
		f := newWRFixture(t)
		file := filepath.Join(f.base, "F")
		wrWrite(t, file, "f\n")
		if got, want := f.remove(t, file, f.p0, ""), refusalFrame(t, hooks+"not a directory"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}

// Row K16 (f6010b97) and row N04 (89cb6289): a `.git` file that names a path that is
// gone and is not a worktree entry. git's configuration listing fails there with "not
// a git repository", so the reason is "git finds no repository here: " and git's
// text. The check also comes before the dir-symlink check. git 2.47 names the
// missing path in its text, and a newer git prints "(null)" there. The daemon passes
// git's text through, so the test accepts either. Mutation: keep the listing refusal
// text for this class.
func TestWorktreeRemoveExternalGitFileToNowhere(t *testing.T) {
	f := newWRFixture(t)
	g := filepath.Join(f.base, "G")
	nowhere := filepath.Join(f.base, "nowhere")
	wrWrite(t, filepath.Join(g, ".git"), "gitdir: "+nowhere+"\n")
	wantFor := func(name string) string {
		return refusalFrame(t, wtUnknown+"git finds no repository here: fatal: not a git repository: "+name)
	}
	want := wantFor(nowhere)
	got := f.remove(t, g, f.p0, "")
	if got == wantFor("(null)") {
		want = got
	}
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	lnk := filepath.Join(f.R, "lnk")
	if err := os.Symlink(filepath.Join(f.R, "proj"), lnk); err != nil {
		t.Fatal(err)
	}
	if got := f.remove(t, g, filepath.Join(lnk, "p0"), ""); got != want {
		t.Errorf("through a symlinked directory level:\ngot  %s\nwant %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
		t.Errorf("the plain folder was touched: %v", err)
	}
}

// Row K13: the worktrees directory of the repository cannot be read (mode 0000). A
// plain folder and a registered worktree both get the lock-check text, with the path
// as given. A worktree that is already gone still answers success. Nothing is deleted.
func TestWorktreeRemoveExternalRegistryUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test depends on")
	}
	f := newWRFixture(t)
	wts := filepath.Join(f.T, ".git", "worktrees")
	if err := os.Chmod(wts, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(wts, 0o755) })
	lock := func(p string) string {
		return refusalFrame(t, "failed to remove worktree: could not check whether "+p+
			" is locked (its registrations could not be examined); retry")
	}
	for _, c := range []struct{ target, branch, want string }{
		{f.p0, "", lock(f.p0)},
		{f.e0, "e0", lock(f.e0)},
		{filepath.Join(f.R, "proj", "gone"), "", `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`},
	} {
		if got := f.remove(t, f.T, c.target, c.branch); got != c.want {
			t.Errorf("%s:\ngot  %s\nwant %s", c.target, got, c.want)
		}
	}
	if err := os.Chmod(wts, 0o755); err != nil {
		t.Fatal(err)
	}
	f.kept(t, f.e0, "e0")
	if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
		t.Errorf("the plain folder was touched: %v", err)
	}
}

// wrRow is one request of the no-git table: baseRepo, worktreePath, worktreeRoot ("" is
// the root of the fixture) and the frame. stays and gone are paths to check after it.
type wrRow struct {
	base, target, root, want string
	stays, gone              []string
}

// git.worktree_remove with worktreeRoot when PATH holds no git, and the twins with git
// on PATH (89cb6289, Linux VM). Each row sends branchName w1.
//
// In the no-git rows where baseRepo exists, the answer is the work-tree refusal with
// the exec error alone. That answer comes before the trust check (row L13t). A
// baseRepo that does not exist skips the work-tree check. A worktree folder that
// holds a file, or is empty, then gets the lock-check text that names the repository.
// So does one whose `.git` file names an entry of the missing repository (row DG1).
// With git that folder is deleted, as on both references (row DG1-g). A gone one
// answers as it does with git. Nothing is deleted with no git.
//
// Mutations for a baseRepo that exists: drop the no-git answer, or give it after the
// trust check. Mutation for a missing baseRepo: answer the exec error.
func TestWorktreeRemoveExternalNoGitOnPath(t *testing.T) {
	const success = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	gitx := refusalFrame(t, wtUnknown+`exec: "git": executable file not found in $PATH`)
	lock := func(target, base string) string {
		return refusalFrame(t, "failed to remove worktree: could not check whether "+target+
			" is locked (the repository at "+base+" could not be read); retry")
	}
	nope := func(f *wrFixture) string { return filepath.Join(f.base, "nope") }
	strayX := func(t *testing.T, f *wrFixture) string {
		cd := filepath.Join(f.T, ".git", "commondir")
		wrWrite(t, cd, "x\n")
		return cd
	}
	emptyLeaf := func(t *testing.T, f *wrFixture) string {
		leaf := filepath.Join(f.R, "proj", "empty")
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		return leaf
	}
	// entryLeaf is a worktree folder whose `.git` file names an entry of the missing
	// baseRepo, with two files in it (rows DG1 and DG1-g).
	entryLeaf := func(t *testing.T, f *wrFixture) string {
		leaf := filepath.Join(f.R, "proj", "lx")
		wrWrite(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.Join(nope(f), ".git", "worktrees", "x")+"\n")
		wrWrite(t, filepath.Join(leaf, "a.txt"), "a\n")
		wrWrite(t, filepath.Join(leaf, "sub", "b.txt"), "b\n")
		return leaf
	}
	emptyRoot := func(t *testing.T, f *wrFixture) string {
		root := filepath.Join(f.base, "ext")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	unreachable := func(root string) string {
		return refusalFrame(t, "failed to remove worktree: the worktree location "+root+" is not reachable ("+root+
			" does not exist); nothing was removed — retry once it is available, or remove the worktree by hand")
	}
	for _, tc := range []struct {
		name  string
		noGit bool
		arm   func(t *testing.T, f *wrFixture) wrRow
	}{
		{"L13xc_repository", true, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: f.T, target: f.p0, want: gitx}
		}},
		{"L13xd_repository_registered_worktree_gone", true, func(t *testing.T, f *wrFixture) wrRow {
			if err := os.RemoveAll(f.e0); err != nil {
				t.Fatal(err)
			}
			return wrRow{base: f.T, target: f.e0, want: gitx}
		}},
		{"L13u_base_is_regular_file", true, func(t *testing.T, f *wrFixture) wrRow {
			file := filepath.Join(f.base, "F")
			wrWrite(t, file, "f\n")
			return wrRow{base: file, target: f.p0, want: gitx}
		}},
		{"L13v_mode0600_repository", true, func(t *testing.T, f *wrFixture) wrRow {
			if os.Geteuid() == 0 {
				t.Skip("root ignores the permission this test depends on")
			}
			if err := os.Chmod(f.T, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f.T, 0o755) })
			return wrRow{base: f.T, target: f.p0, want: gitx}
		}},
		{"L13y_git_file_to_nowhere", true, func(t *testing.T, f *wrFixture) wrRow {
			g := filepath.Join(f.base, "G")
			wrWrite(t, filepath.Join(g, ".git"), "gitdir: "+filepath.Join(f.base, "nowhere")+"\n")
			return wrRow{base: g, target: f.p0, want: gitx}
		}},
		{"L13z_plain_folder", true, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: wrPlainFolder(t, f), target: f.p0, want: gitx}
		}},
		{"L13z-g_plain_folder_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: wrPlainFolder(t, f), target: f.p0, want: refusalFrame(t, wtUnknown+"exit status 128")}
		}},
		{"L13t_refused_git_directory", true, func(t *testing.T, f *wrFixture) wrRow {
			strayX(t, f)
			return wrRow{base: f.T, target: f.p0, want: gitx}
		}},
		{"L13t-g_refused_git_directory_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: f.T, target: f.p0, want: refusalFrame(t, strayText(wantTR(strayX(t, f), "x")))}
		}},
		{"L13wa_base_missing_worktree_holds_a_file", true, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: nope(f), target: f.p0, want: lock(f.p0, nope(f))}
		}},
		{"L13wa-g_base_missing_worktree_holds_a_file_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: nope(f), target: f.p0, want: refusalFrame(t, "refusing to remove worktree: "+f.p0+
				" is not a worktree of "+nope(f)+" ("+f.p0+" has no .git file), so it is left in place; remove it by hand if it is a leftover")}
		}},
		{"L13we_base_missing_worktree_empty", true, func(t *testing.T, f *wrFixture) wrRow {
			leaf := emptyLeaf(t, f)
			return wrRow{base: nope(f), target: leaf, want: lock(leaf, nope(f)), stays: []string{leaf}}
		}},
		{"L13we-g_base_missing_worktree_empty_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			leaf := emptyLeaf(t, f)
			return wrRow{base: nope(f), target: leaf, want: success, gone: []string{leaf}}
		}},
		{"DG1_base_missing_worktree_names_its_entry", true, func(t *testing.T, f *wrFixture) wrRow {
			leaf := entryLeaf(t, f)
			return wrRow{base: nope(f), target: leaf, want: lock(leaf, nope(f)),
				stays: []string{filepath.Join(leaf, ".git"), filepath.Join(leaf, "a.txt"), filepath.Join(leaf, "sub", "b.txt")}}
		}},
		{"DG1-g_base_missing_worktree_names_its_entry_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			leaf := entryLeaf(t, f)
			return wrRow{base: nope(f), target: leaf, want: success, gone: []string{leaf}}
		}},
		{"L13wb_base_missing_worktree_gone", true, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: nope(f), target: filepath.Join(f.R, "proj", "gone"), want: success}
		}},
		{"L13wb-g_base_missing_worktree_gone_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			return wrRow{base: nope(f), target: filepath.Join(f.R, "proj", "gone"), want: success}
		}},
		{"L13wc_base_missing_root_empty", true, func(t *testing.T, f *wrFixture) wrRow {
			root := emptyRoot(t, f)
			return wrRow{base: nope(f), target: filepath.Join(root, "cp", "w1"), root: root, want: success}
		}},
		{"L13wc-g_base_missing_root_empty_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			root := emptyRoot(t, f)
			return wrRow{base: nope(f), target: filepath.Join(root, "cp", "w1"), root: root, want: success}
		}},
		{"L13wr_base_missing_root_missing", true, func(t *testing.T, f *wrFixture) wrRow {
			root := filepath.Join(f.base, "ext")
			return wrRow{base: nope(f), target: filepath.Join(root, "cp", "w1"), root: root, want: unreachable(root)}
		}},
		{"L13wr-g_base_missing_root_missing_with_git", false, func(t *testing.T, f *wrFixture) wrRow {
			root := filepath.Join(f.base, "ext")
			return wrRow{base: nope(f), target: filepath.Join(root, "cp", "w1"), root: root, want: unreachable(root)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWRFixture(t)
			row := tc.arm(t, f)
			if row.root == "" {
				row.root = f.R
			}
			resetUserExcludesCache(t)
			if tc.noGit {
				t.Setenv("PATH", t.TempDir())
			}
			got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
				"baseRepo": row.base, "worktreePath": row.target, "branchName": "w1", "worktreeRoot": row.root}))
			if got != row.want {
				t.Errorf("got  %s\nwant %s", got, row.want)
			}
			for _, p := range append(row.stays, filepath.Join(f.p0, "keep.txt")) {
				if _, err := os.Lstat(p); err != nil {
					t.Errorf("%s was touched: %v", p, err)
				}
			}
			for _, p := range row.gone {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s is still there", p)
				}
			}
		})
	}
}

// wrPlainFolder makes a folder with no .git beside the repositories of f.
func wrPlainFolder(t *testing.T, f *wrFixture) string {
	t.Helper()
	n := filepath.Join(f.base, "N")
	if err := os.MkdirAll(n, 0o755); err != nil {
		t.Fatal(err)
	}
	return n
}

// With git on PATH, a baseRepo that does not exist runs no `git version`. 89cb6289
// runs only the one-time excludes read there (rows L13wa-g and L13wb-g on a Linux
// VM). Mutation: run the configuration check for a baseRepo that does not exist.
func TestWorktreeRemoveExternalMissingBaseRunsNoGitVersion(t *testing.T) {
	f := newListingFixture(t)
	root := filepath.Join(filepath.Dir(f.top), "ext")
	leaf := filepath.Join(root, "cp", "w1")
	wrWrite(t, filepath.Join(leaf, "keep.txt"), "k\n")
	nope := filepath.Join(filepath.Dir(f.top), "nope")
	raw, calls := f.call(t, "git.worktree_remove", map[string]any{
		"baseRepo": nope, "worktreePath": leaf, "branchName": "w1", "worktreeRoot": root})
	if !strings.Contains(raw, "is not a worktree of "+nope) {
		t.Fatalf("remove = %s, want the not-a-worktree refusal", raw)
	}
	for _, c := range calls {
		if c.version() {
			t.Errorf("git version ran in %s", c.cwd)
		}
	}
}

// wrSymlinkChain makes n relative symlinks in the folder of target. Link 1 names
// target, and each later link names the one before it. It returns the last link.
func wrSymlinkChain(t *testing.T, target string, n int) string {
	t.Helper()
	dir, name := filepath.Dir(target), filepath.Base(target)
	for i := 1; i <= n; i++ {
		link := "l" + strconv.Itoa(i)
		if err := os.Symlink(name, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
		name = link
	}
	return filepath.Join(dir, name)
}

// Rows LNKb, LNKr and their twins with git (89cb6289 and f6010b97, Linux VM).
// baseRepo is the head of a chain of 45 relative symlinks to a repository. The kernel does not
// follow that chain, so os.Stat fails, but the trust check resolves it. The removal is
// refused, and the registered worktree and its entry stay. With no git on PATH the
// reason is the exec error. With git it is the hooks refusal with the chdir error.
//
// Mutations: skip the work-tree check for every baseRepo that fails os.Stat. The
// removal then deletes the worktree of the bare repository. Another mutation leaves
// the chain to the later checks. The fixture lives in the test's temporary directory
// only.
func TestWorktreeRemoveExternalDeepSymlinkChainBase(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bare, noGit bool
	}{
		{"LNKb_bare_no_git", true, true},
		{"LNKb-g_bare_with_git", true, false},
		{"LNKr_work_tree_no_git", false, true},
		{"LNKr-g_work_tree_with_git", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWRFixture(t)
			repo, target, branch := f.T, f.e0, "e0"
			entry := filepath.Join(f.T, ".git", "worktrees", "e0")
			if tc.bare {
				repo = filepath.Join(f.base, "B.git")
				runGit(t, f.base, "clone", "-q", "--bare", f.T, repo)
				target, branch = filepath.Join(f.R, "proj", "w1"), "w1"
				runGit(t, repo, "worktree", "add", "-q", target, "-b", branch)
				entry = filepath.Join(repo, "worktrees", "w1")
			}
			head := wrSymlinkChain(t, repo, 45)
			if _, err := os.Stat(head); !errors.Is(err, syscall.ELOOP) {
				// Linux follows 40 links and macOS 32, so the chain fails there.
				if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
					t.Fatalf("stat of a chain of 45 symlinks = %v, want ELOOP", err)
				}
				t.Skipf("stat of a chain of 45 symlinks = %v, want ELOOP on this host", err)
			}
			want := refusalFrame(t, wtUnknown+"config-defined hooks could not be pinned off; git not run: "+
				"listing the configuration in force: chdir "+head+": too many levels of symbolic links")
			resetUserExcludesCache(t)
			if tc.noGit {
				t.Setenv("PATH", t.TempDir())
				want = refusalFrame(t, wtUnknown+`exec: "git": executable file not found in $PATH`)
			}
			if got := f.remove(t, head, target, branch); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			for _, p := range []string{filepath.Join(target, ".git"), entry} {
				if _, err := os.Lstat(p); err != nil {
					t.Errorf("%s is gone (%v), but a refusal deletes nothing", p, err)
				}
			}
		})
	}
}

// Row DG2s-g (89cb6289, Linux VM): the repository has a good HEAD, and its
// `.git/commondir` is a dangling relative symlink that stays in `.git`. The trust check
// passes it, and git then fails each `worktree list`. The removal is refused, and the
// worktree, its `.git` file and its entry stay. The fixture lives in the test's
// temporary directory only. Mutation: go on after the failed `worktree list -z`. The
// removal then deletes the worktree and its entry.
func TestWorktreeRemoveExternalWorktreeListFails(t *testing.T) {
	f := newWRFixture(t)
	if err := os.Symlink("nothing-here", filepath.Join(f.T, ".git", "commondir")); err != nil {
		t.Fatal(err)
	}
	want := refusalFrame(t, "failed to remove worktree: cannot list the repository's worktrees: exit status 128")
	if got := f.remove(t, f.T, f.e0, "e0"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.kept(t, f.e0, "e0")
	if _, err := os.Lstat(filepath.Join(f.e0, ".git")); err != nil {
		t.Errorf("the .git file of the worktree is gone: %v", err)
	}
}

// Row DG2s-g, the git calls. After the failed `worktree list --porcelain -z`, the
// daemon runs one more configuration listing. Then it runs `worktree list --porcelain`
// with no -z. No git call follows the second failure. Mutation: answer after the
// first failed call.
func TestWorktreeRemoveExternalWorktreeListSecondCall(t *testing.T) {
	f := newListingFixture(t)
	root := filepath.Join(filepath.Dir(f.top), "ext")
	leaf := filepath.Join(root, "cp", "w1")
	runGit(t, f.top, "worktree", "add", "-q", leaf, "-b", "w1")
	if err := os.Symlink("nothing-here", filepath.Join(f.top, ".git", "commondir")); err != nil {
		t.Fatal(err)
	}
	raw, calls := f.call(t, "git.worktree_remove", map[string]any{
		"baseRepo": f.top, "worktreePath": leaf, "branchName": "w1", "worktreeRoot": root})
	if !strings.Contains(raw, "cannot list the repository's worktrees: exit status 128") {
		t.Fatalf("remove = %s, want the worktree-list refusal", raw)
	}
	n := len(calls)
	if n < 3 || !argvEndsWith(calls[n-3].argv, []string{"worktree", "list", "--porcelain", "-z"}) ||
		!calls[n-2].listing() || !argvEndsWith(calls[n-1].argv, []string{"worktree", "list", "--porcelain"}) {
		t.Errorf("calls end with %q, want worktree list -z, a listing, worktree list", tailArgv(calls, 3))
	}
	if _, err := os.Lstat(filepath.Join(leaf, ".git")); err != nil {
		t.Errorf("the worktree is gone: %v", err)
	}
}

// tailArgv is the argv of the last n calls.
func tailArgv(calls []envCall, n int) [][]string {
	var out [][]string
	for _, c := range calls[max(0, len(calls)-n):] {
		out = append(out, c.argv)
	}
	return out
}

// Rows DG2-g, DG2-info, DG2-status, DG2h-g and DG2i-g (89cb6289, Linux VM). The git
// directory has a HEAD that reads "garbage". With a dangling relative commondir symlink
// that stays in `.git`, the trust check refuses it with the shape text. That holds on
// a remove with worktreeRoot, on git.info and on git.status. A remove without worktreeRoot
// keeps the lock-check text. With the bad HEAD alone, the remove with worktreeRoot
// answers "exit status 128". Nothing is deleted. Mutation: trust the dangling symlink
// before the git-directory test.
func TestGitDirTrustDanglingCommondirWithBadHead(t *testing.T) {
	arm := func(t *testing.T, link bool) (f *wrFixture, inRepo, shape string) {
		f = newWRFixture(t)
		inRepo = filepath.Join(f.T, ".claude", "worktrees", "w1")
		runGit(t, f.T, "worktree", "add", "-q", inRepo, "-b", "w1")
		wrWrite(t, filepath.Join(f.T, ".git", "HEAD"), "garbage\n")
		cd := filepath.Join(f.T, ".git", "commondir")
		if link {
			if err := os.Symlink("nothing-here", cd); err != nil {
				t.Fatal(err)
			}
		}
		return f, inRepo, wantShapeRefusal(cd)
	}
	t.Run("DG2-g_remove_with_root", func(t *testing.T) {
		f, _, shape := arm(t, true)
		if got, want := f.remove(t, f.T, f.e0, "e0"), refusalFrame(t, wtUnknown+shape); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
	t.Run("DG2-info", func(t *testing.T) {
		f, _, shape := arm(t, true)
		wantRPCError(t, "info(T)", info(t, f.T), shape)
	})
	t.Run("DG2-status", func(t *testing.T) {
		f, _, shape := arm(t, true)
		wantRPCError(t, "status(T,T)", status(t, f.T, f.T), shape)
	})
	t.Run("DG2i-g_remove_in_repository", func(t *testing.T) {
		f, inRepo, _ := arm(t, true)
		got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": f.T, "worktreePath": inRepo, "branchName": "w1"}))
		if want := refusalFrame(t, wantLockCheck(inRepo)); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, inRepo, "w1")
	})
	t.Run("DG2h-g_bad_head_only", func(t *testing.T) {
		f, _, _ := arm(t, false)
		if got, want := f.remove(t, f.T, f.e0, "e0"), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e0, "e0")
	})
}
