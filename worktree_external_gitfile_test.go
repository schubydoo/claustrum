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
	return verifiedWorktreeEntry(gitDir, filepath.Join(repo, ".git"), wp, newWorktreePathSet(wp))
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
