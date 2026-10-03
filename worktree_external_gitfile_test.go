package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyWorktreeForTest runs the two checks of git.worktree_remove on a worktree's
// `.git` file: the read of the file, then the lookup of its entry in repo.
func verifyWorktreeForTest(t *testing.T, repo, wp string) (string, error) {
	t.Helper()
	root, err := os.OpenRoot(wp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	gitDir, err := worktreeGitFileTarget(root, wp)
	if err != nil {
		return "", err
	}
	return verifiedWorktreeEntry(gitDir, filepath.Join(repo, ".git"), "", wp, newWorktreePathSet(wp), nil)
}

// The `.git` file of a worktree names its entry only when the entry is a directory under
// the repository's own worktrees directory, its commondir leads back to that repository,
// and its gitdir record names the worktree. Each refusal text was measured against
// f6010b97 on a macOS VM (rows X03, E07, L03 and the forged-record rows).
func TestVerifiedWorktreeEntry(t *testing.T) {
	requireGit(t)
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	realWT := filepath.Join(base, "realwt")
	runGit(t, repo, "worktree", "add", "-q", realWT, "-b", "realwt")
	adminReal := filepath.Join(repo, ".git", "worktrees", "realwt")

	// A fresh worktree directory with the given `.git` content ("" = no .git).
	mkwt := func(name, gitContent string) string {
		wp := filepath.Join(base, "ext", name, "wt")
		if err := os.MkdirAll(wp, 0o755); err != nil {
			t.Fatal(err)
		}
		if gitContent != "" {
			if err := os.WriteFile(filepath.Join(wp, ".git"), []byte(gitContent), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return wp
	}

	if name, err := verifyWorktreeForTest(t, repo, realWT); err != nil || name != "realwt" {
		t.Fatalf("registered worktree = (%q, %v), want (realwt, nil)", name, err)
	}
	cases := []struct {
		name string
		wp   string
		want string
	}{
		{"missing .git", mkwt("miss", ""), " has no .git file"},
		{"garbage .git", mkwt("garb", "garbage\n"), "/.git does not name a git dir"},
		{"no space after gitdir:", mkwt("nosp", "gitdir:"+adminReal+"\n"), "/.git does not name a git dir"},
		{"parent not worktrees", mkwt("par", "gitdir: /nope/foreign/x\n"),
			" carries a .git file that does not name this repository's own worktree admin directory"},
		{"name not in this repository", mkwt("frn", "gitdir: /foreign/.git/worktrees/x\n"),
			" carries a .git file that does not name this repository's own worktree admin directory"},
		{"entry gone", mkwt("gh", "gitdir: "+filepath.Join(repo, ".git", "worktrees", "ghost")+"\n"),
			" carries a .git file that does not name this repository's own worktree admin directory"},
		{"record names another worktree", mkwt("bp", "gitdir: "+adminReal+"\n"),
			" carries a .git file naming an admin directory whose own record is of a different worktree"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := verifyWorktreeForTest(t, repo, c.wp)
			var r *worktreeRefusal
			if !errors.As(err, &r) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if !strings.Contains(filepath.ToSlash(r.text), c.want) || !strings.HasPrefix(r.text, c.wp) {
				t.Errorf("refusal = %q, want %s followed by %q", r.text, c.wp, c.want)
			}
		})
	}

	// `.git` is a directory: "is not a regular file".
	dirwt := mkwt("dir", "")
	if err := os.MkdirAll(filepath.Join(dirwt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyWorktreeForTest(t, repo, dirwt); err == nil ||
		err.Error() != filepath.Join(dirwt, ".git")+" is not a regular file" {
		t.Errorf("err = %v, want the \"is not a regular file\" refusal", err)
	}

	// A commondir that does not lead back to the repository.
	forged := mkwt("cd", "gitdir: "+adminReal+"\n")
	writeFile(t, filepath.Join(adminReal, "commondir"), "/elsewhere/.git\n", 0o644)
	if _, err := verifyWorktreeForTest(t, repo, forged); err == nil ||
		!strings.Contains(err.Error(), "does not name this repository's own worktree admin directory") {
		t.Errorf("err = %v, want the own-admin-directory refusal", err)
	}

	// A repository with no worktrees directory: the open error itself, not a refusal.
	bareRepo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(bareRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "init", "-q")
	tw := mkwt("trans", "gitdir: /foreign/.git/worktrees/x\n")
	_, err := verifyWorktreeForTest(t, bareRepo, tw)
	var r *worktreeRefusal
	if !errors.Is(err, fs.ErrNotExist) || errors.As(err, &r) {
		t.Errorf("no worktrees dir = %v, want the open error of the worktrees dir", err)
	}
}

// E8, the part of probe row D-subst and battery rows J2, Q1 and Q2 (Windows VM) that
// runs on every system. An entry whose record names another spelling of the worktree
// is verified when respell gives that spelling. A `commondir` that leads to the git
// directory that git answered passes too. respell is not called when the record
// names the worktree as sent.
func TestVerifiedWorktreeEntryRespell(t *testing.T) {
	requireGit(t)
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wp := filepath.Join(repo, ".claude", "worktrees", "w1")
	runGit(t, repo, "worktree", "add", "-q", "-b", "w1", wp)
	commonDir := filepath.Join(repo, ".git")
	gitDir := filepath.Join(commonDir, "worktrees", "w1")
	sent := filepath.Join(base, "other-spelling", ".claude", "worktrees", "w1")
	calls := 0
	respell := func(answer string) func() string {
		return func() string { calls++; return answer }
	}

	if name, err := verifiedWorktreeEntry(gitDir, commonDir, "", wp, newWorktreePathSet(wp), respell("")); err != nil || name != "w1" || calls != 0 {
		t.Errorf("record as sent: name=%q err=%v respell calls=%d, want w1, nil, 0", name, err, calls)
	}
	if name, err := verifiedWorktreeEntry(gitDir, commonDir, "", sent, newWorktreePathSet(sent), respell(wp)); err != nil || name != "w1" || calls != 1 {
		t.Errorf("respelled: name=%q err=%v respell calls=%d, want w1, nil, 1", name, err, calls)
	}
	const other = "whose own record is of a different worktree"
	if _, err := verifiedWorktreeEntry(gitDir, commonDir, "", sent, newWorktreePathSet(sent), respell("")); err == nil || !strings.Contains(err.Error(), other) {
		t.Errorf("no other spelling: err=%v, want %q", err, other)
	}
	if _, err := verifiedWorktreeEntry(gitDir, commonDir, "", sent, newWorktreePathSet(sent), respell(sent)); err == nil || !strings.Contains(err.Error(), other) {
		t.Errorf("a spelling that the record does not name: err=%v, want %q", err, other)
	}

	// The commondir of the entry names a directory that is not commonDir.
	answered := filepath.Join(base, "answered", ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(answered+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const notOurs = "does not name this repository's own worktree admin directory"
	if _, err := verifiedWorktreeEntry(gitDir, commonDir, "", wp, newWorktreePathSet(wp), nil); err == nil || !strings.Contains(err.Error(), notOurs) {
		t.Errorf("foreign commondir: err=%v, want %q", err, notOurs)
	}
	if name, err := verifiedWorktreeEntry(gitDir, commonDir, filepath.ToSlash(answered), wp, newWorktreePathSet(wp), nil); err != nil || name != "w1" {
		t.Errorf("commondir names the answered git dir: name=%q err=%v, want w1, nil", name, err)
	}
}
