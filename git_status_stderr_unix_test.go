//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git writes warnings to stderr while still succeeding on stdout. gitStdoutErr
// must read stdout ONLY: folding the streams together turns those warnings into
// porcelain entries, so a clean repo reports as dirty with the warning text as a
// "change".
//
// Measured against the reference at 5db5e4a with this exact fixture — an
// unreadable core.excludesFile: the reference answers {"isRepo":true,
// "clean":true} while claustrum returned the warning lines in changes[].
//
// Unix-only: the trigger is a 0000 directory, and Windows does not deny reads on
// one. Skipped under root for the same reason — root ignores the mode.
func TestGitStatusIgnoresGitStderr(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0000 directory is still readable, so the warning never fires")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")

	// 7d193f89 reports status for a session worktree of baseRepo; make one now,
	// before the excludes file is locked, so worktree add itself does not trip it.
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	run("worktree", "add", "-q", wt)

	// An unreadable excludes file makes git warn on stderr and still exit 0.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "ignore"), []byte("*.tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	run("config", "core.excludesFile", filepath.Join(locked, "ignore"))

	// Confirm the fixture actually provokes the warning; otherwise the assertion
	// below would pass for the wrong reason.
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if _, err := cmd.Output(); err != nil {
		t.Fatalf("fixture git status failed: %v", err)
	}
	if !strings.Contains(errBuf.String(), "warning:") {
		t.Skipf("this git build emits no warning for an unreadable excludesFile (stderr=%q)", errBuf.String())
	}

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	var got gitStatusResult
	decodeReply(t, []byte(raw), &got)

	if !got.IsRepo {
		t.Fatalf("isRepo = false, want true (raw %s)", raw)
	}
	if !got.Clean {
		t.Errorf("clean = false, want true — git's stderr leaked into the porcelain output: %q", got.Changes)
	}
	for _, c := range got.Changes {
		if strings.Contains(c, "warning:") {
			t.Errorf("changes contains a git stderr line: %q", c)
		}
	}
}
