//go:build darwin

package main

import (
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// kinfoBuf builds a synthetic kinfo_proc record at the darwin offsets.
func kinfoBuf(pid, ppid, pgid, uidReal, uidEff int, stat byte, sec, usec uint64) []byte {
	b := make([]byte, kinfoStride)
	binary.LittleEndian.PutUint64(b[offStartSec:], sec)
	binary.LittleEndian.PutUint32(b[offStartUsec:], uint32(usec))
	b[offStat] = stat
	binary.LittleEndian.PutUint32(b[offPid:], uint32(pid))
	binary.LittleEndian.PutUint32(b[offUIDReal:], uint32(uidReal))
	binary.LittleEndian.PutUint32(b[offUIDeff:], uint32(uidEff))
	binary.LittleEndian.PutUint32(b[offPpid:], uint32(ppid))
	binary.LittleEndian.PutUint32(b[offPgid:], uint32(pgid))
	return b
}

// hcProcArgsBuf builds a synthetic KERN_PROCARGS2 buffer: argc, exec path (NUL + a padding
// NUL), argc argv strings (NUL), then env strings (NUL).
func hcProcArgsBuf(exe string, argv, env []string) []byte {
	var argc [4]byte
	binary.LittleEndian.PutUint32(argc[:], uint32(len(argv)))
	b := append([]byte{}, argc[:]...)
	b = append(b, exe...)
	b = append(b, 0, 0)
	for _, a := range argv {
		b = append(b, a...)
		b = append(b, 0)
	}
	for _, e := range env {
		b = append(b, e...)
		b = append(b, 0)
	}
	return b
}

// seamUID fixes hcGetuid for the sameUID checks.
func seamUID(t *testing.T, uid int) {
	t.Helper()
	old := hcGetuid
	t.Cleanup(func() { hcGetuid = old })
	hcGetuid = func() int { return uid }
}

func TestInspectDarwin(t *testing.T) {
	seamUID(t, 501)
	const sec, usec = 1789319316, 938743
	oldK, oldA := hcKinfoPID, hcProcArgs2
	t.Cleanup(func() { hcKinfoPID, hcProcArgs2 = oldK, oldA })

	// A live process owned by us, running our daemon binary with a serve argv and the marker.
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(4242, 1, 4242, 501, 501, 2 /*SRUN*/, sec, usec), true }
	hcProcArgs2 = func(int) ([]byte, bool) {
		return hcProcArgsBuf("/opt/srv/1/server", []string{"/opt/srv/1/server", "-serve", "-socket", "/opt/run/1/rpc.sock"},
			[]string{"CLAUDE_SSH_DAEMON_CHILD=1", "PATH=/usr/bin"}), true
	}
	tr, res := inspect(4242, true)
	if res != hcInspectOK {
		t.Fatalf("res = %d, want hcInspectOK", res)
	}
	if tr.pid != 4242 || tr.ppid != 1 || tr.pgid != 4242 {
		t.Errorf("ids wrong: pid=%d ppid=%d pgid=%d", tr.pid, tr.ppid, tr.pgid)
	}
	if tr.startTicks != "1789319316.938743" {
		t.Errorf("startTicks = %q, want 1789319316.938743", tr.startTicks)
	}
	if !tr.sameUID || !tr.sameNS || tr.stopped {
		t.Errorf("sameUID=%v sameNS=%v stopped=%v; want true,true,false", tr.sameUID, tr.sameNS, tr.stopped)
	}
	if tr.exe != "/opt/srv/1/server" || len(tr.argv) != 4 {
		t.Errorf("exe=%q argv=%v", tr.exe, tr.argv)
	}
	if !hcHasEnv(tr.env, hcDaemonChildMarker) {
		t.Errorf("env missing the daemon-child marker: %v", tr.env)
	}
	if !tr.haveAge || tr.startWall.Unix() != sec {
		t.Errorf("startWall = %v (unix %d), want unix %d", tr.startWall, tr.startWall.Unix(), sec)
	}

	// A zombie reads as gone.
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(4242, 1, 4242, 501, 501, statZOMB, sec, usec), true }
	if _, res := inspect(4242, false); res != hcInspectGone {
		t.Errorf("zombie: res = %d, want hcInspectGone", res)
	}

	// sameUID is an AND: a differing effective uid makes it false.
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(4242, 1, 4242, 501, 0, 2, sec, usec), true }
	hcProcArgs2 = func(int) ([]byte, bool) { return hcProcArgsBuf("/x", []string{"/x"}, nil), true }
	if tr, res := inspect(4242, false); res != hcInspectOK || tr.sameUID {
		t.Errorf("mismatched effective uid: res=%d sameUID=%v, want OK,false", res, tr.sameUID)
	}

	// SSTOP marks stopped.
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(4242, 1, 4242, 501, 501, statSSTOP, sec, usec), true }
	if tr, _ := inspect(4242, false); !tr.stopped {
		t.Error("SSTOP process not marked stopped")
	}
}

