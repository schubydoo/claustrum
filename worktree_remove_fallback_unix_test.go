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
	// 7d193f89 confines worktrees to inside the repo, so the holder is under
	// .claude/worktrees.
	holder := filepath.Join(repo, ".claude", "worktrees")
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

// 7d193f89 REFUSES a locked worktree removal — success:false with a fixed message
// (independent of the lock reason), leaving the directory in place. Before 7d193f89
// the reference DELETED it and answered success:true; that older behavior is the
// wire divergence this reconciles. Measured against 7d193f89 on an ephemeral VM,
// both binaries returning byte-identical frames.
func TestWorktreeRemoveLockedWorktreeIsRefused(t *testing.T) {
	repo, wt := lockedWorktree(t)
	s := newTestServer(t)

	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	const want = "is locked (git worktree lock); unlock it to remove it"
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, want) {
		t.Errorf("reply = %s, want success:false with the locked refusal", raw)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the locked worktree was deleted (%v); 7d193f89 leaves it in place", err)
	}
}

// A git failure that is NOT a lock still reaches the os.RemoveAll fallback and
// answers success:true: an ordinary non-worktree directory inside the repo is
// deleted. 7d193f89 does the same (measured on an ephemeral VM) — only the locked
// case is refused — so this pins that the fallback survives the locked-refusal
// change and did not accidentally start refusing every git failure.
func TestWorktreeRemoveDeletesNonWorktreeDir(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	nd := filepath.Join(repo, ".claude", "worktrees", "nd") // never a worktree
	writeFile(t, filepath.Join(nd, "KEEP.txt"), "x\n", 0o600)

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": nd}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:true", raw)
	}
	if _, err := os.Stat(nd); err == nil {
		t.Error("the non-worktree directory survived — the fallback delete did not run")
	}
}

// A non-worktree directory whose PATH contains "locked working tree" must still be
// deleted (success:true), not misclassified as a locked worktree. git echoes the
// path in its "'<p>' is not a working tree" error, so a bare "locked working tree"
// substring match would wrongly refuse it; the detection anchors on git's full
// "cannot remove a locked working tree" phrase instead. Raised by wire-byte review.
func TestWorktreeRemoveNonWorktreeDirNamedLikeLock(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	nd := filepath.Join(repo, ".claude", "worktrees", "locked working tree")
	writeFile(t, filepath.Join(nd, "KEEP.txt"), "x\n", 0o600)

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": nd}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:true (path-name collision must not read as locked)", raw)
	}
	if _, err := os.Stat(nd); err == nil {
		t.Error("the directory survived — a path-name collision was treated as a locked worktree")
	}
}

// 7d193f89 refuses a worktree whose path crosses a symlinked component under the
// repo (.claude / .claude/worktrees or below) — a planted link could carry a
// create outside the repo or point the remove fallback's os.RemoveAll at an
// external target. Create carries errorCode "symlinked_component"; remove carries
// none. Measured against the reference on an ephemeral VM.
func TestWorktreeSymlinkedComponentRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	// .claude/worktrees is a symlink escaping the repo — a planted link.
	if err := os.Symlink(base, filepath.Join(repo, ".claude", "worktrees")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	const want = "is a symbolic link; a symlinked .claude or .claude/worktrees inside the " +
		"repository is not supported for SSH sessions, because a repository can plant such a " +
		"link and a planted one cannot reliably be told apart from your own. Replace it with a " +
		"real directory (or delete it and it will be recreated)"

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wt}))
	if !strings.Contains(got, want) || !strings.Contains(got, `"errorCode":"symlinked_component"`) {
		t.Errorf("create = %s, want the symlinked_component refusal", got)
	}
	got = dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(got, want) || strings.Contains(got, `"errorCode"`) {
		t.Errorf("remove = %s, want the symlinked_component refusal with no errorCode", got)
	}
}

// When the manual cleanup ALSO fails, the reference reports it in the result's
// error field with success:false. The reachable case at 7d193f89 is a NON-worktree
// directory (a locked worktree is refused earlier, before any cleanup runs): git
// fails "not a working tree", the os.RemoveAll fallback runs, and an unwritable
// parent makes that fallback fail too. Measured shape:
//
//	{"success":false,"error":"failed to remove worktree: fatal: '<p>' is not a
//	 working tree; manual cleanup also failed: unlinkat <path>: permission denied"}
func TestWorktreeRemoveReportsWhenCleanupAlsoFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable")
	}
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	holder := filepath.Join(repo, ".claude", "worktrees")
	nd := filepath.Join(holder, "nd") // never a worktree
	if err := os.MkdirAll(nd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(holder, 0o500); err != nil { // cleanup cannot unlink nd
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(holder, 0o700) })

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": nd}))

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
	// Sleep on every git command EXCEPT the hook-config enumeration 7d193f89 runs
	// before each one (`git config …`), which must succeed fast, else the
	// hostile-config gate would time out before the removal under test.
	if err := os.WriteFile(filepath.Join(bin, "git"),
		[]byte("#!/bin/sh\ncase \"$*\" in *config*) exit 0 ;; *) exec sleep 30 ;; esac\n"), 0o755); err != nil {
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
