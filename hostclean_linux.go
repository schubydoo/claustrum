//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux-specific host-cleaner primitives (reference build 19f30c46). These are the /proc
// reads, the /proc/net/unix busy probe, the SO_PEERCRED peer lookup, and the /proc/locks
// scan — everything that touches procRoot ("/proc") or a Linux-only kernel interface. The
// shared decision layer lives in hostclean.go and reaches these through the OS-primitive
// seams at the foot of this file, so it compiles on darwin against a darwin implementation.

// hcNamespaces are the namespaces a candidate process must share with this daemon to be a
// sibling the cleaner may act on.
var hcNamespaces = []string{"pid", "mnt"}

// hcStat is the /proc/<pid>/stat subset the cleaner reads.
type hcStat struct {
	ppid       int
	pgid       int
	startTicks string // field 22, clock ticks since boot
	stopped    bool   // process state T (stopped) or t (traced)
	ok         bool   // false when the process is gone (state Z or X) or stat is unparseable
}

// hcReadStat parses /proc/<pid>/stat. Field 2 (comm) is parenthesized, so parsing resumes
// after the last ')': the remaining fields start at 3 (state), so ppid is index 1, pgid is
// index 2, and starttime (field 22) is index 19.
func hcReadStat(pid int) hcStat {
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return hcStat{}
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return hcStat{}
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return hcStat{}
	}
	state := f[0]
	if state == "Z" || state == "X" {
		return hcStat{} // gone
	}
	ppid, e1 := strconv.Atoi(f[1])
	pgid, e2 := strconv.Atoi(f[2])
	if e1 != nil || e2 != nil {
		return hcStat{}
	}
	return hcStat{ppid: ppid, pgid: pgid, startTicks: f[19], stopped: state == "T" || state == "t", ok: true}
}

// hcReadExe resolves /proc/<pid>/exe, dropping a trailing " (deleted)" the kernel appends
// when the binary was replaced.
func hcReadExe(pid int) string {
	t, err := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(t, " (deleted)")
}

// hcUptime returns the host uptime from /proc/uptime, used to turn a process's start-ticks
// into a wall-clock age.
func hcUptime() (time.Duration, bool) {
	b, err := os.ReadFile(procRoot + "/uptime")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// hcFdTargets reads /proc/<pid>/fd and returns each fd name mapped to its link target. The
// second result is false only when the directory itself cannot be opened (the process gone).
func hcFdTargets(pid int) (map[string]string, bool) {
	dir := procRoot + "/" + strconv.Itoa(pid) + "/fd"
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	m := make(map[string]string, len(ents))
	for _, e := range ents {
		if t, err := os.Readlink(dir + "/" + e.Name()); err == nil {
			m[e.Name()] = t
		}
	}
	return m, true
}

// hcStdioArePipes reports whether fds 0, 1 and 2 are all pipes, the stdio signature of a
// daemon-spawned child.
func hcStdioArePipes(pid int) bool {
	m, ok := hcFdTargets(pid)
	if !ok {
		return false
	}
	for _, fd := range []string{"0", "1", "2"} {
		if !strings.HasPrefix(m[fd], "pipe:[") {
			return false
		}
	}
	return true
}

// hcHasFileOpen reports whether the process holds path open on any fd.
func hcHasFileOpen(pid int, path string) bool {
	m, ok := hcFdTargets(pid)
	if !ok {
		return false
	}
	for _, t := range m {
		if strings.TrimSuffix(t, " (deleted)") == path {
			return true
		}
	}
	return false
}

// hcBusy reports whether the process has a connected client on one of its own unix sockets: a
// socket fd whose inode appears in /proc/<pid>/net/unix in the connected state (St 03).
func hcBusy(pid int) bool {
	m, ok := hcFdTargets(pid)
	if !ok {
		return false
	}
	inodes := make(map[string]bool)
	for _, t := range m {
		if rest, ok := strings.CutPrefix(t, "socket:["); ok {
			inodes[strings.TrimSuffix(rest, "]")] = true
		}
	}
	if len(inodes) == 0 {
		return false
	}
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/net/unix")
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 7 {
			continue
		}
		if f[5] == "03" && inodes[f[6]] { // St 03 = connected; Inode column
			return true
		}
	}
	return false
}

// hcClockTick turns a stat start-ticks value into a duration; Linux reports USER_HZ = 100.
const hcClockTick = 10 * time.Millisecond

// hcReadUID parses the real uid from the "Uid:" line of /proc/<pid>/status.
func hcReadUID(pid int) (int, bool) {
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(ln, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			uid, err := strconv.Atoi(f[0])
			return uid, err == nil
		}
	}
	return 0, false
}

// hcSameNamespaces reports whether pid shares every namespace in hcNamespaces with this
// process. The second result is false when a namespace link cannot be read.
func hcSameNamespaces(pid int) (same, readable bool) {
	for _, ns := range hcNamespaces {
		self, e1 := os.Readlink(procRoot + "/self/ns/" + ns)
		other, e2 := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/ns/" + ns)
		if e1 != nil || e2 != nil {
			return false, false
		}
		if self != other {
			return false, true
		}
	}
	return true, true
}

