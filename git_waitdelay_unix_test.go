//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubLingeringGit puts this test binary on PATH as `git` (a symlink, so os.Executable()
// still resolves to the real binary) running in the "git-lingering" helper mode. exit0
// chooses whether the read-tree checkout git EXITS 0 while a descendant keeps holding the
// daemon's combined output pipe (the P1 git-exit-0 case) or blocks past the deadline (the
// killed-during-checkout case). orphanSecs is how long that descendant holds the pipe.
func stubLingeringGit(t *testing.T, exit0 bool, orphanSecs string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("symlink git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUSTRUM_TEST_HELPER", "git-lingering")
	t.Setenv("CLAUSTRUM_GITSTUB_ORPHAN", orphanSecs)
	if exit0 {
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT0", "1")
	} else {
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT0", "0")
	}
}

// runLingeringCreate dispatches one git.worktree_create against the stubbed git and
// returns the raw reply and the wall-clock the reply took.
func runLingeringCreate(t *testing.T, base string, timeoutMs int) (string, time.Duration) {
	t.Helper()
	wt := filepath.Join(base, ".claude", "worktrees", "wt")
	s := newTestServer(t)
	start := time.Now()
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "b", "worktreePath": wt, "timeoutMs": timeoutMs,
	}))
	return raw, time.Since(start)
}

// git.worktree_create must reproduce 4534d86's behaviour when the read-tree checkout
// leaves a descendant holding the daemon's combined output pipe: the reference caps the
// post-checkout drain at a FIXED ~5s from git's OWN exit (independent of timeoutMs),
// reaps the descendant, and only THEN gates the frame on
// timeoutMs — success (worktree kept) when timeoutMs exceeds the drain, else errorCode
// "timeout" ("after the checkout finished", NO "signal: killed") with the worktree rolled
// back (scratch/probe/wt-success-lingering-4534d86.md).
//
// Every stub git is a full re-exec of the (race-instrumented) test binary, so absolute
// wall-clock is unreliable. The test therefore MEASURES the per-command spawn cost first
// and expresses the deadline straddle relative to it: the add + checkout under the caller
// deadline are exactly four git spawns (two `git config` precursors + the add + the
// read-tree), i.e. twice a single `hardenedGit` call, so timeoutMs = that estimate + half
// the drain cap lands the deadline reliably inside the drain window (git exits before it,
// the drain overruns it) regardless of how fast or slow the host is. The orphan lifetime
// is far larger than the drain cap so a capped reply and an unbounded one (the pre-fix
// behaviour, and the mutant this test must catch) are cleanly separated.
func TestWorktreeCreateLingeringDescendant(t *testing.T) {
	const drainCap = 4 * time.Second
	const orphan = "30" // seconds; >> drainCap so an uncapped drain reads very differently

	oldCap := worktreeCreateDrainCap
	oldTimeout := gitTimeout
	t.Cleanup(func() {
		worktreeCreateDrainCap = oldCap
		gitTimeout = oldTimeout
	})
	worktreeCreateDrainCap = drainCap
	gitTimeout = 0 // D5 off: the caller timeoutMs is the only deadline

	// Measure one hardenedGit call = two spawns (its `git config` precursor + the command).
	// Warm the once-cached excludes probe first so it is not counted.
	stubLingeringGit(t, true, "0")
	warm := t.TempDir()
	hardenedGit(warm, false, "rev-parse", "--is-inside-work-tree")
	t0 := time.Now()
	hardenedGit(warm, false, "rev-parse", "--is-inside-work-tree")
	spawn2 := time.Since(t0)

	// The add + checkout under the deadline are four spawns ≈ 2 × spawn2.
	tgMs := int((2 * spawn2).Milliseconds())
	belowMs := tgMs + int(drainCap.Milliseconds())/2 // git exits first; the drain overruns it
	aboveMs := tgMs + 60000                          // clears the whole drain by a mile
	// A capped reply lands ~pre-checkout + drainCap; an uncapped one ~pre-checkout + 30s
	// orphan. pre-checkout + add + checkout is ~5 × spawn2 (ten spawns); allow the drain
	// cap plus a wide margin on top and the uncapped case still overshoots it by ~20s.
	boundedCeiling := 5*spawn2 + drainCap + 12*time.Second

	t.Run("git-exit-0, timeoutMs above the drain: success, worktree kept", func(t *testing.T) {
		stubLingeringGit(t, true, orphan)
		b := t.TempDir()
		raw, elapsed := runLingeringCreate(t, b, aboveMs)
		wt := filepath.Join(b, ".claude", "worktrees", "wt")

		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("reply = %s, want success:true (worktree kept)", raw)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("worktree %s should be kept, but Stat failed: %v", wt, err)
		}
		if elapsed >= boundedCeiling {
			t.Errorf("reply after %s (spawn2 %s); want ~pre-checkout+drainCap, well under the 30s orphan — the drain was not capped", elapsed, spawn2)
		}
	})

	t.Run("git-exit-0, timeoutMs below the drain: timeout after checkout, rolled back", func(t *testing.T) {
		stubLingeringGit(t, true, orphan)
		b := t.TempDir()
		raw, elapsed := runLingeringCreate(t, b, belowMs)
		wt := filepath.Join(b, ".claude", "worktrees", "wt")

		if !strings.Contains(raw, `"errorCode":"timeout"`) {
			t.Fatalf("reply = %s, want errorCode timeout", raw)
		}
		if !strings.Contains(raw, "deadline expired after the checkout finished") {
			t.Errorf("reply = %s, want the after-the-checkout-finished message", raw)
		}
		if strings.Contains(raw, "signal: killed") {
			t.Errorf("reply = %s, must NOT carry signal: killed — git exited 0", raw)
		}
		// Rolled back: the worktree directory is gone.
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("worktree %s should be rolled back (absent); Stat err = %v", wt, err)
		}
		if elapsed >= boundedCeiling {
			t.Errorf("reply after %s (spawn2 %s); want ~pre-checkout+drainCap, well under the 30s orphan — the drain was not capped", elapsed, spawn2)
		}
	})

	t.Run("killed during the checkout: signal killed", func(t *testing.T) {
		// read-tree BLOCKS (does not exit 0), so the deadline kills git mid-checkout.
		stubLingeringGit(t, false, orphan)
		b := t.TempDir()
		raw, elapsed := runLingeringCreate(t, b, belowMs)

		if !strings.Contains(raw, `"errorCode":"timeout"`) {
			t.Fatalf("reply = %s, want errorCode timeout", raw)
		}
		if !strings.Contains(raw, "deadline expired during the checkout") {
			t.Errorf("reply = %s, want the during-the-checkout message", raw)
		}
		if !strings.Contains(raw, "signal: killed") {
			t.Errorf("reply = %s, want git's own signal: killed suffix", raw)
		}
		// The group kill reaps the orphan, so the reply lands near the deadline, not at
		// the orphan's 30s lifetime.
		if elapsed >= boundedCeiling {
			t.Errorf("reply after %s (spawn2 %s); want near the deadline — the checkout was not bounded", elapsed, spawn2)
		}
	})
}
