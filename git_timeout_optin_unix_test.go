//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubSlowGit puts a `git` on PATH that sleeps longer than the bound under test
// and then exits 0 with recognisable output.
//
// `exec sleep`, not `sleep`: without exec the shell survives the SIGKILL holding
// the output pipe, and CombinedOutput blocks on the pipe rather than returning at
// the deadline — which measures the pipe wait instead of the deadline and makes a
// killed git look like a slow one. That trap is recorded on gitTimeout itself.
func stubSlowGit(t *testing.T, sleep string) {
	t.Helper()
	bin := t.TempDir()
	// Sleep on every git command EXCEPT the hook-config enumeration 7d193f89 runs
	// before each one (`git config …`), which must succeed fast — otherwise the
	// hostile-config gate would time out before the git call under test.
	script := "#!/bin/sh\ncase \"$*\" in *config*) exit 0 ;; *) exec sleep " + sleep + " ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// D5's flip, stated as behaviour rather than as a number: with the bound OFF (the
// default) a slow git is waited for, and with it opted in the same git is killed.
//
// Both arms use the SAME stub, so the only thing that differs is gitTimeout. An
// arm that changed the stub as well would not discriminate — it could pass with
// the deadline permanently on or permanently off.
func TestGitTimeoutOptInKillsOnlyWhenSet(t *testing.T) {
	// ⚠️ The stub does NOT straddle the retracted 60 s default, and cannot: a 60 s
	// test is not worth the CI minute. So the OFF arm would pass under the old
	// default too — what discriminates is the explicit `gitTimeout = 0` assignment,
	// not the fixture. Do not read the 250 ms as evidence about the default; the
	// default is pinned separately, by TestGitTimeoutDefaultIsOff.
	// What this arm does prove is that at 0 no deadline fires at all: ok is true and
	// the process ran to completion rather than being killed.
	stubSlowGit(t, "0.25")

	old := gitTimeout
	t.Cleanup(func() { gitTimeout = old })

	t.Run("off by default: the slow git is waited for", func(t *testing.T) {
		gitTimeout = 0
		start := time.Now()
		_, ok := git(t.TempDir(), "status")
		if !ok {
			t.Errorf("git reported failure with the bound off; a deadline fired that should not exist")
		}
		if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
			t.Errorf("returned after %s, want >= the stub's own 250ms — it did not wait for git", elapsed)
		}
	})

	t.Run("opted in: the same git is killed", func(t *testing.T) {
		gitTimeout = 50 * time.Millisecond
		start := time.Now()
		_, ok := git(t.TempDir(), "status")
		if ok {
			t.Errorf("git reported success with a 50ms bound against a 250ms git; the deadline did not fire")
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("returned after %s, want well under the stub's 250ms — the kill did not happen at the deadline", elapsed)
		}
	})
}

// gitDeadline's third bit is what keeps a deadline from authorising the
// destructive worktree_remove fallback. With the bound off it must be false by
// construction, not merely false in practice.
//
// TestWorktreeRemoveTimeoutDoesNotDelete covers the opted-in half — that a
// deadline is NOT read as "git refused". This is its mirror.
func TestGitDeadlineReportsNoTimeoutWhenBoundIsOff(t *testing.T) {
	stubSlowGit(t, "0.25")
	old := gitTimeout
	gitTimeout = 0
	t.Cleanup(func() { gitTimeout = old })

	_, ok, timedOut := gitDeadline(t.TempDir(), "worktree", "remove")
	if timedOut {
		t.Errorf("timedOut = true with the bound off; the destructive fallback would be gated on a deadline that cannot exist")
	}
	if !ok {
		t.Errorf("ok = false; the stub exits 0, so the call should have succeeded")
	}
}

