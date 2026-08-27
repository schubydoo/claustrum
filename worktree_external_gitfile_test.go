package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// externalWorktreeVerify gates the destructive external-remove fallback: it returns a
// refusal reason unless worktreePath is a genuine registered worktree of baseRepo (or a
// stale ghost the reference cleans up). Every case's reason / transient / proceed split
// is measured byte-for-byte against 7d193f89; the reference REFUSES and leaves the path
// in place where claustrum previously os.RemoveAll'd it (home-directory-delete class).
func TestExternalWorktreeVerify(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, repo, "worktree", "add", "-q", filepath.Join(base, "realwt"), "-b", "realwt")
	adminReal := filepath.Join(repo, ".git", "worktrees", "realwt")

	// helper: a fresh external worktree dir with a given `.git` content ("" = no .git).
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

	cases := []struct {
		name        string
		wp          string
		wantReason  string // substring; "" means expect proceed ("")
		wantTransit bool
	}{
		{"missing .git", mkwt("miss", ""), "has no .git file", false},
		{"garbage .git", mkwt("garb", "garbage\n"), "does not name a git dir", false},
		{"parent not worktrees", mkwt("par", "gitdir: /nope/foreign/x\n"),
			"does not name this repository's own worktree admin directory", false},
		{"foreign under worktrees, base has worktrees", mkwt("frn", "gitdir: /foreign/.git/worktrees/x\n"),
			"does not name this repository's own worktree admin directory", false},
		{"ghost registration", mkwt("gh", "gitdir: "+filepath.Join(repo, ".git", "worktrees", "ghost")+"\n"), "", false},
		{"back-pointer names another worktree", mkwt("bp", "gitdir: "+adminReal+"\n"),
			"naming an admin directory whose own record is of a different worktree", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, transient := externalWorktreeVerify(repo, c.wp)
			if c.wantReason == "" {
				if reason != "" {
					t.Errorf("reason = %q, want proceed (\"\")", reason)
				}
				return
			}
			if !strings.Contains(reason, c.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", reason, c.wantReason)
			}
			if transient != c.wantTransit {
				t.Errorf("transient = %v, want %v", transient, c.wantTransit)
			}
		})
	}

	// `.git` is a DIRECTORY → "is not a regular file".
	dirwt := mkwt("dir", "")
	if err := os.MkdirAll(filepath.Join(dirwt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if reason, _ := externalWorktreeVerify(repo, dirwt); !strings.Contains(reason, "is not a regular file") {
		t.Errorf("gitdir = %q, want the \"is not a regular file\" refusal", reason)
	}

	// Transient: baseRepo with NO worktrees dir, a plausible worktrees-shaped foreign gitdir.
	bareBase := t.TempDir()
	bareRepo := filepath.Join(bareBase, "repo")
	if err := os.MkdirAll(bareRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "init", "-q")
	tw := mkwt("trans", "gitdir: /foreign/.git/worktrees/x\n")
	if reason, transient := externalWorktreeVerify(bareRepo, tw); !transient ||
		!strings.Contains(reason, filepath.Join(bareRepo, ".git", "worktrees")) {
		t.Errorf("no-worktrees-dir = (%q, %v), want a transient naming baseRepo's worktrees dir", reason, transient)
	}
}
