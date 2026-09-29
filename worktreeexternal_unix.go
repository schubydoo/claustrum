//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// isSymlinkLoop reports whether err from filepath.EvalSymlinks comes from a symlink
// loop. EvalSymlinks counts the links itself. After 255 links it returns a plain
// error with this text (path/filepath/symlink.go in the Go toolchain). A kernel
// ELOOP counts too. A finite chain of more than 255 links gets the same answer.
func isSymlinkLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP) || err.Error() == "EvalSymlinks: too many links"
}

// parentStepPrefix starts every failure of the parent step of git.worktree_create.
const parentStepPrefix = "failed to create parent directory: "

// externalChainCheck is the root-chain step of git.worktree_create with a
// worktreeRoot. It comes after the containment refusal and before
// worktreeRootShareRefusal. It returns the <directory> level with the symlinks of
// the root resolved. When the create must stop, it also returns the error text and
// the errorCode. claustrum takes these steps, and the texts they send match
// f6010b97 on Linux and macOS VMs:
//
//  1. claustrum resolves the deepest existing prefix of the root with
//     filepath.EvalSymlinks. A symlink loop refuses the create with unsafe_path,
//     and the text names the cleaned root, without a trailing slash. Any other
//     resolve error is sent as Go prints it (not measured). A resolved prefix that is not a
//     directory reads "<path>: not a directory".
//  2. Each directory from "/" down to that prefix is tested for a .git entry of any
//     kind, top first. An entry refuses the create with unsafe_path. An lstat
//     error other than "not exist" is sent as Go prints it (measured with EACCES).
//
// Step 2 was measured with a .git entry five levels above the root. With two
// entries, the top one is named. externalDirLevelCheck runs the <directory> step.
func externalChainCheck(worktreeRoot, worktreePath string) (dir, msg, code string) {
	name := filepath.Base(filepath.Dir(filepath.Clean(worktreePath)))
	prefix := filepath.Clean(worktreeRoot)
	var missing []string
	for {
		if _, err := os.Lstat(prefix); err == nil {
			break
		}
		up := filepath.Dir(prefix)
		if up == prefix {
			break
		}
		missing = append([]string{filepath.Base(prefix)}, missing...)
		prefix = up
	}
	resolved, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		if isSymlinkLoop(err) {
			return "", fmt.Sprintf("refusing to create worktree: %s passes through too many "+
				"symbolic links (a loop?)", filepath.Clean(worktreeRoot)), "unsafe_path"
		}
		return "", parentStepPrefix + err.Error(), "mkdir_failed"
	}
	// A path that EvalSymlinks just resolved exists, so a failed Lstat here means a
	// concurrent change. It gets the same text as a path that is not a directory.
	if fi, err := os.Lstat(resolved); err != nil || !fi.IsDir() {
		return "", parentStepPrefix + resolved + ": not a directory", "mkdir_failed"
	}
	var chain []string
	for d := resolved; ; d = filepath.Dir(d) {
		chain = append([]string{d}, chain...)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range chain {
		_, err := os.Lstat(filepath.Join(d, ".git"))
		if err == nil {
			return "", fmt.Sprintf("refusing to create worktree: %s is inside a git checkout "+
				"(%s has a .git entry); a worktree location must be outside every checkout, "+
				"so that no session working in one can reach it — choose a directory that is "+
				"not part of any repository", worktreeRoot, d), "unsafe_path"
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", parentStepPrefix + err.Error(), "mkdir_failed"
		}
	}
	return filepath.Join(append([]string{resolved}, append(missing, name)...)...), "", ""
}

// externalDirLevelCheck tests dir, the resolved <directory> level, when it exists.
// It comes after worktreeRootShareRefusal and the <directory> symlink refusal, and
// before the non-empty and "already exists" refusals. Linux and macOS VMs measured
// the order against the symlink refusal. The texts match f6010b97 on both:
//   - A dir that is not a directory reads "<path> is not a directory".
//   - A .git entry in it refuses the create with unsafe_path.
//   - A failed lstat of that entry reads "statat <name>/.git: <errno>".
//
// The texts name the resolved path, as Linux and macOS VMs measured under a
// symlinked root.
func externalDirLevelCheck(dir string) (msg, code string) {
	fi, err := os.Stat(dir)
	if err != nil {
		return "", ""
	}
	if !fi.IsDir() {
		return parentStepPrefix + dir + " is not a directory", "mkdir_failed"
	}
	_, err = os.Lstat(filepath.Join(dir, ".git"))
	if err == nil {
		return fmt.Sprintf("refusing to create worktree: %s is itself a git checkout "+
			"(it has a .git entry); a worktree location must be outside every checkout", dir), "unsafe_path"
	}
	if !errors.Is(err, fs.ErrNotExist) {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return parentStepPrefix + "statat " + filepath.Base(dir) + "/.git: " + err.Error(), "mkdir_failed"
	}
	return "", ""
}

// worktreeRootShareRefusal reports 7d193f89's ownership/writability refusal for an
// external worktreeRoot, or "" if the root is safe. Checks run in the reference's
// order (measured byte-for-byte against 7d193f89 on an ephemeral VM):
//
//   - owned by another user -> "is owned by uid <N>, not by you (uid <M>); …"
//   - writable by a shared group, or by every user on the host ->
//     "is writable by <who> (mode <perm>); …"
//
// A root the daemon user owns and only they (and their own group) can write to
// passes. The group is a sharing concern only when the directory's gid is NOT the
// daemon's effective gid: a private per-user group is not shared, so a
// group-writable root under it is accepted (VM-confirmed). Only the write bits
// beyond the owner's matter — group-write (0o020) when the group is shared, and
// world-write (0o002).
func worktreeRootShareRefusal(root string) string {
	fi, err := os.Stat(root)
	if err != nil {
		// A missing/unreadable root is not this check's concern; the create fails
		// later at the parent-creation step with its own message.
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if euid := os.Geteuid(); int(st.Uid) != euid {
		return fmt.Sprintf("refusing to create worktree: %s is owned by uid %d, not by you "+
			"(uid %d); choose a directory you own, for example under your home directory "+
			"(directories on network or container storage that report a different owner "+
			"are refused the same way)", root, st.Uid, euid)
	}
	perm := fi.Mode().Perm()
	groupShared := perm&0o020 != 0 && int(st.Gid) != os.Getegid()
	worldWritable := perm&0o002 != 0
	if who := writableWho(groupShared, worldWritable); who != "" {
		return fmt.Sprintf("refusing to create worktree: %s is writable by %s (mode %04o); "+
			"choose a directory only you can write to, or remove the extra write "+
			"permission (chmod go-w)", root, who, perm)
	}
	return ""
}

// writableWho names who — beyond the owner — can write to the root, or "" if only
// the owner (and their own group) can. The three spellings are the reference's.
func writableWho(groupShared, worldWritable bool) string {
	switch {
	case groupShared && worldWritable:
		return "its group and every user on this host"
	case worldWritable:
		return "every user on this host"
	case groupShared:
		return "its group"
	default:
		return ""
	}
}
