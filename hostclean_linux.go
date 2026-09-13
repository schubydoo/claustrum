//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The host cleaner (reference build 19f30c46). The cleaner is a periodic,
// host-wide sweep the daemon starts at -serve startup: it ends stranded sibling daemons and
// orphaned Claude Code process groups, and tidies their stale run dirs. This file holds the
// whole subsystem — the containment (roots + membership), the /proc inspection, the ownership
// and lock and socket probes, the judges, the destructive enders, the run-dir tidy, and the
// Start/Pass/Keepalive lifecycle. Everything it touches is confined to the install "roots"
// derived from the daemon's own socket and executable, and to this user's own processes, so
// the sweep never reaches an unrelated process or path.
//
// This path is DESTRUCTIVE: the cleaner ends OTHER daemons under its roots. Every /proc read,
// kill, dial, rename and remove goes through a package-var seam, so unit tests exercise the
// whole decision without touching a real process or file. The real sweep is validated only on
// a throwaway VM; a cleaner-enabled -serve must never run on a host that also runs siblings.
// Darwin and Windows link no cleaner yet (a later slice); it is a no-op there (hostclean_other.go).

// hostRoots is the containment envelope. roots holds the install root dir(s) the cleaner may
// touch (a daemon socket <root>/run/<id>/rpc.sock yields <root>); daemonBin is the deployed
// daemon binary's basename, which sits at <root>/srv/<id>/<daemonBin>.
type hostRoots struct {
	roots     []string // install root dir(s): the socket's root, plus the exe's root when it differs
	daemonBin string   // the deployed daemon binary basename (from this daemon's own exe)
	socket    string   // this daemon's own socket, <root>/run/<id>/rpc.sock
	runDir    string   // this daemon's own run dir, <root>/run/<id>
}

const (
	runComponent    = "run"
	srvComponent    = "srv"
	cliComponent    = "ccd-cli"
	rpcSockBasename = "rpc.sock"
)

// deriveRoots builds the containment from the daemon's own socket and, when haveExe is set,
// its resolved executable. The socket must be <root>/run/<id>/rpc.sock. When haveExe is set
// the executable must be a deployed daemon under <root>/srv, and its root is added when it
// differs from the socket's. It returns an error when the socket is not run-dir shaped or the
// executable is not a deployed daemon (reference build 19f30c46).
func deriveRoots(socket, exe string, haveExe bool) (*hostRoots, error) {
	socket = filepath.Clean(socket)
	if filepath.Base(socket) != rpcSockBasename {
		return nil, fmt.Errorf("socket %q is not a run-dir socket", socket)
	}
	runDir := filepath.Dir(socket)  // <root>/run/<id>
	runRoot := filepath.Dir(runDir) // <root>/run
	if filepath.Base(runRoot) != runComponent {
		return nil, fmt.Errorf("socket %q is not a run-dir socket", socket)
	}
	root := filepath.Dir(runRoot) // <root>
	r := &hostRoots{roots: []string{root}, socket: socket, runDir: runDir}
	if !haveExe {
		return r, nil
	}
	exe = filepath.Clean(exe)
	r.daemonBin = filepath.Base(exe)
	exeRoot := filepath.Dir(filepath.Dir(filepath.Dir(exe))) // <exeRoot>/srv/<id>/<bin> -> <exeRoot>
	if exeRoot != root {
		r.roots = append(r.roots, exeRoot)
	}
	if !r.isDaemonBinary(exe) {
		return nil, fmt.Errorf("executable %q is not a deployed daemon", exe)
	}
	return r, nil
}

// under reports whether path is <root>/<sub>/<...> for one of the roots, with exactly depth
// path components below <root>/<sub> (so under(exe, "srv", 2) matches <root>/srv/<id>/server).
func (r *hostRoots) under(path, sub string, depth int) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, root := range r.roots {
		prefix := filepath.Join(root, sub) + "/"
		if rest, ok := strings.CutPrefix(path, prefix); ok && rest != "" && strings.Count(rest, "/") == depth-1 {
			return true
		}
	}
	return false
}

// isDaemonBinary reports whether exe is a deployed daemon binary <root>/srv/<id>/<daemonBin>.
func (r *hostRoots) isDaemonBinary(exe string) bool {
	return filepath.Base(exe) == r.daemonBin && r.under(exe, srvComponent, 2)
}

// isCliBinary reports whether exe is a CLI binary under <root>/ccd-cli, either versioned
// (<root>/ccd-cli/<ver>/<bin>) or directly in the dir (<root>/ccd-cli/<bin>).
func (r *hostRoots) isCliBinary(exe string) bool {
	if r.under(exe, cliComponent, 2) {
		return true
	}
	for _, root := range r.roots {
		if filepath.Dir(exe) == filepath.Join(root, cliComponent) {
			return true
		}
	}
	return false
}

// isRunDirSocket reports whether path is a run-dir socket, either <root>/run/<id>/rpc.sock or
// <root>/rpc.sock.
func (r *hostRoots) isRunDirSocket(path string) bool {
	if filepath.Base(path) != rpcSockBasename {
		return false
	}
	if r.under(path, runComponent, 2) {
		return true
	}
	for _, root := range r.roots {
		if filepath.Clean(path) == filepath.Join(root, rpcSockBasename) {
			return true
		}
	}
	return false
}

