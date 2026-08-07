//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// lockedWorktree builds a repo with a LOCKED worktree, the reachable case where
// `git worktree remove --force` refuses (exit 128) and leaves the directory.
func lockedWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	holder := filepath.Join(base, "holder")
	wt = filepath.Join(holder, "wt")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(holder, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "wtb", wt)
	run("worktree", "lock", wt)
	return repo, wt
}

// When git refuses the removal, the reference removes the directory itself and
// still answers {"success":true}. claustrum used to answer success:true while
// leaving the directory in place — the same reply for the opposite outcome.
// Measured at 5db5e4a.
func TestWorktreeRemoveFallsBackToManualCleanup(t *testing.T) {
	repo, wt := lockedWorktree(t)
	s := newTestServer(t)

	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:true", raw)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Error("the worktree directory survived — the manual cleanup fallback did not run")
	}
}

// When the manual cleanup ALSO fails, the reference reports it in the result's
// error field with success:false. Measured at 5db5e4a:
//
//	{"success":false,"error":"failed to remove worktree: fatal: cannot remove a
//	 locked working tree;\nuse 'remove -f -f' to override or unlock first;
//	 manual cleanup also failed: unlinkat <path>: permission denied"}
func TestWorktreeRemoveReportsWhenCleanupAlsoFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable")
	}
	repo, wt := lockedWorktree(t)
	holder := filepath.Dir(wt)
	if err := os.Chmod(holder, 0o500); err != nil { // cleanup cannot unlink wt
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(holder, 0o700) })

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))

	if !strings.Contains(raw, `"success":false`) {
		t.Errorf("reply = %s, want success:false", raw)
	}
	// Assert git's OWN message lands between the two halves, not just that both
	// halves are present. They are adjacent in the format string, so a regression
	// that fed %s an empty string — dropping git's output entirely, which is the
	// whole reason this reads combined output — would leave two separate
	// Contains checks green. Raised on review of #204.
	if !errRe.MatchString(raw) {
		t.Errorf("reply = %s, want git's own message between the two halves (%s)", raw, errRe)
	}
}

// errRe pins the shape "failed to remove worktree: <non-empty git output>;
// manual cleanup also failed: <non-empty err>". [^"]+ rather than .+ so it
// cannot run past the end of the JSON string value.
var errRe = regexp.MustCompile(
	`failed to remove worktree: [^"]*fatal:[^"]+; manual cleanup also failed: [^"]+`)

// OUR gitTimeout must not authorise the destructive fallback.
//
// gitTimeout is a claustrum-only divergence: the reference showed no deadline at
// or below the 75 s probed and simply blocks, so it never reaches a delete on
// this path. Before the fix a wedged git produced exactly what the reference cannot — the
// directory removed and a bare {"success":true} — which turns a safety measure
// into data loss.
//
// The fixture is a stub `git` on PATH that sleeps, with gitTimeout shrunk. It is
// unix-only because it relies on a shell stub and an executable bit.
func TestWorktreeRemoveTimeoutDoesNotDelete(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"),
		[]byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := gitTimeout
	gitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })

	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(wt, "KEEP.txt")
	if err := os.WriteFile(keep, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": root, "worktreePath": wt}))

	// The surviving file is the assertion that matters; the reply is secondary.
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a timed-out git deleted the worktree directory: %v", err)
	}
	if !strings.Contains(raw, "timed out") {
		t.Errorf("reply = %s, want it to report the timeout rather than claim success", raw)
	}
	if strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:false — the removal did not complete", raw)
	}
	// The reply must not assert a filesystem fact the daemon cannot observe: the
	// SIGKILLed git unlinks as it goes, so "nothing was removed" would be a claim
	// about a directory state nobody checked.
	if strings.Contains(raw, "nothing was removed") {
		t.Errorf("reply = %s, asserts a filesystem fact the daemon cannot know", raw)
	}
	if !strings.Contains(raw, "no cleanup was attempted") {
		t.Errorf("reply = %s, want it to say what the daemon actually knows", raw)
	}
}
