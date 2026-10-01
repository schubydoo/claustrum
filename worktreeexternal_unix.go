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

// rootAncestorStat reads the owner, the group and the mode of path. It does not
// follow a final symlink. Tests replace it to report a foreign owner.
var rootAncestorStat = func(path string) (uid, gid uint32, mode fs.FileMode, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, fmt.Errorf("%s: no owner in the stat answer", path)
	}
	return st.Uid, st.Gid, fi.Mode(), nil
}

// rootAncestorFault judges one directory above a worktreeRoot. uid, gid and mode
// are its owner, group and mode, and euid is the daemon user. It returns "" when
// the directory passes. Otherwise it returns the part of the refusal after
// "which is ". The rules match f6010b97 and 89cb6289 on Linux and macOS VMs:
//
//   - The owner test comes first. uid 0 and the daemon user pass. Any other owner
//     refuses, and the sticky bit does not clear it (rows G9, G10, G31 and G32).
//     The uid prints as an unsigned number (4294967294 on macOS).
//   - The sticky bit clears both mode tests (rows G4, G5 and G27).
//   - The group-write bit refuses unless the group is private, as in
//     rootGroupIsPrivate (rows K1, G1, G6, G6b, G7, G8 and G33c). The other-write
//     bit always refuses (rows G2 and G3). An owner of uid 0 gets no exemption
//     here (rows G8, G28, G33a and G33b).
//   - The mode prints the permission bits only, as four octal digits (rows G7,
//     G36a and G36b).
//
// Rows G1, G32, G33a, G33c, G34b and G36b ran on Linux only. A macOS VM ran the
// other round 2 rows against 89cb6289 only. Not measured: whether "you" is the
// real or the effective uid. claustrum uses the effective uid, as the root's own
// write test does. The private-group test on an ancestor is measured only for the
// stock layouts.
func rootAncestorFault(uid, gid uint32, mode fs.FileMode, euid int) string {
	if uid != 0 && int64(uid) != int64(euid) {
		return fmt.Sprintf("owned by uid %d, neither you nor the system; "+
			"choose a location you reach through your own directories", uid)
	}
	if mode&fs.ModeSticky != 0 {
		return ""
	}
	perm := mode.Perm()
	groupShared := perm&0o020 != 0 && !rootGroupIsPrivate(int(gid), euid)
	who := writableWho(groupShared, perm&0o002 != 0)
	if who == "" {
		return ""
	}
	return fmt.Sprintf("writable by %s without the sticky bit (mode %04o), so they could "+
		"replace what is beneath it; choose a location under directories only you (or the "+
		"system) control, or remove the extra write permission (chmod go-w)", who, perm)
}

// worktreeRootAncestorRefusal refuses a worktreeRoot below a directory owned by a
// user other than you or uid 0. It also refuses one below a directory writable by
// a shared group or by every user, without the sticky bit. It returns "" when the
// root passes. Only git.worktree_create runs it, with a worktreeRoot (rows G21 to
// G26). The text names worktreeRoot as sent and the failing directory with its
// symlinks resolved.
// The walk matches f6010b97 and 89cb6289 on Linux and macOS VMs:
//
//  1. It takes the directories above the cleaned root, top first. The root itself
//     is not judged. Only the root's chain is walked, not the chain of baseRepo
//     (row G20).
//  2. It resolves each one with filepath.EvalSymlinks. A missing level ends the
//     walk. Every level below it is missing too, so no existing level above the
//     root is skipped for that reason (rows G13a and G13b). Any other failed resolve also
//     ends the walk with "". The root-chain step then gives its own answer (rows
//     G37b and G39).
//  3. It judges each directory from "/" down to that resolved path, with
//     rootAncestorFault, and skips one it judged before. So a directory above a
//     symlink target and a lexical directory above a symlink are both judged
//     (rows G14 to G15b, G14b, G14d and G27g).
//  4. Last it resolves the root itself. It judges each directory from "/" down to
//     the parent of the resolved root, and skips one it judged before. So when the
//     root is a symlink, the chain above its target is judged (row G42, Linux
//     and macOS VMs, 89cb6289 only). A root that does not resolve ends the walk
//     with "".
//  5. The first failing directory reached refuses the create. When the failing
//     directories lie on one chain, that is the highest one (rows G11, G12, G34a,
//     G34b and G35).
//
// A directory is judged before the walk looks into it. So a failing directory above
// the root is named even when its owner has no search access to it (row G36a).
//
// Rows G1, G32, G33a, G33c, G34b and G36b ran on Linux only. A macOS VM ran the
// other round 2 rows and row G42 against 89cb6289 only. Not measured, and
// claustrum's choice:
//   - Two failing directories on different branches of the resolution, where the
//     first one reached is not the highest. claustrum names the first one reached.
//   - A failing directory above a symlink loop, a dangling link or a regular file.
//     The walk reaches the failing directory first and refuses.
//   - A regular file above the root. Its mode is judged like a directory's.
//   - An ancestor that cannot be searched, with more levels below it. The resolve
//     fails and the walk returns "". No directory below it is judged.
//   - Whether "/" is judged. claustrum judges it. It passes on every host measured.
//   - A failed stat of a path that the walk just resolved. The walk returns "".
//   - The target of a symlinked root is not judged itself, as the root is not.
func worktreeRootAncestorRefusal(worktreeRoot string) string {
	root := filepath.Clean(worktreeRoot)
	var above []string
	for d, prev := filepath.Dir(root), root; d != prev; d, prev = filepath.Dir(d), d {
		above = append([]string{d}, above...)
	}
	euid := os.Geteuid()
	judged := map[string]bool{}
	// judge judges each directory from "/" down to top. It reports whether the walk
	// ends, with the refusal or "".
	judge := func(top string) (string, bool) {
		var chain []string
		for d, prev := top, ""; d != prev; d, prev = filepath.Dir(d), d {
			chain = append([]string{d}, chain...)
		}
		for _, d := range chain {
			if judged[d] {
				continue
			}
			judged[d] = true
			uid, gid, mode, err := rootAncestorStat(d)
			if err != nil {
				return "", true
			}
			if fault := rootAncestorFault(uid, gid, mode, euid); fault != "" {
				return "refusing to create worktree: " + worktreeRoot + " passes through " + d + ", which is " + fault, true
			}
		}
		return "", false
	}
	for _, p := range above {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return ""
		}
		if msg, done := judge(resolved); done {
			return msg
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	msg, _ := judge(filepath.Dir(resolved))
	return msg
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
