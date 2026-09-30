package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// External worktrees (the "Worktree location" capability, added by reference build
// 7d193f89): git.worktree_create / git.worktree_remove accept a worktreeRoot, and
// the session folder is then created OUTSIDE the repository, beneath that root, at
// exactly <worktreeRoot>/<directory>/<name> — two components below the root. When
// worktreeRoot is set the in-repo containment (worktreePathRefusal) is replaced by
// the checks here. Every message and the marker bytes below were measured
// byte-for-byte against 7d193f89 on an ephemeral VM.

// worktreeExternalSpellingRefusal and worktreeExternalShapeRefusal report the
// reference's refusal when an external worktreeRoot/worktreePath pair is not a valid
// location, or "" if it is. verb is "create" or "remove". Checks run in the
// reference's measured order:
//
//  1. worktreeRoot must be absolute and carry no ".." component — its refusals name
//     the root and use the "worktree location … beneath the filesystem root" wording;
//  2. worktreePath must be absolute (an empty path reads as relative) and carry no
//     ".." — same wording as the in-repo containment ("session folder");
//  3. worktreePath must be exactly <worktreeRoot>/<directory>/<name>.
//
// The ".." checks run on the raw path before any filepath.Clean, so a path that
// would clean back to a valid location is still refused — matching 7d193f89.
// worktreeExternalSpellingRefusal is steps 1 and 2. Both worktree methods check
// baseRepo between steps 2 and 3. On git.worktree_create that order is not measured.
func worktreeExternalSpellingRefusal(worktreeRoot, worktreePath, verb string) string {
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
	return sessionFolderSpellingRefusal(worktreePath, verb)
}

// worktreeExternalShapeRefusal is step 3 of the checks listed above
// worktreeExternalSpellingRefusal. Callers run worktreeExternalSpellingRefusal first,
// so worktreePath is absolute and has no ".." component here.
func worktreeExternalShapeRefusal(worktreeRoot, worktreePath, verb string) string {
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
// the registration checks on remove. The <directory> comes
// from the cleaned path, so a worktreePath that ends in a slash names its real
// parent (see externalWorktreeDirNotEmptyRefusal).
func worktreeExternalDirSymlinkRefusal(worktreePath, verb string) string {
	dir := filepath.Dir(filepath.Clean(worktreePath))
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("refusing to %s worktree: %s is a symbolic link; the directory "+
			"under the worktree location must be a real directory", verb, dir)
	}
	return ""
}

// verifyGitDir is the git directory whose worktrees directory git.worktree_remove
// reads. It is the repository git directory that the trust check pinned for baseRepo
// (pruneGitDir). For a linked worktree used as baseRepo, that is the git directory of
// the main repository. Its own `.git` is a file, so <baseRepo>/.git/worktrees never
// exists. When the pin is <baseRepo>/.git itself, the path keeps the spelling of
// baseRepo as given, so the transient text does not change.
//
// f6010b97 reads the worktrees directory of that repository git directory. Measured
// on a Linux VM with worktreeRoot set:
//   - A linked worktree as baseRepo removes a worktree that the main repository
//     registered. The worktree, its entry and its branch go, and the answer is
//     {"success":true} (rows WR07 and K08).
//   - The same holds when the main repository carries a stray commondir (row WR14).
//     The check judges the git directory of baseRepo, which is the entry of the
//     linked worktree. The main repository's git directory is only the pin, and
//     the check does not judge it again.
//   - A subdirectory of a repository and a submodule as baseRepo also remove a
//     registered worktree (rows K02 and K09).
//   - With a daemon GIT_DIR that names another repository X, the transient text
//     names X/.git/worktrees (row K15).
func verifyGitDir(baseRepo string) string {
	asGiven := filepath.Join(baseRepo, ".git")
	g := pruneGitDir(baseRepo)
	if canonicalPath(g) == canonicalPath(asGiven) {
		return asGiven
	}
	return g
}

// workTreeUnknownPrefix starts every refusal of git.worktree_remove with worktreeRoot
// that comes before the registration check. See externalWorkTreeRefusal.
const workTreeUnknownPrefix = "failed to remove worktree: cannot determine the repository's work tree: "

