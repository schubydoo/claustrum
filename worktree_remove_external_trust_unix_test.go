//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func strayText(cd string) string {
	return wtUnknown + trustText + `"` + cd + `" exists where git itself never writes one ` +
		"(git keeps that file only in a linked worktree's entry under .git/worktrees/); " +
		"remove it if you did not create it, and treat its appearance as tampering"
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
		{"WR01_stray_commondir_dot", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			wrWrite(t, cd, ".\n")
			return f.T, f.e0, "e0", strayText(cd)
		}},
		{"WR02_stray_commondir_own_abs", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			wrWrite(t, cd, filepath.Join(f.T, ".git")+"\n")
			return f.T, f.e0, "e0", strayText(cd)
		}},
		{"WR03_stray_commondir_is_dir", func(t *testing.T, f *wrFixture) (string, string, string, string) {
			cd := filepath.Join(f.T, ".git", "commondir")
			if err := os.MkdirAll(cd, 0o755); err != nil {
				t.Fatal(err)
			}
			return f.T, f.e0, "e0", strayText(cd)
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
		wrWrite(t, cd, ".\n")
		if got, want := f.remove(t, f.T, f.p0, "p0"), refusalFrame(t, strayText(cd)); got != want {
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
// command runs.
func TestWorktreeRemoveExternalRepositoryCheckTimeoutRefuses(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(bin, "ran")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in config) exit 0 ;; rev-parse) exec sleep 30 ;; esac; done\n" +
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

// Row K16: a `.git` file that names a path that is gone and is not a worktree entry.
// git's configuration listing fails there, so the reason is the hooks refusal. The
// check also comes before the dir-symlink check.
func TestWorktreeRemoveExternalGitFileToNowhere(t *testing.T) {
	f := newWRFixture(t)
	g := filepath.Join(f.base, "G")
	nowhere := filepath.Join(f.base, "nowhere")
	wrWrite(t, filepath.Join(g, ".git"), "gitdir: "+nowhere+"\n")
	want := refusalFrame(t, wtUnknown+"config-defined hooks could not be pinned off; git not run: "+
		"listing the configuration in force: exit status 128: fatal: not a git repository: "+nowhere)
	if got := f.remove(t, g, f.p0, ""); got != want {
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
