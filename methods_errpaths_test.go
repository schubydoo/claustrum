package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every params-taking method must route a present-but-mistyped params object
// through bindParams to the shared -32602 Invalid params error (probe-verified
// contract; the value-typed cousins live in rpc_test.go). This sweeps the
// methods the earlier suites didn't reach one by one.
func TestBindParamsMistypedPerMethod(t *testing.T) {
	s := newTestServer(t)
	cases := map[string]map[string]any{
		"files.stat":          {"path": 123},
		"files.list":          {"path": 123},
		"files.validate":      {"path": 123},
		"files.extract_tar":   {"archivePath": 123},
		"git.info":            {"path": 123},
		"git.list_branches":   {"path": 123},
		"git.worktree_create": {"branchName": 123},
		"process.stdin":       {"id": 123},
		"process.killAndWait": {"id": 123},
	}
	for method, params := range cases {
		got := dispatchRaw(t, s, rpcLine(t, method, params))
		if !strings.Contains(got, "Invalid params") {
			t.Errorf("dispatch %s with mistyped params = %s, want Invalid params", method, got)
		}
	}
}

// git.worktree_create validates branchName presence right after bindParams,
// before any repo probing.
func TestGitWorktreeCreateMissingBranchName(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{"path": t.TempDir()}))
	if !strings.Contains(got, "branchName is required") {
		t.Errorf("worktree_create without branchName = %s, want branchName is required", got)
	}
}

// git.status on a clean repo (committed tree, no changes) takes the empty
// porcelain fast path: {isRepo:true, clean:true} with no changes array.
func TestGitStatusCleanRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "hi\n", 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	// 7d193f89 reports status for a session worktree of baseRepo, not the repo.
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "add", "-q", wt)

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	if !strings.Contains(got, `"clean":true`) {
		t.Errorf("git.status clean repo = %s, want clean:true", got)
	}
	if strings.Contains(got, `"changes"`) {
		t.Errorf("git.status clean repo = %s, want no changes field", got)
	}
}

// TestGitStatusDetectsSameSizeModification pins the index-mtime preservation in the
// isolated temp-gitdir assembly (statusCopyFile): the os.Chtimes that copies the
// source index's mtime onto the temp copy. git.status runs against a COPY of the
// worktree's index in a temp gitdir. git's racy-clean rule re-hashes a work-tree file
// only when its cached entry mtime is not older than the index file's own mtime; a fresh
// (newer) index copy disarms that check, so a same-size edit whose stat still matches the
// cache is trusted-as-clean and MISSED. The fix copies the source index mtime so the
// racy-clean check stays armed. Dropping that os.Chtimes gives the copy a fresh mtime and
// the edit is missed.
//
// This is a FAITHFUL mutant guard, not a smoke test: it isolates the racy-clean path so
// removing the os.Chtimes makes it fail. Two arrangements do the isolating:
//
//   - core.checkStat=minimal makes git compare only size+mtime, dropping ctime, ino, dev,
//     uid and gid. That matters because a same-size overwrite always bumps ctime (which Go
//     cannot reset), and a ctime/ino mismatch would flag the edit via a plain stat diff
//     regardless of the fix — masking a broken copy. With only size+mtime compared, and
//     the file's mtime restored to the cached value below, the stat matches the cache and
//     a content re-hash is the ONLY way to detect the edit.
//   - the worktree's real index is aged 2s BEFORE its cached entry mtime, so preserving it
//     (the fix) keeps index-mtime < entry-mtime and racy-clean fires; a fresh copy (the
//     mutant) is newer than the entry, so racy-clean stays disarmed. The pre-status sleep
//     forces status to run in a later wall-clock second than the entry mtime, so the
//     mutant's fresh index copy is unambiguously newer (no sub-second tie).
//
// Cross-platform (no build tag): only os/filepath/time and the in-process harness — no
// POSIX shell, /bin path, symlink or geteuid. A separate cross-binary check runs at
// each pin bump.
func TestGitStatusDetectsSameSizeModification(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	// Compare only size+mtime, so ctime (unresettable from Go) and ino/dev/uid/gid cannot
	// flag the same-size edit through a plain stat mismatch — leaving racy-clean's content
	// re-hash as the sole detector. Written to the repo config git.status reads via
	// GIT_COMMON_DIR.
	runGit(t, dir, "config", "core.checkStat", "minimal")
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n", 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "add", "-q", wt)

	// The checkout recorded a.txt's mtime in the worktree index (the cached entry stat).
	// Capture it before touching the file.
	f := filepath.Join(wt, "a.txt")
	fi, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	cached := fi.ModTime()

	// Same byte count as the committed content, then restore the cached mtime so the file's
	// (size, mtime) matches the index cache exactly. Now only a content re-hash reveals it.
	writeFile(t, f, "two\n", 0o644)
	if err := os.Chtimes(f, cached, cached); err != nil {
		t.Fatal(err)
	}

	// Age the worktree's real index below the cached entry mtime. Preserving this mtime (the
	// fix) keeps index-mtime < entry-mtime, so racy-clean re-hashes and catches the edit; a
	// fresh copy (the mutant) is newer than the entry, so racy-clean stays off and the edit
	// is missed.
	idx := filepath.Join(dir, ".git", "worktrees", "wt", "index")
	aged := cached.Add(-2 * time.Second)
	if err := os.Chtimes(idx, aged, aged); err != nil {
		t.Fatal(err)
	}

	// Run status in a strictly later wall-clock second than the cached entry mtime, so the
	// mutant's fresh index copy is unambiguously newer than the entry (no same-second nsec
	// tie that could re-arm racy-clean by luck). Harmless to the fix, whose aged copy is
	// older regardless.
	time.Sleep(1100 * time.Millisecond)

	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	if !strings.Contains(got, `" M a.txt"`) {
		t.Errorf("git.status = %s, want an unstaged modification \" M a.txt\"", got)
	}
}

// 7d193f89 runs status with --untracked-files=all, so an untracked file inside an
// untracked directory is listed individually ("?? sub/u.txt") rather than as the
// directory ("?? sub/"). Plain `git status --porcelain` reports the directory, so
// this is a wire change the frame battery (top-level untracked only) did not
// cover. Measured against the reference.
func TestGitStatusUntrackedSubdirListsFiles(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "hi\n", 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "add", "-q", wt)
	writeFile(t, filepath.Join(wt, "sub", "u.txt"), "u\n", 0o644) // untracked, one dir deep

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	if !strings.Contains(got, `?? sub/u.txt`) {
		t.Errorf("git.status = %s, want the file listed individually (?? sub/u.txt)", got)
	}
	if strings.Contains(got, `?? sub/"`) {
		t.Errorf("git.status = %s, listed the directory (?? sub/) — --untracked-files=all is missing", got)
	}
}