// The p.TimeoutMs>0 guard in gitWorktreeCreateLocked keeps a D5 (git-timeout)
// deadline that fires during the `worktree add` reporting worktree_add_failed —
// NOT the caller-facing errorCode "timeout", which is reserved for a caller-supplied
// timeoutMs (4534d86). Without the guard a D5-only kill is misclassified as a
// caller timeout. The stub is fast for the pre-checks and slow only on the add
// (matched by its unique --no-checkout arg), so D5 fires on the add specifically.
func TestWorktreeCreateD5DeadlineIsNotReportedAsTimeoutErrorCode(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in " +
		"*is-inside-work-tree*) echo true; exit 0 ;; " +
		"*abbrev-ref*) echo main; exit 0 ;; " +
		"*no-checkout*) exec sleep 30 ;; " +
		"*) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	old := gitTimeout
	gitTimeout = 100 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })

	base := t.TempDir()
	wt := filepath.Join(base, ".claude", "worktrees", "wt")
	s := newTestServer(t)
	// No timeoutMs in the request: the fired deadline is D5's, so the guard must
	// route it to worktree_add_failed, not errorCode "timeout".
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": base, "branchName": "b", "worktreePath": wt}))
	if strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Errorf("D5-only kill misreported as errorCode timeout: %s", raw)
	}
	if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Errorf("reply = %s, want errorCode worktree_add_failed", raw)
	}
}

// The reply text for an opted-in timeout quotes the configured duration, so it
// must reflect the value in force rather than a hardcoded 60s.
func TestWorktreeRemoveTimeoutMessageQuotesTheConfiguredBound(t *testing.T) {
	stubSlowGit(t, "30")
	old := gitTimeout
	gitTimeout = 120 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })

	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": root, "worktreePath": wt}))

	if !strings.Contains(raw, "120ms") {
		t.Errorf("reply = %s, want it to quote the configured 120ms bound", raw)
	}
	if strings.Contains(raw, "1m0s") || strings.Contains(raw, "60s") {
		t.Errorf("reply = %s, quotes the retracted 60s default rather than the value in force", raw)
	}
}

// A caller-supplied timeoutMs (4534d86) that fires while the read-tree checkout is
// still running yields the "during the checkout" timeout message, with git's own
// kill error appended and errorCode "timeout". The stub lets the pre-checks and the
// `worktree add` succeed fast (writing the linked `.git` so worktreeAdminDir resolves
// the admin dir), then sleeps on the read-tree so the per-request deadline kills it —
// exercising the second, post-add timeout arm the "before the checkout started" test
// never reaches. The sibling "after the checkout finished" arm is a near-unhittable
// race (read-tree returns clean in the microsecond window before the deadline) and is
// reproduced from the reference string rather than force-covered.
func TestWorktreeCreateTimeoutMsFiresDuringCheckout(t *testing.T) {
	bin := t.TempDir()
	// The `worktree add` case writes the worktree's `.git` back-pointer into its
	// (already-created) leaf dir — the last positional arg — so worktreeAdminDir
	// returns non-empty and the read-tree runs. read-tree then sleeps past the bound.
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$*\" in " +
		"*read-tree*) exec sleep 30 ;; " +
		"*is-inside-work-tree*) echo true; exit 0 ;; " +
		"*abbrev-ref*) echo main; exit 0 ;; " +
		"*--no-checkout*) printf 'gitdir: %s/.gitadmin\\n' \"$last\" > \"$last/.git\"; exit 0 ;; " +
		"*) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// D5 off: the caller's timeoutMs is the only deadline in force.
	old := gitTimeout
	gitTimeout = 0
	t.Cleanup(func() { gitTimeout = old })

	base := t.TempDir()
	wt := filepath.Join(base, ".claude", "worktrees", "wt")
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": base, "branchName": "b", "worktreePath": wt, "timeoutMs": 300}))

	if !strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Errorf("reply = %s, want errorCode timeout", raw)
	}
	if !strings.Contains(raw, "deadline expired during the checkout") {
		t.Errorf("reply = %s, want the during-the-checkout message", raw)
	}
	if !strings.Contains(raw, "git worktree add timed out after 300ms") {
		t.Errorf("reply = %s, want it to quote the 300ms bound", raw)
	}
}