// runRoots returns each root's run dir, <root>/run.
func (r *hostRoots) runRoots() []string {
	out := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		out = append(out, filepath.Join(root, runComponent))
	}
	return out
}

// sameSocketPath reports whether a and b denote the same socket, either after cleaning or
// after stripping a common install root, so the same socket named under different root
// spellings compares equal.
func (r *hostRoots) sameSocketPath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, rb := r.stripRoot(a), r.stripRoot(b)
	return ra != "" && ra == rb
}

// stripRoot returns path relative to the first root that contains it, or "" when no root does.
func (r *hostRoots) stripRoot(path string) string {
	path = filepath.Clean(path)
	for _, root := range r.roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return ""
}

// serveArgv extracts the socket path from a daemon's argv. It requires argv[0]'s basename to
// be binName and the argv to carry a -serve/--serve flag with an absolute -socket/--socket
// value, and returns "" when the argv is a -bridge/-stop/-install invocation or lacks the
// serve-plus-socket pair (reference build 19f30c46).
func serveArgv(argv []string, binName string) string {
	if len(argv) < 2 || filepath.Base(argv[0]) != binName {
		return ""
	}
	serve := false
	socket := ""
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--serve" || a == "-serve":
			serve = true
		case a == "--socket" || a == "-socket":
			if i+1 < len(argv) {
				i++
				socket = argv[i]
			}
		case strings.HasPrefix(a, "--socket="):
			socket = strings.TrimPrefix(a, "--socket=")
		case strings.HasPrefix(a, "-socket="):
			socket = strings.TrimPrefix(a, "-socket=")
		case a == "--bridge" || a == "-bridge" || a == "--stop" || a == "-stop" || a == "--install" || a == "-install":
			return ""
		}
	}
	if serve && strings.HasPrefix(socket, "/") {
		return filepath.Clean(socket)
	}
	return ""
}

// ---- inspection layer (read-only /proc reads, reference build 19f30c46) ----

const (
	hcMaxSnapshot = 4096 // cap on the processes one snapshot enumerates
	hcLogName     = "remote-server.log"
	hcLogOldName  = "remote-server.log.old"
)

// hcNamespaces are the namespaces a candidate process must share with this daemon to be a
// sibling the cleaner may act on.
var hcNamespaces = []string{"pid", "mnt"}

// hcGetuid is os.Getuid behind a seam so a snapshot test does not depend on the runner's uid.
var hcGetuid = os.Getuid

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

// hcTracked is one inspected candidate process — the record the judges act on.
type hcTracked struct {
	pid        int
	ppid       int
	pgid       int
	startTicks string    // /proc/<pid>/stat field 22, the pid-reuse-proof identity
	startWall  time.Time // wall-clock start, from the host uptime minus the start-ticks
	exe        string    // resolved /proc/<pid>/exe
	argv       []string  // /proc/<pid>/cmdline
	env        []string  // /proc/<pid>/environ (only when inspect is asked for it)
	haveAge    bool      // startWall was computable (uptime + start-ticks both read)
	sameUID    bool      // the real uid matches this daemon's
	sameNS     bool      // the pid and mnt namespaces match this daemon's
	stopped    bool      // process state T or t
}

// asTracked returns the pid-reuse-proof identity used by the signal helpers.
func (t hcTracked) asTracked() tracked {
	return tracked{pid: t.pid, pgid: t.pgid, startTicks: t.startTicks}
}

// hcHasEnv reports whether env contains an exact entry.
func hcHasEnv(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

// hcInspectResult is inspect's outcome.
type hcInspectResult int

const (
	hcInspectOK   hcInspectResult = iota // fully inspected; the record is valid
	hcInspectGone                        // the process vanished, or its pid was reused mid-read
	hcInspectErr                         // a /proc read failed for another reason
)

// hcClock is time.Now behind a seam, so the start-wall computation is testable.
var hcClock = time.Now

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

// splitNul splits a NUL-delimited /proc field (cmdline, environ) into its entries.
func splitNul(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
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

// hcSleep is time.Sleep behind a seam so the busy debounce does not really sleep in tests.
var hcSleep = time.Sleep

// hcDirIdle returns how long since the most recent modification among the run dir itself and
// its log files (remote-server.log, remote-server.log.old) — the dir's idleness, used to
// decide whether an abandoned run dir is old enough to tidy. The second result is false when
// the dir is missing or not a directory.
func hcDirIdle(dir string, now time.Time) (time.Duration, bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return 0, false
	}
	idle := now.Sub(fi.ModTime())
	for _, name := range []string{hcLogName, hcLogOldName} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			if age := now.Sub(fi.ModTime()); age < idle {
				idle = age
			}
		}
	}
	return idle, true
}

// hcSnapshot enumerates the pids under /proc owned by our uid. It appends at most hcMaxSnapshot
// pids; when it finds a further matching process it stops and reports overflow, so the pass acts
// only on per-process facts and skips run-dir retirement (matching the reference: the slice is
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

