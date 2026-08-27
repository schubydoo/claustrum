package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// External worktrees (the "Worktree location" capability, added by reference build
// 7d193f89): git.worktree_create / git.worktree_remove accept a worktreeRoot, and
// the session folder is then created OUTSIDE the repository, beneath that root, at
// exactly <worktreeRoot>/<directory>/<name> — two components below the root. When
// worktreeRoot is set the in-repo containment (worktreePathRefusal) is replaced by
// the checks here. Every message and the marker bytes below were measured
// byte-for-byte against 7d193f89 on an ephemeral VM.

// worktreeExternalContainmentRefusal reports the reference's refusal when an
// external worktreeRoot/worktreePath pair is not a valid location, or "" if it is.
// verb is "create" or "remove". Checks run in the reference's measured order:
//
//  1. worktreeRoot must be absolute and carry no ".." component — its refusals name
//     the root and use the "worktree location … beneath the filesystem root" wording;
//  2. worktreePath must be absolute (an empty path reads as relative) and carry no
//     ".." — same wording as the in-repo containment ("session folder");
//  3. worktreePath must be exactly <worktreeRoot>/<directory>/<name>.
//
// The ".." checks run on the raw path before any filepath.Clean, so a path that
// would clean back to a valid location is still refused — matching 7d193f89.
func worktreeExternalContainmentRefusal(worktreeRoot, worktreePath, verb string) string {
	if !filepath.IsAbs(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..")
	}
	if pathHasDotDot(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s contains a %q component; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..", "..")
	}
	if !filepath.IsAbs(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the "+
			"session folder by its absolute path, without %q", verb, worktreePath, "..")
	}
	if pathHasDotDot(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s contains a %q component; choose the "+
			"session folder by its absolute path, without %q", verb, worktreePath, "..", "..")
	}
	root := filepath.Clean(worktreeRoot)
	wp := filepath.Clean(worktreePath)
	if filepath.Dir(filepath.Dir(wp)) != root {
		return fmt.Sprintf("refusing to %s worktree: %s is not "+
			"<worktree location>/<directory>/<name> beneath %s", verb, wp, root)
	}
	return ""
}

// worktreeExternalDirSymlinkRefusal reports 7d193f89's refusal when the <directory>
// level (filepath.Dir(worktreePath)) is a symbolic link, or "" if it is a real
// directory (or absent). The worktreeRoot itself MAY be a symlink — only the
// per-repository directory beneath it must be real, so a repo cannot plant a link
// that carries the create out of the chosen location. verb is "create" or "remove"
// (create carries errorCode unsafe_path; remove carries none). Measured against
// 7d193f89: this runs after the ownership/writability checks on create, and before
// the registration verify (externalWorktreeVerify) on remove.
func worktreeExternalDirSymlinkRefusal(worktreePath, verb string) string {
	dir := filepath.Dir(worktreePath)
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("refusing to %s worktree: %s is a symbolic link; the directory "+
			"under the worktree location must be a real directory", verb, dir)
	}
	return ""
}

