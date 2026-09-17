package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Reference build 90fca6e6 stopped seeding a new worktree with Claude's own runtime
// state. Both copy paths skip a match: the manifest copy and the `.claude/` copy.
//
// The measured behaviour is whole path components, anchored directly under
// `.claude/`, and case-insensitive. isClaudeRuntimeState carries the fixture that
// pins each of those three.
//
// claustrum has the manifest copy as copyWorktreeIncludes, and this file pins that
// half. TestPopulateWorktreeCopiesIgnoredClaudeDir in worktreecopy_test.go pins
// the `.claude/` copy.

// claudeRuntimeStateFixture builds a repo whose `.claude/` tree is git-ignored and
// named by the manifest, so every file under it is a copy candidate today. Only the
// runtime-state paths must be dropped.
func claudeRuntimeStateFixture(t *testing.T) (repo, wt string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "master", "repo")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n", 0o644)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n", 0o644)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".claude/\n", 0o644)
	runGit(t, repo, "add", "tracked.txt", ".gitignore")

	// Kept: ordinary configuration under `.claude/`.
	writeFile(t, filepath.Join(repo, ".claude", "settings.local.json"), "{}\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "agents", "a.md"), "a\n", 0o644)

	// Dropped: one file for each shape in the nine-name list. A plain file name, a
	// directory name, a two-component name, and one that differs only in case.
	writeFile(t, filepath.Join(repo, ".claude", "scheduled_tasks.json"), "{}\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "mailbox", "m1.txt"), "m\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "routines", ".state", "r.json"), "r\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "worktrees", "w", "x.txt"), "x\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "Checkpoints", "c.json"), "c\n", 0o644)

	// The two boundary cases, both COPIED on the reference. A name that only
	// prefixes a listed one, and a listed name one level deeper than `.claude/`.
	// Without these a mutant that matched substrings, or matched at any depth,
	// would pass every arm below.
	writeFile(t, filepath.Join(repo, ".claude", "mailboxes", "keep.txt"), "k\n", 0o644)
	writeFile(t, filepath.Join(repo, ".claude", "nested", "mailbox", "deep.txt"), "d\n", 0o644)

	runGit(t, repo, "commit", "-m", "init")
	return repo, filepath.Join(root, "wt")
}

// TestWorktreeIncludeSkipsClaudeRuntimeState pins the manifest half of the skip.
func TestWorktreeIncludeSkipsClaudeRuntimeState(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	repo, wt := claudeRuntimeStateFixture(t)
	runGit(t, repo, "worktree", "add", "-b", "b1", wt)
	populateWorktree(repo, wt)

	// The controls that must keep passing. Without them a mutant that copies
	// nothing at all under `.claude/` would look correct.
	for _, rel := range []string{
		".claude/settings.local.json",
		".claude/agents/a.md",
		".claude/mailboxes/keep.txt",
		".claude/nested/mailbox/deep.txt",
	} {
		if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s was not seeded: %v", rel, err)
		}
	}

	// The new arms. claustrum copies every one of these today.
	for _, rel := range []string{
		".claude/scheduled_tasks.json",
		".claude/mailbox/m1.txt",
		".claude/routines/.state/r.json",
		".claude/worktrees/w/x.txt",
		".claude/Checkpoints/c.json",
	} {
		if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was seeded into the worktree; it is Claude runtime state", rel)
		}
	}
}
