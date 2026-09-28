package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The git.worktree_remove rows measured side by side against f6010b97 on a macOS VM.
// Each test names its rows. Every fixture lives under t.TempDir().

// rmFixture is a repository with one registered worktree, wt1 on branch wt1, under
// <repo>/.claude/worktrees.
type rmFixture struct {
	repo string
	wt   string
}

func (f rmFixture) entry() string { return filepath.Join(f.repo, ".git", "worktrees", "wt1") }

func (f rmFixture) hasBranch(name string) bool {
	return gitExitOK(f.repo, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
}

// newRmFixture builds the fixture under a resolved temp root, so that the spelling of
// the repository in the texts is the one the daemon resolves (macOS /var).
func newRmFixture(t *testing.T) rmFixture {
	t.Helper()
	requireGit(t)
	repo := filepath.Join(resolveTestRoot(t, t.TempDir()), "repo")
	copyFixtureTemplate(t, "worktree-remove", repo, func(t *testing.T, dir string) {
		runGit(t, dir, "init", "-q", "-b", "main")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	})
	wt := filepath.Join(repo, ".claude", "worktrees", "wt1")
	runGit(t, repo, "worktree", "add", "-q", "-b", "wt1", wt)
	return rmFixture{repo: repo, wt: wt}
}

// removeFrame sends one git.worktree_remove and returns the raw frame.
func removeFrame(t *testing.T, params map[string]any) string {
	t.Helper()
	return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", params))
}

const removeOK = `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`

// removeErrorField decodes a git.worktree_remove frame and returns result.error.
func removeErrorField(t *testing.T, raw string) string {
	t.Helper()
	var resp struct {
		Result struct {
			Error string `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return resp.Result.Error
}

func wantRemoveError(t *testing.T, raw, want string) {
	t.Helper()
	if got := removeErrorField(t, raw); got != want || !strings.Contains(raw, `"success":false`) {
		t.Errorf("reply = %s\nwant error %q", raw, want)
	}
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s is gone (%v), want it kept", p, err)
	}
}

func mustBeGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s is still there (err=%v), want it deleted", p, err)
	}
}

// A leaf that is a symbolic link is refused, and nothing is deleted. Before, a link
// that pointed at a sibling worktree made the removal delete that sibling, its entry
// and its branch (rows S01 and S05).
func TestWorktreeRemoveSymlinkLeafIsRefused(t *testing.T) {
	f := newRmFixture(t)
	lnk := filepath.Join(filepath.Dir(f.wt), "lnk")
	if err := os.Symlink(f.wt, lnk); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": lnk, "branchName": "wt1"})
	wantRemoveError(t, raw, "refusing to remove worktree: "+lnk+" is a symbolic link, not a worktree directory")
	mustExist(t, lnk)
	mustExist(t, filepath.Join(f.wt, ".git"))
	mustExist(t, f.entry())
	if !f.hasBranch("wt1") {
		t.Error("branch wt1 was deleted")
	}
}

// A leaf that is a regular file is refused, and nothing is deleted (row S02).
func TestWorktreeRemoveFileLeafIsRefused(t *testing.T) {
	f := newRmFixture(t)
	runGit(t, f.repo, "branch", "f0")
	leaf := filepath.Join(filepath.Dir(f.wt), "f0")
	writeFile(t, leaf, "x\n", 0o644)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "f0"})
	wantRemoveError(t, raw, "refusing to remove worktree: "+leaf+" is not a directory")
	mustExist(t, leaf)
	if !f.hasBranch("f0") {
		t.Error("branch f0 was deleted")
	}
}

// A branchName that starts with "-" or "+" is not deleted. The removal still answers
// success. A plain name is deleted (rows B01, B02, B03 and B05).
func TestWorktreeRemoveSkipsDashAndPlusBranch(t *testing.T) {
	for _, name := range []string{"-x", "+x"} {
		t.Run(name, func(t *testing.T) {
			f := newRmFixture(t)
			runGit(t, f.repo, "update-ref", "refs/heads/"+name, "HEAD")
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": name})
			if raw != removeOK {
				t.Errorf("reply = %s, want %s", raw, removeOK)
			}
			mustBeGone(t, f.wt)
			if !f.hasBranch(name) {
				t.Errorf("refs/heads/%s was deleted, want it kept", name)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		f := newRmFixture(t)
		runGit(t, f.repo, "branch", "x")
		if raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "x"}); raw != removeOK {
			t.Errorf("reply = %s, want %s", raw, removeOK)
		}
		if f.hasBranch("x") {
			t.Error("refs/heads/x is still there")
		}
	})
}

// A worktree whose directory is gone, with a locked registration, is refused with its
// own text. The entry and the branch stay (row G01). An unlocked one drops its entry
// by the recorded path and deletes the branch (row G03).
func TestWorktreeRemoveGoneWorktree(t *testing.T) {
	t.Run("locked", func(t *testing.T) {
		f := newRmFixture(t)
		runGit(t, f.repo, "worktree", "lock", f.wt)
		if err := os.RemoveAll(f.wt); err != nil {
			t.Fatal(err)
		}
		raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
		wantRemoveError(t, raw, "refusing to remove worktree: "+f.wt+" is gone but its registration is "+
			"locked (git worktree lock); unlock it to remove the registration and branch")
		mustExist(t, filepath.Join(f.entry(), "locked"))
		if !f.hasBranch("wt1") {
			t.Error("branch wt1 was deleted")
		}
	})
	t.Run("unlocked", func(t *testing.T) {
		f := newRmFixture(t)
		if err := os.RemoveAll(f.wt); err != nil {
			t.Fatal(err)
		}
		raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
		if raw != removeOK {
			t.Errorf("reply = %s, want %s", raw, removeOK)
		}
		mustBeGone(t, f.entry())
		mustExist(t, filepath.Dir(f.entry()))
		if f.hasBranch("wt1") {
			t.Error("branch wt1 is still there")
		}
	})
}

// A gone worktree under a baseRepo that holds no repository answers success. There are
// no registrations to check (rows G05 and G05b). Before, the lock-check text answered.
func TestWorktreeRemoveGoneWithoutRepository(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".claude", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	wp := filepath.Join(base, ".claude", "worktrees", "gone")
	if raw := removeFrame(t, map[string]any{"baseRepo": base, "worktreePath": wp, "branchName": "gone"}); raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
}

// A gone worktree under a repository whose config cannot be listed, or whose git
// directory fails the trust check, is refused with the reason in the lock-check text
// (rows G07 and T02). A present worktree answers the generic text (row G08).
func TestWorktreeRemoveGoneHostileRepository(t *testing.T) {
	t.Run("bad config", func(t *testing.T) {
		f := newRmFixture(t)
		cfg := filepath.Join(f.repo, ".git", "config")
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, cfg, string(b)+"[[[broken\n", 0o644)
		wp := filepath.Join(filepath.Dir(f.wt), "gone")
		got := removeErrorField(t, removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": wp}))
		prefix := "failed to remove worktree: could not check whether " + wp + " is locked (" + hooksRefusalPrefix + "exit status 128: "
		if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "); retry") {
			t.Errorf("error = %q\nwant %q…); retry", got, prefix)
		}
		wantRemoveError(t, removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt}), lockCheckRefusal(f.wt))
		mustExist(t, f.wt)
	})
	t.Run("stray commondir", func(t *testing.T) {
		f := newRmFixture(t)
		writeFile(t, filepath.Join(f.repo, ".git", "commondir"), ".\n", 0o644)
		wp := filepath.Join(filepath.Dir(f.wt), "gone")
		got := removeErrorField(t, removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": wp}))
		want := "failed to remove worktree: could not check whether " + wp + " is locked (" +
			refuseStrayCommondir(filepath.Join(f.repo, ".git", "commondir")).refusal + "); retry"
		if got != want {
			t.Errorf("error = %q\nwant  %q", got, want)
		}
	})
}

// A worktree whose `.git` file is damaged still has its entry dropped, found by the
// recorded path (row R01). Two entries that record the same path are both kept (row
// R03). Before, the entry stayed in the first case.
func TestWorktreeRemoveDropsEntryByRecordedPath(t *testing.T) {
	t.Run("one record", func(t *testing.T) {
		f := newRmFixture(t)
		writeFile(t, filepath.Join(f.wt, ".git"), "garbage\n", 0o644)
		if raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"}); raw != removeOK {
			t.Errorf("reply = %s, want %s", raw, removeOK)
		}
		mustBeGone(t, f.wt)
		mustBeGone(t, f.entry())
		if f.hasBranch("wt1") {
			t.Error("branch wt1 is still there")
		}
	})
	t.Run("two records", func(t *testing.T) {
		f := newRmFixture(t)
		dup := f.entry() + "dup"
		if err := os.CopyFS(dup, os.DirFS(f.entry())); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.wt, ".git"), "garbage\n", 0o644)
		if raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt}); raw != removeOK {
			t.Errorf("reply = %s, want %s", raw, removeOK)
		}
		mustBeGone(t, f.wt)
		mustExist(t, f.entry())
		mustExist(t, dup)
	})
}

// A `locked` marker that is a dangling symbolic link still locks the worktree (row
// L04). Before, only a marker that resolved to a file counted.
func TestWorktreeRemoveDanglingLockMarker(t *testing.T) {
	f := newRmFixture(t)
	if err := os.Symlink(filepath.Join(f.repo, "nowhere"), filepath.Join(f.entry(), "locked")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
	wantRemoveError(t, raw, lockedWorktreeRefusal(f.wt))
	mustExist(t, filepath.Join(f.wt, ".git"))
	if !f.hasBranch("wt1") {
		t.Error("branch wt1 was deleted")
	}
}

// A baseRepo under a managed worktrees directory that does not exist skips the
// managed-worktrees refusal (row M01).
func TestWorktreeRemoveMissingManagedBase(t *testing.T) {
	top := t.TempDir()
	base := filepath.Join(top, ".claude", "worktrees", "nb")
	wp := filepath.Join(base, ".claude", "worktrees", "w")
	if raw := removeFrame(t, map[string]any{"baseRepo": base, "worktreePath": wp}); raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
}

// checkLeafIdentity refuses a leaf handle that is no longer the directory at the name.
func TestCheckLeafIdentityDetectsReplacement(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	b, err := parent.OpenRoot("b")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if err := checkLeafIdentity(parent, "b", b, "T"); err != nil {
		t.Errorf("same directory = %v, want nil", err)
	}
	err = checkLeafIdentity(parent, "a", b, "T")
	if err == nil || worktreeRemoveText(err) != "refusing to remove worktree: T changed while the removal was checking it" {
		t.Errorf("other directory = %v, want the replaced refusal", err)
	}
	if err := checkLeafIdentity(parent, "missing", b, "T"); err == nil {
		t.Error("missing name = nil, want an error")
	}
}
