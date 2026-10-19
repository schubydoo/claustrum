//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A caller-supplied timeoutMs (4534d86) that fires while the read-tree checkout is
// still running yields the "during the checkout" timeout message, with git's own
// kill error appended and errorCode "timeout". The stub lets the pre-checks and the
// `worktree add` succeed fast (writing the linked `.git` so worktreeAdminDir resolves
// the admin dir), then sleeps on the read-tree so the per-request deadline kills it —
// exercising the second, post-add timeout arm the "before the checkout started" test
// never reaches. The measured "after the checkout finished" path (a descendant holds the pipe past
// the drain cap) is force-covered by TestWorktreeCreateLingeringDescendant in
// git_waitdelay_unix_test.go.
func TestWorktreeCreateTimeoutMsFiresDuringCheckout(t *testing.T) {
	bin := t.TempDir()
	// The `worktree add` case writes the worktree's `.git` back-pointer into its
	// (already-created) leaf dir — the last positional arg — so worktreeAdminDir
	// returns non-empty and the read-tree runs. It makes the registration in the
	// repository too, which the create tests. read-tree then sleeps past the bound.
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$*\" in " +
		"*read-tree*) exec sleep 30 ;; " +
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

	// D5 off: the caller's timeoutMs is the only deadline in force.

	base := t.TempDir()
	shapeAsGitRepo(t, base)
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