// ---- ownership / liveness / lock + socket probes (reference build 19f30c46) ----

// hcDialTimeout and hcWaitPoll match the reference.
const (
	hcDialTimeout = 300 * time.Millisecond // probeSocket dial timeout
	hcWaitPoll    = 100 * time.Millisecond // waitGone poll interval
)

// tracked is a pid plus the identity that defeats pid reuse: its process group and its
// start-ticks. A reused pid has a different start-ticks, so fate can tell it apart.
type tracked struct {
	pid        int
	pgid       int
	startTicks string
}

// fate values.
const (
	hcFateAlive  = 0 // the same live process (pgid and start-ticks still match)
	hcFateGone   = 1 // the process is gone
	hcFateReused = 2 // the pid now belongs to a different process
)

// fate re-reads /proc and reports whether the tracked process is still itself, gone, or
// replaced by a reused pid.
func (t tracked) fate() int {
	s := hcReadStat(t.pid)
	if !s.ok {
		return hcFateGone
	}
	if s.pgid == t.pgid && s.startTicks == t.startTicks {
		return hcFateAlive
	}
	return hcFateReused
}

// same reports whether the tracked process is still exactly itself.
func (t tracked) same() bool { return t.fate() == hcFateAlive }

// hcGroupExists reports whether a live process group pgid exists (kill(-pgid, 0) succeeds,
// or fails only for lack of permission).
func hcGroupExists(pgid int) bool {
	if pgid < 2 || pgid >= 1<<31 {
		return false
	}
	err := killGroup(pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// hcIsNoSuchProcess reports whether err means the target process no longer exists.
func hcIsNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}

// hcDial dials a unix socket with the probe timeout. A seam so a test can drive each
// probeSocket state without a real socket.
var hcDial = func(addr string) (net.Conn, error) {
	return net.DialTimeout("unix", addr, hcDialTimeout)
}

// socket-probe states.
const (
	hcSockUnknown = 0 // dialed but unusable, or an unclassified error
	hcSockMissing = 1 // the path does not exist (ENOENT/ENOTDIR)
	hcSockStale   = 2 // the path exists but nobody is listening (ECONNREFUSED)
	hcSockNotSock = 3 // the path exists but is not a socket (ENOTSOCK)
	hcSockLive    = 4 // a live listener answered; pid/uid are its peer credentials
)

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

// probeSocket classifies a unix socket path: live listener (with its peer pid/uid), missing,
// stale, wrong file type, or unknown.
func probeSocket(addr string) (state, pid int, uid uint32) {
	conn, err := hcDial(addr)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR):
			return hcSockMissing, 0, 0
		case errors.Is(err, syscall.ECONNREFUSED):
			return hcSockStale, 0, 0
		case errors.Is(err, syscall.ENOTSOCK):
			return hcSockNotSock, 0, 0
		default:
			return hcSockUnknown, 0, 0
		}
	}
	defer conn.Close()
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return hcSockUnknown, 0, 0
	}
	pid, uid, ok := peerOf(uc)
	if !ok || pid <= 1 {
		return hcSockUnknown, 0, 0
	}
	return hcSockLive, pid, uid
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

// lock-probe states.
const (
	hcLockStale   = 0 // no lock file, or a record present but no live holder
	hcLockHeld    = 1 // a record present and a live process holds the lock
	hcLockUnknown = 2 // the lock file could not be opened, stat'd, or parsed
)

// readLockRecord reads the first kilobyte of an open daemon.lock and parses it as the owner
// record. Returns nil when empty or not valid JSON.
func readLockRecord(f *os.File) *ownerRecord {
	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	var rec ownerRecord
	if json.Unmarshal(buf[:n], &rec) != nil {
		return nil
	}
	return &rec
}

// lockProbe classifies <dir>/daemon.lock as held by a live daemon, stale, or indeterminate,
// and returns the parsed owner record and whether the file was present.
func lockProbe(dir string) (state int, rec *ownerRecord, present bool) {
	path := filepath.Join(dir, runDirLockName)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return hcLockStale, nil, false
		}
		return hcLockUnknown, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return hcLockUnknown, nil, true
	}
	rec = readLockRecord(f)
	_ = f.Close()
	if rec == nil {
		return hcLockUnknown, nil, true
	}
	if lockFileHeld(fi) {
		return hcLockHeld, rec, true
	}
	return hcLockStale, rec, true
}

// waitEntry is a tracked process the wait may SIGKILL (kill set) if it dies but leaves a
// live process group behind.
type waitEntry struct {
	tracked
	kill bool
}

// waitGone polls the entries until they are gone or the deadline passes. A tracked process
// that dies while its process group still has members has that group SIGKILLed (reaping the
// orphaned children); a pid-reused entry is dropped. It returns the entries still alive.
func waitGone(entries []waitEntry, deadline time.Time) []waitEntry {
	for {
		var survivors []waitEntry
		for _, e := range entries {
			switch e.fate() {
			case hcFateAlive:
				survivors = append(survivors, e)
			case hcFateGone:
				if e.kill && e.pid > 1 && hcGroupExists(e.pid) {
					_ = killGroup(e.pid, syscall.SIGKILL)
				}
			}
			// hcFateReused: dropped, never signaled.
		}
		entries = survivors
		if len(entries) == 0 || !hcClock().Before(deadline) {
			return entries
		}
		hcSleep(hcWaitPoll)
	}
}

