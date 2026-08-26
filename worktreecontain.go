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

// worktreeSymlinkRefusal returns the reference's refusal when a component of
// worktreePath BELOW baseRepo — excluding the leaf — is a symbolic link, or "" if
// none is. A planted `.claude` / `.claude/worktrees` symlink could carry a create
// outside the repo or point the destructive remove fallback at an external target,
// so 7d193f89 refuses it by name (verb is "create" or "remove"). A symlinked LEAF
// is not caught here — it exists, so the caller's freshness check refuses it as
// "already exists" instead (measured). Confirmed byte-for-byte against 7d193f89.
func worktreeSymlinkRefusal(repo, worktreePath, verb string) string {
	link := symlinkedComponent(repo, worktreePath)
	if link == "" {
		return ""
	}
	return fmt.Sprintf("refusing to %s worktree: %s is a symbolic link; a symlinked "+
		".claude or .claude/worktrees inside the repository is not supported for SSH "+
		"sessions, because a repository can plant such a link and a planted one cannot "+
		"reliably be told apart from your own. Replace it with a real directory (or "+
		"delete it and it will be recreated)", verb, link)
}

// symlinkedComponent walks the components of worktreePath below repo, EXCLUDING the
// leaf, and returns the first one that exists and is a symbolic link (or ""). Runs
// only after the caller has confirmed worktreePath is strictly inside repo.
func symlinkedComponent(repo, worktreePath string) string {
	rel, err := filepath.Rel(repo, worktreePath)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return ""
	}
	cur := repo
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return cur
		}
	}
	return ""
}

// managedWorktreesMarker is the file 7d193f89 uses to tag a directory as holding
// managed session worktrees. A baseRepo beneath such a directory (or beneath a
// literal `.claude/worktrees`) is refused as an invalid trust root — a session
// worktree must be created from a real top-level repository, never from inside
// another session's worktree tree.
const managedWorktreesMarker = ".claude-managed-worktrees"

// managedWorktreesRefusal is the fixed message 7d193f89 returns when a baseRepo is
// not a valid trust root, byte-for-byte.
const managedWorktreesRefusal = "baseRepo is inside a managed worktrees directory " +
	"(beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees " +
	"marker), or could not be validated as a trust root"

// baseRepoUnderManagedWorktrees reports whether repo sits inside a managed
// worktrees tree: beneath a `.claude/worktrees` directory, or beneath any ancestor
// directory that holds a `.claude-managed-worktrees` marker file. 7d193f89 refuses
// such a baseRepo on git.worktree_create (errorCode "nested_base_repo"),
// git.worktree_remove (no errorCode), and git.list_branches (bare isRepo:false).
// Confirmed byte-for-byte against 7d193f89 on an ephemeral VM.
func baseRepoUnderManagedWorktrees(repo string) bool {
	p := canonicalPath(repo)
	for {
		if fi, err := os.Stat(filepath.Join(p, managedWorktreesMarker)); err == nil && !fi.IsDir() {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		if filepath.Base(p) == worktreesSubdir && filepath.Base(parent) == claudeDirName {
			return true
		}
		p = parent
	}
}

// pathHasDotDot reports whether any path segment is exactly "..". The check is on
// the raw (already ~-expanded) path, before any cleaning — the reference reports
// the ".." as present, so a filepath.Clean that resolved it away would change the
// frame. Both separators are considered so a Windows-style path is judged too.
func pathHasDotDot(p string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

// pathStrictlyUnder reports whether p resolves to a path strictly below root. It uses
// filepath.Rel + filepath.IsLocal rather than a byte-wise strings.HasPrefix so the
// predicate follows the platform's own path-equality rules: filepath.Rel compares
// path components case-insensitively on Windows, so a worktreePath that differs from
// baseRepo only in drive-letter or directory-name case is still judged inside. This
// matches the reference's observable containment — a case-variant worktreePath under
// baseRepo is accepted on Windows (measured on the Windows 7d193f89 build) — where the
// earlier case-sensitive strings.HasPrefix wrongly refused it. p is under root iff
// Rel(root, p) succeeds, is not "." (equal paths are "not inside" the repo, keeping the
// repository root out of reach of the remove fallback), and is filepath-local (no ".."
// escape). By the time this runs the caller has already refused a raw ".." component.
func pathStrictlyUnder(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != "." && filepath.IsLocal(rel)
}
