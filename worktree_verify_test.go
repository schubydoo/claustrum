package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyCreatedWorktree is the post-`git worktree add` guard against a directory or
// ancestor swapped during the add. It is exercised at the function seam because no
// honest RPC input can reach a non-empty return (an unraced add preserves the leaf's
// identity). Each mutation below stands in for a distinct swap and maps to the
// reference's wording.
func TestVerifyCreatedWorktree(t *testing.T) {
	// An empty checkpoint (capture failed) must never invent a failure.
	if msg := verifyCreatedWorktree(filepath.Join(t.TempDir(), "gone"), worktreeCheckpoint{}); msg != "" {
		t.Errorf("empty checkpoint = %q, want no failure", msg)
	}

	// Checkpointing a path that does not exist captures nothing (a stat failure) — a
	// best-effort guard, never a way to fail an honest create.
	if cp := checkpointCreatedWorktree(filepath.Join(t.TempDir(), "absent")); cp.info != nil {
		t.Errorf("checkpoint of a missing path captured %v, want empty", cp.info)
	}

	base := t.TempDir()
	wt := filepath.Join(base, "d", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := checkpointCreatedWorktree(wt)
	defer cp.release()
	if cp.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}

	// Unchanged: the sound case an honest create always hits — passes.
	if msg := verifyCreatedWorktree(wt, cp); msg != "" {
		t.Errorf("sound worktree = %q, want pass", msg)
	}

	// The path no longer resolves (removed): "no longer leads to the directory".
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(wt, cp); !strings.Contains(msg, "no longer leads to the directory that was created for the worktree") {
		t.Errorf("removed = %q, want the no-longer-leads refusal", msg)
	}

	// The path resolves but is no longer a directory (a file took its place):
	// "is no longer the directory that was created".
	if err := os.WriteFile(wt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(wt, cp); !strings.Contains(msg, "is no longer the directory that was created for the worktree") {
		t.Errorf("file-at-path = %q, want the not-a-directory refusal", msg)
	}
	if err := os.Remove(wt); err != nil {
		t.Fatal(err)
	}

	// A directory with a DIFFERENT identity at the same path: "was not populated by git
	// worktree add". Two filesystem hazards make this sub-case OS-specific:
	//   - POSIX: a delete-then-recreate at the same path can reuse the freed inode, so
	//     os.SameFile reports the two identical (measured: flaky on the ubuntu CI runner).
	//     Creating the replacement ALONGSIDE the original (below) forces a distinct inode,
	//     making the check deterministic on POSIX rather than luck-of-the-allocator.
	//   - Windows: an identity from os.Stat of a path is read from the path only when
	//     os.SameFile runs, so a checkpoint taken that way read the swapped directory
	//     too, and the windows-latest runner reported the two as identical. The
	//     checkpoint now takes the identity from its held leaf handle, which reads it
	//     at once, so this sub-case runs on Windows too.
	orig := filepath.Join(base, "e", "wt")
	if err := os.MkdirAll(orig, 0o755); err != nil {
		t.Fatal(err)
	}
	origCP := checkpointCreatedWorktree(orig)
	defer origCP.release()
	if origCP.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}
	swap := filepath.Join(base, "e", "swap")
	if err := os.MkdirAll(swap, 0o755); err != nil { // coexists with orig → distinct identity
		t.Fatal(err)
	}
	if err := os.RemoveAll(orig); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, orig); err != nil { // orig's path now holds swap's identity
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(orig, origCP); !strings.Contains(msg, "was not populated by git worktree add") {
		t.Errorf("swapped dir = %q, want the not-populated refusal", msg)
	}
}

