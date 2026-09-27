package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The git-directory trust check that every git.* method applies to the directory it
// runs git in, before git runs. It follows reference f6010b97. It was measured black
// box, side by side against that build, on a Linux VM. Where a case can exist on macOS
// or Windows, it was measured on those VMs too. docs/PROTOCOL.md → Git-directory trust
// check describes it.
//
// The check finds the git directory, then judges it. A linked-worktree entry must carry
// a `commondir` file that names its own repository. Any other git directory must carry
// no `commondir` at all. Three verdicts come out:
//
//   - trusted: git runs with GIT_COMMON_DIR pinned to the repository git directory.
//     For a linked-worktree entry that is the repository the entry names. For any
//     other git directory it is that directory itself.
//   - no repository: the method answers as if the directory held no repository.
//   - refused: the method answers with one of the fixed texts below, and git never runs.

const (
	gitDirTrustPrefix = "the repository's git directory could not be trusted; git not run: " +
		"commondir not as git writes it: "
	// gitDirEntryGoneRefusal carries the older hooks prefix. It answers a daemon GIT_DIR
	// that names a linked-worktree entry which no longer exists.
	gitDirEntryGoneRefusal = "config-defined hooks could not be pinned off; git not run: " +
		"the worktree entry this folder's .git names no longer exists"
	// gitDirAddWrap prefixes a refusal on git.worktree_create when the daemon's own
	// environment already carries GIT_COMMON_DIR.
	gitDirAddWrap = "git worktree add failed: cannot locate the repository's git directory: "

	commondirMaxBytes = 1 << 20 // exactly 1 MiB passes, one byte more fails
	gitFileMaxBytes   = 1 << 20 // a larger `.git` file names no repository
	operandMaxRunes   = 300     // each quoted path is cut to this many runes first
	headReadMaxBytes  = 255     // HEAD is judged on its first 255 bytes only
)

type gitDirVerdict int

const (
	gitDirTrusted gitDirVerdict = iota
	gitDirNoRepo
	gitDirRefused
)

// gitDirTrust is the outcome of the check for one request directory.
type gitDirTrust struct {
	verdict gitDirVerdict
	// refusal is the full wire text when verdict is gitDirRefused.
	refusal string
	// pinCommonDir is the repository git directory of a trusted git directory. Git
	// runs with GIT_COMMON_DIR set to it, and a remove prunes and verifies entries
	// under it. For a daemon GIT_DIR that names nothing, it is the absolute form of
	// that GIT_DIR. It is empty when the check left the
	// directory to git with no pin.
	pinCommonDir string
}

// quoteOperand cuts s to its first operandMaxRunes runes and quotes it with Go %q. An
// invalid UTF-8 byte counts as one rune and stays a byte, so %q prints it as \xNN.
func quoteOperand(s string) string {
	n := 0
	for i := range s {
		if n == operandMaxRunes {
			s = s[:i]
			break
		}
		n++
	}
	return fmt.Sprintf("%q", s)
}

func refuseStrayCommondir(cd string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" exists where git itself never writes one (git keeps that file only in a linked " +
		"worktree's entry under .git/worktrees/); remove it if you did not create it, and " +
		"treat its appearance as tampering"}
}

func refuseForeignCommondir(cd, repo string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" does not name the entry's own repository, " + quoteOperand(repo) +
		"; restore the file to read ../.. or remove that worktree entry and add the worktree again"}
}

func refuseMissingCommondir(entry string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + "worktree entry " +
		quoteOperand(entry) + " has none (git always writes one there, containing ../..); the " +
		"entry is damaged or was not made by git — recreate the worktree with git worktree add, " +
		"or remove the entry"}
}

func refuseUnreadableCommondir(cd, reason string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" is not a small plain file (" + reason + "); remove it, or remove that worktree entry " +
		"and add the worktree again so git rewrites it"}
}

// daemonCommonDirSet reports whether the daemon's own environment carries
// GIT_COMMON_DIR. git.info and git.list_branches then skip the check.
func daemonCommonDirSet() bool {
	return os.Getenv("GIT_COMMON_DIR") != ""
}

// requestGitDirTrust runs the check for one method. readMethod is true for git.info
// and git.list_branches, which skip the check when the daemon's environment carries
// GIT_COMMON_DIR. git.status, git.worktree_create and git.worktree_remove check anyway.
func requestGitDirTrust(dir string, readMethod bool) gitDirTrust {
	if readMethod && daemonCommonDirSet() {
		return gitDirTrust{}
	}
	return gitDirTrustFor(dir)
}