func TestInspectDarwinPidReuseMidRead(t *testing.T) {
	seamUID(t, 501)
	oldK, oldA := hcKinfoPID, hcProcArgs2
	t.Cleanup(func() { hcKinfoPID, hcProcArgs2 = oldK, oldA })
	hcProcArgs2 = func(int) ([]byte, bool) { return hcProcArgsBuf("/x", []string{"/x"}, nil), true }
	call := 0
	hcKinfoPID = func(int) ([]byte, bool) {
		call++
		// Second read (the re-check) shows a different start-time: the pid was reused.
		if call == 1 {
			return kinfoBuf(4242, 1, 4242, 501, 501, 2, 100, 5), true
		}
		return kinfoBuf(4242, 1, 4242, 501, 501, 2, 200, 6), true
	}
	if _, res := inspect(4242, false); res != hcInspectGone {
		t.Errorf("pid reused mid-inspect: res = %d, want hcInspectGone", res)
	}
}

// TestInspectDarwinZombieMidRead: a process that turns into a zombie between the first read and
// the re-read keeps the same start-time, so the token check alone would miss it; the re-read's
// SZOMB check catches it (matching the first-read skip and linux's re-read).
func TestInspectDarwinZombieMidRead(t *testing.T) {
	seamUID(t, 501)
	oldK, oldA := hcKinfoPID, hcProcArgs2
	t.Cleanup(func() { hcKinfoPID, hcProcArgs2 = oldK, oldA })
	hcProcArgs2 = func(int) ([]byte, bool) { return hcProcArgsBuf("/x", []string{"/x"}, nil), true }
	call := 0
	hcKinfoPID = func(int) ([]byte, bool) {
		call++
		stat := byte(2) // SRUN on the first read
		if call > 1 {
			stat = statZOMB // a zombie by the re-read, same start-time
		}
		return kinfoBuf(4242, 1, 4242, 501, 501, stat, 100, 5), true
	}
	if _, res := inspect(4242, false); res != hcInspectGone {
		t.Errorf("zombie mid-inspect: res = %d, want hcInspectGone", res)
	}
}

func TestHcSnapshotDarwin(t *testing.T) {
	seamUID(t, 501)
	old := hcKinfoUID
	t.Cleanup(func() { hcKinfoUID = old })
	// Three entries: a live one, a zombie (skipped), another live one.
	buf := append(append(kinfoBuf(10, 1, 10, 501, 501, 2, 1, 1),
		kinfoBuf(11, 1, 11, 501, 501, statZOMB, 1, 1)...),
		kinfoBuf(12, 1, 12, 501, 501, 3, 1, 1)...)
	hcKinfoUID = func(int) ([]byte, bool) { return buf, true }
	pids, overflow, ok := hcSnapshot()
	if !ok || overflow {
		t.Fatalf("ok=%v overflow=%v, want true,false", ok, overflow)
	}
	if len(pids) != 2 || pids[0] != 10 || pids[1] != 12 {
		t.Errorf("pids = %v, want [10 12] (zombie 11 skipped)", pids)
	}
	// sysctl failure -> ok false.
	hcKinfoUID = func(int) ([]byte, bool) { return nil, false }
	if _, _, ok := hcSnapshot(); ok {
		t.Error("snapshot ok=true on sysctl failure")
	}
}

func TestHcReadIdentDarwin(t *testing.T) {
	old := hcKinfoPID
	t.Cleanup(func() { hcKinfoPID = old })
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(7, 1, 99, 501, 501, 2, 42, 7), true }
	pgid, start, ok := hcReadIdent(7)
	if !ok || pgid != 99 || start != "42.7" {
		t.Errorf("hcReadIdent = (%d, %q, %v), want (99, 42.7, true)", pgid, start, ok)
	}
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(7, 1, 99, 501, 501, statZOMB, 42, 7), true }
	if _, _, ok := hcReadIdent(7); ok {
		t.Error("hcReadIdent ok=true for a zombie")
	}
}

