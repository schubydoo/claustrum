package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// unusableGitText is the refusal of git.worktree_create for a `.git` that git cannot
// use. %s is the path of that `.git`, in backticks and not quoted (4c111e86, measured
// on Linux, macOS and Windows).
const unusableGitText = "Git can't use `%s`, so Claude made no worktree. A Claude session can damage it, so check it first. Then repair it, or turn off Worktree if this isn't meant to be a repository."

// notARepoResult is the answer of a create that finds no repository at repo. 4c111e86
// answers worktree_add_failed with unusableGitText where the walk met a `.git` that git
// cannot use (unusableDotGit), and not_a_repo otherwise. Measured on Linux, macOS and
// Windows: an empty `.git` folder, one with no HEAD and a nested empty `.git` below a good
// repository. So is a `.git` file with no `gitdir: ` line. A garbage HEAD is measured on
// Linux and Windows, and a `.git` folder of junk only on Linux. A `.git` file that is
// empty, names a missing path or names an empty folder is measured on Windows only. A
// dangling `.git` link and a `.git` FIFO are measured on Linux and macOS. The git calls
// before the answer stay as they were (measured on Linux).
func notARepoResult(id interface{}, repo string) rpc.Response {
	if g := unusableDotGit(repo); g != "" {
		return rpc.OKResult(id, rpc.WorktreeResult{Success: false, Error: fmt.Sprintf(unusableGitText, g), ErrorCode: "worktree_add_failed"})
	}
	return rpc.OKResult(id, rpc.WorktreeResult{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
}

// unusableDotGit is the path of the first `.git` that git cannot use on the walk of
// findGitDir from dir. It is "" when the walk meets none, or when it ends at a usable
// git directory. The path has the spelling of the walk: dir after walkStart and
// filepath.Abs. A `.git` folder or file ends the walk. A dangling `.git` link and a
// `.git` of another type, such as a FIFO, do not: the walk goes on, and a repository
// above them is used (4c111e86, measured on Linux and macOS).
//
// claustrum's own, not measured: a daemon GIT_DIR or GIT_COMMON_DIR turns the test off. A git directory
// that the daemon cannot search counts as usable, so it keeps not_a_repo as 683cc6a9
// answers. So does a stat of a `.git` file target that fails with another error than
// "does not exist". A `.git` file that names a regular file is unusable. Where the walk
// meets two such `.git`, the first one is named.
func unusableDotGit(dir string) string {
	if os.Getenv("GIT_DIR") != "" || daemonCommonDirSet() {
		return ""
	}
	if dir == "" {
		dir = "."
	}
	start, err := walkStart(dir)
	if err != nil {
		return ""
	}
	if start, err = filepath.Abs(start); err != nil {
		return ""
	}
	first := ""
	for d := start; ; {
		dotGit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotGit)
		unusable := false
		switch {
		case err != nil:
			if isGitDirShape(d) {
				return ""
			}
			if _, lerr := os.Lstat(dotGit); lerr == nil {
				unusable = true // a dangling link
			}
		case fi.IsDir():
			if !gitDirUnusable(dotGit) {
				return ""
			}
			if first == "" {
				first = dotGit
			}
			return first
		case fi.Mode().IsRegular():
			if !gitFileUnusable(dotGit, d) {
				return ""
			}
			if first == "" {
				first = dotGit
			}
			return first
		default:
			unusable = true // a FIFO, a device
		}
		if unusable && first == "" {
			first = dotGit
		}
		parent := filepath.Dir(d)
		if parent == d {
			return first
		}
		d = parent
	}
}

// gitDirUnusable reports whether g, a git directory that the walk found, is one that
// git cannot use: it fails the git-directory test and holds no commondir entry. A
// commondir there is judged by the trust check (judgeStrayCommondir).
func gitDirUnusable(g string) bool {
	if _, err := os.Lstat(filepath.Join(g, "commondir")); err == nil {
		return false
	}
	if gitDirUnsearchable(g) {
		return false
	}
	// An `objects` or `refs` that exists and fails only gitDirPartUsable keeps
	// not_a_repo, as 52d82f0a answers for `refs` 0600. So does a HEAD that the daemon
	// cannot read, as 5fd08069 answers for HEAD 000 (measured on Linux). Both are
	// claustrum's own on 4c111e86, not measured.
	for _, name := range []string{"objects", "refs"} {
		p := filepath.Join(g, name)
		if _, err := os.Stat(p); err == nil && !gitDirPartUsable(p) {
			return false
		}
	}
	// The open never makes HEAD the controlling terminal of the daemon, as in
	// openRepoFile. A HEAD that links to a pty slave is claustrum's own, not measured.
	if f, err := os.OpenFile(filepath.Join(g, "HEAD"), os.O_RDONLY|openNonBlocking|openNoTerminal, 0); errors.Is(err, fs.ErrPermission) {
		return false
	} else if err == nil {
		_ = f.Close()
	}
	return !isGitDirShape(g)
}

// gitFileUnusable reports whether the `.git` file path in the folder holder names no
// git directory that git can use: it has no `gitdir: ` line, or it names a missing
// path, a file or a folder that gitDirUnusable refuses. A linked-worktree entry is
// usable.
func gitFileUnusable(path, holder string) bool {
	target, ok := readWalkGitFile(path, holder)
	if !ok {
		return true
	}
	fi, err := os.Stat(target)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	if !fi.IsDir() {
		return true
	}
	if isWorktreeEntry(target) {
		return false
	}
	return gitDirUnusable(target)
}
