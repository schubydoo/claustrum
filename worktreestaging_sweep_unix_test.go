//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pinStagingClock takes the time once and makes it the clock of the sweep
// until the test ends. It returns that time.
func pinStagingClock(t *testing.T) time.Time {
	t.Helper()
	now := time.Now()
	old := stagingClock
	t.Cleanup(func() { stagingClock = old })
	stagingClock = func() time.Time { return now }
	return now
}

// newSweepFixture is a shadow fixture in the state shadow with one old staging
// folder `.claude-new-worktree-old` in its git directory, and a pinned clock.
// It returns that folder and the time of the clock.
func newSweepFixture(t *testing.T) (shadowFixture, string, time.Time) {
	t.Helper()
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	now := pinStagingClock(t)
	return f, plantStagingEntryAt(t, f.gitDir, ".claude-new-worktree-old", now.Add(-2*time.Hour)), now
}

// createW1 runs one git.worktree_create of the branch w1 in the fixture.
func createW1(t *testing.T, f shadowFixture) string {
	t.Helper()
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	return raw
}

// TestCreateSweepsOldStagingEntries runs one git.worktree_create in the state
// shadow on Linux and macOS, with planted entries in the git directory. It
// pins the sweep that be1c1ca5 runs there after a good add:
//
//   - An old folder with a file goes as a tree, and an old link to a folder
//     goes as a link, and the target keeps its file (measured on Linux and
//     macOS).
//   - The age edge: an entry of 3590 s stays and one of 3610 s goes (measured
//     on Linux).
//   - `.claude-new-worktree-` alone goes. `.claude-new-worktreeX` (no dash) and
//     a bystander stay (measured on Linux).
func TestCreateSweepsOldStagingEntries(t *testing.T) {
	f, old, now := newSweepFixture(t)
	under := plantStagingEntryAt(t, f.gitDir, ".claude-new-worktree-a", now.Add(-3590*time.Second))
	over := plantStagingEntryAt(t, f.gitDir, ".claude-new-worktree-b", now.Add(-3610*time.Second))
	bare := plantStagingEntryAt(t, f.gitDir, ".claude-new-worktree-", now.Add(-2*time.Hour))
	noDash := plantStagingEntryAt(t, f.gitDir, ".claude-new-worktreeX", now.Add(-2*time.Hour))
	keep := plantStagingEntryAt(t, f.gitDir, "keep", now.Add(-2*time.Hour))
	target := filepath.Join(t.TempDir(), "t")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(target, "tfile"), "t\n")
	link := filepath.Join(f.gitDir, ".claude-new-worktree-lnk")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	at := unix.NsecToTimeval(now.Add(-2 * time.Hour).UnixNano())
	if err := unix.Lutimes(link, []unix.Timeval{at, at}); err != nil {
		t.Fatal(err)
	}

	if raw := createW1(t, f); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	for _, gone := range []string{old, over, bare, link} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it gone", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{
		filepath.Join(under, "f.txt"),
		filepath.Join(noDash, "f.txt"),
		filepath.Join(keep, "f.txt"),
		filepath.Join(target, "tfile"),
	} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s: %v, want it kept", kept, err)
		}
	}
}

// TestCreateSweepsNothingAfterAFailedAdd: the branch w1 exists, so `worktree
// add -b w1` fails, and the old staging entry stays (claustrum's own, not
// measured). Mutation: drop `err == nil` from the sweep test in worktreeAdd.
func TestCreateSweepsNothingAfterAFailedAdd(t *testing.T) {
	f, old, _ := newSweepFixture(t)
	runGit(t, f.repo, "branch", "w1")
	if raw := createW1(t, f); !strings.Contains(raw, `"success":false`) {
		t.Fatalf("git.worktree_create = %s, want a failed add", raw)
	}
	if _, err := os.Lstat(filepath.Join(old, "f.txt")); err != nil {
		t.Errorf("the old staging entry: %v, want it kept", err)
	}
}

// TestCreateSweepsNothingAfterASwap: at the step of the sweep the path of the
// git directory names a link to another folder. The held folder keeps its old
// staging entry (claustrum's own, not measured). Mutation: drop the swapped
// test from the sweep test in worktreeAdd.
func TestCreateSweepsNothingAfterASwap(t *testing.T) {
	f, old, _ := newSweepFixture(t)
	swapAt(t, "sweep", f.gitDir, t.TempDir())
	createW1(t, f)
	held := filepath.Join(f.gitDir+"-real", filepath.Base(old), "f.txt")
	if _, err := os.Lstat(held); err != nil {
		t.Errorf("the old staging entry of the held folder: %v, want it kept", err)
	}
}

// TestCreateSweepsNothingUnderTheOffSwitch: a request with "shadowDirOff":true
// starts between the gate of the create and its add. The add then names the
// git directory and no copy, and the old staging entry stays (claustrum's own,
// not measured). Mutation: drop `named` from the sweep test in worktreeAdd.
func TestCreateSweepsNothingUnderTheOffSwitch(t *testing.T) {
	f, old, _ := newSweepFixture(t)
	atStep(t, "stale", func() {
		f.s.offGitRequests.Add(1)
		t.Cleanup(func() { f.s.offGitRequests.Add(-1) })
	})
	if raw := createW1(t, f); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if _, err := os.Lstat(filepath.Join(old, "f.txt")); err != nil {
		t.Errorf("the old staging entry: %v, want it kept", err)
	}
}