func TestHcProcArgsDarwin(t *testing.T) {
	old := hcProcArgs2
	t.Cleanup(func() { hcProcArgs2 = old })
	// A value with spaces and '=' must survive intact (NUL-delimited).
	hcProcArgs2 = func(int) ([]byte, bool) {
		return hcProcArgsBuf("/bin/x", []string{"/bin/x", "-serve"},
			[]string{"CLAUDE_SSH_RUN_DIR=/Users/a b=c/run/x", "CLAUDE_SSH_DAEMON_CHILD=1"}), true
	}
	exe, argv, env, ok := hcProcArgs(1, true)
	if !ok || exe != "/bin/x" || len(argv) != 2 || argv[1] != "-serve" {
		t.Fatalf("exe=%q argv=%v ok=%v", exe, argv, ok)
	}
	if len(env) != 2 || env[0] != "CLAUDE_SSH_RUN_DIR=/Users/a b=c/run/x" {
		t.Errorf("env[0] = %q, want the spaced/= run dir intact", env[0])
	}
	// wantEnv=false leaves env nil.
	if _, _, env, _ := hcProcArgs(1, false); env != nil {
		t.Errorf("env = %v with wantEnv=false, want nil", env)
	}
}

func TestParseLsofAndBusyDarwin(t *testing.T) {
	old := runLsof
	t.Cleanup(func() { runLsof = old })

	// A bare listener: one unix record, no connected peer -> not busy.
	runLsof = func(...string) (string, bool, bool) {
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf5\ntPIPE\nn->0xabc\n", true, true
	}
	if hcBusy(1) {
		t.Error("bare listener read as busy")
	}
	if recs := parseLsofFtn("p1\nf3\ntunix\nn/run/x/rpc.sock\nf5\ntPIPE\nn->0xabc\n"); len(recs) != 2 || recs[0].fd != "3" || recs[0].name != "/run/x/rpc.sock" {
		t.Errorf("parseLsofFtn = %v", recs)
	}

	// Listener plus an accepted connection: two unix records -> busy.
	runLsof = func(...string) (string, bool, bool) {
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n", true, true
	}
	if !hcBusy(1) {
		t.Error("listener with an accepted connection not read as busy")
	}

	// A connected-peer name ("->") is an immediate yes.
	runLsof = func(...string) (string, bool, bool) { return "p1\nf3\ntunix\nn->/run/other\n", true, true }
	if !hcBusy(1) {
		t.Error("connected unix peer not read as busy")
	}
}

// TestStdioPipesAndBusyCheckDarwin covers the two darwin reads the orphan and daemon judges
// use: pipe stdio from lsof's PIPE type, and hcBusyCheck. An abandoned run is a reading there.
func TestStdioPipesAndBusyCheckDarwin(t *testing.T) {
	old := runLsof
	t.Cleanup(func() { runLsof = old })

	// fds 0, 1 and 2 all PIPE: a daemon-spawned child's stdio.
	runLsof = func(...string) (string, bool, bool) {
		return "p1\nf0\ntPIPE\nn->0x1\nf1\ntPIPE\nn->0x2\nf2\ntPIPE\nn->0x3\n", true, true
	}
	if pipes, canRead := hcStdioArePipes(1); !pipes || !canRead {
		t.Errorf("three PIPE stdio records: pipes=%v canRead=%v, want true true", pipes, canRead)
	}
	// One unix record and nothing else: a reading, and not pipe stdio.
	runLsof = func(...string) (string, bool, bool) { return "p1\nf0\ntunix\nn->0x1\n", true, true }
	if pipes, canRead := hcStdioArePipes(1); pipes || !canRead {
		t.Errorf("a unix stdio record: pipes=%v canRead=%v, want false true", pipes, canRead)
	}
	// An abandoned run tells nothing: not pipes, and not a reading.
	runLsof = func(...string) (string, bool, bool) { return "", false, true }
	if pipes, canRead := hcStdioArePipes(1); pipes || canRead {
		t.Errorf("an abandoned lsof run: pipes=%v canRead=%v, want false false", pipes, canRead)
	}

	// hcBusyCheck: a completed run with a connected peer is busy. An abandoned run reads busy
	// under D17, and both are readings.
	runLsof = func(...string) (string, bool, bool) { return "p1\nf3\ntunix\nn->/run/other\n", true, true }
	if busy, canRead := hcBusyCheck(1); !busy || !canRead {
		t.Errorf("connected peer: busy=%v canRead=%v, want true true", busy, canRead)
	}
	runLsof = func(...string) (string, bool, bool) { return "p1\nf3\ntunix\nn/run/x/rpc.sock\n", true, true }
	if busy, canRead := hcBusyCheck(1); busy || !canRead {
		t.Errorf("bare listener: busy=%v canRead=%v, want false true", busy, canRead)
	}
	runLsof = func(...string) (string, bool, bool) { return "", false, true }
	if busy, canRead := hcBusyCheck(1); !busy || !canRead {
		t.Errorf("abandoned run: busy=%v canRead=%v, want true true (D17)", busy, canRead)
	}
}