// ---- the cleaner: lifecycle, summary, and the destructive enders (reference build 19f30c46) ----

// Timing and count gates (reference build 19f30c46).
const (
	hcMinAge     = 5 * time.Minute            // a daemon or orphan younger than this is never touched
	hcMarkerAge  = 2 * hcMinAge               // an unmarked --serve daemon older than this is spared, not skipped
	hcIdleAge    = 30 * 24 * time.Hour        // a run dir must be idle this long before tidy removes it
	hcTermGrace  = 3 * time.Second            // wait after SIGTERM before SIGKILL
	hcKillSettle = 1 * time.Second            // wait after SIGKILL before warning
	hcDaemonWait = hcTermGrace + hcKillSettle // wait for a SIGKILLed daemon to leave the kernel
	hcPassDelay  = 15 * time.Second           // delay before the first pass
	hcPassEvery  = 24 * time.Hour             // interval between passes and keepalives
	hcMaxDaemons = 16                         // per-pass cap on stranded daemons ended
	hcMaxGroups  = 64                         // per-pass cap on orphan groups ended
)

// hcSummary counts what one pass did. Its zero value renders as "no action needed".
type hcSummary struct {
	strandedSignalled int
	strandedEnded     int
	strandedSurvived  int
	strandedLeftover  int
	orphanSignalled   int
	orphanSurvived    int
	daemonsRetired    int
	runDirsRemoved    int
	undecided         int
}

// String renders the pass summary in claustrum's own words (not the reference's verbatim
// format), and returns a short "no action needed" when the pass took no action.
func (s hcSummary) String() string {
	if s == (hcSummary{}) {
		return "no action needed"
	}
	return fmt.Sprintf("stranded daemons: signaled %d, ended %d, survived %d, left behind %d; orphan groups: signaled %d, survived %d; daemons retired %d; run dirs removed %d; undecided %d",
		s.strandedSignalled, s.strandedEnded, s.strandedSurvived, s.strandedLeftover,
		s.orphanSignalled, s.orphanSurvived, s.daemonsRetired, s.runDirsRemoved, s.undecided)
}

// hostCleaner is the periodic host-wide sweep. It touches only processes and run dirs within
// its roots, and never itself (selfPid / selfStart).
type hostCleaner struct {
	roots     *hostRoots
	ownSocket string
	ownRunDir string
	selfPid   int
	selfStart string // this daemon's own /proc start-ticks
}

// hcSignalPid signals a single process (not its group). A seam so tests never signal a real
// process; the group signal reuses killGroup from the reap.
var hcSignalPid = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// hcSignalTracked signals a leftover process the way the reference does: a group leader
// (pid == pgid) has its whole group signalled (kill(-pid)); a non-leader gets a single-pid
// signal. A helper spawned without its own group would be missed by a blind kill(-pid), which
// returns ESRCH for a pgid that does not exist.
func hcSignalTracked(t tracked, sig syscall.Signal) error {
	if t.pid == t.pgid {
		return killGroup(t.pid, sig)
	}
	return hcSignalPid(t.pid, sig)
}

// newHostCleaner builds a cleaner for this daemon's own socket and executable, or returns an
// error when the socket is not run-dir shaped or the executable is not a deployed daemon.
func newHostCleaner(socket string) (*hostCleaner, error) {
	exe, haveExe := "", false
	if p, err := os.Readlink(procRoot + "/self/exe"); err == nil {
		exe, haveExe = strings.TrimSuffix(p, " (deleted)"), true
	}
	roots, err := deriveRoots(socket, exe, haveExe)
	if err != nil {
		return nil, err
	}
	c := &hostCleaner{
		roots:     roots,
		ownSocket: filepath.Clean(socket),
		ownRunDir: filepath.Dir(filepath.Clean(socket)),
		selfPid:   os.Getpid(),
	}
	if s := hcReadStat(c.selfPid); s.ok {
		c.selfStart = s.startTicks
	}
	return c, nil
}

// endGroupsTwoPhase signals a set of process groups SIGTERM, waits the grace, SIGKILLs the
// survivors, waits the settle, and reports the ones that outlived SIGKILL. label names the
// entity in the logs ("orphaned process group", "process left behind by a stranded daemon").
// It returns the count signalled and the count that survived SIGKILL.
func endGroupsTwoPhase(groups []tracked, label string) (signalled, survived int) {
	var live []waitEntry
	for _, g := range groups {
		if g.pid < 2 {
			continue
		}
		err := hcSignalTracked(g, syscall.SIGTERM)
		if err != nil {
			if hcIsNoSuchProcess(err) {
				continue // already gone
			}
			if errors.Is(err, syscall.EPERM) {
				logInfof("[process.HostClean] %s %d belongs to another owner; not signalling it", label, g.pid)
				continue
			}
			// Any other error: still wait to see whether it exits.
		}
		signalled++
		live = append(live, waitEntry{tracked: g})
	}
	survivors := waitGone(live, hcClock().Add(hcTermGrace))
	for _, g := range survivors {
		_ = hcSignalTracked(g.tracked, syscall.SIGKILL)
	}
	for _, g := range waitGone(survivors, hcClock().Add(hcKillSettle)) {
		survived++
		logInfof("[process.HostClean] %s %d survived SIGKILL", label, g.pid)
	}
	return signalled, survived
}