// commonDirPinEnv returns the GIT_COMMON_DIR pin for a git command run in dir, or nil.
// The pin applies to every trusted git directory, and only when the daemon's own
// environment does not already set GIT_COMMON_DIR.
func commonDirPinEnv(dir string) []string {
	if daemonCommonDirSet() {
		return nil
	}
	if t := gitDirTrustFor(dir); t.verdict == gitDirTrusted && t.pinCommonDir != "" {
		return []string{"GIT_COMMON_DIR=" + t.pinCommonDir}
	}
	return nil
}

// gitDirTrustFor finds and judges the git directory for dir. A dir that does not
// resolve to an existing directory is left to git, which answers for itself.
func gitDirTrustFor(dir string) gitDirTrust {
	if dir == "" {
		dir = "."
	}
	start, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return gitDirTrust{}
	}
	if start, err = filepath.Abs(start); err != nil {
		return gitDirTrust{}
	}
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		return gitDirTrust{}
	}
	// A GIT_DIR in the daemon's environment is inherited by git, so that directory is
	// the one judged, for every request directory. A relative value is taken relative
	// to the request directory, as git takes it.
	if gd := os.Getenv("GIT_DIR"); gd != "" {
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(start, gd)
		}
		return judgeGitDir(gd, true)
	}
	g, ok := findGitDir(start)
	if !ok {
		return gitDirTrust{verdict: gitDirNoRepo}
	}
	return judgeGitDir(g, false)
}

// findGitDir walks up from start the way git discovers a repository. It returns false
// for "no repository". Unlike git, a `.git` directory that fails the git-directory test
// and holds no commondir ends the walk: git itself walks on to an outer repository.
func findGitDir(start string) (string, bool) {
	for d := start; ; {
		dotGit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotGit)
		switch {
		case err != nil:
			// No `.git` here: the directory itself can be a bare git directory.
			if isGitDirShape(d) {
				return d, true
			}
		case fi.IsDir():
			if isGitDirShape(dotGit) {
				return dotGit, true
			}
			if _, err := os.Lstat(filepath.Join(dotGit, "commondir")); err == nil {
				return dotGit, true // judged next, and refused as a stray commondir
			}
			return "", false
		case fi.Mode().IsRegular():
			return readGitFile(dotGit, d)
		}
		// A `.git` of another type (a FIFO, a device) is skipped.
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// readGitFile reads a `.git` file. It must start with the exact bytes "gitdir:" and be
// at most gitFileMaxBytes long. The rest is trimmed of white space and, when relative,
// taken relative to holder, the directory that holds the `.git` file.
func readGitFile(path, holder string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, gitFileMaxBytes+1))
	if err != nil || len(b) > gitFileMaxBytes || !bytes.HasPrefix(b, []byte("gitdir:")) {
		return "", false
	}
	target := strings.Trim(string(b[len("gitdir:"):]), " \t\r\n")
	if target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(holder, target)
	}
	return target, true
}

// judgeGitDir judges the git directory g. fromEnv is true when the daemon's GIT_DIR
// named g, false when findGitDir found it.
func judgeGitDir(g string, fromEnv bool) gitDirTrust {
	fi, err := os.Stat(g)
	if err != nil {
		// Gone. A GIT_DIR that names a gone linked-worktree entry is refused. Any other
		// GIT_DIR is left to git, with GIT_COMMON_DIR pinned to its absolute form. A
		// relative GIT_DIR is joined to the request directory first. f6010b97 pins
		// that value on Linux and Windows VMs (rows E02 and E05, and E06 where
		// GIT_DIR=/dev/null names nothing on Windows). A directory found by the walk
		// means no repository.
		if fromEnv {
			if isWorktreeEntry(g) {
				return gitDirTrust{verdict: gitDirRefused, refusal: gitDirEntryGoneRefusal}
			}
			return gitDirTrust{pinCommonDir: g}
		}
		return gitDirTrust{verdict: gitDirNoRepo}
	}
	if !fi.IsDir() && nonDirGitDirIsNoRepo {
		return gitDirTrust{verdict: gitDirNoRepo}
	}
	g = resolveGitDirLinks(g)
	cd := filepath.Join(g, "commondir")
	if !isWorktreeEntry(g) {
		// Git writes commondir only in a linked worktree's entry. One of any type
		// anywhere else is refused, even when it names g itself.
		if _, err := os.Lstat(cd); err == nil {
			return refuseStrayCommondir(cd)
		}
		// Pinned to itself. Where g is a `.git` file (Windows only, see
		// nonDirGitDirIsNoRepo), git then fails with its own "not a git repository"
		// for the entry the file names. Measured against f6010b97 on a Windows 11 VM.
		return gitDirTrust{pinCommonDir: g}
	}
	repo := filepath.Dir(filepath.Dir(g))
	content, reason, present := readCommondir(g)
	if !present {
		return refuseMissingCommondir(g)
	}
	if reason != "" {
		return refuseUnreadableCommondir(cd, reason)
	}
	named := strings.Trim(content, " \t\r\n")
	if filepath.IsAbs(named) {
		named = filepath.Clean(named)
	} else {
		named = filepath.Join(g, named)
	}
	// A string comparison: no symlink in the value is resolved. On Windows the
	// comparison also ignores letter case and the slash direction.
	if !sameGitDirPath(named, repo) {
		return refuseForeignCommondir(cd, repo)
	}
	return gitDirTrust{pinCommonDir: repo}
}

