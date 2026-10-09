package main

import (
	"errors"
	"fmt"
	"io/fs"
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
// git.worktree_remove's delete off the repository root.
//
// This containment is also why it subsumes the D2 home-wipe guard: a "~"-expanded
// home path is not strictly under a repo, so it is refused here — with the
// reference's own wording — before wipesHomeDir (homeguard.go) is ever reached.
//
// Empty worktreePath is NOT judged here. Both callers answer "" with a "does not
// name a directory" text before they call this.
func worktreePathRefusal(repo, worktreePath, verb string) string {
	const guidance = "session worktrees are only created and removed under <repository>/.claude/worktrees"
	if msg := sessionFolderSpellingRefusal(worktreePath, verb); msg != "" {
		return msg
	}
	if windowsPathSpellingHazard(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s has a component Windows reads as a different name (trailing dot or space, or a colon); choose the session folder by its absolute path, without %q",
			verb, worktreePath, "..")
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
// outside the repo or point the destructive remove at an external target,
// so 7d193f89 refuses it by name (verb is "create" or "remove"). The refusal text
// is confirmed byte-for-byte against 7d193f89. A symlinked LEAF
// is not caught here. Create refuses it as "already exists" (measured). Remove
// refuses it as "is a symbolic link, not a worktree directory" (f6010b97, measured
// on a macOS VM).
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
// such a baseRepo on git.worktree_create (errorCode "nested_base_repo") and
// git.worktree_remove (no errorCode). git.list_branches tests its path and its
// baseRepo (docs/record/git-list-branches.md).
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

// baseRepoWalkFails is claustrum's own trust-root test for baseRepo. It fails a repo
// that os.Stat does not report as missing and that the symlink walk of
// filepath.EvalSymlinks cannot resolve. The test is fitted to the measured rows of
// f6010b97. There git.worktree_create and git.worktree_remove answer
// managedWorktreesRefusal with no git call.
//
// On Linux and macOS, rows G1, G2 and G4 (remove) and G1c, G2c and G4c (create) got
// that refusal. They send <T>/a.txt/.., <T>/loop/.. and a path through a directory
// with mode 000, where os.Stat fails with ENOTDIR, ELOOP or EACCES. On Windows os.Stat
// folds ".." by name, so <T>\missing\.. stats while the walk fails at "missing". On
// the Windows VM, round 1 rows A1, A1f, A2, A3, O1 to O4, C1 and WR1 got the refusal.
// So did round 2 rows K1, D1, D2, D4, J1 and J3.
//
// A missing path skips the test. That is round 1 row A1 and rows G3 and G3c on Linux
// and macOS, and round 1 row WX1 on the Windows VM. On the Windows VM, round 1 rows A4
// and A8 stat and walk, and pass. So do round 2 rows K0, D3, J2, L1, L2, Q1, Q2 and
// S1. repo is repoDir(): the baseRepo after the ~ expansion, or "." when it is absent.
//
// On Windows, after its walk, filepath.EvalSymlinks looks up each component of the
// resolved path with FindFirstFile, which lists its parent. So a parent that denies List to the
// daemon can fail EvalSymlinks after os.Stat passed, and claustrum then refuses. No
// row measured that case.
func baseRepoWalkFails(repo string) bool {
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, err := evalSymlinks(repo)
	return err != nil
}

// sessionFolderSpellingRefusal is the refusal of a relative path, an empty one
// included, or of a path with a ".." component, or "" if p is neither. The text
// names p as sent. verb is "create" or "remove".
func sessionFolderSpellingRefusal(p, verb string) string {
	if !filepath.IsAbs(p) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the "+
			"session folder by its absolute path, without %q", verb, p, "..")
	}
	if pathHasDotDot(p) {
		return fmt.Sprintf("refusing to %s worktree: %s contains a %q component; choose the "+
			"session folder by its absolute path, without %q", verb, p, "..", "..")
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

// pathStrictlyUnder reports whether p resolves to a path strictly below root. It uses
// filepath.Rel + filepath.IsLocal rather than a byte-wise strings.HasPrefix so the
// predicate follows the platform's own path-equality rules: filepath.Rel compares
// path components case-insensitively on Windows, so a worktreePath that differs from
// baseRepo only in drive-letter or directory-name case is still judged inside. This
// matches the reference's observable containment — a case-variant worktreePath under
// baseRepo is accepted on Windows (measured on the Windows 7d193f89 build) — where the
// earlier case-sensitive strings.HasPrefix wrongly refused it. p is under root iff
// Rel(root, p) succeeds, is not "." (equal paths are "not inside" the repo, keeping the
// repository root out of reach of the remove), and is filepath-local (no ".."
// escape). By the time this runs the caller has already refused a raw ".." component.
func pathStrictlyUnder(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != "." && filepath.IsLocal(rel)
}
