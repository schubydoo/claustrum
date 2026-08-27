package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// externalWorktreeMissingGitRefusal gates the destructive external-remove fallback: only
// a path whose `.git` is a REGULAR FILE (a linked worktree's `gitdir:` pointer) may be
// removed. A missing `.git`, or a `.git` DIRECTORY (an ordinary repository the caller
// pointed the remove at), is refused and left in place — otherwise the fallback would
// os.RemoveAll a real repo and its contents. Both refusal shapes are measured byte-for-
// byte against 7d193f89; the regular-file gate matches the reference (which refuses a
// `.git` directory with "is not a regular file", where claustrum previously deleted it).
func TestExternalWorktreeMissingGitRefusal(t *testing.T) {
	root := t.TempDir()
	wp := filepath.Join(root, "wt")
	if err := os.MkdirAll(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(wp, ".git")

	// No `.git` at all → "<wp> has no .git file".
	if msg := externalWorktreeMissingGitRefusal("/repo", wp); !strings.Contains(msg, wp+" has no .git file") {
		t.Errorf("no .git = %q, want the \"has no .git file\" refusal", msg)
	}

	// `.git` is a DIRECTORY (an ordinary repo) → "<wp>/.git is not a regular file",
	// NOT deleted. Guards the home-directory-delete incident class for external removes.
	if err := os.MkdirAll(gitPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := externalWorktreeMissingGitRefusal("/repo", wp); !strings.Contains(msg, gitPath+" is not a regular file") {
		t.Errorf("gitdir = %q, want the \"is not a regular file\" refusal", msg)
	}

	// `.git` is a regular pointer file → a real worktree, allowed through ("").
	if err := os.RemoveAll(gitPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitPath, []byte("gitdir: /somewhere/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := externalWorktreeMissingGitRefusal("/repo", wp); msg != "" {
		t.Errorf("regular .git file = %q, want no refusal", msg)
	}
}