// endGroups ends a set of orphaned Claude Code process groups (Pass step 9).
func (c *hostCleaner) endGroups(groups []tracked, sum *hcSummary) {
	sig, surv := endGroupsTwoPhase(groups, "orphaned process group")
	sum.orphanSignalled += sig
	sum.orphanSurvived += surv
}

// daemonTarget is a stranded daemon judgeDaemon marked for reaping, plus the leftover child
// process groups the endDaemons phase-B pass should clean up once the daemon is dead.
type daemonTarget struct {
	tracked
	runDir   string
	children []tracked
}

// endDaemons SIGKILLs each stranded daemon (single pid, no grace — it is already stranded),
// waits for them to exit, then SIGTERM/SIGKILLs the process groups they left behind (Pass
// step 7). A daemon that outlives SIGKILL keeps its children, which are left until it exits.
func (c *hostCleaner) endDaemons(targets []daemonTarget, sum *hcSummary) {
	if len(targets) == 0 {
		return
	}
	var live []waitEntry
	signaled := make(map[int]bool, len(targets))
	for _, t := range targets {
		sum.strandedSignalled++
		logInfof("[process.HostClean] SIGKILLing stranded daemon pid %d: its own socket resolves elsewhere and no process holds its run-dir lock", t.pid)
		err := hcSignalPid(t.pid, syscall.SIGKILL)
		if err != nil && errors.Is(err, syscall.EPERM) {
			logInfof("[process.HostClean] stranded daemon pid %d belongs to another owner; not signalling it", t.pid)
			continue
		}
		live = append(live, waitEntry{tracked: t.tracked})
		signaled[t.pid] = true
	}
	survivors := waitGone(live, hcClock().Add(hcDaemonWait))
	stuck := make(map[int]bool, len(survivors))
	for _, d := range survivors {
		stuck[d.pid] = true
		sum.strandedSurvived++
		logInfof("[process.HostClean] WARNING stranded daemon pid %d outlived SIGKILL and is wedged in the kernel; its children stay put until it finally exits", d.pid)
	}
	sum.strandedEnded += len(live) - len(survivors)
	// Phase B: end the child groups of daemons that were signaled and actually died. A child
	// of a daemon that outlived SIGKILL (still its parent) is left alone.
	var reapable []tracked
	for _, t := range targets {
		if signaled[t.pid] && !stuck[t.pid] {
			reapable = append(reapable, t.children...)
		}
	}
	sig, _ := endGroupsTwoPhase(reapable, "process left behind by a stranded daemon")
	sum.strandedLeftover += sig
}

// hcDaemonChildMarker is the environment entry a daemon-spawned process carries.
const hcDaemonChildMarker = "CLAUDE_SSH_DAEMON_CHILD=1"

// verifyListener returns "" when the process answering a socket is verifiably one of our
// daemons serving that socket, or a short reason otherwise. It is the strict gate before the
// cleaner treats a live listener as (or evicts it as) our daemon.
func (c *hostCleaner) verifyListener(pid int, socket string) string {
	t, res := inspect(pid, true)
	if res != hcInspectOK {
		return "the listener could not be inspected"
	}
	if !t.sameUID {
		return "the listener belongs to another user"
	}
	if !t.sameNS {
		return "the listener runs in another namespace"
	}
	if t.exe == "" || !c.roots.isDaemonBinary(t.exe) {
		return "its binary is not our deployed daemon"
	}
	if s := serveArgv(t.argv, c.roots.daemonBin); s == "" || !c.roots.sameSocketPath(s, socket) {
		return "it serves a different socket"
	}
	if !hcHasEnv(t.env, hcDaemonChildMarker) {
		return "it lacks the daemon-child marker"
	}
	return ""
}

// daemonVerdict is judgeDaemon's decision.
type daemonVerdict int

const (
	dvSkip  daemonVerdict = iota // silent: not a candidate
	dvSpare                      // logged with a reason
	dvReap                       // end it
)

