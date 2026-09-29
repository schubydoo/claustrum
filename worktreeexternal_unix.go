//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// A root the daemon user owns and only they can write to passes. The group-write
// bit (0o020) counts unless rootGroupIsPrivate finds the group private to the
// daemon user. The world-write bit (0o002) always counts.
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
	euid := os.Geteuid()
	if int(st.Uid) != euid {
		return fmt.Sprintf("refusing to create worktree: %s is owned by uid %d, not by you "+
			"(uid %d); choose a directory you own, for example under your home directory "+
			"(directories on network or container storage that report a different owner "+
			"are refused the same way)", root, st.Uid, euid)
	}
	perm := fi.Mode().Perm()
	groupShared := perm&0o020 != 0 && !rootGroupIsPrivate(int(st.Gid), euid)
	worldWritable := perm&0o002 != 0
	if who := writableWho(groupShared, worldWritable); who != "" {
		return fmt.Sprintf("refusing to create worktree: %s is writable by %s (mode %04o); "+
			"choose a directory only you can write to, or remove the extra write "+
			"permission (chmod go-w)", root, who, perm)
	}
	return ""
}

// flatPasswdPath and flatGroupPath are the flat account files that
// rootGroupIsPrivate reads. They are variables so that tests can point them at
// fixture files.
var (
	flatPasswdPath = "/etc/passwd"
	flatGroupPath  = "/etc/group"
)

// rootGroupIsPrivate reports whether gid is the private group of the daemon user
// with uid. It reads only the flat files, never NSS or the macOS directory
// service. All four tests must pass. Measured against f6010b97 on Linux and
// macOS VMs:
//
//   - gid is the gid of the daemon.
//   - The passwd file has exactly one line with gid as its primary gid. That
//     line must be the daemon user's line (P11, P12, both on Linux only).
//     Whether it is found by uid or by name is not measured.
//   - At least one group file line has gid. Every such line has the name of
//     that passwd line (P8, P9, X3, MP8, MP9, MX3).
//   - No such line lists a member other than that user.
//
// Both files skip a line whose first character is '#' (P1, P2, MP1, MP2). A
// passwd line with 6, 7 or 8 fields is read (P4, P5, MP4, MP5). A passwd name
// with a leading space does not match the user (P10, Linux only). A '+name' line
// is not skipped (P7, MP7). A leading space and a trailing CR around a group
// member are trimmed (P3, P6, MP3, MP6), and an empty member is ignored.
//
// These choices are not measured. claustrum uses the effective gid, and no capture
// told the real and the effective gid apart. A file that cannot be read makes the
// group shared. A passwd line with fewer than 6 or more than 8 fields is skipped.
// A group line with other than 4 fields is skipped. A line with a non-numeric uid
// or gid is skipped. Two identical group lines for the user pass. A member is
// also trimmed of trailing spaces. No other field is trimmed.
func rootGroupIsPrivate(gid, uid int) bool {
	if gid != os.Getegid() {
		return false
	}
	passwd, err := os.ReadFile(flatPasswdPath)
	if err != nil {
		return false
	}
	name, found := "", 0
	for _, line := range strings.Split(string(passwd), "\n") {
		// name:password:uid:gid:gecos:home[:shell[:extra]]
		f := strings.Split(line, ":")
		if strings.HasPrefix(line, "#") || len(f) < 6 || len(f) > 8 {
			continue
		}
		lineUID, errU := strconv.Atoi(f[2])
		lineGID, errG := strconv.Atoi(f[3])
		if errU != nil || errG != nil || lineGID != gid {
			continue
		}
		found++
		if lineUID == uid {
			name = f[0]
		}
	}
	if found != 1 || name == "" {
		return false
	}
	group, err := os.ReadFile(flatGroupPath)
	if err != nil {
		return false
	}
	seen := false
	for _, line := range strings.Split(string(group), "\n") {
		// name:password:gid:member,member
		f := strings.Split(line, ":")
		if strings.HasPrefix(line, "#") || len(f) != 4 {
			continue
		}
		if lineGID, err := strconv.Atoi(f[2]); err != nil || lineGID != gid {
			continue
		}
		if f[0] != name {
			return false
		}
		for _, m := range strings.Split(f[3], ",") {
			m = strings.Trim(strings.TrimSuffix(strings.Trim(m, " "), "\r"), " ")
			if m != "" && m != name {
				return false
			}
		}
		seen = true
	}
	return seen
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
