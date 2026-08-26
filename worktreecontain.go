package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// worktreePathRefusal reports the reference daemon's refusal reason when
// worktreePath is not a valid session-worktree location for repo, or "" if it
// passes the location checks. verb is "create" or "remove" — the only part of the
// message that differs between the two methods.
//
// Confirmed byte-for-byte against reference build 7d193f89 on an ephemeral VM,
// through git.worktree_create and git.worktree_remove. The enforced predicate is
// "strictly inside baseRepo": worktreePath must be absolute, carry no ".."
// component, and clean to a path *under* the repository directory. The message
// RECOMMENDS <repository>/.claude/worktrees, but the daemon enforces only repo
// containment — a path directly under the repo with no .claude/worktrees
// component is accepted (measured: <repo>/notclaude/wt succeeded). A worktreePath
// equal to the repo itself is refused as "not inside", which is what keeps
// git.worktree_remove's os.RemoveAll fallback off the repository root.
//
// This containment is also why it subsumes the D2 home-wipe guard: a "~"-expanded
// home path is not strictly under a repo, so it is refused here — with the
// reference's own wording — before wipesHomeDir (homeguard.go) is ever reached.
//
// Empty worktreePath is NOT judged here. The reference does not treat "" as a
// relative path; it fails later with a "does not name a directory" message, so
// the two callers special-case "" before calling this.
func worktreePathRefusal(repo, worktreePath, verb string) string {
	const guidance = "session worktrees are only created and removed under <repository>/.claude/worktrees"
	if !filepath.IsAbs(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the session folder by its absolute path, without %q",
			verb, worktreePath, "..")
	}
	if pathHasDotDot(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s contains a %q component; choose the session folder by its absolute path, without %q",
			verb, worktreePath, "..", "..")
	}
	if !pathStrictlyUnder(worktreePath, repo) {
		return fmt.Sprintf("refusing to %s worktree: %s is not inside the repository %s; %s",
			verb, worktreePath, repo, guidance)
	}
	return ""
}

// pathHasDotDot reports whether any path segment is exactly "..". The check is on
// the raw (already ~-expanded) path, before any cleaning — the reference reports
// the ".." as present, so a filepath.Clean that resolved it away would change the
// frame. Both separators are considered so a Windows-style path is judged too.
func pathHasDotDot(p string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

// pathStrictlyUnder reports whether p cleans to a path strictly below root. Equal
// paths return false: worktreePath == repo is "not inside" the repo, matching the
// reference and keeping the repository root out of reach of the remove fallback.
// By the time this runs the caller has already refused "..", so cleaning cannot
// resolve p out of the tree.
func pathStrictlyUnder(p, root string) bool {
	cp := filepath.Clean(p)
	cr := filepath.Clean(root)
	return cp != cr && strings.HasPrefix(cp, cr+string(os.PathSeparator))
}
