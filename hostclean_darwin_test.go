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
	runLsof = func(...string) string { return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf5\ntPIPE\nn->0xabc\n" }
	if hcBusy(1) {
		t.Error("bare listener read as busy")
	}
	if m, ok := hcFdTargets(1); !ok || m["3"] != "/run/x/rpc.sock" {
		t.Errorf("hcFdTargets = %v, ok=%v", m, ok)
	}

	// Listener plus an accepted connection: two unix records -> busy.
	runLsof = func(...string) string {
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n"
	}
	if !hcBusy(1) {
		t.Error("listener with an accepted connection not read as busy")
	}

	// A connected-peer name ("->") is an immediate yes.
	runLsof = func(...string) string { return "p1\nf3\ntunix\nn->/run/other\n" }
	if !hcBusy(1) {
		t.Error("connected unix peer not read as busy")
	}

	// No lsof output -> process gone / nothing found.
	runLsof = func(...string) string { return "" }
	if _, ok := hcFdTargets(1); ok {
		t.Error("hcFdTargets ok=true with no lsof output")
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
	runLsof = func(...string) string {
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n"
	}
	start := now
	if !hcSettledBusy(1) {
		t.Error("busy-in-all-samples not settled busy")
	}
	// The window, not a sample count: claustrum's previous rule settled after about
	// 450 ms.
	if elapsed := now.Sub(start); elapsed < hcBusyWindow {
		t.Errorf("sampled for %v, want at least the %v window", elapsed, hcBusyWindow)
	}
	// Busy at first, then idle -> not settled busy.
	n := 0
	runLsof = func(...string) string {
		n++
		if n >= 3 {
			return "p1\nf3\ntunix\nn/run/x/rpc.sock\n" // one unix = not busy
		}
		return "p1\nf3\ntunix\nn/run/x/rpc.sock\nf7\ntunix\nn/run/x/rpc.sock\n"
	}
	if hcSettledBusy(1) {
		t.Error("busy-then-idle wrongly settled busy")
	}
}

func TestHcLockHeldAtDarwin(t *testing.T) {
	old := runLsof
	t.Cleanup(func() { runLsof = old })
	runLsof = func(...string) string { return "p1234\n" }
	if !hcLockHeldAt("/run/x/daemon.lock", nil) {
		t.Error("a held lock read as unheld")
	}
	runLsof = func(...string) string { return "" }
	if hcLockHeldAt("/run/x/daemon.lock", nil) {
		t.Error("an unheld lock read as held")
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
	if !hcLockHeldAt(lock, fi) {
		t.Errorf("hcLockHeldAt(daemon.lock) = false, want true (the daemon holds it)")
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
