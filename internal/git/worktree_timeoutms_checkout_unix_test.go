//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A caller-supplied timeoutMs (4534d86) that fires while the read-tree checkout is
// still running yields the "during the checkout" timeout message, with git's own
// kill error appended and errorCode "timeout". The stub lets the pre-checks and the
// `worktree add` succeed fast (writing the linked `.git` so linkedGitDirOf resolves
// the admin dir), then marks the start of the read-tree and sleeps. The caller's
// deadline fires at that mark (deadlineAt), not at a time, so a slow runner still
// reaches the checkout first. That exercises the second, post-add timeout arm,
// which the "before the checkout started" test never reaches. The measured "after the checkout finished" path (a descendant holds the pipe past
// the drain cap) is force-covered by TestWorktreeCreateLingeringDescendant in
// git_waitdelay_unix_test.go.
func TestWorktreeCreateTimeoutMsFiresDuringCheckout(t *testing.T) {
	bin := t.TempDir()
	// The read-tree case makes this file before it sleeps.
	started := filepath.Join(t.TempDir(), "checkout-started")
	// The `worktree add` case writes the worktree's `.git` back-pointer into its
	// (already-created) leaf dir — the last positional arg — so linkedGitDirOf
	// returns non-empty and the read-tree runs. It makes the registration in the
	// repository too, which the create tests. read-tree marks its start, then
	// sleeps until the deadline kills it.
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$*\" in " +
		"*read-tree*) : > '" + started + "'; exec sleep 30 ;; " +
		"*is-inside-work-tree*) echo true; exit 0 ;; " +
		"*abbrev-ref*) echo main; exit 0 ;; " +
		"*--no-checkout*) reg=\"$PWD/.git/worktrees/wt\"; mkdir -p \"$reg\"; printf '../..\\n' > \"$reg/commondir\"; " +
		"printf '%s/.git\\n' \"$(cd \"$last\" && pwd -P)\" > \"$reg/gitdir\"; " +
		"printf 'gitdir: %s\\n' \"$reg\" > \"$last/.git\"; exit 0 ;; " +
		"*) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// D5 off: the caller's timeoutMs is the only deadline in force. It fires once
	// the read-tree has started, and its timer of markerTimeoutMs stays a backstop.
	deadlineAt(t, func() bool { return exists(started) }, nil)

	base := t.TempDir()
	shapeAsGitRepo(t, base)
	wt := filepath.Join(base, ".claude", "worktrees", "wt")
	h := newTestGitDeps()
	raw := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": base, "branchName": "b", "worktreePath": wt, "timeoutMs": markerTimeoutMs}))

	if !strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Errorf("reply = %s, want errorCode timeout", raw)
	}
	if !strings.Contains(raw, "deadline expired during the checkout") {
		t.Errorf("reply = %s, want the during-the-checkout message", raw)
	}
	if !strings.Contains(raw, "git worktree add timed out after "+strconv.Itoa(markerTimeoutMs)+"ms") {
		t.Errorf("reply = %s, want it to quote the bound of %dms", raw, markerTimeoutMs)
	}
}