// isWorktreeEntry reports whether g is a linked-worktree entry: its parent is named
// "worktrees", its own name is not ".git", and its grandparent passes the git-directory
// test. That covers the entries of a main repository, a bare repository and a submodule.
func isWorktreeEntry(g string) bool {
	parent := filepath.Dir(g)
	return filepath.Base(parent) == "worktrees" && filepath.Base(g) != ".git" &&
		isGitDirShape(filepath.Dir(parent))
}

// readCommondir reads <entry>/commondir. present is false when there is no commondir
// entry at all. A non-empty reason means the file is not a small plain file. It must be
// a regular file of at most commondirMaxBytes. A symlink is followed only when the link
// is relative and stays inside the entry directory.
func readCommondir(entry string) (content, reason string, present bool) {
	if _, err := os.Lstat(filepath.Join(entry, "commondir")); errors.Is(err, fs.ErrNotExist) {
		return "", "", false
	}
	root, err := os.OpenRoot(entry)
	if err != nil {
		return "", err.Error(), true
	}
	defer func() { _ = root.Close() }()
	// Opened without blocking, so a FIFO is judged at once instead of waiting for a
	// writer.
	f, err := root.OpenFile("commondir", os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return "", err.Error(), true
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err.Error(), true
	}
	if !fi.Mode().IsRegular() {
		return "", "commondir is not a regular file", true
	}
	tooLarge := fmt.Sprintf("commondir is larger than %d bytes", commondirMaxBytes)
	if fi.Size() > commondirMaxBytes {
		return "", tooLarge, true
	}
	b, err := io.ReadAll(io.LimitReader(f, commondirMaxBytes+1))
	if err != nil {
		return "", err.Error(), true
	}
	if len(b) > commondirMaxBytes {
		return "", tooLarge, true
	}
	return string(b), "", true
}

// isGitDirShape is the git-directory test: `objects` and `refs` exist (of any type) and
// HEAD passes headLooksValid.
func isGitDirShape(d string) bool {
	for _, name := range []string{"objects", "refs"} {
		if _, err := os.Stat(filepath.Join(d, name)); err != nil {
			return false
		}
	}
	return headLooksValid(filepath.Join(d, "HEAD"))
}

// headLooksValid judges HEAD. A symlink passes when its target text starts with
// "refs/". A regular file passes when its first headReadMaxBytes bytes hold "ref:",
// optional white space and then "refs/", or start with 40 hex digits. Anything else
// fails at once, including a FIFO.
func headLooksValid(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		return err == nil && strings.HasPrefix(target, "refs/")
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	f, err := os.OpenFile(p, os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	buf := make([]byte, headReadMaxBytes)
	n, _ := io.ReadFull(f, buf)
	return headContentValid(buf[:n])
}

func headContentValid(b []byte) bool {
	if rest, ok := bytes.CutPrefix(b, []byte("ref:")); ok {
		return bytes.HasPrefix(bytes.TrimLeft(rest, " \t\r\n"), []byte("refs/"))
	}
	if len(b) < 40 {
		return false
	}
	for _, c := range b[:40] {
		if !isHexDigit(c) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