// TestJudgeOrphanReadsDescriptorsOnceDarwin: judgeOrphan answers both "could the descriptors
// be read" and "are they pipes" from one lsof run, not two.
func TestJudgeOrphanReadsDescriptorsOnceDarwin(t *testing.T) {
	old, oldClock := runLsof, hcClock
	t.Cleanup(func() { runLsof, hcClock = old, oldClock })
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	runs := 0
	runLsof = func(...string) (string, bool, bool) {
		runs++
		return "p1\nf0\ntPIPE\nn->0x1\nf1\ntPIPE\nn->0x2\nf2\ntPIPE\nn->0x3\n", true, true
	}
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}}
	orphan := hcTracked{
		pid: 77, pgid: 77, ppid: 1, sameUID: true, sameNS: true, haveAge: true,
		startWall: now.Add(-time.Hour), exe: "/opt/claude/ccd-cli/2.1.0",
		argv: []string{"claude", "stream-json"}, env: []string{hcDaemonChildMarker},
	}
	if reap, reason := c.judgeOrphan(orphan); !reap {
		t.Fatalf("orphan not reaped (reason %q)", reason)
	}
	if runs != 1 {
		t.Errorf("lsof ran %d times for one orphan, want 1", runs)
	}
}

// TestHcBusyAbandonedRunReadsBusy pins D17.
//
// The two arms differ ONLY in the flag: both return no output. A completed run that
// saw nothing is evidence the pid is idle, and must read as not busy. That arm is
// the control. An abandoned run is not evidence of anything, and claustrum reads it
// as busy. Deleting the D17 arm in hcBusy makes the second assertion fail while
// the control still passes.
func TestHcBusyAbandonedRunReadsBusy(t *testing.T) {
	old := runLsof
	t.Cleanup(func() { runLsof = old })

	// CONTROL: lsof ran, found nothing. Not busy.
	runLsof = func(...string) (string, bool, bool) { return "", true, true }
	if hcBusy(1) {
		t.Fatal("a completed run that found nothing read as busy; that is the parity " +
			"answer and D17 must not change it")
	}

	// The divergence: the run was abandoned, so nothing is known about the pid.
	runLsof = func(...string) (string, bool, bool) { return "", false, true }
	if !hcBusy(1) {
		t.Error("an abandoned run read as not busy; a wedged lsof would then let the " +
			"cleaner SIGTERM a daemon that is still serving a client")
	}
}

// hcSettledBusy is shared code now (hostclean.go), so this covers the darwin half
// of it: that the darwin hcBusy it samples answers, and that the loop behaves on
// this OS. The rule itself is pinned on linux by TestHcSettledBusyHonoursTheDeadline.
//
// claustrum's own previous darwin debounce took ten fixed samples, so the window
// assertion below is what separates the two.
func TestHcSettledBusyDarwin(t *testing.T) {
	oldRun, oldSleep, oldClock := runLsof, hcSleep, hcClock
	t.Cleanup(func() { runLsof, hcSleep, hcClock = oldRun, oldSleep, oldClock })
	// A clock the sleeps drive, so the 3-second window costs no real time.
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }

	// Busy in every sample -> settled busy.
	runLsof = func(...string) (string, bool, bool) {
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n", true, true
	}
	start := now
	if !hcSettledBusy(1) {
		t.Error("busy-in-all-samples not settled busy")
	}
	// The window, not a sample count: claustrum's previous rule settled after about
	// 900 ms.
	if elapsed := now.Sub(start); elapsed < hcBusyWindow {
		t.Errorf("sampled for %v, want at least the %v window", elapsed, hcBusyWindow)
	}
	// Busy at first, then idle -> not settled busy.
	n := 0
	runLsof = func(...string) (string, bool, bool) {
		n++
		if n >= 3 {
			return "p1\nf3\ntunix\nn/run/x/rpc.sock\n", true, true // one unix = not busy
		}
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n", true, true
	}
	if hcSettledBusy(1) {
		t.Error("busy-then-idle wrongly settled busy")
	}
}

