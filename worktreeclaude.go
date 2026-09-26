package main

import (
	"os"
	"path/filepath"
	"slices"
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
// The pass names each child of `.claude/` as a literal pathspec and leaves out
// `worktrees` in any case. If no other child exists, no git call runs.
// f6010b97 names the same children (measured on Linux, macOS and Windows VMs).
// Its order is not sorted on Linux or macOS, so claustrum keeps the order of
// the directory read. The `worktrees` case rule was measured on Linux only.
//
// The pathspecs go into batches, one git call per batch. See claudeDirPassCalls.
// Each call runs through hardenedGitStdout, so the config precursor runs before
// each batch. f6010b97 makes that call before each batch too (measured on Linux
// and Windows).
//
// Best-effort, like the manifest copy: the worktree already exists, so a copy
// failure does not fail the request. A failed batch is skipped, and the other
// batches still copy. The directory batches of the `.worktreeinclude` scan do
// the same.
func copyClaudeDir(repo, worktree string) {
	copyClaudeDirCalls(repo, worktree, claudeDirPassCalls(repo, gitArgvBudget))
}

// copyClaudeDirCalls runs each argv of calls and copies the files it lists.
func copyClaudeDirCalls(repo, worktree string, calls [][]string) {
	skipPrefix := claudeDirName + "/" + worktreesSubdir + "/"
	for _, args := range calls {
		out, err := hardenedGitStdout(repo, false, args...)
		if err != nil {
			continue
		}
		for _, rel := range strings.Split(out, "\x00") {
			if rel == "" || strings.HasPrefix(rel, skipPrefix) || isClaudeRuntimeState(rel) {
				continue
			}
			if dst := safeOverlayDest(worktree, rel); dst != "" {
				copyFile(filepath.Join(repo, rel), dst)
			}
		}
	}
}

// claudeDirPassFixedArgs is the fixed argv of one `.claude/` batch, up to and
// including `--`.
var claudeDirPassFixedArgs = []string{"--literal-pathspecs", "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"}

// claudeDirPassCalls returns the git argv of each `.claude/` batch, or nil when
// the pass runs no git. Readdirnames keeps the order of the directory read.
func claudeDirPassCalls(repo string, budget int) [][]string {
	dir, err := os.Open(filepath.Join(repo, claudeDirName))
	if err != nil {
		return nil
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return nil
	}
	var paths []string
	for _, name := range names {
		if !strings.EqualFold(name, worktreesSubdir) {
			paths = append(paths, claudeDirName+"/"+name)
		}
	}
	return claudeDirPassBatches(paths, budget)
}

// claudeDirPassBatches splits the `.claude/` pathspecs into git calls, in the
// given order. The fixed argv and the pathspecs of one call share budget. Each
// argument costs its length plus 3 bytes (argvCost). The fixed argv costs 87.
// When the next pathspec does not fit, the call closes and a new call starts.
// This matches the measured f6010b97 split points on Linux and Windows.
func claudeDirPassBatches(paths []string, budget int) [][]string {
	var calls [][]string
	for _, batch := range batchIncludePathspecs(paths, budget-argvCost(claudeDirPassFixedArgs)) {
		calls = append(calls, append(slices.Clone(claudeDirPassFixedArgs), batch...))
	}
	return calls
}
