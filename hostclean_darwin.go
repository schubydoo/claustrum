//go:build darwin

package main

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// The darwin half of the host cleaner (reference build 19f30c46). It provides the OS
// primitives the shared decision layer (hostclean.go) calls: process enumeration and
// inspection via sysctl kern.proc + kinfo_proc (darwin has no /proc), argv/env via
// KERN_PROCARGS2, open-file / socket-busy / lock probes via /usr/sbin/lsof, and the
// socket-peer pid via LOCAL_PEERPID. The judges, enders, tidy and lifecycle are shared.
//
// This path is DESTRUCTIVE. Every enumeration, kinfo read and lsof shell-out goes through a
// package-var seam, so unit tests drive the whole decision without a real process or lsof.
// The real sweep is validated only on a throwaway macOS VM.

// kinfo_proc offsets (darwin-amd64/arm64, verified against `ps` on the target SDK). The
// struct is a stable legacy ABI; stride is sizeof(kinfo_proc).
const (
	ckCTLKern     = 1  // CTL_KERN
	ckKernProc    = 14 // KERN_PROC
	ckKernProcPID = 1  // KERN_PROC_PID
	ckKernProcUID = 5  // KERN_PROC_UID
	ckProcArgs2   = 49 // KERN_PROCARGS2

	kinfoStride  = 0x288 // sizeof(kinfo_proc) = 648
	offStartSec  = 0x000 // kp_proc.p_starttime.tv_sec  (int64)
	offStartUsec = 0x008 // kp_proc.p_starttime.tv_usec (int32)
	offStat      = 0x024 // kp_proc.p_stat (byte)
	offPid       = 0x028 // kp_proc.p_pid (int32)
	offUIDReal   = 0x188 // real uid
	offUIDeff    = 0x1a4 // effective/cred uid
	offPpid      = 0x230 // kp_eproc.e_ppid (int32)
	offPgid      = 0x234 // kp_eproc.e_pgid (int32)

	statSSTOP = 4 // SSTOP: stopped (the darwin analogue of linux state T/t)
	statZOMB  = 5 // SZOMB: a zombie, treated as gone
)

// hcLsofPath is /usr/sbin/lsof. hcLsofEnv pins the locale so the -F output format is stable.
// The path is a var only so a test can point it at a fixture command.
var hcLsofPath = "/usr/sbin/lsof"

var hcLsofEnv = []string{"LC_ALL=C", "LANG=C", "PATH=/bin:/usr/bin:/usr/sbin"}

// The three bounds on one lsof run.
//
// claustrum bounds every lsof run its host cleaner makes. The three values are
// claustrum's own. The reference side is not probe-measured, because staging it needs
// an lsof that hangs on a macOS host.
//
// Why three and not two. The deadline kills the command. The wait delay then bounds
// how long the run waits for the output pipe to close, which covers a child that
// exited while a grandchild still holds the pipe. Neither covers a process wedged in
// the kernel, because waiting on a command waits on the process first and only then
// consults the delay, and that is exactly the case these bounds exist for: an lsof
// stuck on an unresponsive mount. The abandon bound is what covers it: the run is
// given up on.
//
// The gap between the bounds is what makes D17 safe. A merely slow lsof is killed by
// the deadline and its output pipe closes within the wait delay, so the run COMPLETES
// with no output well before the abandon bound. So the two empty results really do
// mean different things, and only the second one is the divergence.
//
// The cost of getting this wrong is a whole cleaner pass: retireAbandoned samples
// hcBusy for up to hcBusyWindow, and on darwin every one of those samples is an lsof
// run.
var (
	hcLsofTimeout   = 15 * time.Second
	hcLsofWaitDelay = 1 * time.Second
	hcLsofAbandon   = 17 * time.Second
)