// TestHcLockHeldAtDarwin pins the lock read against the rows of macOS VM runs (89cb6289).
// The arguments of the run stay as they were.
func TestHcLockHeldAtDarwin(t *testing.T) {
	old := runLsof
	t.Cleanup(func() { runLsof = old })
	self := "p" + strconv.Itoa(os.Getpid()) + "\n"
	for _, tc := range []struct {
		name                string
		out                 string
		completed, answered bool
		held, asked         bool
	}{
		{"row C2: another pid holds the file", "p1234\n", true, true, true, true},
		{"row C6: no holder", "", true, true, false, true},
		{"row C3: the run did not start", "", true, false, false, false},
		{"rows D2p and D2f: the run wrote to stderr", "p1234\n", true, false, false, false},
		{"row C4: only this process holds the file", self, true, true, false, true},
		{"this process and another pid hold the file", self + "p1234\n", true, true, true, true},
		{"an abandoned run reads as not held, as before", "", false, true, false, true},
	} {
		var got []string
		runLsof = func(args ...string) (string, bool, bool) { got = args; return tc.out, tc.completed, tc.answered }
		held, asked := hcLockHeldAt("/run/x/daemon.lock", nil)
		if held != tc.held || asked != tc.asked {
			t.Errorf("%s: held=%v asked=%v, want %v %v", tc.name, held, asked, tc.held, tc.asked)
		}
		if strings.Join(got, " ") != "-F p /run/x/daemon.lock" {
			t.Errorf("%s: lsof arguments = %q", tc.name, got)
		}
	}
}

// TestLsofRunThatDoesNotAnswerDarwin drives the REAL run. A command that cannot start, and
// a command that writes to stderr, read as no answer. lockProbe then answers
// hcLockUnexamined for a dir with a lock file (rows C3, D2p and D2f). A dir with no lock file
// reads stale with no run. The busy read is then no reading, and the stdio read neither. The
// control is this test binary as the command: it starts, writes nothing to stderr, and its
// output is read.
func TestLsofRunThatDoesNotAnswerDarwin(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	oldPath, oldEnv := hcLsofPath, hcLsofEnv
	t.Cleanup(func() { hcLsofPath, hcLsofEnv = oldPath, oldEnv })
	withHelper := func(mode string) {
		hcLsofPath = exe
		hcLsofEnv = append(append([]string{}, oldEnv...), "CLAUSTRUM_TEST_HELPER="+mode, "GORACE="+os.Getenv("GORACE"))
	}
	locked, bare := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(locked, runDirLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	noExec := filepath.Join(t.TempDir(), "lsof")
	if err := os.WriteFile(noExec, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		stage func()
	}{
		{"a missing command", func() { hcLsofPath, hcLsofEnv = filepath.Join(bare, "missing"), oldEnv }},
		{"a command that is not executable", func() { hcLsofPath, hcLsofEnv = noExec, oldEnv }},
		{"a command that writes to stderr", func() { withHelper("stderr-exit5") }},
	} {
		tc.stage()
		if out, completed, answered := runLsof("-F", "p", "/x"); out != "" || !completed || answered {
			t.Errorf("%s: out=%q completed=%v answered=%v, want empty, completed, no answer", tc.name, out, completed, answered)
		}
		if state, _, present := lockProbe(locked); state != hcLockUnexamined || !present {
			t.Errorf("%s: lockProbe(dir with a lock) = %d present=%v, want hcLockUnexamined", tc.name, state, present)
		}
		if state, _, present := lockProbe(bare); state != hcLockStale || present {
			t.Errorf("%s: lockProbe(dir with no lock) = %d present=%v, want stale", tc.name, state, present)
		}
		if busy, canRead := hcBusyCheck(1); busy || canRead {
			t.Errorf("%s: hcBusyCheck = %v %v, want false false: no reading", tc.name, busy, canRead)
		}
		if !hcDaemonFilesUnread(1) {
			t.Errorf("%s: hcDaemonFilesUnread = false, want true", tc.name)
		}
		if pipes, canRead := hcStdioArePipes(1); pipes || canRead {
			t.Errorf("%s: hcStdioArePipes = %v %v, want false false", tc.name, pipes, canRead)
		}
	}
	// CONTROL: the command starts and writes only to stdout. The helper prints "p1", which
	// is a holder.
	withHelper("print-quiet")
	if out, completed, answered := runLsof("-F", "p", "/x"); out != "p1\n" || !completed || !answered {
		t.Fatalf("control: out=%q completed=%v answered=%v, want %q, completed, answered", out, completed, answered, "p1\n")
	}
	if state, _, _ := lockProbe(locked); state != hcLockHeld {
		t.Errorf("control: lockProbe = %d, want held", state)
	}
	if _, canRead := hcBusyCheck(1); !canRead || hcDaemonFilesUnread(1) {
		t.Errorf("control: the busy read is no reading")
	}
}

// TestLockProbeUnopenableLockDarwin pins row D4: a lock file of mode 0000 reads as
// hcLockUnexamined, with no lsof run.
func TestLockProbeUnopenableLockDarwin(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens a file of mode 0000")
	}
	old := runLsof
	t.Cleanup(func() { runLsof = old })
	runs := 0
	runLsof = func(...string) (string, bool, bool) { runs++; return "", true, true }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runDirLockName), nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := lockProbe(dir); state != hcLockUnexamined || runs != 0 {
		t.Errorf("lockProbe = %d with %d lsof runs, want hcLockUnexamined and no run", state, runs)
	}
}