// externalWorkTreeRefusal is the check of git.worktree_remove with worktreeRoot that
// comes after the containment checks and before the dir-symlink check. It answers ""
// when the removal goes on. Otherwise it answers workTreeUnknownPrefix and a reason,
// and nothing is deleted. It runs before the "already gone" test, so a missing
// worktree gets the same answer. The reasons, in order:
//
//  1. Git cannot start in baseRepo (unenterableBaseListing). The reason is the hooks
//     refusal with the start error (rows K10, K11 and K14).
//  2. The trust check refuses the git directory of baseRepo. The reason is the
//     trust refusal text (rows WR01 to WR06, and WR08 with a plain folder).
//  3. The trust check finds no repository. The reason is "exit status 128" (rows
//     WR10 to WR13, K03 and K05). A `.git` file with a gone target that is not an
//     entry goes on to reason 4 (row K16).
//  4. The configuration cannot be listed. The reason is the hooks refusal text
//     (rows WR15, K06 and K16).
//  5. baseRepo is itself a git directory. The reason is "exit status 128" (rows
//     K07 and K25). A work tree whose own repository has core.bare set still passes
//     (row K17).
//  6. The hardened `git rev-parse --absolute-git-dir` fails in baseRepo. The reason
//     is the exec error, for example "exit status 128" (rows K12, K18 and K21).
//
// A baseRepo that does not exist skips every reason. Every row, and the order against
// the containment and dir-symlink checks, was measured side by side against f6010b97
// on a Linux VM.
//
// Before these reasons comes the check of the daemon's own GIT_CONFIG_COUNT, for a
// baseRepo that exists. When it refuses, the reason is the trust refusal text if the
// trust check refuses, and else the count refusal. See docs/PROTOCOL.md, "The
// daemon's own git environment".
func externalWorkTreeRefusal(repo string) string {
	if _, err := os.Stat(repo); err == nil {
		if msg, bad := daemonCountRefusal(); bad {
			if t := requestGitDirTrust(repo, false); t.verdict == gitDirRefused {
				msg = t.refusal
			}
			return workTreeUnknownPrefix + msg
		}
	}
	if err := unenterableBaseListing(repo); err != nil {
		return workTreeUnknownPrefix + hooksRefusalPrefix + err.Error()
	}
	switch t := requestGitDirTrust(repo, false); t.verdict {
	case gitDirRefused:
		return workTreeUnknownPrefix + t.refusal
	case gitDirNoRepo:
		if !goneNonEntryGitFile(repo) {
			return workTreeUnknownPrefix + "exit status 128"
		}
	}
	// This listing is light, and the rev-parse below runs its own heavy one. That is
	// the order of the listings of f6010b97 here (Linux VM, rows WR00 to WR15).
	if detail, bad := hostileConfigRefusal(repo, false); bad {
		return workTreeUnknownPrefix + detail
	}
	// The call after that light listing is `rev-parse --show-toplevel` in baseRepo,
	// on f6010b97 (Linux VM, rows WR00, WR07, WR09 and WR14). claustrum makes the
	// call and does not use its answer. The checks below decide.
	hardenedGitFirst(repo, false, "rev-parse", "--show-toplevel")
	if baseIsGitDir(repo) {
		return workTreeUnknownPrefix + "exit status 128"
	}
	if err := repositoryCheckError(repo, true); err != nil {
		return workTreeUnknownPrefix + err.Error()
	}
	return ""
}

// externalWorktreeListing makes the two git calls that f6010b97 makes before it
// decides whether worktreePath is a registered worktree of baseRepo: a light
// `worktree list --porcelain -z`, then a heavy `rev-parse --absolute-git-dir`, each
// in baseRepo with its listing. Measured on a Linux VM (rows WR00, WR07, WR09 and
// WR14). claustrum does not use their answers.
func externalWorktreeListing(repo string) {
	hardenedGit(repo, false, "worktree", "list", "--porcelain", "-z")
	hardenedGit(repo, true, "rev-parse", "--absolute-git-dir")
}

