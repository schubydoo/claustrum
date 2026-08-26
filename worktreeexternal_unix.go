//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

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