// Seams. Each is a package var a unit test replaces to drive the decision tree without a real
// sysctl or lsof, mirroring linux's procRoot / hcDial seams.
var (
	// hcKinfoPID returns the kinfo_proc bytes for one pid (sysctl KERN_PROC_PID), or ok=false
	// when the process is gone.
	hcKinfoPID = func(pid int) ([]byte, bool) {
		return sysctlBytes([]int32{ckCTLKern, ckKernProc, ckKernProcPID, int32(pid)})
	}
	// hcKinfoUID returns the kinfo_proc array for every process owned by uid (sysctl
	// KERN_PROC_UID); the uid filter is applied kernel-side.
	hcKinfoUID = func(uid int) ([]byte, bool) {
		return sysctlBytes([]int32{ckCTLKern, ckKernProc, ckKernProcUID, int32(uid)})
	}
	// hcProcArgs2 returns the KERN_PROCARGS2 buffer for one pid (argc + exec path + argv +
	// env), or ok=false when it is unreadable (gone, or another user's process → EPERM).
	hcProcArgs2 = func(pid int) ([]byte, bool) {
		return sysctlBytes([]int32{ckCTLKern, ckProcArgs2, int32(pid)})
	}
	// runLsof runs `/usr/sbin/lsof -nP -w <args...>` with the pinned env and returns stdout;
	// lsof exits non-zero when it simply finds nothing, so the stdout is used regardless. Like
	// the linux lock/busy probes (which parse /proc and read an unreadable source as not-held),
	// the caller keys only on the output. lsof
	// is a system binary always present on macOS and needs no privilege for this user's own
	// processes, so a run failure is not a reachable path.
	//
	// Bounded three ways. An lsof that stalls on an unresponsive
	// mount would otherwise hold the whole cleaner pass open with nothing to end it, and
	// the deadline alone does not cover that case. See the bound constants above.
	//
	// The run happens on its own goroutine so the abandon bound can win the race. The
	// goroutine is left to finish on its own; the channel is buffered, so its send never
	// blocks even when nobody is listening any more.
	//
	// Returning also cancels the context, which kills a child that can still be killed,
	// so an ordinary stall leaves nothing behind. For the case this bound exists for,
	// a process wedged in the kernel, the kill does not land and the goroutine really
	// does outlive the call. That is the point: the CALLER is unblocked either way, and
	// the goroutine costs one buffered send whenever the process finally goes.
	//
	// ⚠️ A wedged mount therefore leaks one process and one goroutine per abandoned run,
	// and nothing caps that. It stays uncapped here: a pass runs
	// every hcPassEvery (24 h) and reaches at most a couple of runs per candidate, so
	// the accumulation is slow, and a host with a permanently wedged mount has a larger
	// problem than this daemon.
	//
	// The second result says whether the run COMPLETED. An abandoned run answers false
	// with no output, which is not the same fact as "lsof looked and found nothing" —
	// see D17 and hcBusy below.
	runLsof = func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), hcLsofTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, hcLsofPath, append([]string{"-nP", "-w"}, args...)...)
		cmd.Env = hcLsofEnv
		cmd.WaitDelay = hcLsofWaitDelay
		done := make(chan string, 1)
		go func() {
			out, _ := cmd.Output()
			done <- string(out)
		}()
		abandon := time.NewTimer(hcLsofAbandon)
		defer abandon.Stop()
		select {
		case out := <-done:
			return out, true
		case <-abandon.C:
			return "", false // gave up on the run; the caller learns nothing about the pid
		}
	}
)

// sysctlBytes performs the two-call sysctl dance (size probe, then read) for a MIB and returns
// the buffer, or ok=false on any error or an empty result.
func sysctlBytes(mib []int32) ([]byte, bool) {
	var n uintptr
	if _, _, e := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		0, uintptr(unsafe.Pointer(&n)), 0, 0); e != 0 || n == 0 {
		return nil, false
	}
	buf := make([]byte, n)
	if _, _, e := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0); e != 0 {
		return nil, false
	}
	return buf[:n], true
}

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off : off+4]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }

// kinfoStart formats the process start-time as "<sec>.<usec>", the pid-reuse identity token.
// It is only ever compared kinfo-to-kinfo (snapshot vs the inspect re-read), so it needs to be
// self-consistent, not byte-identical to any ps rendering.
func kinfoStart(buf []byte) string {
	return strconv.FormatUint(le64(buf, offStartSec), 10) + "." + strconv.FormatUint(uint64(le32(buf, offStartUsec)), 10)
}

// kinfoStartWall turns the start-time timeval into a wall-clock time for the age gate.
func kinfoStartWall(buf []byte) time.Time {
	return time.Unix(int64(le64(buf, offStartSec)), int64(le32(buf, offStartUsec))*1000)
}