// judgeDaemon decides one of our --serve daemons: skip (not a candidate), spare (with a
// reason), or reap. It reaps ONLY a marked, old-enough daemon whose own socket no longer
// leads to a live listener, that holds no run-dir lock, and that is not still serving a
// client — a genuinely stranded daemon. Anything ambiguous is spared; anything fresh, not
// ours, or still healthy is skipped. d is already inspected (with its environment).
func (c *hostCleaner) judgeDaemon(d hcTracked) (daemonVerdict, string, *daemonTarget) {
	if d.pid == c.selfPid || !d.sameNS {
		return dvSkip, "", nil // never ourselves, never another namespace
	}
	if d.stopped {
		return dvSpare, "could not read it (it is stopped)", nil
	}
	if d.exe == "" || !c.roots.isDaemonBinary(d.exe) {
		return dvSkip, "", nil
	}
	socket := serveArgv(d.argv, c.roots.daemonBin)
	if socket == "" {
		return dvSkip, "", nil // not a --serve daemon
	}
	if !d.haveAge {
		return dvSpare, "its age could not be determined", nil
	}
	if hcClock().Sub(d.startWall) < hcMinAge {
		return dvSkip, "", nil // too young to touch
	}
	if d.argv == nil {
		return dvSpare, "its argv could not be read", nil
	}
	if !hcHasEnv(d.env, hcDaemonChildMarker) {
		if hcClock().Sub(d.startWall) > hcMarkerAge {
			return dvSpare, "it runs our --serve daemon without the daemon-child marker (started by hand, or predating the marker)", nil
		}
		return dvSkip, "", nil // unmarked but not yet old enough to flag
	}
	runDir := filepath.Dir(socket)
	switch state, peer, uid := probeSocket(socket); state {
	case hcSockLive:
		switch {
		case peer == d.pid:
			return dvSkip, "", nil // its socket still leads to itself: healthy
		case uid != uint32(hcGetuid()):
			return dvSpare, "another user's process answers its socket", nil
		case c.verifyListener(peer, socket) != "":
			return dvSpare, fmt.Sprintf("pid %d answers its socket but did not verify as one of our daemons (%s)", peer, socket), nil
		default:
			return dvSkip, "", nil // another verified daemon serves it; not ours to reap
		}
	case hcSockUnknown:
		return dvSpare, fmt.Sprintf("the probe of its socket %s was inconclusive", socket), nil
	}
	// The socket is missing/stale/not-a-socket: it no longer leads to a live daemon.
	switch lockState, _, _ := lockProbe(runDir); lockState {
	case hcLockHeld:
		return dvSkip, "", nil // a live process holds its run-dir lock
	case hcLockUnknown:
		// The reference spares a daemon whose lock state it cannot determine, rather than
		// reaping it: an unreadable lock is not proof the daemon is dead.
		return dvSpare, "whether a process holds its run-dir lock could not be determined", nil
	}
	if hcBusy(d.pid) {
		return dvSpare, "it still has a live connection; skipped until that closes", nil
	}
	return dvReap, "", &daemonTarget{tracked: d.asTracked(), runDir: runDir}
}

// hcArgvHasStreamJSON reports whether argv marks a Claude Code stream process.
func hcArgvHasStreamJSON(argv []string) bool {
	for _, a := range argv {
		if a == "stream-json" || strings.HasSuffix(a, "=stream-json") {
			return true
		}
	}
	return false
}

// judgeOrphan reports whether a process is an orphaned Claude Code child whose owning daemon
// is gone (the caller passes only daemon-gone candidates). It must be this user's own process,
// in this daemon's namespace, running one of our deployed CLI binaries, old enough, a
// stream-json process, and carrying the daemon-child marker. The uid, namespace and CLI-binary
// gates match the reference: they keep the cleaner from ending an unrelated same-user process
// that merely inherited the marker and happens to carry a stream-json argument. The reason is
// for the spare log; an empty reason with reap=false is a silent skip.
func (c *hostCleaner) judgeOrphan(t hcTracked) (reap bool, reason string) {
	if t.stopped {
		return false, "could not read it (it is stopped)"
	}
	if !t.sameUID || !t.sameNS {
		return false, "" // not ours: a foreign uid or namespace
	}
	if !t.haveAge {
		return false, "its age could not be determined"
	}
	if hcClock().Sub(t.startWall) < hcMinAge {
		return false, "" // too young
	}
	if t.exe == "" || !c.roots.isCliBinary(t.exe) {
		return false, "" // not one of our deployed CLI binaries
	}
	if t.argv == nil {
		return false, "its argv could not be read"
	}
	if !hcArgvHasStreamJSON(t.argv) {
		return false, "" // not a stream process
	}
	if t.env == nil {
		return false, "its environ could not be read"
	}
	if !hcHasEnv(t.env, hcDaemonChildMarker) {
		return false, "" // not a daemon child
	}
	if _, ok := hcFdTargets(t.pid); !ok {
		return false, "its open descriptors were unreadable" // cannot confirm it, so spare it
	}
	return true, ""
}

// retireAbandoned SIGTERMs a still-live daemon whose run dir has been idle past the threshold.
// It re-verifies identity (rejecting a reused pid), requires the daemon be old enough and
// verifiably ours, then SIGTERMs the single pid and waits the grace. It returns true only when
// the daemon exited; it never escalates to SIGKILL (that is judgeDaemon/endGroups' job).
func (c *hostCleaner) retireAbandoned(pid int, socket string) bool {
	if pid < 2 || pid == c.selfPid {
		return false
	}
	t, res := inspect(pid, false)
	if res != hcInspectOK || !t.haveAge || hcClock().Sub(t.startWall) < hcMinAge {
		return false
	}
	if reason := c.verifyListener(pid, socket); reason != "" {
		logInfof("[process.HostClean] run-dir daemon pid %d: %s; left running", pid, reason)
		return false
	}
	logInfof("[process.HostClean] SIGTERM to idle daemon pid %d: nothing has connected to its run dir within the window", pid)
	if err := hcSignalPid(pid, syscall.SIGTERM); err != nil && !hcIsNoSuchProcess(err) {
		return false
	}
	if len(waitGone([]waitEntry{{tracked: t.asTracked()}}, hcClock().Add(hcTermGrace))) == 0 {
		return true
	}
	logInfof("[process.HostClean] daemon pid %d did not exit within the grace after SIGTERM; left running", pid)
	return false
}