// TestRetireSendsNoSignalWithoutAnLsofAnswerDarwin pins rows D1i and D1k: when the lsof run
// for the open files of a daemon does not answer, the retire refuses the daemon with the
// measured reason, and NO signal goes out. The daemon is a fake pid behind the kinfo seams,
// and the signal goes through the hcHoldPid seam, so nothing real is signalled. The control
// runs first: a run that answers and shows no client retires the daemon.
func TestRetireSendsNoSignalWithoutAnLsofAnswerDarwin(t *testing.T) {
	const pid = 4300
	const exe = "/opt/claude/srv/a/server"
	const socket = "/opt/claude/run/s1/rpc.sock"
	seamUID(t, 501)
	oldK, oldA, oldRun, oldHold := hcKinfoPID, hcProcArgs2, runLsof, hcHoldPid
	oldClock, oldSleep, oldPpid := hcClock, hcSleep, hcGetppid
	t.Cleanup(func() {
		hcKinfoPID, hcProcArgs2, runLsof, hcHoldPid = oldK, oldA, oldRun, oldHold
		hcClock, hcSleep, hcGetppid = oldClock, oldSleep, oldPpid
	})
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	hcGetppid = func() int { return 1 }
	hcProcArgs2 = func(int) ([]byte, bool) {
		return hcProcArgsBuf(exe, []string{exe, "-serve", "-socket", socket}, []string{hcDaemonChildMarker}), true
	}
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	e := runDirEntry{name: "s1", dirPath: filepath.Dir(socket), socket: socket, idle: 40 * 24 * time.Hour}

	run := func(answered bool) (retired bool, reason string, signals []syscall.Signal) {
		gone := false
		hcKinfoPID = func(int) ([]byte, bool) {
			if gone {
				return nil, false
			}
			return kinfoBuf(pid, 1, pid, 501, 501, 2, uint64(now.Unix())-3600, 0), true
		}
		hcHoldPid = func(int) (func(syscall.Signal) error, func()) {
			return func(sig syscall.Signal) error { signals = append(signals, sig); gone = true; return nil }, func() {}
		}
		runLsof = func(...string) (string, bool, bool) { return "", true, answered }
		retired, reason = c.retireAbandoned(e, pid, 501)
		return retired, reason, signals
	}

	if retired, reason, signals := run(true); !retired || len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Fatalf("control: retired=%v reason=%q signals=%v, want one SIGTERM and a retire", retired, reason, signals)
	}
	retired, reason, signals := run(false)
	if len(signals) != 0 {
		t.Errorf("signals = %v, want none: the open files of the daemon could not be read", signals)
	}
	if retired || reason != hcRetireBusyReason {
		t.Errorf("retired=%v reason=%q, want a refusal with %q", retired, reason, hcRetireBusyReason)
	}
}

// TestPeerOfDarwin exercises the LOCAL_PEERPID read plus the peer-uid-by-kinfo derivation over
// a real connected unix socket pair (both ends are this test process, so the peer pid is ours).
func TestPeerOfDarwin(t *testing.T) {
	oldK := hcKinfoPID
	t.Cleanup(func() { hcKinfoPID = oldK })
	hcKinfoPID = func(int) ([]byte, bool) { return kinfoBuf(os.Getpid(), 1, os.Getpid(), 4242, 4242, 2, 1, 1), true }

	dir := t.TempDir()
	addr := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() { c, _ := ln.Accept(); _ = c; close(done) }()
	c, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	<-done
	pid, uid, ok := peerOf(c.(*net.UnixConn))
	if !ok || pid != os.Getpid() {
		t.Fatalf("peerOf = (%d, %d, %v), want pid=%d ok=true", pid, uid, ok, os.Getpid())
	}
	if uid != 4242 {
		t.Errorf("peer uid = %d, want 4242 (derived from the peer pid's kinfo)", uid)
	}
}

