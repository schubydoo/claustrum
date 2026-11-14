//go:build unix

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cover the Windows worktreeRoot-unsupported guard branches in gitWorktreeCreate /
// gitWorktreeRemove on the linux coverage cell: off Windows the guard is a no-op, so
// stub it to the Windows behaviour and assert the create (errorCode unsafe_path) and
// remove (no errorCode) refusal frames actually return through the guard.
func TestExternalWorktreeUnsupportedGuardBranch(t *testing.T) {
	requireGit(t)
	orig := externalWorktreeUnsupportedRefusal
	t.Cleanup(func() { externalWorktreeUnsupportedRefusal = orig })
	externalWorktreeUnsupportedRefusal = func(root, verb string) string {
		return fmt.Sprintf("refusing to %s worktree: %s cannot be used: a custom worktree location is not supported on Windows hosts yet", verb, tickPath(root))
	}

	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "ext")
	wtp := filepath.Join(root, "d", "wt")
	h := newTestGitDeps()

	create := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "w", "worktreePath": wtp, "worktreeRoot": root}))
	if !strings.Contains(create, "a custom worktree location is not supported on Windows hosts yet") ||
		!strings.Contains(create, `"errorCode":"unsafe_path"`) {
		t.Errorf("create with worktreeRoot = %s, want the unsupported refusal + unsafe_path", create)
	}

	remove := gitDispatch(t, h, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wtp, "worktreeRoot": root}))
	if !strings.Contains(remove, "a custom worktree location is not supported on Windows hosts yet") {
		t.Errorf("remove with worktreeRoot = %s, want the unsupported refusal", remove)
	}
	// This checks the stub above. TestExternalWorktreeUnsupportedOnWindows guards the
	// production text.
	if !strings.Contains(worktreeErrorField(t, remove), "worktree: `"+root+"` cannot be used") ||
		!strings.Contains(worktreeErrorField(t, create), "worktree: `"+root+"` cannot be used") {
		t.Errorf("remove = %s, create = %s, want the root in backticks", remove, create)
	}
	if strings.Contains(remove, "errorCode") {
		t.Errorf("remove refusal must carry no errorCode, got %s", remove)
	}
}
