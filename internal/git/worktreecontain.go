package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// worktreePathRefusal reports the reference daemon's refusal reason when
// worktreePath is not a valid session-worktree location for repo, or "" if it
// passes the location checks. verb is "create" or "remove" — the only part of the
// message that differs between the two methods.
//
// Confirmed byte-for-byte against reference build 7d193f89,
// through git.worktree_create and git.worktree_remove. The enforced predicate is
// "strictly inside baseRepo": worktreePath must be absolute, carry no ".."
// component, and clean to a path *under* the repository directory. The message
// RECOMMENDS <repository>/.claude/worktrees, but the daemon enforces only repo
// containment — a path directly under the repo with no .claude/worktrees
// component is accepted (measured: <repo>/notclaude/wt succeeded). A worktreePath
// equal to the repo itself is refused as "not inside", which is what keeps
// git.worktree_remove's delete off the repository root.
//
// The "not inside" text shows worktreePath in backticks (tickPath) and the
// repository as it is, on create and remove (be1c1ca5, measured on Linux and
// macOS). The spelling texts before it keep the path as it is (not measured),
// except the relative-path text (worktreePathSpellingRefusal).
//
// This containment is also why it subsumes the D2 home-wipe guard: a "~"-expanded
// home path is not strictly under a repo, so it is refused here — with the
// reference's own wording — before homeguard.WipesHomeDir (internal/homeguard/homeguard.go) is ever reached.
//
// Empty worktreePath is NOT judged here. Both callers answer "" with a "does not
// name a directory" text before they call this.
func worktreePathRefusal(repo, worktreePath, verb string) string {
	const guidance = "session worktrees are only created and removed under <repository>/.claude/worktrees"
	if msg := worktreePathSpellingRefusal(worktreePath, verb); msg != "" {
		return msg
	}
	if windowsPathSpellingHazard(worktreePath) {
		return fmt.Sprintf("refusing to %s worktree: %s has a component Windows reads as a different name (trailing dot or space, or a colon); choose the session folder by its absolute path, without %q",
			verb, worktreePath, "..")
	}
	if !pathStrictlyUnder(worktreePath, repo) {
		return fmt.Sprintf("refusing to %s worktree: %s is not inside the repository %s; %s",
			verb, tickPath(worktreePath), repo, guidance)
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
// refuses it as "is a symbolic link, not a worktree directory" (`f6010b97`,
// measured on macOS). A create shows the path in backticks (tickPath, be1c1ca5,
// measured on Linux, macOS and Windows). A remove shows it as it is (not measured).
func worktreeSymlinkRefusal(repo, worktreePath, verb string) string {
	link := symlinkedComponent(repo, worktreePath)
	if link == "" {
		return ""
	}
	if verb == "create" {
		link = tickPath(link)
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
// managed session worktrees.
const managedWorktreesMarker = ".claude-managed-worktrees"

// managedWorktreesRefusal is the fixed message 7d193f89 returns when a baseRepo is
// not a valid trust root, byte-for-byte.
const managedWorktreesRefusal = "baseRepo is inside a managed worktrees directory " +
	"(beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees " +
	"marker), or could not be validated as a trust root"

// baseRepoUnderManagedWorktrees reports whether repo sits inside a managed
// worktrees tree: beneath a `.claude/worktrees` directory, or in or beneath a
// directory that holds a `.claude-managed-worktrees` entry and no `.git`. b3081b51 refuses
// such a baseRepo on git.worktree_create (errorCode "nested_base_repo") and
// git.worktree_remove (no errorCode). git.list_branches ignores a baseRepo member
// (measured on Linux).
//
// The walk runs on the resolved path and on the path as spelled, so a marker
// above the path as sent counts too (b3081b51 and be1c1ca5, measured on Linux).
// The spelling walk tests the marker only, not the `.claude/worktrees` name (claustrum's own, not measured).
//
// git.worktree_create skips this test for a baseRepo that does not exist, a dangling link
// included. It answers not_a_repo then (b3081b51, measured on Linux and macOS, and on
// Windows for a missing path below a marker).
func baseRepoUnderManagedWorktrees(repo string) bool {
	if markerAboveSpelling(repo) {
		return true
	}
	p := pathcanon.Canonical(repo)
	for {
		if holdsMarkerEntry(p) {
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

// holdsMarkerEntry reports whether dir holds a `.claude-managed-worktrees` entry
// that counts. A marker beside the `.git` folder of a repository does not count, and
// the walk goes on to the parent (b3081b51, measured on Linux, macOS and Windows). That
// holds for a marker inside baseRepo beside its own `.git` too (b3081b51, measured on
// Linux, macOS and Windows). The stat test, which
// a `.git` file or link passes too, is claustrum's own, not measured. A link counts when its target
// exists (claustrum's own, not measured).
func holdsMarkerEntry(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, managedWorktreesMarker)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err != nil
}

// markerAboveSpelling reports whether repo, made absolute and cleaned but with no
// link resolved, or one of its parents holds a marker entry. A ".." is folded by
// name (claustrum's own, not measured).
func markerAboveSpelling(repo string) bool {
	p, err := filepath.Abs(repo)
	if err != nil {
		return false
	}
	for {
		if holdsMarkerEntry(p) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// baseRepoWalkFails is claustrum's own trust-root test for baseRepo. It fails a repo
// that os.Stat does not report as missing and whose walk fails (linkWalkFails). The
// test is fitted to the measured rows of f6010b97. There git.worktree_create and
// git.worktree_remove answer managedWorktreesRefusal with no git call.
//
// On Linux and macOS, measured removes and creates got that refusal. They send
// <T>/a.txt/.., <T>/loop/.. and a path through a directory with mode 000, where
// os.Stat fails with ENOTDIR, ELOOP or EACCES. On Windows os.Stat folds ".." by
// name, so <T>\missing\.. stats. f6010b97 refused it there. b3081b51 judges
// <T>\missing\.. and <T>\t.txt\.. as <T> (measured on Windows), so the walk drops a
// name before ".." first.
//
// A missing path skips the test. That is measured on Linux, macOS and
// Windows. On Windows, other measured cases stat and walk, and pass.
// repo is repoDir(): the baseRepo after the ~ expansion, or "." when it is
// absent.
//
// On Windows the walk follows a junction before "..", so <out>\J\..\repo passes and
// names <out>\repo (b3081b51, measured on Windows). A parent folder that denies List
// to the daemon no longer refuses (claustrum's own, not measured).
func baseRepoWalkFails(repo string) bool {
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return linkWalkFails(repo, true)
}

// sessionFolderSpellingRefusal is the refusal of a relative path, an empty one
// included, or of a path with a ".." component, or "" if p is neither. The text
// names p as sent. verb is "create" or "remove". A relative baseRepo with
// worktreeRoot gets this text (f6010b97, measured on Linux and macOS).
func sessionFolderSpellingRefusal(p, verb string) string {
	return spellingRefusal(p, verb, p)
}

// worktreePathSpellingRefusal is sessionFolderSpellingRefusal for worktreePath
// without worktreeRoot. Its relative-path text puts the path in backticks.
// 4c111e86 and c9125eb8 answer that text for the worktreePath "commondir" on
// create and remove (measured on Linux). macOS, Windows and a path with a
// backtick are claustrum's own, not measured. With worktreeRoot, f6010b97 named
// a relative worktreePath with no backticks (measured on Linux and macOS), so
// that path keeps sessionFolderSpellingRefusal.
func worktreePathSpellingRefusal(p, verb string) string {
	return spellingRefusal(p, verb, "`"+p+"`")
}

// spellingRefusal is the text of both spelling refusals. rel is p as the
// relative-path text names it.
func spellingRefusal(p, verb, rel string) string {
	if !filepath.IsAbs(p) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the "+
			"session folder by its absolute path, without %q", verb, rel, "..")
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