// hcReadIdent returns pid's process-group id and start-time token for the pid-reuse re-check
// (tracked.fate). ok is false when the process is gone or a zombie.
func hcReadIdent(pid int) (pgid int, startTicks string, ok bool) {
	buf, k := hcKinfoPID(pid)
	if !k || len(buf) < kinfoStride || buf[offStat] == statZOMB {
		return 0, "", false
	}
	return int(int32(le32(buf, offPgid))), kinfoStart(buf), true
}

// hcProcArgs reads pid's KERN_PROCARGS2 block: the executable path, argv, and (when wantEnv)
// env. ok is false when the block is unreadable (gone or another user's process).
func hcProcArgs(pid int, wantEnv bool) (exe string, argv, env []string, ok bool) {
	buf, k := hcProcArgs2(pid)
	if !k || len(buf) < 4 {
		return "", nil, nil, false
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	if argc < 0 {
		return "", nil, nil, false
	}
	p := 4
	// The executable path is the first NUL-terminated string after argc.
	start := p
	for p < len(buf) && buf[p] != 0 {
		p++
	}
	exe = string(buf[start:p])
	// Skip the NUL padding between the exec path and argv[0].
	for p < len(buf) && buf[p] == 0 {
		p++
	}
	// argc NUL-terminated argv strings.
	for i := 0; i < argc && p < len(buf); i++ {
		s := p
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		argv = append(argv, string(buf[s:p]))
		p++ // step past the terminating NUL
	}
	if wantEnv {
		for p < len(buf) {
			s := p
			for p < len(buf) && buf[p] != 0 {
				p++
			}
			if p > s {
				env = append(env, string(buf[s:p]))
			}
			p++
		}
	}
	return exe, argv, env, true
}

// inspect gathers the full record for one pid via kinfo_proc + KERN_PROCARGS2. It re-reads the
// start-time at the end so a pid that vanished or was reused during the read is reported gone.
// darwin has no namespaces, so sameNS is always true (all same-machine processes are peers).
func inspect(pid int, wantEnv bool) (hcTracked, hcInspectResult) {
	buf, ok := hcKinfoPID(pid)
	if !ok || len(buf) < kinfoStride || buf[offStat] == statZOMB {
		return hcTracked{}, hcInspectGone
	}
	startTicks := kinfoStart(buf)
	uid := uint32(hcGetuid())
	t := hcTracked{
		pid:        pid,
		ppid:       int(int32(le32(buf, offPpid))),
		pgid:       int(int32(le32(buf, offPgid))),
		startTicks: startTicks,
		startWall:  kinfoStartWall(buf),
		haveAge:    true,
		// darwin requires BOTH the real and effective uid to match ours (stricter than linux,
		// which checks only the real uid); the snapshot's KERN_PROC_UID filter is effective-uid.
		// This only ever spares more, so it stays on the conservative side.
		sameUID: le32(buf, offUIDReal) == uid && le32(buf, offUIDeff) == uid,
		sameNS:  true,
		stopped: buf[offStat] == statSSTOP,
	}
	if exe, argv, env, ok := hcProcArgs(pid, wantEnv); ok {
		t.exe = exe
		t.argv = argv
		if wantEnv {
			t.env = env
		}
	}
	// Re-read: a pid reused (or gone) mid-inspect has a different (or absent) start-time, and a
	// pid that turned into a zombie mid-inspect is gone too (matching the first-read SZOMB skip
	// and linux's re-read, which drops state Z/X).
	if buf2, ok := hcKinfoPID(pid); !ok || len(buf2) < kinfoStride || buf2[offStat] == statZOMB || kinfoStart(buf2) != startTicks {
		return hcTracked{}, hcInspectGone
	}
	return t, hcInspectOK
}

// hcSnapshot enumerates this user's pids via sysctl KERN_PROC_UID (the uid filter is
// kernel-side). Zombies are skipped. It appends at most hcMaxSnapshot pids and flags overflow
// when the user owns more, so the pass acts on per-process facts but skips run-dir retirement.
// ok is false only when the sysctl fails.
func hcSnapshot() (pids []int, overflow bool, ok bool) {
	buf, k := hcKinfoUID(hcGetuid())
	if !k {
		return nil, false, false
	}
	n := len(buf) / kinfoStride
	matched := 0
	for i := 0; i < n; i++ {
		e := buf[i*kinfoStride:]
		if e[offStat] == statZOMB {
			continue
		}
		pid := int(int32(le32(e, offPid)))
		if pid <= 0 {
			continue
		}
		matched++
		if matched > hcMaxSnapshot {
			overflow = true
			continue
		}
		pids = append(pids, pid)
	}
	return pids, overflow, true
}

// peerOf returns the pid and uid of the process on the other end of a unix connection. darwin's
// LOCAL_PEERPID yields only the peer pid (unlike linux SO_PEERCRED, which also gives the uid),
// so the uid is derived by reading the peer pid's kinfo. ok is false when either read fails.
func peerOf(conn *net.UnixConn) (pid int, uid uint32, ok bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var peerPid int
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		// SOL_LOCAL = 0, LOCAL_PEERPID = 2.
		peerPid, cerr = syscall.GetsockoptInt(int(fd), 0, 2)
	}); err != nil || cerr != nil || peerPid <= 1 {
		return 0, 0, false
	}
	buf, k := hcKinfoPID(peerPid)
	if !k || len(buf) < kinfoStride {
		return 0, 0, false
	}
	return peerPid, le32(buf, offUIDReal), true
}

