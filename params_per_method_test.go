package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParamsUnreadMemberIgnored: a member that another method of the namespace
// reads is ignored, whatever its type, and a wrong-typed wantPid counts as absent.
// The reply is the reply to the request without that member. 5fd08069 answers so
// in cells F1 to F3, G1 to G3, G4b, P1 and P2 of round task015-01 (Linux VM). A
// files or git method that decodes the struct of another one fails here.
func TestParamsUnreadMemberIgnored(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	writeFile(t, file, "hello\n", 0o644)
	for _, tc := range []struct {
		name   string
		method string
		base   map[string]any
		bad    map[string]any
	}{
		{"F1", "files.stat", map[string]any{"path": file}, map[string]any{"maxBytes": "{"}},
		{"F2", "files.list", map[string]any{"path": dir}, map[string]any{"maxBytes": "{"}},
		{"F3", "files.validate", map[string]any{"path": dir}, map[string]any{"maxBytes": "{"}},
		{"G1", "git.info", map[string]any{"path": dir}, map[string]any{"timeoutMs": "x"}},
		{"G2", "git.info", map[string]any{"path": dir}, map[string]any{"baseRepo": 7}},
		{"G3", "git.list_branches", map[string]any{"path": dir}, map[string]any{"worktreePath": []any{}}},
		{"list_branches baseRepo", "git.list_branches", map[string]any{"path": dir}, map[string]any{"baseRepo": 7}},
		{"G4b", "git.status", map[string]any{"path": dir, "baseRepo": dir}, map[string]any{"branchName": map[string]any{}}},
		{"status worktreePath", "git.status", map[string]any{"path": dir, "baseRepo": dir}, map[string]any{"worktreePath": 5}},
		{"G4", "git.status", map[string]any{"path": dir}, map[string]any{"branchName": map[string]any{}}},
		{"G6", "git.worktree_create", map[string]any{"baseRepo": dir, "branchName": "b2", "worktreePath": filepath.Join(dir, "w2")}, map[string]any{"path": 123}},
		{"G7", "git.worktree_remove", map[string]any{"baseRepo": dir, "branchName": "b1", "worktreePath": filepath.Join(dir, "w1")}, map[string]any{"timeoutMs": "x"}},
		{"G8", "git.worktree_remove", map[string]any{"baseRepo": dir, "branchName": "b1", "worktreePath": filepath.Join(dir, "w1")}, map[string]any{"path": 123, "existingBranch": 5}},
		{"P1", "process.spawn", map[string]any{"id": "p1", "command": "/nonexistent_binary_xyz_claustrum"}, map[string]any{"wantPid": "x"}},
		{"P2", "process.reattach", map[string]any{"id": "p2", "fromSeq": 0}, map[string]any{"wantPid": "x"}},
	} {
		want := dispatchRaw(t, s, rpcLine(t, tc.method, tc.base))
		with := map[string]any{}
		for k, v := range tc.base {
			with[k] = v
		}
		for k, v := range tc.bad {
			with[k] = v
		}
		got := dispatchRaw(t, s, rpcLine(t, tc.method, with))
		if got != want || strings.Contains(got, `"Invalid params"`) {
			t.Errorf("%s: %s with %v = %s, want %s", tc.name, tc.method, tc.bad, got, want)
		}
	}
}

// TestParamsReadMemberWrongType: a wrong type in a member that the method reads
// stays -32602 "Invalid params". 5fd08069 answers so in cells F4 and G5 of round
// task015-01 (Linux, macOS VMs). Each member of each git struct is here once, so
// a struct that loses a member fails.
func TestParamsReadMemberWrongType(t *testing.T) {
	s := newTestServer(t)
	const want = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"files.read", map[string]any{"path": "x", "maxBytes": "4"}},
		{"git.info", map[string]any{"path": 7}},
		{"git.list_branches", map[string]any{"path": 7}},
		{"git.status", map[string]any{"path": 7, "baseRepo": "x"}},
		{"git.status", map[string]any{"path": "x", "baseRepo": 7}},
		{"git.worktree_remove", map[string]any{"baseRepo": 7}},
		{"git.worktree_remove", map[string]any{"branchName": 7}},
		{"git.worktree_remove", map[string]any{"worktreePath": 7}},
		{"git.worktree_remove", map[string]any{"worktreeRoot": 7}},
		{"git.worktree_create", map[string]any{"baseRepo": 7}},
		{"git.worktree_create", map[string]any{"branchName": 7}},
		{"git.worktree_create", map[string]any{"worktreePath": 7}},
		{"git.worktree_create", map[string]any{"worktreeRoot": 7}},
		{"git.worktree_create", map[string]any{"sourceBranch": 7}},
		{"git.worktree_create", map[string]any{"existingBranch": 7}},
		{"git.worktree_create", map[string]any{"timeoutMs": "x"}},
	} {
		if got := dispatchRaw(t, s, rpcLine(t, tc.method, tc.params)); got != want {
			t.Errorf("%s %v = %s, want %s", tc.method, tc.params, got, want)
		}
	}
}

