package main

import (
	"path/filepath"
	"strings"
)

// Seeding a new worktree with the repo's `.claude/` directory, and the rule that
// keeps Claude's own runtime state out of it.
//
// Both halves are measured against the reference on an ephemeral linux VM, with a
// repo whose `.claude/` is git-ignored (scratch/probe/worktreecopy_probe.py and
// scratch/probe/ref90_probe.py):
//
//   - 19f30c46 and 90fca6e6 both copy the git-ignored files under `.claude/` into
//     a new worktree, with no `.worktreeinclude` entry involved at all.
//   - 90fca6e6 additionally drops the runtime-state paths listed below. 19f30c46
//     copies eight of the nine. The ninth is `worktrees`, which both builds drop.
//
// The fixture matters. `git ls-files --others --ignored --exclude-standard`
// returns nothing when `.claude/` is merely untracked, so a probe repo without
// `.claude/` in its `.gitignore` shows an empty copy and reads as "no copy at
// all". That is how the earlier "7d193f89 no longer copies .claude/" conclusion
// was reached.

// claudeRuntimeStateNames are the entries under `.claude/` that are Claude's own
// runtime state rather than project configuration. A new worktree must not
// inherit them: they are per-session bookkeeping, and a copy makes the new
// worktree act on another session's queue.
//
// Every name is measured, one fixture file per name: 90fca6e6 drops all nine, and
// 19f30c46 copies eight of them. It drops `worktrees` as well.
//
// Sorted. The predicate is a disjunction, so the order cannot change an answer.
var claudeRuntimeStateNames = []string{
	"agent-registry.json",
	"assistant-daemon-state.json",
	"checkpoints",
	"first-run",
	"mailbox",
	"routines/.state",
	"scheduled_tasks.json",
	"scheduled_tasks.lock",
	"worktrees",
}

// isClaudeRuntimeState reports whether rel names one of the runtime-state entries
// directly under a `.claude/` directory, or anything inside one.
//
// The behaviour, all four parts measured against 90fca6e6:
//   - whole path components, so `.claude/mailboxes/keep.txt` is COPIED
//   - anchored directly under a `.claude/`, so `.claude/nested/mailbox/deep.txt`
//     is COPIED
//   - but that `.claude/` need not be the repo root's: `sub/.claude/mailbox/m1.txt`
//     is DROPPED (scratch/probe/nestedclaude_probe.py)
//   - case-insensitive, so a `Checkpoints` directory is DROPPED
//
// Only the manifest pass can reach a nested `.claude/`. copyClaudeDir's listing is
// pathspec-limited to the repo-root `.claude/`, so it never names one.
//
// claustrum implements that by lowercasing, wrapping the path in slashes and
// substring-matching.
//
// rel is repo-relative with forward slashes, the form `git ls-files` prints.
func isClaudeRuntimeState(rel string) bool {
	s := "/" + strings.ToLower(filepath.ToSlash(rel)) + "/"
	for _, name := range claudeRuntimeStateNames {
		if strings.Contains(s, "/"+claudeDirName+"/"+name+"/") {
			return true
		}
	}
	return false
}

// copyClaudeDir seeds a new worktree with the repo's git-ignored `.claude/` files.
//
// `git worktree add` checks out tracked files only, so an ignored `.claude/`
// arrives empty. The reference fills it from the base repo, which is what carries
// a project's local Claude settings into a session worktree.
//
// Two exclusions, both from the reference:
//
//   - `.claude/worktrees/` is skipped. That is where session worktrees live, so
//     copying it would copy the worktree into itself.
//   - a runtime-state path is skipped (90fca6e6 only; see isClaudeRuntimeState).
//
// `-z` here is not optional. git C-quotes any path with a tab, a quote, a
// backslash or a non-ASCII byte, and the quoted form names no real file.
//
// Best-effort, like the manifest copy: the worktree already exists, so a copy
// failure does not fail the request.
func copyClaudeDir(repo, worktree string) {
	out, err := hardenedGitStdout(repo, false, "ls-files", "--others", "--ignored",
		"--exclude-standard", "-z", "--", claudeDirName+"/")
	if err != nil || out == "" {
		return
	}
	skipPrefix := claudeDirName + "/" + worktreesSubdir + "/"
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" || strings.HasPrefix(rel, skipPrefix) || isClaudeRuntimeState(rel) {
			continue
		}
		if dst := safeOverlayDest(worktree, rel); dst != "" {
			copyFile(filepath.Join(repo, rel), dst)
		}
	}
}