// fakeRegistration makes the two halves of a genuine worktree registration under
// repo: the leaf's `.git` pointer and the admin dir's `gitdir` record pointing back.
// No git runs. It returns the admin dir.
func fakeRegistration(t *testing.T, repo, wt string) string {
	t.Helper()
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	for _, d := range []string{wt, admin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return admin
}

// undoFailedAdd is the rollback of an add that git reported as failed. It runs no git
// call and removes the leaf only if the leaf is an empty directory (measured against
// f6010b97 and 90fca6e6). Every path handed to it here, including the stand-in HOME,
// is inside a t.TempDir().
func TestUndoFailedAdd(t *testing.T) {
	t.Run("removes_an_empty_leaf", func(t *testing.T) {
		wt := filepath.Join(t.TempDir(), "wt")
		if err := os.Mkdir(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		cp := checkpointCreatedWorktree(wt)
		defer cp.release()
		undoFailedAdd(wt, cp)
		if _, err := os.Lstat(wt); !os.IsNotExist(err) {
			t.Errorf("the empty leaf survived: Lstat err %v", err)
		}
	})

	// A failed add that left content keeps the leaf, the content and the registration.
	t.Run("keeps_a_leaf_with_content", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "repo")
		wt := filepath.Join(repo, ".claude", "worktrees", "wt")
		admin := fakeRegistration(t, repo, wt)
		cp := checkpointCreatedWorktree(wt)
		defer cp.release()
		undoFailedAdd(wt, cp)
		for _, p := range []string{filepath.Join(wt, ".git"), admin} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: %v, want it kept", p, err)
			}
		}
	})

	// The always-on home guard (D2). An empty home directory passes the rmdir, so
	// only the guard keeps it.
	t.Run("refuses_the_home_directory", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home")
		if err := os.Mkdir(home, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(homeEnvVar(), home)
		undoFailedAdd(home, worktreeCheckpoint{})
		if _, err := os.Stat(home); err != nil {
			t.Errorf("the rollback removed the home directory: %v", err)
		}
	})

	// The path no longer resolves to where the leaf was made (an ancestor swapped for
	// a symlink). The empty directory it leads to is left alone.
	t.Run("refuses_a_moved_path", func(t *testing.T) {
		wt := filepath.Join(t.TempDir(), "wt")
		if err := os.Mkdir(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		cp := checkpointCreatedWorktree(wt)
		defer cp.release()
		if cp.info == nil {
			t.Fatal("checkpoint did not capture the leaf")
		}
		cp.resolved = filepath.Join(filepath.Dir(cp.resolved), "elsewhere")
		undoFailedAdd(wt, cp)
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("the rollback removed a path that no longer resolves to the leaf: %v", err)
		}
	})
}

// undoFailedCheckout is the rollback after a successful add. The RPC tests drive its
// normal path. The guards are exercised here at the function seam, because a failed
// guard needs a swap during the call. branch is "", so no git runs.
func TestUndoFailedCheckout(t *testing.T) {
	t.Run("removes_the_leaf_and_the_registration", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "repo")
		wt := filepath.Join(repo, ".claude", "worktrees", "wt")
		admin := fakeRegistration(t, repo, wt)
		writeFile(t, filepath.Join(wt, "sub", "f.txt"), "f\n", 0o644)
		cp := checkpointCreatedWorktree(wt)
		defer cp.release()
		if got := undoFailedCheckout(repo, wt, "", cp); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		for _, p := range []string{admin, wt} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Errorf("%s survived the rollback (Lstat err %v)", p, err)
			}
		}
		if _, err := os.Stat(filepath.Dir(admin)); err != nil {
			t.Errorf("the registrations directory: %v, want it kept", err)
		}
	})

	// The always-on home guard (D2) stops both deletes of the leaf.
	t.Run("refuses_the_home_directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(homeEnvVar(), home)
		keep := filepath.Join(home, "KEEP.txt")
		if err := os.WriteFile(keep, []byte("must survive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := undoFailedCheckout(t.TempDir(), home, "", worktreeCheckpoint{}); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("the rollback deleted the home directory: %v", err)
		}
	})

	// The leaf identity is re-checked, so a swap during the call cannot redirect the
	// deletes onto a replacement. A file where the checkpointed directory was is such
	// a swap, and it must be left alone.
	t.Run("refuses_a_stale_checkpoint", func(t *testing.T) {
		base := t.TempDir()
		wt := filepath.Join(base, "wt")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		cp := checkpointCreatedWorktree(wt)
		defer cp.release()
		if cp.info == nil {
			t.Fatal("checkpoint did not capture the leaf")
		}
		if err := os.Remove(wt); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wt, []byte("not the directory that was created"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := undoFailedCheckout(base, wt, "", cp); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("the rollback deleted a path that no longer matched the checkpoint: %v", err)
		}
	})
}