// TestPluginsPruneWithoutParams: plugins.prune with no params member answers the
// decode error of an empty input, as 5fd08069 does (cell P0 of round task015-01,
// Linux VM). It prunes nothing.
func TestPluginsPruneWithoutParams(t *testing.T) {
	s := newTestServer(t)
	line := `{"jsonrpc":"2.0","id":1,"method":"plugins.prune","auth":"` + testToken + `"}`
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params: unexpected end of JSON input"}}`
	if got := dispatchRaw(t, s, line); got != want {
		t.Errorf("plugins.prune with no params = %s, want %s", got, want)
	}
}

// TestSocketWorktreeUnreadMembersIgnored: git.worktree_create with "path":123 makes
// the worktree, and git.worktree_remove with "timeoutMs":"x", "path":123 and
// "existingBranch":5 removes it, its registration and its branch. 5fd08069 does so
// in cells G6 to G8 of round task015-01 (Linux VM, and G6 on macOS and Windows VMs).
func TestSocketWorktreeUnreadMembersIgnored(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")
	writeFile(t, filepath.Join(repo, "a.txt"), "x\n", 0o644)
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "init")
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")

	cl := dial(t, startSocketServer(t))
	got := string(cl.call(req(1, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "b1", "worktreePath": leaf, "path": 123})))
	if !strings.Contains(got, `"result":{"success":true,`) || !strings.HasSuffix(got, `"sourceBranch":"main","branch":"b1"}}`) {
		t.Fatalf("create = %s, want success with branch b1", got)
	}
	if fi, err := os.Stat(leaf); err != nil || !fi.IsDir() {
		t.Fatalf("worktree %s not made (err %v)", leaf, err)
	}
	writeFile(t, filepath.Join(leaf, "leaf.txt"), "u\n", 0o644)

	got = string(cl.call(req(2, "git.worktree_remove", map[string]any{
		"baseRepo": repo, "branchName": "b1", "worktreePath": leaf,
		"timeoutMs": "x", "path": 123, "existingBranch": 5})))
	if want := `{"jsonrpc":"2.0","id":2,"result":{"success":true}}`; got != want {
		t.Fatalf("remove = %s, want %s", got, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("worktree %s still there (err %v)", leaf, err)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git", "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("registration still there (err %v)", err)
	}
	if gitExitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/b1") {
		t.Error("branch b1 still there")
	}
}

// TestGitParamsViewsCopyEveryMember: each git struct hands every member that it
// decodes on to gitParams, and no other member.
func TestGitParamsViewsCopyEveryMember(t *testing.T) {
	raw := `{"path":"p","baseRepo":"b","branchName":"n","worktreePath":"w","sourceBranch":"s",` +
		`"existingBranch":"e","worktreeRoot":"r","timeoutMs":9,"shadowDirOff":true}`
	for _, tc := range []struct {
		view gitMethodParams
		want gitParams
	}{
		{&infoViewParams{}, gitParams{Path: "p", ShadowDirOff: true}},
		{&statusViewParams{}, gitParams{Path: "p", BaseRepo: "b", ShadowDirOff: true}},
		{&removeViewParams{}, gitParams{BaseRepo: "b", BranchName: "n", WorktreePath: "w", WorktreeRoot: "r", ShadowDirOff: true}},
		{&createViewParams{}, gitParams{BaseRepo: "b", BranchName: "n", WorktreePath: "w", SourceBranch: "s",
			ExistingBranch: "e", WorktreeRoot: "r", TimeoutMs: 9, ShadowDirOff: true}},
	} {
		got, bad := bindGitParams(&request{Params: []byte(raw)}, tc.view)
		if bad != nil || got != tc.want {
			t.Errorf("%T: got %+v (reply %v), want %+v", tc.view, got, bad, tc.want)
		}
	}
}