// lsofRec is one open-file record from `lsof -F ftn`: fd name, type and object name.
type lsofRec struct {
	fd, typ, name string
}

// parseLsofFtn parses `lsof -F ftn` output. Each line begins with a field letter: 'f'=fd
// (starts a record), 't'=type, 'n'=name; 'p' is the process header. A record is complete once
// its 'f' has been seen; 't'/'n' fill the current record.
func parseLsofFtn(out string) []lsofRec {
	var recs []lsofRec
	cur := -1
	for _, ln := range strings.Split(out, "\n") {
		if ln == "" {
			continue
		}
		val := ln[1:]
		switch ln[0] {
		case 'f':
			recs = append(recs, lsofRec{fd: val})
			cur = len(recs) - 1
		case 't':
			if cur >= 0 {
				recs[cur].typ = val
			}
		case 'n':
			if cur >= 0 {
				recs[cur].name = val
			}
		}
	}
	return recs
}

// hcFdTargets returns each fd mapped to its lsof name. The second result is false when lsof
// returned nothing for the pid (the process gone or its files unreadable), and also when the
// run was abandoned. Both read as "unreadable", which is what the callers already act on.
func hcFdTargets(pid int) (map[string]string, bool) {
	out, ok := runLsof("-p", strconv.Itoa(pid), "-F", "ftn")
	if !ok {
		return nil, false
	}
	recs := parseLsofFtn(out)
	if len(recs) == 0 {
		return nil, false
	}
	m := make(map[string]string, len(recs))
	for _, r := range recs {
		m[r.fd] = r.name
	}
	return m, true
}

// hcBusy reports whether the process has a live client on one of its unix sockets. A unix
// record whose name shows a connected peer ("->") is an immediate yes; otherwise more than one
// unix endpoint means an accepted connection sits alongside the bare listener.
//
// D17: an ABANDONED run answers busy. A run that finished and saw nothing and a run that was
// given up on both produce no output. claustrum keeps them apart, because only the first is
// evidence. The reference side is not probe-measured. This arm is reachable only when the
// read failed outright.
func hcBusy(pid int) bool {
	out, ok := runLsof("-p", strconv.Itoa(pid), "-F", "ftn")
	if !ok {
		return true
	}
	unix := 0
	for _, r := range parseLsofFtn(out) {
		if r.typ != "unix" {
			continue
		}
		if strings.HasPrefix(r.name, "->") {
			return true
		}
		unix++
	}
	return unix > 1
}

// hcLockHeldAt reports whether a live process holds path open (its run-dir lock). darwin has no
// /proc/locks, so it asks lsof whether any process has the path open. fi is unused on darwin.
//
// Deliberately NOT under D17: an abandoned run reads as not-held here.
// The same argument would apply — a held lock that reads stale lets the tidy remove a live
// daemon's run dir — but D17 was scoped to the busy predicate, and widening a divergence
// without deciding it is how one grows by accident. Raised rather than taken.
func hcLockHeldAt(path string, _ os.FileInfo) bool {
	out, _ := runLsof("-F", "p", path)
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "p") {
			return true
		}
	}
	return false
}

// hcSelfExe returns this daemon's own executable path, from its KERN_PROCARGS2 exec path — the
// same source inspect uses for other processes, so the deployed-daemon check is consistent.
func hcSelfExe() (exe string, ok bool) {
	exe, _, _, ok = hcProcArgs(os.Getpid(), false)
	return exe, ok && exe != ""
}