// ---- run-dir tidy + the pass orchestration + lifecycle (reference build 19f30c46) ----

const (
	hcMaxRunDirs    = 64           // run dirs examined per pass
	hcMaxRetire     = 8            // daemon retirements per pass
	hcStagingPrefix = ".removing-" // prefix of a run dir mid-removal
)

// Filesystem seams so a tidy test never renames or removes a real run dir.
var (
	hcRename    = os.Rename
	hcRemoveAll = os.RemoveAll
	hcChtimes   = os.Chtimes
)

// runDirEntry is one run dir the tidy pass considers.
type runDirEntry struct {
	name    string
	dirPath string        // <root>/<name>
	socket  string        // <root>/<name>/rpc.sock
	idle    time.Duration // how long since the dir last showed activity
}

// runDirs enumerates the run dirs under the roots, deduped by device+inode, capped at
// hcMaxRunDirs.
func (c *hostCleaner) runDirs() []runDirEntry {
	var out []runDirEntry
	seen := make(map[string]bool)
	now := hcClock()
	for _, root := range c.roots.runRoots() {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			name := e.Name()
			if name == "." || name == ".." {
				continue
			}
			dirPath := filepath.Join(root, name)
			fi, err := os.Stat(dirPath)
			if err != nil || !fi.IsDir() {
				continue
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				key := strconv.FormatUint(st.Dev, 10) + ":" + strconv.FormatUint(st.Ino, 10)
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			idle, _ := hcDirIdle(dirPath, now)
			out = append(out, runDirEntry{name: name, dirPath: dirPath, socket: filepath.Join(dirPath, rpcSockBasename), idle: idle})
			if len(out) >= hcMaxRunDirs {
				logInfof("[process.HostClean] run-dir count over %d: capping this sweep at the first %d", hcMaxRunDirs, hcMaxRunDirs)
				return out
			}
		}
	}
	return out
}

// tidyRunDirs retires stale run dirs: a dir idle past the threshold (or a leftover staging
// dir) whose socket no longer answers and whose lock nobody holds is renamed aside and
// removed; a dir that answers, holds a live lock, or comes back into use mid-removal is kept.
func (c *hostCleaner) tidyRunDirs(entries []runDirEntry, sum *hcSummary) {
	retired := 0
	for _, e := range entries {
		staging := strings.HasPrefix(e.name, hcStagingPrefix)
		if e.idle < hcIdleAge && !staging {
			continue // too fresh and not an interrupted removal
		}
		if retired >= hcMaxRetire {
			continue // per-pass retirement budget spent
		}
		state, peer, _ := probeSocket(e.socket)
		if state == hcSockLive {
			if c.retireAbandoned(peer, e.socket) {
				sum.daemonsRetired++
				retired++
				state, _, _ = probeSocket(e.socket)
			}
		}
		switch state {
		case hcSockLive:
			continue // something still answers: keep
		case hcSockUnknown:
			continue // the socket probe was inconclusive: keep it
		}
		switch lockState, _, _ := lockProbe(e.dirPath); lockState {
		case hcLockHeld:
			continue // a live process holds its lock: keep
		case hcLockUnknown:
			continue // its lock could not be examined: keep
		}
		if c.removeRunDir(e, staging) {
			sum.runDirsRemoved++
		}
	}
}

// removeRunDir removes a run dir in two steps: rename it to a .removing- staging name, then
// RemoveAll it. If a socket or lock reappears in the rename window it renames the dir back.
func (c *hostCleaner) removeRunDir(e runDirEntry, alreadyStaging bool) bool {
	stagingPath := e.dirPath
	if !alreadyStaging {
		root := filepath.Dir(e.dirPath)
		stagingPath = filepath.Join(root, hcStagingPrefix+e.name+"-"+strconv.FormatInt(hcClock().UnixNano(), 36))
		if err := hcRename(e.dirPath, stagingPath); err != nil {
			logInfof("[process.HostClean] run dir %q: could not stage it for removal: %v; kept", e.name, err)
			return false
		}
		sock := filepath.Join(stagingPath, rpcSockBasename)
		if st, _, _ := probeSocket(sock); st == hcSockLive {
			c.undoRename(stagingPath, e.dirPath, "a listener reappeared mid-removal")
			return false
		}
		if ls, _, _ := lockProbe(stagingPath); ls == hcLockHeld {
			c.undoRename(stagingPath, e.dirPath, "the run-dir lock was re-acquired mid-removal")
			return false
		}
	}
	if err := hcRemoveAll(stagingPath); err != nil {
		logInfof("[process.HostClean] run dir %q: staged for removal but the remove failed: %v; left staged", e.name, err)
		return false
	}
	logInfof("[process.HostClean] removed idle run dir %q: its socket was dead and its lock unheld", e.name)
	return true
}

