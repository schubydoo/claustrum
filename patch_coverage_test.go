package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git.status refuses any path that is not a linked worktree of baseRepo before it
// runs status, answering the bare isRepo:false shape. Two arms of gitStatusWorktreeOf
// that the worktree-based suites do not reach: the MAIN checkout (rev-parse succeeds
// but git-dir equals common-dir) and a NON-repository path (rev-parse fails). Both
// match 7d193f89.
func TestGitStatusRejectsNonWorktreePaths(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "a.txt"), "hi\n", 0o644)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	if got := dispatchRaw(t, s, rpcLine(t, "git.status",
		map[string]any{"path": repo, "baseRepo": repo})); !strings.Contains(got, `"isRepo":false`) {
		t.Errorf("git.status on the main checkout = %s, want isRepo:false", got)
	}

	nonRepo := t.TempDir()
	if got := dispatchRaw(t, s, rpcLine(t, "git.status",
		map[string]any{"path": nonRepo, "baseRepo": nonRepo})); !strings.Contains(got, `"isRepo":false`) {
		t.Errorf("git.status on a non-repository = %s, want isRepo:false", got)
	}
}

// git.worktree_create's location refusals, all taken before git runs and all
// echoing worktreeResult{success:false}: an empty path fails as a non-directory
// (mkdir_failed), a path outside the repo is unsafe_path, and a path whose parent
// is a regular file fails at parent creation (mkdir_failed). Matches 7d193f89.
func TestGitWorktreeCreateLocationRefusals(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	writeFile(t, filepath.Join(repo, "afile"), "x\n", 0o644)
	s := newTestServer(t)

	cases := []struct {
		name, worktreePath, wantCode string
	}{
		{"empty", "", `"errorCode":"mkdir_failed"`},
		// A NON-existent path outside the repo, so the containment refusal is the only
		// thing that can produce unsafe_path — an existing dir would also trip the
		// later "already exists" guard (same code), masking whether containment fired.
		{"outside_repo", filepath.Join(t.TempDir(), "wt"), `"errorCode":"unsafe_path"`},
		{"parent_is_a_file", filepath.Join(repo, "afile", "wt"), `"errorCode":"mkdir_failed"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": tc.worktreePath}))
			if !strings.Contains(got, `"success":false`) || !strings.Contains(got, tc.wantCode) {
				t.Errorf("create %s = %s, want success:false with %s", tc.name, got, tc.wantCode)
			}
		})
	}
}

// The pure containment/admin helpers, called directly for the branches the
// RPC-level tests cannot reach on every platform (filepath.Join in the RPC path
// would clean a literal ".." away before it reaches worktreePathRefusal).
func TestWorktreeHelperEdges(t *testing.T) {
	// Absolute for the current OS ("/repo" on unix, "C:\repo" on Windows), so the
	// refusal reaches the ".." branch rather than the relative-path branch.
	sep := string(os.PathSeparator)
	base, err := filepath.Abs(sep + "repo")
	if err != nil {
		t.Fatal(err)
	}
	// worktreePathRefusal flags a literal ".." component. Joined by hand so the ".."
	// survives — filepath.Join would clean it away before the check sees it.
	dotdot := base + sep + "a" + sep + ".." + sep + "b"
	if msg := worktreePathRefusal(base, dotdot, "create"); !strings.Contains(msg, `".." component`) {
		t.Errorf("worktreePathRefusal with a .. component = %q, want the dotdot refusal", msg)
	}
	// symlinkedComponent returns "" when the paths are not relatable (one absolute,
	// one relative): filepath.Rel errors and there is nothing to walk.
	if got := symlinkedComponent(base, "relative"+sep+"wt"); got != "" {
		t.Errorf("symlinkedComponent on unrelatable paths = %q, want \"\"", got)
	}
	// worktreeAdminDir returns "" for a .git file that is not a "gitdir:" pointer.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git"), "not a gitdir pointer\n", 0o644)
	if got := worktreeAdminDir(dir); got != "" {
		t.Errorf("worktreeAdminDir on a non-pointer .git = %q, want \"\"", got)
	}
}

// git.worktree_remove prunes the worktree registration by reading the worktree's
// own `.git` pointer — attacker-writable content — so a FORGED pointer aimed at
// unrelated data under the repo must not turn the prune into a delete of it. The
// prune is constrained to <repo>/.git/worktrees/, so a `gitdir: <repo>/src` pointer
// is ignored; the removal still answers success:true (parity) and <repo>/src
// survives. Without the constraint the fallback os.RemoveAll would delete it.
// Raised by review on PR 286.
func TestWorktreeRemoveForgedGitdirDoesNotDeleteRepoData(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	precious := filepath.Join(repo, "src", "keep.go")
	writeFile(t, precious, "package x\n", 0o644)

	// A fake worktree dir strictly inside the repo with a forged .git pointer.
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, "src")+"\n", 0o644)

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("remove of a forged worktree = %s, want success:true (parity)", raw)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("the forged .git pointer turned the prune into a delete of repo data: %v", err)
	}
}

// copyClaudeDir's MkdirAll-fail give-up path: the worktree itself is a regular file,
// so MkdirAll(worktree/.claude) fails. This branch has NO observable output difference
// — without the guard the downstream copies fail just as silently — so this is a
// no-panic / smoke guard, not a behavioral oracle (same spirit as the existing
// TestWorktreeCopyFailuresAreSilent). It exists to exercise the give-up branch.
func TestCopyClaudeDirUndestinable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, claudeDirName, "settings.json"), "{}\n", 0o644)
	wtFile := filepath.Join(dir, "wt") // a FILE, not a directory
	writeFile(t, wtFile, "x\n", 0o644)
	copyClaudeDir(dir, wtFile)
	if _, err := os.Stat(filepath.Join(wtFile, claudeDirName)); err == nil {
		t.Error("copyClaudeDir created .claude under a file destination")
	}
}