// externalWorktreeVerify reproduces 7d193f89's "is worktreePath a registered worktree
// of baseRepo" check on the external-remove path, whose non-locked git failure would
// otherwise recursively os.RemoveAll the path. The reference REFUSES (leaves the path in
// place) unless it can positively confirm a genuine registration — unlike an in-repo
// remove, which deletes a plain directory as a fallback (measured: in-repo plain dir is
// deleted at 7d193f89, an external one is not). It returns:
//   - ("", false) when the path may be removed: a genuine registration, a GHOST whose
//     admin dir is gone (the reference cleans those up), or a path already gone;
//   - (reason, false) to REFUSE and leave it in place — the caller wraps it as
//     "... is not a worktree of <baseRepo> (<reason>), so it is left in place; ...";
//   - (detail, true) when the daemon cannot decide (baseRepo's worktrees dir is
//     unreadable) — the caller wraps it as "could not verify that ... (<detail>); retry".
//
// A linked worktree's `.git` is a `gitdir:` pointer FILE naming an admin directory
// <gitdir>/worktrees/<name> under baseRepo, whose own `gitdir` record points back at this
// worktree. Each reason string, and the delete/leave/transient split, was measured
// byte-for-byte against 7d193f89 on an ephemeral VM.
func externalWorktreeVerify(baseRepo, worktreePath string) (reason string, transient bool) {
	wp := filepath.Clean(worktreePath)
	if _, err := os.Stat(wp); err != nil {
		// Already gone — os.RemoveAll of a missing path is a nil no-op → success:true.
		return "", false
	}
	gp := filepath.Join(wp, ".git")
	fi, err := os.Stat(gp)
	if err != nil {
		return wp + " has no .git file", false
	}
	if !fi.Mode().IsRegular() {
		return gp + " is not a regular file", false
	}
	b, err := os.ReadFile(gp)
	if err != nil {
		return gp + " does not name a git dir", false
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return gp + " does not name a git dir", false
	}
	admin := filepath.Clean(strings.TrimSpace(line[len(prefix):]))
	const notOurs = " carries a .git file that does not name this repository's own worktree admin directory"
	// A worktree admin dir is <gitdir>/worktrees/<name>; if the parent component is not
	// "worktrees" it cannot be one, and the reference does not even consult baseRepo.
	if filepath.Base(filepath.Dir(admin)) != worktreesSubdir {
		return wp + notOurs, false
	}
	// It is shaped like a worktree admin — confirm it is baseRepo's OWN. The reference
	// opens baseRepo's worktrees directory; if that fails it cannot decide → transient.
	baseWT := filepath.Join(baseRepo, ".git", worktreesSubdir)
	d, err := os.Open(baseWT)
	if err != nil {
		return err.Error(), true // e.g. "open <baseRepo>/.git/worktrees: no such file or directory"
	}
	_ = d.Close()
	if canonicalPath(filepath.Dir(admin)) != canonicalPath(baseWT) {
		return wp + notOurs, false
	}
	// Under baseRepo's own worktrees. A ghost admin dir (registration gone) may be
	// removed as a stale leftover; a present one must record THIS worktree.
	if _, err := os.Stat(admin); err != nil {
		return "", false
	}
	if !worktreeAdminBelongsTo(admin, wp) {
		return wp + " carries a .git file naming an admin directory whose own record is of a different worktree", false
	}
	return "", false
}

// externalWorktreeDirNotEmptyRefusal reports 7d193f89's refusal when the
// <directory> level (filepath.Dir(worktreePath)) already exists, carries no
// .claude-managed-worktrees marker, and is not empty — the per-repository directory
// under a worktree location must start out empty (or already be a managed one) so a
// create cannot silently graft a worktree into a directory of unrelated files.
// Returns "" when the directory is absent, empty, or already marked. The example
// filename is the first entry in os.ReadDir's sorted order — wire-visible, measured
// byte-for-byte against 7d193f89.
func externalWorktreeDirNotEmptyRefusal(worktreePath string) string {
	dir := filepath.Dir(worktreePath)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	for _, e := range entries {
		if e.Name() == managedWorktreesMarker {
			return ""
		}
	}
	return fmt.Sprintf("refusing to create worktree: %s already exists, is not marked as a "+
		"worktree directory, and holds other files (for example %q); the per-repository "+
		"directory under a worktree location must start out empty — remove it, restore "+
		"its .claude-managed-worktrees file if you deleted it, or choose another location",
		dir, entries[0].Name())
}

// managedWorktreesMarkerBody is the exact content 7d193f89 writes into a
// .claude-managed-worktrees marker on a successful external create (285 bytes,
// sha256 45da7f8a…). A client can read it via files.read, so the bytes are part of
// the observable contract and are reproduced verbatim — like the version spoof, this
// is measured reference OUTPUT, not copied source.
const managedWorktreesMarkerBody = "This directory holds Claude Code session worktrees placed here by the\n" +
	"claude-ssh daemon (the \"Worktree location\" setting). Its subdirectories are\n" +
	"managed checkouts; open the repository itself, not one of them, as a\n" +
	"session folder. Safe to delete once the directory is otherwise empty.\n"

// ensureManagedWorktreesMarker writes the marker into the <directory> level of an
// external worktree (filepath.Dir(worktreePath)). claustrum writes it before `git
// worktree add`, so the directory is tagged as managed as soon as it is committed to
// holding a worktree; best-effort — the directory was just created and is owned by
// the daemon user. (A successful create writing the marker was measured against
// 7d193f89; the reference's behavior on a subsequent add failure was not.)
func ensureManagedWorktreesMarker(dir string) error {
	return os.WriteFile(filepath.Join(dir, managedWorktreesMarker), []byte(managedWorktreesMarkerBody), 0o644)
}
