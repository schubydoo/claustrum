package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 refuses a baseRepo that sits inside a managed worktrees tree — beneath a
// `.claude/worktrees` directory, or beneath a directory holding a
// `.claude-managed-worktrees` marker — as an invalid trust root. It applies on
// git.list_branches (bare isRepo:false), git.worktree_create (errorCode
// "nested_base_repo"), and git.worktree_remove (no errorCode). Measured byte-for-byte
// against 7d193f89; git.info/git.status are NOT gated.
func TestBaseRepoUnderManagedWorktreesRefused(t *testing.T) {
	requireGit(t)
	const refusal = "baseRepo is inside a managed worktrees directory"

	// A repo nested under <base>/.claude/worktrees.
	base := t.TempDir()
	nested := filepath.Join(base, ".claude", "worktrees", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "init", "-q")
	runGit(t, nested, "commit", "-q", "--allow-empty", "-m", "init")

	// A repo beneath a directory holding the .claude-managed-worktrees marker.
	markBase := t.TempDir()
	writeFile(t, filepath.Join(markBase, managedWorktreesMarker), "", 0o644)
	markRepo := filepath.Join(markBase, "holder")
	if err := os.MkdirAll(markRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, markRepo, "init", "-q")
	runGit(t, markRepo, "commit", "-q", "--allow-empty", "-m", "init")

	h := newTestGitDeps()
	for _, repo := range []string{nested, markRepo} {
		lb := gitDispatch(t, h, rpcLine(t, "git.list_branches", map[string]any{"path": repo, "baseRepo": repo}))
		if !strings.Contains(lb, `"isRepo":false`) || strings.Contains(lb, `"master"`) {
			t.Errorf("list_branches(%s) = %s, want isRepo:false + empty branches", repo, lb)
		}
		wc := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "x", "worktreePath": filepath.Join(repo, ".claude", "worktrees", "x")}))
		if !strings.Contains(wc, `"errorCode":"nested_base_repo"`) || !strings.Contains(wc, refusal) {
			t.Errorf("worktree_create(%s) = %s, want nested_base_repo refusal", repo, wc)
		}
		wr := gitDispatch(t, h, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": repo, "worktreePath": filepath.Join(repo, ".claude", "worktrees", "x")}))
		if !strings.Contains(wr, `"success":false`) || !strings.Contains(wr, refusal) || strings.Contains(wr, `"errorCode"`) {
			t.Errorf("worktree_remove(%s) = %s, want the refusal with no errorCode", repo, wr)
		}
	}

	// Control: a normal top-level repo is NOT refused (list_branches lists its branch).
	normal := t.TempDir()
	runGit(t, normal, "init", "-q")
	runGit(t, normal, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, normal, "branch", "-M", "main")
	lb := gitDispatch(t, h, rpcLine(t, "git.list_branches", map[string]any{"path": normal, "baseRepo": normal}))
	if !strings.Contains(lb, `"isRepo":true`) || !strings.Contains(lb, `"main"`) {
		t.Errorf("list_branches(normal) = %s, want isRepo:true with the branch", lb)
	}
}

// markerLayoutFrames sends git.list_branches, git.worktree_remove of the worktree w2
// and git.worktree_create of w1 for the baseRepo base, and wants the refusals of
// b3081b51 and be1c1ca5 (measured on Linux). real is the repository behind base.
// The worktree w2 and its entry stay, and w1 is not made.
func markerLayoutFrames(t *testing.T, base, real string) {
	t.Helper()
	h := newTestGitDeps()
	refusal := jsonString(t, managedWorktreesRefusal)
	if got, want := gitDispatch(t, h, rpcLine(t, "git.list_branches", map[string]any{"path": base})),
		`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`; got != want {
		t.Errorf("list_branches(%s) = %s\nwant %s", base, got, want)
	}
	w2 := filepath.Join(base, ".claude", "worktrees", "w2")
	if got, want := gitDispatch(t, h, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": base, "worktreePath": w2, "branchName": "w2"})),
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":`+refusal+`}}`; got != want {
		t.Errorf("worktree_remove(%s) = %s\nwant %s", base, got, want)
	}
	w1 := filepath.Join(base, ".claude", "worktrees", "w1")
	if got, want := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": base, "worktreePath": w1, "branchName": "w1"})),
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":`+refusal+`,"errorCode":"nested_base_repo"}}`; got != want {
		t.Errorf("worktree_create(%s) = %s\nwant %s", base, got, want)
	}
	mustExist(t, filepath.Join(real, ".claude", "worktrees", "w2", ".git"))
	mustExist(t, filepath.Join(real, ".git", "worktrees", "w2"))
	for _, p := range []string{filepath.Join(real, ".claude", "worktrees", "w1"), filepath.Join(real, ".git", "worktrees", "w1")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (err=%v), want no new worktree", p, err)
		}
	}
}

// markerLayoutRepo makes the repository dir with the linked worktree w2 in
// dir/.claude/worktrees.
func markerLayoutRepo(t *testing.T, dir string) {
	t.Helper()
	initTrustMain(t, dir)
	runGit(t, dir, "worktree", "add", "-q", "-b", "w2", filepath.Join(dir, ".claude", "worktrees", "w2"))
}

// A marker that is a folder refuses a baseRepo below it, as a marker file does
// (b3081b51 and be1c1ca5, measured on Linux).
func TestBaseRepoBelowMarkerFolderRefused(t *testing.T) {
	requireGit(t)
	m := t.TempDir()
	if err := os.Mkdir(filepath.Join(m, managedWorktreesMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	r := filepath.Join(m, "R")
	markerLayoutRepo(t, r)
	markerLayoutFrames(t, r, r)
}