// inspect gathers the full record for one pid: its stat, uid ownership, namespace match,
// wall-clock start, executable, argv and (optionally) environ. It re-reads stat at the end
// so a pid that vanished or was reused during the read is reported gone rather than acted on.
func inspect(pid int, wantEnv bool) (hcTracked, hcInspectResult) {
	s := hcReadStat(pid)
	if !s.ok {
		return hcTracked{}, hcInspectGone
	}
	uid, ok := hcReadUID(pid)
	if !ok {
		return hcTracked{}, hcInspectErr
	}
	sameNS, nsReadable := hcSameNamespaces(pid)
	if !nsReadable {
		return hcTracked{}, hcInspectErr
	}
	t := hcTracked{
		pid: pid, ppid: s.ppid, pgid: s.pgid, startTicks: s.startTicks, stopped: s.stopped,
		sameUID: uid == hcGetuid(), sameNS: sameNS, exe: hcReadExe(pid),
	}
	if up, ok := hcUptime(); ok {
		if ticks, err := strconv.ParseInt(s.startTicks, 10, 64); err == nil {
			t.startWall = hcClock().Add(-(up - time.Duration(ticks)*hcClockTick))
			t.haveAge = true
		}
	}
	if b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/cmdline"); err == nil {
		t.argv = splitNul(string(b))
	}
	if wantEnv {
		if b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/environ"); err == nil {
			t.env = splitNul(string(b))
		}
	}
	if s2 := hcReadStat(pid); !s2.ok || s2.startTicks != s.startTicks {
		return hcTracked{}, hcInspectGone // vanished or reused during the read
	}
	return t, hcInspectOK
}

// hcSnapshot enumerates the pids under /proc owned by our uid. It appends at most hcMaxSnapshot
// pids; when it finds a further matching process it stops and reports overflow, so the pass acts
// only on per-process facts and skips run-dir retirement (the slice is
// capped but a separate flag, not the slice length, signals "too many"). The uid filter keeps
// the cleaner to this user's own processes. ok is false only when /proc is unreadable.
func hcSnapshot() (pids []int, overflow bool, ok bool) {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false, false
	}
	uid := hcGetuid()
	matched := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fi, err := os.Stat(procRoot + "/" + strconv.Itoa(pid))
		if err != nil {
			continue
		}
		st, stok := fi.Sys().(*syscall.Stat_t)
		if !stok || int(st.Uid) != uid {
			continue
		}
		matched++
		if matched > hcMaxSnapshot {
			return pids, true, true // more matches than one snapshot enumerates: cap + flag overflow
		}
		pids = append(pids, pid)
	}
	return pids, false, true
}

// peerOf returns the pid and uid of the process on the other end of a unix connection, via
// SO_PEERCRED.
func peerOf(conn *net.UnixConn) (pid int, uid uint32, ok bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var ucred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		ucred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || cerr != nil || ucred == nil {
		return 0, 0, false
	}
	return int(ucred.Pid), ucred.Uid, true
}

// hcDevMajorMinor splits a raw st_dev the way /proc/locks reports it (the glibc encoding).
func hcDevMajorMinor(dev uint64) (major, minor uint64) {
	major = (dev>>32)&0xfffff000 | (dev&0xfff00)>>8
	minor = (dev&0xffffff00000)>>12 | dev&0xff
	return major, minor
}

// lockFileHeld reports whether the file described by fi is currently locked by a live
// process, by matching its device and inode against /proc/locks.
func lockFileHeld(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	b, err := os.ReadFile(procRoot + "/locks")
	if err != nil {
		return false
	}
	major, minor := hcDevMajorMinor(st.Dev)
	target := fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino)
	tail := ":" + strconv.FormatUint(st.Ino, 10) // major==0 anonymous-fs fallback (00:00:<ino>)
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 6 {
			continue
		}
		if f[5] == target {
			return true
		}
		if major == 0 && strings.Count(f[5], ":") == 2 && strings.HasSuffix(f[5], tail) {
			return true
		}
	}
	return false
}

// ---- OS-primitive seams (linux implementations; darwin provides its own) ----

// hcReadIdent returns the pid-reuse-proof identity (process group and start-ticks) of pid.
func hcReadIdent(pid int) (pgid int, startTicks string, ok bool) {
	s := hcReadStat(pid)
	return s.pgid, s.startTicks, s.ok
}

// hcLockHeldAt reports whether the lock file at path (described by fi) is held by a live
// process. On linux the answer comes from fi alone (via /proc/locks); path is unused.
func hcLockHeldAt(path string, fi os.FileInfo) bool {
	return lockFileHeld(fi)
}

// hcSelfExe resolves this process's own executable path, dropping a trailing " (deleted)".
func hcSelfExe() (exe string, ok bool) {
	if p, err := os.Readlink(procRoot + "/self/exe"); err == nil {
		return strings.TrimSuffix(p, " (deleted)"), true
	}
	return "", false
}

// splitNul splits a NUL-delimited /proc field (cmdline, environ) into its entries. Linux-only:
// darwin reads argv/env from KERN_PROCARGS2 in hostclean_darwin.go instead.
func splitNul(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}