// unenterableBaseListing covers a baseRepo that exists but that git cannot start in:
// a regular file, or a directory without search permission (mode 0600). It then runs
// the configuration listing with baseRepo as the working directory and returns the
// start error. Go names the git that PATH resolves, for example "fork/exec
// /usr/bin/git: permission denied" or "fork/exec /usr/bin/git: not a directory". The
// frame matches f6010b97, measured side by side on a Linux VM (rows K10, K11 and K14).
// It returns nil for every other baseRepo and runs nothing then.
func unenterableBaseListing(repo string) error {
	fi, err := os.Stat(repo)
	if err != nil {
		return nil
	}
	if fi.IsDir() {
		if _, err := os.Lstat(filepath.Join(repo, ".git")); !errors.Is(err, fs.ErrPermission) {
			return nil
		}
	}
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "-z", "--list", "--name-only")
	cmd.Dir = repo
	// This listing does not use precursorEnv. Git never starts here, so its
	// environment never reaches git, and only the Go start error reaches the frame.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh")
	var ee *exec.ExitError
	if _, err := cmd.Output(); err != nil && !errors.As(err, &ee) {
		return err
	}
	return nil
}

// goneNonEntryGitFile reports whether the walk of the trust check found a `.git` file
// whose target is gone and is not shaped like a linked-worktree entry. The trust check
// answers "no repository" there. f6010b97 instead runs git, whose configuration
// listing fails, and answers the hooks refusal. A gone entry under .git/worktrees
// still answers "exit status 128". Measured side by side against f6010b97 on a Linux
// VM (rows K16 and WR13). A daemon GIT_DIR skips the walk, so it reports false then.
func goneNonEntryGitFile(repo string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return false
	}
	start, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false
	}
	if start, err = filepath.Abs(start); err != nil {
		return false
	}
	g, ok := findGitDir(start)
	if !ok {
		return false
	}
	_, err = os.Lstat(g)
	return errors.Is(err, fs.ErrNotExist) && !isWorktreeEntry(g)
}

// baseIsGitDir reports whether the walk of the trust check finds repo itself as the
// git directory, as for a bare repository or a .git directory. A daemon GIT_DIR skips
// the walk, so it reports false then. A directory inside a git directory is not
// measured, so it is not covered.
func baseIsGitDir(repo string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return false
	}
	start, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false
	}
	if start, err = filepath.Abs(start); err != nil {
		return false
	}
	g, ok := findGitDir(start)
	return ok && g == start
}

// externalWorktreeDirNotEmptyRefusal reports 7d193f89's refusal when the
// <directory> level (filepath.Dir(worktreePath)) already exists, carries no
// .claude-managed-worktrees marker, and is not empty — the per-repository directory
// under a worktree location must start out empty (or already be a managed one) so a
// create cannot silently graft a worktree into a directory of unrelated files.
// Returns "" when the directory is absent, empty, or already marked. The example
// filename is the first entry in os.ReadDir's sorted order — wire-visible, measured
// byte-for-byte against 7d193f89.
//
// The <directory> comes from the cleaned path. For "R/proj/w1/", filepath.Dir of
// the raw path is "R/proj/w1", not "R/proj". f6010b97 and 90fca6e6 refuse that
// create and name "R/proj" (measured on Linux and macOS VMs).
func externalWorktreeDirNotEmptyRefusal(worktreePath string) string {
	dir := filepath.Dir(filepath.Clean(worktreePath))
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
// .claude-managed-worktrees marker on an external create (285 bytes,
// sha256 45da7f8a…). A client can read it via files.read, so the bytes are part of
// the observable contract and are reproduced verbatim — like the version spoof, this
// is measured reference OUTPUT, not copied source.
const managedWorktreesMarkerBody = "This directory holds Claude Code session worktrees placed here by the\n" +
	"claude-ssh daemon (the \"Worktree location\" setting). Its subdirectories are\n" +
	"managed checkouts; open the repository itself, not one of them, as a\n" +
	"session folder. Safe to delete once the directory is otherwise empty.\n"

// ensureManagedWorktreesMarker writes the marker into dir, the <directory> level of
// an external worktree with the symlinks of its root resolved. claustrum writes it
// after the parent step and before `git worktree add`. The directory is then
// tagged as managed before git runs.
//
// It creates the marker only when no entry of that name exists. An existing entry
// of any kind keeps its content and its mode, and a failed add keeps the marker.
// Any other open error stops the create with "cannot mark <dir> as a worktree
// location: openat .claude-managed-worktrees: <errno>". f6010b97 does the same on
// Linux and macOS VMs. Not measured: a write or close that fails after the open.
// The create goes on then.
func ensureManagedWorktreesMarker(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, managedWorktreesMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("cannot mark %s as a worktree location: openat %s: %w", dir, managedWorktreesMarker, err)
	}
	_, _ = f.WriteString(managedWorktreesMarkerBody)
	_ = f.Close()
	return nil
}