// TestHostcleanLivePrimitivesDarwin runs the REAL darwin primitives (sysctl kinfo_proc,
// KERN_PROCARGS2, lsof, LOCAL_PEERPID) against a real claustrum daemon, on a macOS VM. It is
// env-gated so it never runs in CI: set CLAUSTRUM_VM_LIVE=1 and CLAUSTRUM_BIN=<daemon path>.
// It confirms the composed reads (which the seamed unit tests above cannot) match a live
// process before the destructive judges act on them.
func TestHostcleanLivePrimitivesDarwin(t *testing.T) {
	if os.Getenv("CLAUSTRUM_VM_LIVE") != "1" {
		t.Skip("live VM test; set CLAUSTRUM_VM_LIVE=1 and CLAUSTRUM_BIN")
	}
	bin := os.Getenv("CLAUSTRUM_BIN")
	if bin == "" {
		t.Fatal("CLAUSTRUM_BIN not set")
	}
	// A short /tmp base: a macOS unix socket path must fit in ~104 bytes, which t.TempDir()
	// (under /var/folders/...) blows past.
	base := "/tmp/hcl" + strconv.Itoa(os.Getpid())
	_ = os.RemoveAll(base)
	defer os.RemoveAll(base)
	runDir := filepath.Join(base, "run", "c0ffee")
	sock := filepath.Join(runDir, "rpc.sock")
	lock := filepath.Join(runDir, runDirLockName)
	tok := filepath.Join(base, "token")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(tok, []byte("tk"), 0o600)

	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-file", tok)
	// CLAUSTRUM_DAEMON_CHILD=1 keeps the daemon foreground (this pid is the daemon);
	// CLAUDE_SSH_DAEMON_CHILD=1 is the reference-parity marker a normally-daemonized daemon
	// carries, which the cleaner's judge looks for.
	cmd.Env = append(os.Environ(), "CLAUSTRUM_DAEMON_CHILD=1", "CLAUDE_SSH_DAEMON_CHILD=1")
	var dlog strings.Builder
	cmd.Stdout, cmd.Stderr = &dlog, &dlog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	pid := cmd.Process.Pid
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("daemon socket never appeared; daemon log:\n%s", dlog.String())
	}
	time.Sleep(300 * time.Millisecond)

	// inspect reads the live daemon's real facts.
	tr, res := inspect(pid, true)
	if res != hcInspectOK {
		t.Fatalf("inspect(daemon) res = %d, want hcInspectOK", res)
	}
	if !tr.sameUID || !tr.sameNS || !tr.haveAge {
		t.Errorf("sameUID=%v sameNS=%v haveAge=%v", tr.sameUID, tr.sameNS, tr.haveAge)
	}
	if filepath.Base(tr.exe) != filepath.Base(bin) {
		t.Errorf("exe basename = %q, want %q", filepath.Base(tr.exe), filepath.Base(bin))
	}
	if serveArgv(tr.argv, filepath.Base(bin)) == "" {
		t.Errorf("serveArgv could not extract a socket from real argv %v", tr.argv)
	}
	if !hcHasEnv(tr.env, hcDaemonChildMarker) {
		t.Errorf("real daemon env missing the daemon-child marker")
	}

	// snapshot includes the daemon; readIdent agrees with inspect.
	pids, _, ok := hcSnapshot()
	if !ok {
		t.Fatal("hcSnapshot ok=false")
	}
	found := false
	for _, p := range pids {
		if p == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot did not include the daemon pid %d", pid)
	}
	if _, start, ok := hcReadIdent(pid); !ok || start != tr.startTicks {
		t.Errorf("hcReadIdent start=%q ok=%v, want %q", start, ok, tr.startTicks)
	}

	// probeSocket sees a live listener whose peer is the daemon.
	if st, peer, _ := probeSocket(sock); st != hcSockLive || peer != pid {
		t.Errorf("probeSocket(live) = (state %d, peer %d), want (hcSockLive, %d)", st, peer, pid)
	}
	if st, _, _ := probeSocket(filepath.Join(runDir, "nope.sock")); st != hcSockMissing {
		t.Errorf("probeSocket(missing) = %d, want hcSockMissing", st)
	}

	// the daemon holds its run-dir lock.
	fi, _ := os.Stat(lock)
	if held, asked := hcLockHeldAt(lock, fi); !held || !asked {
		t.Errorf("hcLockHeldAt(daemon.lock) = %v %v, want true true (the daemon holds it)", held, asked)
	}

	// hcBusy: idle now, busy with a client connected.
	if hcBusy(pid) {
		t.Errorf("hcBusy = true for an idle daemon")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	defer c.Close()
	time.Sleep(200 * time.Millisecond)
	if !hcBusy(pid) {
		t.Errorf("hcBusy = false while a client is connected")
	}
}

