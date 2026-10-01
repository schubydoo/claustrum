//go:build darwin

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The macOS rows of 89cb6289 that need a file system which ignores letter case and
// Unicode form (APFS). Each test names the mutation that turns it red.

const (
	cafeNFC = "café"
	cafeNFD = "café"
)

// The root keeps the request's spelling. Folder Repo sent as rEPO answers
// rEPO, and an NFC folder sent in NFD answers the NFD bytes (rows I08 and I07a).
// Mutation: canonicalize the root.
func TestInfoRootKeepsRequestSpellingDarwin(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	initTrustMain(t, filepath.Join(base, "Repo"))
	initTrustMain(t, filepath.Join(base, cafeNFC))
	for _, sent := range []string{"rEPO", cafeNFD} {
		t.Run(sent, func(t *testing.T) {
			dir := filepath.Join(base, sent)
			if !exists(dir) {
				t.Skipf("%s does not name the folder: the volume keeps letter case or the Unicode form", dir)
			}
			m := infoFields(t, info(t, dir))
			if m["root"] != dir || m["repo"] != sent {
				t.Errorf("info = %q, want root %q and repo %q", m, dir, sent)
			}
		})
	}
}

// An NFC folder created through an NFD request is refused after `git worktree add`.
// git records the worktree in NFC, and the record no longer names worktreePath after
// symlink resolution. No checkout runs, so the new worktree has no index (row I07a).
// The same request in NFC succeeds. Mutation: compare after Unicode normalization.
func TestCreateAdminRecordSpellingDarwin(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	initTrustMain(t, filepath.Join(base, cafeNFC))
	repo := filepath.Join(base, cafeNFD)
	if !exists(repo) {
		t.Skipf("%s does not name the folder: the volume keeps the Unicode form", repo)
	}
	wt := filepath.Join(repo, ".claude", "worktrees", "w1")
	rep := trustCall(t, "git.worktree_create", map[string]any{"baseRepo": repo, "worktreePath": wt, "branchName": "w1"})
	want, _ := json.Marshal(worktreeResult{Success: false, ErrorCode: "unsafe_path",
		Error: "refusing to create worktree: " + wt + " carries a .git file naming an admin directory whose own record is of a different worktree"})
	wantResult(t, "create(NFD)", rep, string(want))
	if exists(filepath.Join(base, cafeNFC, ".git", "worktrees", "w1", "index")) {
		t.Errorf("the refused create ran the checkout")
	}
	ok := filepath.Join(base, cafeNFC)
	rep = trustCall(t, "git.worktree_create", map[string]any{"baseRepo": ok,
		"worktreePath": filepath.Join(ok, ".claude", "worktrees", "w2"), "branchName": "w2"})
	wantResultPrefix(t, "create(NFC)", rep, `{"success":true`)
}