// undoRename renames a .removing- staging dir back to its original name when a removal is
// aborted because the dir came back into use.
func (c *hostCleaner) undoRename(stagingPath, origPath, reason string) {
	orig := filepath.Base(origPath)
	if err := hcRename(stagingPath, origPath); err != nil {
		logInfof("[process.HostClean] run dir %q could not be restored (%s while removing it): %v", orig, reason, err)
		return
	}
	logInfof("[process.HostClean] run dir %q kept: %s while removing it", orig, reason)
}

// Pass runs one host-wide sweep and returns what it did. It refuses to act unless its own
// socket still leads to itself, ends stranded daemons and their leftover groups, ends
// orphaned Claude Code groups, and (unless the host has too many processes) tidies stale run
// dirs.
func (c *hostCleaner) Pass() hcSummary {
	var sum hcSummary
	if state, peer, _ := probeSocket(c.ownSocket); state != hcSockLive || peer != c.selfPid {
		logInfof("[process.HostClean] own socket %q does not lead to this daemon (state %d, pid %d); skipping this pass", c.ownSocket, state, peer)
		return sum
	}
	pids, tooMany, ok := hcSnapshot()
	if !ok {
		logInfof("[process.HostClean] could not enumerate this user's process list; skipping this sweep")
		return sum
	}
	procs := make([]hcTracked, 0, len(pids))
	byGroup := make(map[int][]hcTracked)
	daemonPids := make(map[int]bool)
	for _, pid := range pids {
		t, res := inspect(pid, true)
		if res != hcInspectOK {
			continue
		}
		procs = append(procs, t)
		byGroup[t.pgid] = append(byGroup[t.pgid], t)
		if t.exe != "" && c.roots.isDaemonBinary(t.exe) && serveArgv(t.argv, c.roots.daemonBin) != "" {
			daemonPids[t.pid] = true
		}
	}
	// Stranded daemons.
	var targets []daemonTarget
	for _, t := range procs {
		if !daemonPids[t.pid] || len(targets) >= hcMaxDaemons {
			continue
		}
		v, reason, target := c.judgeDaemon(t)
		switch v {
		case dvSpare:
			logInfof("[process.HostClean] spared daemon pid %d this sweep: %s", t.pid, reason)
			sum.undecided++
		case dvReap:
			for _, ch := range procs {
				if ch.ppid == t.pid && ch.pid != t.pid {
					target.children = append(target.children, ch.asTracked())
				}
			}
			targets = append(targets, *target)
		}
	}
	c.endDaemons(targets, &sum)
	// Orphaned Claude Code groups: a group leader whose owning daemon is no longer live.
	var orphanGroups []tracked
	for pgid, members := range byGroup {
		var leader *hcTracked
		for i := range members {
			if members[i].pid == pgid {
				leader = &members[i]
				break
			}
		}
		if leader == nil || daemonPids[leader.ppid] {
			continue // no leader, or still parented by a live daemon
		}
		reap, reason := c.judgeOrphan(*leader)
		if reason != "" {
			logInfof("[process.HostClean] spared process group %d this sweep: %s", pgid, reason)
			sum.undecided++
		}
		if reap && len(orphanGroups) < hcMaxGroups {
			orphanGroups = append(orphanGroups, leader.asTracked())
		}
	}
	c.endGroups(orphanGroups, &sum)
	if !tooMany {
		c.tidyRunDirs(c.runDirs(), &sum)
	}
	return sum
}

// Keepalive refreshes the mtime of the cleaner's own run dir so an age-based tidy never
// retires the dir hosting this very daemon.
func (c *hostCleaner) Keepalive() {
	now := hcClock()
	if err := hcChtimes(c.ownRunDir, now, now); err != nil {
		logInfof("[process.HostClean] could not bump the mtime of own run dir %q (the keepalive that keeps the retire sweep off it): %v", c.ownRunDir, err)
	}
}

// Start launches the two background loops: a keepalive that touches the own run dir, and the
// periodic sweep (first pass after an initial delay, then once per interval). Fire-and-forget.
func (c *hostCleaner) Start() {
	go func() {
		for {
			c.Keepalive()
			hcSleep(hcPassEvery)
		}
	}()
	go func() {
		hcSleep(hcPassDelay)
		for {
			start := hcClock()
			sum := c.Pass()
			logInfof("[process.HostClean] sweep finished (%s elapsed): %s", hcClock().Sub(start).Round(time.Millisecond), sum)
			hcSleep(hcPassEvery)
		}
	}()
}

// startHostCleaner builds the cleaner for this daemon's socket and starts its background
// sweep. It is a no-op when the socket is not a run-dir socket or the executable is not a
// deployed daemon (the cleaner has no roots to act within). Called from -serve startup.
func startHostCleaner(socket string) {
	if socket == "" {
		return
	}
	c, err := newHostCleaner(socket)
	if err != nil {
		return // not a deployed run-dir daemon; nothing to clean
	}
	c.Start()
}