// TestRunLsofIsBounded drives the REAL runLsof, not the seam every other darwin test
// replaces, and pins that one run cannot outlive its bounds.
//
// Before this, runLsof used a plain exec.Command with no deadline, so an lsof that
// stalled on an unresponsive mount held the cleaner pass open for as long as it
// stalled. retireAbandoned samples hcBusy for up to hcBusyWindow and on darwin every
// sample is an lsof run, so one stall is enough.
//
// The fixture is this test binary, never /usr/sbin/lsof: a real lsof cannot be made
// to hang on demand. Both helper modes ignore their arguments, because the runner
// prepends its own flags.
func TestRunLsofIsBounded(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	oldPath, oldEnv := hcLsofPath, hcLsofEnv
	oldTimeout, oldDelay, oldAbandon := hcLsofTimeout, hcLsofWaitDelay, hcLsofAbandon
	t.Cleanup(func() {
		hcLsofPath, hcLsofEnv = oldPath, oldEnv
		hcLsofTimeout, hcLsofWaitDelay, hcLsofAbandon = oldTimeout, oldDelay, oldAbandon
	})
	hcLsofPath = exe
	// runLsof REPLACES the child's environment with hcLsofEnv, so the child inherits
	// nothing from the test process. GORACE has to be forwarded by hand: TestMain
	// publishes atexit_sleep_ms=0 into this process's environment, and without it a
	// race-built helper sleeps tsan's default second on the way out. Measured on a
	// macOS VM: the prompt fixture took 1.04s under -race without this, and 0.09s
	// with it. CI runs -race on macos-latest, so that is a guaranteed red, not a flake.
	withHelper := func(mode string) {
		hcLsofEnv = append(append([]string{}, oldEnv...),
			"CLAUSTRUM_TEST_HELPER="+mode, "GORACE="+os.Getenv("GORACE"))
	}

	t.Run("control: a prompt command is read", func(t *testing.T) {
		// First, and a t.Fatal: without it a runner that killed every run at once, or
		// returned "" always, would pass both bound arms below. The assertion is on
		// the OUTPUT, not on how long the child took to produce it. A wall-clock gate
		// here would be the tightest bound in the file and would measure process
		// startup rather than anything this test is about; the arms below already
		// bound a fixture that genuinely hangs.
		hcLsofTimeout, hcLsofWaitDelay, hcLsofAbandon = 5*time.Second, time.Second, 6*time.Second
		withHelper("print-quiet")
		got, ok, _ := runLsof("-p", "1")
		if got != "p1\n" {
			t.Fatalf("runLsof of a prompt command = %q, want %q", got, "p1\n")
		}
		if !ok {
			t.Fatal("a completed run reported itself abandoned")
		}
	})

	t.Run("the deadline ends a killable stall", func(t *testing.T) {
		// The abandon bound is set far out, so only the command deadline can end this
		// run. That is what makes this arm about the deadline rather than the race.
		hcLsofTimeout, hcLsofWaitDelay, hcLsofAbandon = 300*time.Millisecond, time.Second, time.Minute
		withHelper("stall-quiet")
		start := time.Now()
		got, ok, _ := runLsof("-p", "1")
		elapsed := time.Since(start)
		if got != "" {
			t.Errorf("runLsof of a stalled command = %q, want no output", got)
		}
		// The deadline killed the command, so the run COMPLETED: the caller learns
		// "lsof looked and saw nothing", which is a fact. That is the difference D17
		// turns on, and it is why this arm asserts ok and the next asserts !ok.
		if !ok {
			t.Error("a run the deadline ended reported itself abandoned")
		}
		if elapsed > 10*time.Second {
			t.Errorf("runLsof took %v; the deadline did not end it", elapsed)
		}
	})

	t.Run("the abandon bound wins when the deadline cannot", func(t *testing.T) {
		// This is the arm that matters, and the one a deadline-only implementation
		// fails. Waiting on a command waits on the process first, so a process the
		// deadline cannot shift holds the run open however short the wait delay is.
		// Here the deadline is set beyond the fixture's own lifetime, which stands in
		// for a process wedged in the kernel: nothing but the abandon bound can end it.
		hcLsofTimeout, hcLsofWaitDelay, hcLsofAbandon = time.Minute, time.Minute, 300*time.Millisecond
		withHelper("stall-quiet")
		start := time.Now()
		got, ok, _ := runLsof("-p", "1")
		elapsed := time.Since(start)
		if ok {
			t.Error("an abandoned run reported itself completed; D17 keys on this flag, " +
				"so a wrong answer here reads an unreadable pid as idle")
		}
		if got != "" {
			t.Errorf("runLsof of an unkillable stall = %q, want no output", got)
		}
		if elapsed > 10*time.Second {
			t.Errorf("runLsof took %v; the run was not abandoned", elapsed)
		}
	})
}
