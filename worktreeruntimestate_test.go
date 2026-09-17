package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
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

// droppedRuntimeStatePaths gives one repo-relative path per runtime-state name, in
// the shape that name actually takes on disk: a file for the ones that are files, a
// file inside it for the ones that are directories. Every entry must be dropped.
//
// It is keyed by the name so mustCoverEveryRuntimeStateName can prove the fixture
// covers the whole list. An earlier version planted five of the nine by hand, which
// left four exclusions with no behavioural test at all: a typo in one of those
// would have let session state into a new worktree with the suite still green.
//
// `Checkpoints` is deliberately spelled with a capital, since the match is
// case-insensitive and nothing else here would catch a mutant that dropped that.
var runtimeStateFixturePaths = map[string]string{
	"agent-registry.json":         ".claude/agent-registry.json",
	"assistant-daemon-state.json": ".claude/assistant-daemon-state.json",
	"checkpoints":                 ".claude/Checkpoints/c.json",
	"first-run":                   ".claude/first-run",
	"mailbox":                     ".claude/mailbox/m1.txt",
	"routines/.state":             ".claude/routines/.state/r.json",
	"scheduled_tasks.json":        ".claude/scheduled_tasks.json",
	"scheduled_tasks.lock":        ".claude/scheduled_tasks.lock",
	"worktrees":                   ".claude/worktrees/w/x.txt",
}

func droppedRuntimeStatePaths() []string {
	out := make([]string, 0, len(runtimeStateFixturePaths))
	for _, rel := range runtimeStateFixturePaths {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// mustCoverEveryRuntimeStateName fails when the fixture and the production list
// have drifted apart in either direction.
func mustCoverEveryRuntimeStateName(t *testing.T) {
	t.Helper()
	for _, name := range claudeRuntimeStateNames {
		if _, ok := runtimeStateFixturePaths[name]; !ok {
			t.Fatalf("runtime-state name %q has no fixture path; add one so it is tested", name)
		}
	}
	for name := range runtimeStateFixturePaths {
		if !slices.Contains(claudeRuntimeStateNames, name) {
			t.Fatalf("fixture plants %q, which is not a runtime-state name any more", name)
		}
	}
}

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

	// Dropped: one planted path for EVERY name in the list, not a sample of it.
	// droppedRuntimeStatePaths is checked against the list itself, so a tenth name
	// added without a fixture fails rather than going untested.
	for _, rel := range droppedRuntimeStatePaths() {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(rel)), "x\n", 0o644)
	}

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
	mustCoverEveryRuntimeStateName(t)
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

	// The new arms: every name in the list, not a sample of it.
	for _, rel := range droppedRuntimeStatePaths() {
		if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was seeded into the worktree; it is Claude runtime state", rel)
		}
	}
}
