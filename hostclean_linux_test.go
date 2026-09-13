//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeProc points procRoot at a temp tree and returns a helper to populate a pid's files.
func fakeProc(t *testing.T) (root string, mk func(pid int, name, content string), link func(pid int, name, target string)) {
	t.Helper()
	root = t.TempDir()
	old := procRoot
	t.Cleanup(func() { procRoot = old })
	procRoot = root
	mk = func(pid int, name, content string) {
		d := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link = func(pid int, name, target string) {
		p := filepath.Join(root, strconv.Itoa(pid), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	return root, mk, link
}

func TestHcReadStat(t *testing.T) {
	_, mk, _ := fakeProc(t)
	// A running process: state R, ppid 1, pgid 42, starttime 9000 (field 22).
	mk(42, "stat", "42 (server) R 1 42 "+strings.Repeat("0 ", 16)+"9000 x")
	s := hcReadStat(42)
	if !s.ok || s.ppid != 1 || s.pgid != 42 || s.startTicks != "9000" || s.stopped {
		t.Errorf("running stat parsed wrong: %+v", s)
	}
	// A zombie is gone.
	mk(43, "stat", "43 (server) Z 1 43 "+strings.Repeat("0 ", 16)+"9000 x")
	if hcReadStat(43).ok {
		t.Error("zombie reported ok")
	}
	// A stopped process.
	mk(44, "stat", "44 (server) T 1 44 "+strings.Repeat("0 ", 16)+"9000 x")
	if s := hcReadStat(44); !s.ok || !s.stopped {
		t.Errorf("stopped process not flagged: %+v", s)
	}
	// Missing pid reads not-ok.
	if hcReadStat(999999).ok {
		t.Error("missing pid reported ok")
	}
}

func TestHcReadExeAndUptime(t *testing.T) {
	_, mk, link := fakeProc(t)
	link(42, "exe", "/opt/claude/srv/a/server (deleted)")
	if got := hcReadExe(42); got != "/opt/claude/srv/a/server" {
		t.Errorf("readExe = %q, want the path without the deleted suffix", got)
	}
	mk(0, "uptime", "12345.67 9999.00\n")
	// /proc/uptime lives at procRoot/uptime, not under a pid; write it directly.
	if err := os.WriteFile(filepath.Join(procRoot, "uptime"), []byte("100.5 50.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up, ok := hcUptime()
	if !ok || up != time.Duration(100.5*float64(time.Second)) {
		t.Errorf("uptime = %v ok=%v, want 100.5s", up, ok)
	}
}

func TestHcFdPredicates(t *testing.T) {
	_, _, link := fakeProc(t)
	link(42, "fd/0", "pipe:[111]")
	link(42, "fd/1", "pipe:[112]")
	link(42, "fd/2", "pipe:[113]")
	link(42, "fd/3", "/opt/claude/run/x/rpc.sock")
	link(42, "fd/4", "socket:[555]")
	if !hcStdioArePipes(42) {
		t.Error("stdio pipes not detected")
	}
	if !hcHasFileOpen(42, "/opt/claude/run/x/rpc.sock") {
		t.Error("open file not detected")
	}
	if hcHasFileOpen(42, "/nope") {
		t.Error("false positive open file")
	}
	// A process whose stdout is not a pipe.
	link(43, "fd/0", "pipe:[1]")
	link(43, "fd/1", "/dev/null")
	if hcStdioArePipes(43) {
		t.Error("non-pipe stdout accepted as pipes")
	}
}

func TestHcBusy(t *testing.T) {
	_, mk, link := fakeProc(t)
	link(42, "fd/4", "socket:[555]")
	// net/unix header + a connected (St 03) row for inode 555.
	mk(42, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n0000: 00000002 00000000 00000000 0001 03 555 /opt/claude/run/x/rpc.sock\n")
	if !hcBusy(42) {
		t.Error("connected client not detected as busy")
	}
	// A listening-only socket (St 01) is not busy.
	mk(43, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n0000: 00000002 00000000 00010000 0001 01 556 /x\n")
	link(43, "fd/4", "socket:[556]")
	if hcBusy(43) {
		t.Error("listen-only socket reported busy")
	}
}

func TestInspect(t *testing.T) {
	root, mk, link := fakeProc(t)
	// A fully inspectable, same-uid, same-namespace process.
	mk(42, "stat", "42 (server) R 1 42 "+strings.Repeat("0 ", 16)+"9000 x")
	mk(42, "status", "Name:\tserver\nUid:\t1000\t1000\t1000\t1000\n")
	mk(42, "cmdline", "/opt/claude/srv/a/server\x00--serve\x00")
	link(42, "exe", "/opt/claude/srv/a/server")
	link(42, "ns/pid", "pid:[4026531836]")
	link(42, "ns/mnt", "mnt:[4026531840]")
	if err := os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:[4026531836]", filepath.Join(root, "self", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mnt:[4026531840]", filepath.Join(root, "self", "ns", "mnt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("100.0 50.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	hcGetuid = func() int { return 1000 }

	tr, res := inspect(42, false)
	if res != hcInspectOK {
		t.Fatalf("inspect result = %v, want OK", res)
	}
	if !tr.sameUID || !tr.sameNS {
		t.Errorf("sameUID=%v sameNS=%v, want both true", tr.sameUID, tr.sameNS)
	}
	if tr.exe != "/opt/claude/srv/a/server" || len(tr.argv) != 2 || tr.argv[1] != "--serve" {
		t.Errorf("exe/argv wrong: exe=%q argv=%v", tr.exe, tr.argv)
	}
	if tr.pgid != 42 || tr.startTicks != "9000" || !tr.haveAge {
		t.Errorf("pgid/startTicks/haveAge = %d/%q/%v, want 42/9000/true", tr.pgid, tr.startTicks, tr.haveAge)
	}

	// A different uid -> sameUID false.
	hcGetuid = func() int { return 4242 }
	if tr, _ := inspect(42, false); tr.sameUID {
		t.Error("sameUID true for a foreign uid")
	}
	// A gone process.
	if _, res := inspect(999999, false); res != hcInspectGone {
		t.Errorf("inspect(missing) = %v, want Gone", res)
	}
}

func TestHcDirIdle(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	if err := os.Chtimes(dir, base.Add(-time.Hour), base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	idle, ok := hcDirIdle(dir, base)
	if !ok || idle < 55*time.Minute || idle > 65*time.Minute {
		t.Fatalf("idle=%v ok=%v, want ~1h", idle, ok)
	}
	// A recently-modified log file makes the dir active (small idle age).
	logf := filepath.Join(dir, hcLogName)
	if err := os.WriteFile(logf, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logf, base.Add(-5*time.Minute), base.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, base.Add(-time.Hour), base.Add(-time.Hour)); err != nil {
		t.Fatal(err) // WriteFile bumped the dir mtime; reset it
	}
	if idle, _ := hcDirIdle(dir, base); idle < time.Minute || idle > 10*time.Minute {
		t.Errorf("idle=%v, want ~5min after a recent log write", idle)
	}
	if _, ok := hcDirIdle(filepath.Join(dir, "nope"), base); ok {
		t.Error("missing dir reported readable")
	}
}

// hcFakeDaemon populates the fake procfs for a fully-inspectable daemon-like process: stat,
// status (uid 1000), cmdline, exe, matching pid+mnt namespaces, and (when marked) the
// daemon-child env marker. base is used to place the process's start in the past.
func hcFakeDaemon(t *testing.T, root string, mk func(int, string, string), link func(int, string, string), pid int, exe string, argv []string, marked bool, startTicks string) {
	hcFakeDaemonUID(t, root, mk, link, pid, exe, argv, marked, startTicks, 1000)
}

func hcFakeDaemonUID(t *testing.T, root string, mk func(int, string, string), link func(int, string, string), pid int, exe string, argv []string, marked bool, startTicks string, uid int) {
	t.Helper()
	mk(pid, "stat", strconv.Itoa(pid)+" (server) R 1 "+strconv.Itoa(pid)+" "+strings.Repeat("0 ", 16)+startTicks+" x")
	mk(pid, "status", fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid))
	mk(pid, "cmdline", strings.Join(argv, "\x00")+"\x00")
	link(pid, "exe", exe)
	link(pid, "ns/pid", "pid:[4026531836]")
	link(pid, "ns/mnt", "mnt:[4026531840]")
	env := "PATH=/bin\x00"
	if marked {
		env += "CLAUDE_SSH_DAEMON_CHILD=1\x00"
	}
	mk(pid, "environ", env)
	// self namespaces (once).
	_ = os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755)
	_ = os.Symlink("pid:[4026531836]", filepath.Join(root, "self", "ns", "pid"))
	_ = os.Symlink("mnt:[4026531840]", filepath.Join(root, "self", "ns", "mnt"))
	if _, err := os.Stat(filepath.Join(root, "uptime")); err != nil {
		_ = os.WriteFile(filepath.Join(root, "uptime"), []byte("100000.0 0.0\n"), 0o644)
	}
}

// hcTestCleaner builds a cleaner over one root and seams the clock, sleep and uid.
func hcTestCleaner(t *testing.T, root string) *hostCleaner {
	t.Helper()
	oldClock, oldSleep, oldUID := hcClock, hcSleep, hcGetuid
	t.Cleanup(func() { hcClock, hcSleep, hcGetuid = oldClock, oldSleep, oldUID })
	hcClock = func() time.Time { return time.Unix(1_000_000, 0) } // "now"; fake starts are far earlier
	hcSleep = func(time.Duration) {}
	hcGetuid = func() int { return 1000 }
	return &hostCleaner{
		roots:     &hostRoots{roots: []string{root}, daemonBin: "server"},
		ownSocket: "/opt/claude/run/self/rpc.sock",
		ownRunDir: "/opt/claude/run/self",
		selfPid:   999999,
	}
}

func TestVerifyListener(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	_ = proot
	socket := "/opt/claude/run/x/rpc.sock"
	hcFakeDaemon(t, proot, mk, link, 42, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "9000")
	if reason := c.verifyListener(42, socket); reason != "" {
		t.Errorf("verified daemon rejected: %q", reason)
	}
	// Not our binary.
	hcFakeDaemon(t, proot, mk, link, 43, "/usr/bin/evil", []string{"/usr/bin/evil", "--serve", "--socket", socket}, true, "9000")
	if c.verifyListener(43, socket) == "" {
		t.Error("non-daemon binary accepted")
	}
	// Missing marker.
	hcFakeDaemon(t, proot, mk, link, 44, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, false, "9000")
	if c.verifyListener(44, socket) == "" {
		t.Error("unmarked listener accepted")
	}
}

func TestJudgeDaemon(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })

	inspectDaemon := func(pid int, marked bool, start string) hcTracked {
		hcFakeDaemon(t, proot, mk, link, pid, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, marked, start)
		tr, res := inspect(pid, true)
		if res != hcInspectOK {
			t.Fatalf("inspect(%d) = %v", pid, res)
		}
		return tr
	}

	// REAP: marked, old, socket no longer answers, no lock, not busy.
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED} }
	v, _, target := c.judgeDaemon(inspectDaemon(42, true, "100"))
	if v != dvReap || target == nil || target.pid != 42 {
		t.Errorf("stranded daemon not reaped: v=%v target=%v", v, target)
	}
	// SPARE: an unmarked daemon older than the marker age -> hand-started reason.
	v2, reason2, _ := c.judgeDaemon(inspectDaemon(45, false, "100"))
	if v2 != dvSpare || reason2 == "" {
		t.Errorf("unmarked old daemon: v=%v reason=%q, want spare", v2, reason2)
	}
	// SKIP: it is us.
	self := inspectDaemon(c.selfPid, true, "100")
	if v, _, _ := c.judgeDaemon(self); v != dvSkip {
		t.Errorf("self not skipped: %v", v)
	}
	// SKIP: a marked --serve daemon younger than the age gate is never touched. Its start-ticks
	// sit just below the fake uptime (100000s at 100Hz), so its age is ~100s.
	if v, _, _ := c.judgeDaemon(inspectDaemon(46, true, "9990000")); v != dvSkip {
		t.Errorf("young daemon not skipped: %v", v)
	}
	// SKIP: a daemon whose run-dir lock is held by a live process is left alone even when its
	// socket no longer answers.
	lockedDir := t.TempDir()
	lockedSock := filepath.Join(lockedDir, "rpc.sock")
	lockPath := filepath.Join(lockedDir, runDirLockName)
	if err := os.WriteFile(lockPath, []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(lockPath)
	lst := fi.Sys().(*syscall.Stat_t)
	lmaj, lmin := hcDevMajorMinor(lst.Dev)
	if err := os.WriteFile(filepath.Join(proot, "locks"),
		[]byte("1: POSIX ADVISORY WRITE 1 "+fmt.Sprintf("%02x:%02x:%d", lmaj, lmin, lst.Ino)+" 0 EOF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hcFakeDaemon(t, proot, mk, link, 47, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", lockedSock}, true, "100")
	if v, _, _ := c.judgeDaemon(mustInspect(t, 47)); v != dvSkip {
		t.Errorf("daemon with a held run-dir lock not skipped: %v", v)
	}
	// SPARE: a daemon whose run-dir lock state cannot be determined (here the lock path is a
	// directory, so lockProbe returns unknown) is spared, not reaped. An unreadable lock is not
	// proof the daemon is dead.
	unknownDir := t.TempDir()
	unknownSock := filepath.Join(unknownDir, "rpc.sock")
	if err := os.Mkdir(filepath.Join(unknownDir, runDirLockName), 0o755); err != nil {
		t.Fatal(err)
	}
	hcFakeDaemon(t, proot, mk, link, 48, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", unknownSock}, true, "100")
	if v, reason, _ := c.judgeDaemon(mustInspect(t, 48)); v != dvSpare || reason == "" {
		t.Errorf("daemon with an indeterminate run-dir lock: v=%v reason=%q, want spare", v, reason)
	}
}

func mustInspect(t *testing.T, pid int) hcTracked {
	t.Helper()
	tr, res := inspect(pid, true)
	if res != hcInspectOK {
		t.Fatalf("inspect(%d) = %v", pid, res)
	}
	return tr
}

func TestJudgeOrphan(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	_ = proot
	// An old, marked stream-json process with readable descriptors -> reap.
	hcFakeDaemon(t, proot, mk, link, 60, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, true, "100")
	link(60, "fd/0", "pipe:[1]")
	tr, _ := inspect(60, true)
	if reap, _ := c.judgeOrphan(tr); !reap {
		t.Error("old marked stream-json orphan not reaped")
	}
	// No stream-json -> silent skip.
	hcFakeDaemon(t, proot, mk, link, 61, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--interactive"}, true, "100")
	tr, _ = inspect(61, true)
	if reap, reason := c.judgeOrphan(tr); reap || reason != "" {
		t.Errorf("non-stream process judged orphan: reap=%v reason=%q", reap, reason)
	}
	// Missing marker -> silent skip.
	hcFakeDaemon(t, proot, mk, link, 62, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, false, "100")
	tr, _ = inspect(62, true)
	if reap, _ := c.judgeOrphan(tr); reap {
		t.Error("unmarked stream process judged orphan")
	}
	// A young stream-json orphan (start-ticks near the fake uptime, so age ~100s) is left alone.
	hcFakeDaemon(t, proot, mk, link, 63, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, true, "9990000")
	link(63, "fd/0", "pipe:[1]")
	tr, _ = inspect(63, true)
	if reap, _ := c.judgeOrphan(tr); reap {
		t.Error("young orphan reaped; the age gate must spare it")
	}
	// A stream-json process running a binary that is not one of our deployed CLI binaries is
	// skipped even with the marker and the right argv (an unrelated same-user process that
	// merely inherited the marker must not be ended).
	hcFakeDaemon(t, proot, mk, link, 64, "/usr/bin/claude", []string{"claude", "--output-format=stream-json"}, true, "100")
	link(64, "fd/0", "pipe:[1]")
	if reap, _ := c.judgeOrphan(mustInspect(t, 64)); reap {
		t.Error("a non-deployed-binary stream process was judged an orphan")
	}
	// A stream-json process in another namespace is skipped.
	hcFakeDaemon(t, proot, mk, link, 65, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, true, "100")
	link(65, "fd/0", "pipe:[1]")
	_ = os.Remove(filepath.Join(proot, "65", "ns", "pid"))
	if err := os.Symlink("pid:[9999999]", filepath.Join(proot, "65", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if reap, _ := c.judgeOrphan(mustInspect(t, 65)); reap {
		t.Error("a foreign-namespace stream process was judged an orphan")
	}
	// A stream-json process owned by another user is skipped.
	hcFakeDaemonUID(t, proot, mk, link, 66, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, true, "100", 4242)
	link(66, "fd/0", "pipe:[1]")
	if reap, _ := c.judgeOrphan(mustInspect(t, 66)); reap {
		t.Error("a foreign-uid stream process was judged an orphan")
	}
}

func TestVerifyListenerGates(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude") // hcGetuid = 1000
	socket := "/opt/claude/run/x/rpc.sock"
	argv := []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}

	// A listener owned by another user is rejected.
	hcFakeDaemonUID(t, proot, mk, link, 90, "/opt/claude/srv/a/server", argv, true, "1", 4242)
	if c.verifyListener(90, socket) == "" {
		t.Error("a foreign-uid listener was verified")
	}
	// A listener in another namespace is rejected.
	hcFakeDaemonUID(t, proot, mk, link, 91, "/opt/claude/srv/a/server", argv, true, "1", 1000)
	_ = os.Remove(filepath.Join(proot, "91", "ns", "pid"))
	if err := os.Symlink("pid:[9999999]", filepath.Join(proot, "91", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if c.verifyListener(91, socket) == "" {
		t.Error("a foreign-namespace listener was verified")
	}
	// A listener whose argv0 basename is "server" but whose exe is not a deployed daemon (not
	// under <root>/srv) is rejected by the binary gate, not merely by the argv gate.
	hcFakeDaemonUID(t, proot, mk, link, 92, "/tmp/server", argv, true, "1", 1000)
	if c.verifyListener(92, socket) == "" {
		t.Error("a non-deployed-binary listener was verified")
	}
}

func TestEndGroupsTwoPhase(t *testing.T) {
	root, mk, _ := fakeProc(t)
	oldKill, oldClock, oldSleep := killGroup, hcClock, hcSleep
	t.Cleanup(func() { killGroup, hcClock, hcSleep = oldKill, oldClock, oldSleep })
	now := time.Unix(0, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	var terms, kills []int
	killGroup = func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGTERM:
			terms = append(terms, pid)
			if pid == 2001 {
				_ = os.RemoveAll(filepath.Join(root, "2001")) // g1 exits during the grace
			}
		case syscall.SIGKILL:
			kills = append(kills, pid)
		case 0: // hcGroupExists probe: the group exists iff its leader pid is still present
			if _, err := os.Stat(filepath.Join(root, strconv.Itoa(pid))); err != nil {
				return syscall.ESRCH
			}
		}
		return nil
	}
	// Both are live at SIGTERM time (present in procfs, matching pgid+start-ticks). g1 exits
	// during the grace; g2 stays alive and survives SIGKILL.
	mk(2001, "stat", "2001 (x) R 1 2001 "+strings.Repeat("0 ", 16)+"5 x")
	mk(2002, "stat", "2002 (x) R 1 2002 "+strings.Repeat("0 ", 16)+"5 x")
	groups := []tracked{
		{pid: 2001, pgid: 2001, startTicks: "5"}, // exits during the grace
		{pid: 2002, pgid: 2002, startTicks: "5"}, // alive throughout
	}
	sig, surv := endGroupsTwoPhase(groups, "orphan")
	if sig != 2 {
		t.Errorf("signalled=%d, want 2", sig)
	}
	if len(kills) != 1 || kills[0] != 2002 {
		t.Errorf("SIGKILL = %v, want [2002] (2001 exited in the grace)", kills)
	}
	if surv != 1 {
		t.Errorf("survived=%d, want 1", surv)
	}
}

func TestTidyRunDirsRemoveAndUndo(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })

	// Socket does not answer, no lock -> the dir is removed (rename then RemoveAll).
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	var renamed, removed []string
	hcRename = func(from, to string) error { renamed = append(renamed, from+"=>"+to); return nil }
	hcRemoveAll = func(p string) error { removed = append(removed, p); return nil }
	var sum hcSummary
	e := runDirEntry{name: "stale", dirPath: "/opt/claude/run/stale", socket: "/opt/claude/run/stale/rpc.sock", idle: 40 * 24 * time.Hour}
	c.tidyRunDirs([]runDirEntry{e}, &sum)
	if sum.runDirsRemoved != 1 || len(removed) != 1 {
		t.Errorf("stale dir not removed: removed=%v sum=%d", removed, sum.runDirsRemoved)
	}
	// A fresh dir (idle below the threshold) is left alone.
	sum = hcSummary{}
	removed = nil
	c.tidyRunDirs([]runDirEntry{{name: "fresh", dirPath: "/opt/claude/run/fresh", socket: "/opt/claude/run/fresh/rpc.sock", idle: time.Hour}}, &sum)
	if sum.runDirsRemoved != 0 || len(removed) != 0 {
		t.Errorf("fresh dir was removed: removed=%v", removed)
	}
}

// hcSeamSignals records single-pid and group signals and removes a pid's fake-procfs entry
// on any signal, so waitGone sees the process exit. It also seams the clock/sleep.
func hcSeamSignals(t *testing.T, root string) (single, group *[]int) {
	t.Helper()
	var s, g []int
	oldSig, oldGrp, oldClock, oldSleep := hcSignalPid, killGroup, hcClock, hcSleep
	t.Cleanup(func() { hcSignalPid, killGroup, hcClock, hcSleep = oldSig, oldGrp, oldClock, oldSleep })
	now := time.Unix(2_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	die := func(pid int) { _ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid))) }
	hcSignalPid = func(pid int, sig syscall.Signal) error { s = append(s, pid); die(pid); return nil }
	killGroup = func(pid int, sig syscall.Signal) error {
		if sig == 0 { // hcGroupExists probe: the group exists iff its leader pid is still present
			if _, err := os.Stat(filepath.Join(root, strconv.Itoa(pid))); err != nil {
				return syscall.ESRCH
			}
			return nil
		}
		g = append(g, pid)
		die(pid)
		return nil
	}
	return &s, &g
}

func TestSummaryString(t *testing.T) {
	if (hcSummary{}).String() != "no action needed" {
		t.Errorf("empty summary = %q, want %q", hcSummary{}.String(), "no action needed")
	}
	s := hcSummary{strandedEnded: 2, runDirsRemoved: 1}
	if !strings.Contains(s.String(), "ended 2") || !strings.Contains(s.String(), "run dirs removed 1") {
		t.Errorf("summary string missing counts: %q", s.String())
	}
}

func TestEndDaemons(t *testing.T) {
	root, mk, _ := fakeProc(t)
	single, group := hcSeamSignals(t, root)
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 111}
	// A daemon (dies to SIGKILL) with one leftover child group. Both are present in procfs with
	// matching identity so the re-validation before signalling passes.
	mk(3000, "stat", "3000 (server) R 1 3000 x")                          // daemon, present until signaled
	mk(3001, "stat", "3001 (x) R 1 3001 "+strings.Repeat("0 ", 16)+"2 x") // leftover child group leader
	targets := []daemonTarget{{
		tracked:  tracked{pid: 3000, pgid: 3000, startTicks: "1"},
		runDir:   "/opt/claude/run/x",
		children: []tracked{{pid: 3001, pgid: 3001, startTicks: "2"}},
	}}
	var sum hcSummary
	c.endDaemons(targets, &sum)
	if len(*single) != 1 || (*single)[0] != 3000 {
		t.Errorf("daemon SIGKILL = %v, want [3000]", *single)
	}
	if len(*group) == 0 || (*group)[0] != 3001 {
		t.Errorf("child group not signaled: %v", *group)
	}
	if sum.strandedSignalled != 1 || sum.strandedEnded != 1 {
		t.Errorf("counts: signalled=%d ended=%d, want 1/1", sum.strandedSignalled, sum.strandedEnded)
	}
}

func TestRetireAbandoned(t *testing.T) {
	proot, mk, link := fakeProc(t)
	single, _ := hcSeamSignals(t, proot) // seams clock+sleep+signals
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	hcGetuid = func() int { return 1000 }
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	socket := "/opt/claude/run/x/rpc.sock"
	// A verifiable, old, marked daemon that exits on SIGTERM -> retired.
	hcFakeDaemon(t, proot, mk, link, 4000, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	if !c.retireAbandoned(4000, socket) {
		t.Error("verifiable daemon not retired")
	}
	if len(*single) != 1 || (*single)[0] != 4000 {
		t.Errorf("SIGTERM = %v, want [4000]", *single)
	}
	// An unverifiable (unmarked) daemon is left running.
	hcFakeDaemon(t, proot, mk, link, 4001, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, false, "1")
	if c.retireAbandoned(4001, socket) {
		t.Error("unverifiable daemon retired")
	}
}

func TestRunDirsAndUndoRename(t *testing.T) {
	// runDirs enumerates real dirs under a temp root.
	base := t.TempDir()
	runRoot := filepath.Join(base, "run")
	for _, name := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	hcClock = time.Now
	c := &hostCleaner{roots: &hostRoots{roots: []string{base}}}
	if dirs := c.runDirs(); len(dirs) != 2 {
		t.Errorf("runDirs = %d entries, want 2", len(dirs))
	}

	// undoRename renames the staging dir back.
	oldRen := hcRename
	t.Cleanup(func() { hcRename = oldRen })
	var renamed string
	hcRename = func(from, to string) error { renamed = from + "=>" + to; return nil }
	c.undoRename("/opt/claude/run/.removing-x-1", "/opt/claude/run/x", "a listener reappeared mid-removal")
	if renamed != "/opt/claude/run/.removing-x-1=>/opt/claude/run/x" {
		t.Errorf("undoRename = %q", renamed)
	}
}

func TestKeepalive(t *testing.T) {
	oldCh, oldClock := hcChtimes, hcClock
	t.Cleanup(func() { hcChtimes, hcClock = oldCh, oldClock })
	hcClock = func() time.Time { return time.Unix(5, 0) }
	var touched string
	hcChtimes = func(p string, _, _ time.Time) error { touched = p; return nil }
	c := &hostCleaner{ownRunDir: "/opt/claude/run/self"}
	c.Keepalive()
	if touched != "/opt/claude/run/self" {
		t.Errorf("Keepalive touched %q, want the own run dir", touched)
	}
}

func TestPassSelfProbeBail(t *testing.T) {
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}}, ownSocket: "/opt/claude/run/self/rpc.sock", selfPid: 1}
	if sum := c.Pass(); sum != (hcSummary{}) {
		t.Errorf("Pass should bail (empty summary) when its own socket does not answer; got %+v", sum)
	}
}

func TestPassReapsStrandedDaemon(t *testing.T) {
	proot, mk, link := fakeProc(t)
	// A real own socket so the self-probe confirms this pid; do NOT seam hcDial (real dials).
	ownSock := filepath.Join(t.TempDir(), "rpc.sock")
	ln, err := net.Listen("unix", ownSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			conn.Close()
		}
	}()
	single, _ := hcSeamSignals(t, proot) // seams clock/sleep/signals (dies on signal)
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	uid := os.Getuid()
	hcGetuid = func() int { return uid }
	// A stranded daemon owned by our uid whose own socket (a dead path under a temp dir that
	// is guaranteed absent) no longer answers. Its pid must differ from this test process's
	// (which is the cleaner's selfPid, and would be skipped as self).
	strandedPid := os.Getpid() + 1
	deadSock := filepath.Join(t.TempDir(), "gone", "rpc.sock")
	hcFakeDaemonUID(t, proot, mk, link, strandedPid, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", deadSock}, true, "1", uid)
	c := &hostCleaner{
		roots:     &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"},
		ownSocket: ownSock,
		ownRunDir: filepath.Dir(ownSock),
		selfPid:   os.Getpid(),
	}
	sum := c.Pass()
	if len(*single) != 1 || (*single)[0] != strandedPid {
		t.Errorf("stranded daemon not reaped by Pass: signals=%v", *single)
	}
	if sum.strandedEnded != 1 {
		t.Errorf("summary strandedEnded=%d, want 1", sum.strandedEnded)
	}
}

func TestEndGroupsWrapper(t *testing.T) {
	root, mk, _ := fakeProc(t)
	_, group := hcSeamSignals(t, root)
	c := &hostCleaner{}
	var sum hcSummary
	mk(6001, "stat", "6001 (x) R 1 6001 "+strings.Repeat("0 ", 16)+"1 x") // live at signal time; dies on SIGTERM
	c.endGroups([]tracked{{pid: 6001, pgid: 6001, startTicks: "1"}}, &sum)
	if sum.orphanSignalled != 1 {
		t.Errorf("orphanSignalled=%d, want 1", sum.orphanSignalled)
	}
	if len(*group) == 0 || (*group)[0] != 6001 {
		t.Errorf("group not signaled: %v", *group)
	}
}

func TestJudgeDaemonSocketLive(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	// Match the peer's real uid so the "another user" gate passes and the verifyListener
	// branch (the one this test exercises) is reached deterministically on any runner uid.
	hcGetuid = func() int { return os.Getuid() }

	inspectDaemon := func(pid int) hcTracked {
		hcFakeDaemon(t, proot, mk, link, pid, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
		tr, res := inspect(pid, true)
		if res != hcInspectOK {
			t.Fatalf("inspect(%d)=%v", pid, res)
		}
		return tr
	}
	// The socket is answered by a live process that is not this candidate daemon and cannot
	// be verified as ours (its pid is not in our fake /proc) -> spare, not reap.
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil }
	if v, reason, _ := c.judgeDaemon(inspectDaemon(70)); v != dvSpare || reason == "" {
		t.Errorf("socket answered by an unverifiable peer: v=%v reason=%q, want spare", v, reason)
	}
}

func TestJudgeDaemonBranches(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude") // selfPid 999999, hcGetuid 1000, hcClock at Unix(1e6)
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	daemonExe := "/opt/claude/srv/a/server"
	serve := []string{daemonExe, "--serve", "--socket", "/opt/claude/run/x/rpc.sock"}
	now := hcClock()

	// Every early return, driven by a directly-built record (no socket needed).
	cases := []struct {
		name string
		d    hcTracked
		want daemonVerdict
	}{
		{"stopped", hcTracked{pid: 100, sameNS: true, stopped: true}, dvSpare},
		{"foreign-ns", hcTracked{pid: 100, sameNS: false}, dvSkip},
		{"non-daemon-exe", hcTracked{pid: 100, sameNS: true, exe: "/usr/bin/other"}, dvSkip},
		{"not-serve", hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: []string{daemonExe, "--bridge"}}, dvSkip},
		{"age-unknown", hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: serve, haveAge: false}, dvSpare},
		{"too-young", hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: serve, haveAge: true, startWall: now.Add(-time.Minute)}, dvSkip},
		{"old-unmarked", hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: serve, haveAge: true, startWall: now.Add(-20 * time.Minute), env: []string{"PATH=/x"}}, dvSpare},
		{"unmarked-not-old-enough", hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: serve, haveAge: true, startWall: now.Add(-6 * time.Minute), env: []string{"PATH=/x"}}, dvSkip},
	}
	for _, tc := range cases {
		if v, _, _ := c.judgeDaemon(tc.d); v != tc.want {
			t.Errorf("%s: verdict=%v, want %v", tc.name, v, tc.want)
		}
	}

	// Marked + old, socket probe inconclusive (a dial error that is not missing/refused/wrong-type).
	marked := hcTracked{pid: 100, sameNS: true, exe: daemonExe, argv: serve, haveAge: true, startWall: now.Add(-20 * time.Minute), env: []string{hcDaemonChildMarker}}
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.EIO} }
	if v, reason, _ := c.judgeDaemon(marked); v != dvSpare || reason == "" {
		t.Errorf("inconclusive socket: v=%v reason=%q, want spare", v, reason)
	}

	// Marked + old, socket dead, no lock, but the daemon still holds a live connection -> spare.
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	mk(100, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n0: 00000002 0 0 0001 03 555 /opt/claude/run/x/rpc.sock\n")
	link(100, "fd/4", "socket:[555]")
	_ = proot
	if v, reason, _ := c.judgeDaemon(marked); v != dvSpare || reason == "" {
		t.Errorf("busy daemon: v=%v reason=%q, want spare", v, reason)
	}
}

// TestJudgeDaemonSocketLiveHealthyAndForeign covers the two socket-live sub-cases the primary
// live test does not: the socket still answering the candidate itself (healthy -> skip), and a
// foreign-uid answerer (-> spare).
func TestJudgeDaemonSocketLiveHealthyAndForeign(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil } // peer = this test process
	self := os.Getpid()

	// Its socket still leads to itself (peer == pid): healthy, skip. The candidate's pid is this
	// process, which is what SO_PEERCRED reports for the fake peer connection.
	hcGetuid = func() int { return os.Getuid() }
	hcFakeDaemon(t, proot, mk, link, self, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	if v, _, _ := c.judgeDaemon(mustInspect(t, self)); v != dvSkip {
		t.Errorf("healthy self-answered daemon: v=%v, want skip", v)
	}
	// The socket is answered by a process owned by another user -> spare.
	hcGetuid = func() int { return os.Getuid() + 12345 }
	hcFakeDaemon(t, proot, mk, link, 71, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	if v, reason, _ := c.judgeDaemon(mustInspect(t, 71)); v != dvSpare || reason == "" {
		t.Errorf("foreign-uid answerer: v=%v reason=%q, want spare", v, reason)
	}
}

// TestEndDaemonsEPERMAndSurvivor covers the "not ours to signal" arm and the SIGKILL-survivor
// warning in endDaemons.
func TestEndDaemonsEPERMAndSurvivor(t *testing.T) {
	_, mk, _ := fakeProc(t)
	oldSig, oldGrp, oldClock, oldSleep := hcSignalPid, killGroup, hcClock, hcSleep
	t.Cleanup(func() { hcSignalPid, killGroup, hcClock, hcSleep = oldSig, oldGrp, oldClock, oldSleep })
	now := time.Unix(9_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	killGroup = func(int, syscall.Signal) error { return nil }
	// Daemon 8001: EPERM (not ours) -> dropped. Daemon 8002: signalled but never exits -> survivor.
	mk(8001, "stat", "8001 (server) R 1 8001 "+strings.Repeat("0 ", 16)+"1 x")
	mk(8002, "stat", "8002 (server) R 1 8002 "+strings.Repeat("0 ", 16)+"1 x")
	hcSignalPid = func(pid int, _ syscall.Signal) error {
		if pid == 8001 {
			return syscall.EPERM
		}
		return nil // 8002 stays alive in procfs -> waitGone times out -> survivor
	}
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 111}
	var sum hcSummary
	c.endDaemons([]daemonTarget{
		{tracked: tracked{pid: 8001, pgid: 8001, startTicks: "1"}, runDir: "/opt/claude/run/a"},
		{tracked: tracked{pid: 8002, pgid: 8002, startTicks: "1"}, runDir: "/opt/claude/run/b"},
	}, &sum)
	if sum.strandedSignalled != 2 {
		t.Errorf("strandedSignalled=%d, want 2", sum.strandedSignalled)
	}
	if sum.strandedSurvived != 1 {
		t.Errorf("strandedSurvived=%d, want 1 (8002 never exited)", sum.strandedSurvived)
	}
}

// TestEndGroupsTwoPhaseForeignOwner covers the EPERM ("belongs to another owner") arm.
func TestEndGroupsTwoPhaseForeignOwner(t *testing.T) {
	_, mk, _ := fakeProc(t)
	oldGrp, oldClock, oldSleep := killGroup, hcClock, hcSleep
	t.Cleanup(func() { killGroup, hcClock, hcSleep = oldGrp, oldClock, oldSleep })
	now := time.Unix(9_100_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	killGroup = func(_ int, sig syscall.Signal) error {
		if sig == 0 {
			return nil
		}
		return syscall.EPERM
	}
	mk(8100, "stat", "8100 (x) R 1 8100 "+strings.Repeat("0 ", 16)+"1 x")
	sig, _ := endGroupsTwoPhase([]tracked{{pid: 8100, pgid: 8100, startTicks: "1"}}, "orphan")
	if sig != 0 {
		t.Errorf("signalled=%d, want 0 (EPERM: not ours)", sig)
	}
}

// TestTidyRunDirsKeepBranches covers the socket-inconclusive and lock-indeterminate keeps.
func TestTidyRunDirsKeepBranches(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRm := hcDial, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
	removed := 0
	hcRemoveAll = func(string) error { removed++; return nil }

	// Socket probe inconclusive (a dial error that is not missing/refused/wrong-type) -> keep.
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.EIO} }
	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{{name: "u", dirPath: "/opt/claude/run/u", socket: "/opt/claude/run/u/rpc.sock", idle: 40 * 24 * time.Hour}}, &sum)
	if removed != 0 {
		t.Errorf("dir kept-on-inconclusive-socket was removed (removed=%d)", removed)
	}
	// Socket dead but the lock state is indeterminate (a directory in place of daemon.lock) -> keep.
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, runDirLockName), 0o755); err != nil {
		t.Fatal(err)
	}
	sum = hcSummary{}
	c.tidyRunDirs([]runDirEntry{{name: "v", dirPath: dir, socket: filepath.Join(dir, "rpc.sock"), idle: 40 * 24 * time.Hour}}, &sum)
	if removed != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("dir kept-on-indeterminate-lock was removed (removed=%d)", removed)
	}
}

// TestRemoveRunDirErrors covers the rename-failed and remove-failed arms (the dir is kept).
func TestRemoveRunDirErrors(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }

	// Rename for staging fails -> the dir is kept, RemoveAll is never called.
	hcRename = func(string, string) error { return syscall.EACCES }
	removed := 0
	hcRemoveAll = func(string) error { removed++; return nil }
	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{{name: "r", dirPath: "/opt/claude/run/r", socket: "/opt/claude/run/r/rpc.sock", idle: 40 * 24 * time.Hour}}, &sum)
	if removed != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("rename-failed dir was still removed (removed=%d)", removed)
	}
	// Rename succeeds but RemoveAll fails -> not counted as removed.
	hcRename = func(string, string) error { return nil }
	hcRemoveAll = func(string) error { return syscall.EIO }
	sum = hcSummary{}
	c.tidyRunDirs([]runDirEntry{{name: "s", dirPath: "/opt/claude/run/s", socket: "/opt/claude/run/s/rpc.sock", idle: 40 * 24 * time.Hour}}, &sum)
	if sum.runDirsRemoved != 0 {
		t.Errorf("remove-failed dir counted as removed: %d", sum.runDirsRemoved)
	}
}

// TestHcReadersMalformed covers the error arms of the small /proc readers.
func TestHcReadersMalformed(t *testing.T) {
	root, mk, link := fakeProc(t)
	// hcUptime: missing and malformed both report not-ok.
	if _, ok := hcUptime(); ok {
		t.Error("hcUptime with no /proc/uptime reported ok")
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := hcUptime(); ok {
		t.Error("hcUptime on garbage reported ok")
	}
	// hcReadExe: a missing exe link reads empty.
	if got := hcReadExe(4242); got != "" {
		t.Errorf("hcReadExe(missing) = %q, want empty", got)
	}
	// hcReadUID: a status file with no Uid line yields not-ok.
	mk(50, "status", "Name:\tx\n")
	if _, ok := hcReadUID(50); ok {
		t.Error("hcReadUID with no Uid line reported ok")
	}
	// hcReadStat on a truncated (<20 field) line is not ok.
	mk(51, "stat", "51 (x) R 1 51 too short")
	if hcReadStat(51).ok {
		t.Error("hcReadStat on a short line reported ok")
	}
	// splitNul: an all-NUL (or empty) buffer yields nil; a delimited one splits.
	if got := splitNul("\x00\x00"); got != nil {
		t.Errorf("splitNul(all-nul) = %v, want nil", got)
	}
	if got := splitNul("a\x00b\x00"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitNul = %v, want [a b]", got)
	}
	_ = link
}

func TestTidyRunDirsKeepOnLock(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	// A stale dir whose daemon.lock is held by a live process -> kept.
	dir := t.TempDir()
	lockPath := filepath.Join(dir, runDirLockName)
	if err := os.WriteFile(lockPath, []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(lockPath)
	st := fi.Sys().(*syscall.Stat_t)
	major, minor := hcDevMajorMinor(st.Dev)
	locks := "1: POSIX ADVISORY WRITE 1 " + fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino) + " 0 EOF\n"
	if err := os.WriteFile(filepath.Join(proot, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}
	var sum hcSummary
	oldRm := hcRemoveAll
	t.Cleanup(func() { hcRemoveAll = oldRm })
	removed := 0
	hcRemoveAll = func(string) error { removed++; return nil }
	c.tidyRunDirs([]runDirEntry{{name: "x", dirPath: dir, socket: filepath.Join(dir, "rpc.sock"), idle: 40 * 24 * time.Hour}}, &sum)
	if removed != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("dir with a held lock was removed (removed=%d)", removed)
	}
}

// newFakePeerConn returns a real connected *net.UnixConn to a throwaway listener. Its
// SO_PEERCRED peer is this test process, which judgeDaemon then fails to verify against the
// fake /proc (that pid is not in the fake tree), exercising the socket-answered-by-an-
// unverifiable-peer spare branch. SO_PEERCRED cannot be forged, so the peer pid/uid are real.
func newFakePeerConn(t *testing.T) net.Conn {
	t.Helper()
	dir := t.TempDir()
	addr := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, e := ln.Accept()
		if e == nil {
			c.Close()
		}
	}()
	conn, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestStart(t *testing.T) {
	oldCh, oldClock, oldSleep := hcChtimes, hcClock, hcSleep
	t.Cleanup(func() { hcChtimes, hcClock, hcSleep = oldCh, oldClock, oldSleep })
	hcClock = time.Now
	var chtimes int32
	hcChtimes = func(string, time.Time, time.Time) error { atomic.AddInt32(&chtimes, 1); return nil }
	reached := make(chan struct{}, 4)
	hcSleep = func(time.Duration) { reached <- struct{}{}; select {} } // signal, then block the goroutine
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/x"}}, ownSocket: "/x/run/self/rpc.sock", ownRunDir: "/x/run/self", selfPid: 1}
	c.Start()
	timeout := time.After(3 * time.Second)
	for i := 0; i < 2; i++ { // both goroutines reach their first sleep
		select {
		case <-reached:
		case <-timeout:
			t.Fatal("Start goroutines did not reach their first sleep")
		}
	}
	if atomic.LoadInt32(&chtimes) == 0 {
		t.Error("Start did not run Keepalive")
	}
}

func TestJudgeOrphanBranches(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	// Stopped process -> reason.
	hcFakeDaemon(t, proot, mk, link, 80, "/opt/claude/ccd-cli/x/claude", []string{"claude", "--output-format=stream-json"}, true, "1")
	mk(80, "stat", "80 (claude) T 1 80 "+strings.Repeat("0 ", 16)+"1 x") // T = stopped
	tr, _ := inspect(80, true)
	if reap, reason := c.judgeOrphan(tr); reap || reason == "" {
		t.Errorf("stopped orphan: reap=%v reason=%q, want a spare reason", reap, reason)
	}
	// Unreadable command line -> reason. Build a tracked that passes the identity gates (our uid,
	// our namespace, a deployed CLI binary) but has no argv, old, marked env.
	old := hcTracked{pid: 81, sameUID: true, sameNS: true, exe: "/opt/claude/ccd-cli/x/claude", haveAge: true, startWall: hcClock().Add(-time.Hour), env: []string{hcDaemonChildMarker}}
	if reap, reason := c.judgeOrphan(old); reap || reason == "" {
		t.Errorf("nil-argv orphan: reap=%v reason=%q, want a spare reason", reap, reason)
	}
}

func TestRemoveRunDirUndoOnReappear(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	var renames []string
	removed := 0
	hcRename = func(from, to string) error { renames = append(renames, from+"=>"+to); return nil }
	hcRemoveAll = func(string) error { removed++; return nil }
	// After the rename, a socket appears in the staging dir -> the removal is undone.
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil } // live socket
	e := runDirEntry{name: "x", dirPath: "/opt/claude/run/x", socket: "/opt/claude/run/x/rpc.sock"}
	if c.removeRunDir(e, false) {
		t.Error("removeRunDir returned true despite a socket reappearing")
	}
	if removed != 0 {
		t.Error("RemoveAll ran despite the undo")
	}
	if len(renames) != 2 { // rename aside, then rename back
		t.Errorf("expected a rename and an undo rename, got %v", renames)
	}
}

func TestRetireAbandonedLeftRunning(t *testing.T) {
	proot, mk, link := fakeProc(t)
	// Signals recorded but the process does NOT die (no die-on-signal), so it survives the grace.
	oldSig, oldClock, oldSleep, oldUID := hcSignalPid, hcClock, hcSleep, hcGetuid
	t.Cleanup(func() { hcSignalPid, hcClock, hcSleep, hcGetuid = oldSig, oldClock, oldSleep, oldUID })
	now := time.Unix(3_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	hcGetuid = func() int { return 1000 }
	hcSignalPid = func(int, syscall.Signal) error { return nil } // no death
	socket := "/opt/claude/run/x/rpc.sock"
	hcFakeDaemon(t, proot, mk, link, 4100, "/opt/claude/srv/a/server", []string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	if c.retireAbandoned(4100, socket) {
		t.Error("daemon that outlived SIGTERM was reported retired")
	}
}

func TestTrackedFate(t *testing.T) {
	_, mk, _ := fakeProc(t)
	mk(42, "stat", "42 (server) R 1 42 "+strings.Repeat("0 ", 16)+"9000 x")
	tr := tracked{pid: 42, pgid: 42, startTicks: "9000"}
	if tr.fate() != hcFateAlive || !tr.same() {
		t.Error("matching process not alive")
	}
	// pid reused: start-ticks changed.
	mk(42, "stat", "42 (server) R 1 42 "+strings.Repeat("0 ", 16)+"7777 x")
	if tr.fate() != hcFateReused {
		t.Error("changed start-ticks not detected as reuse")
	}
	// gone.
	if (tracked{pid: 999999}).fate() != hcFateGone {
		t.Error("missing pid not gone")
	}
}

func TestGroupExistsAndNoSuchProcess(t *testing.T) {
	old := killGroup
	t.Cleanup(func() { killGroup = old })
	killGroup = func(int, syscall.Signal) error { return nil }
	if !hcGroupExists(500) {
		t.Error("group with nil kill error not existing")
	}
	killGroup = func(int, syscall.Signal) error { return syscall.EPERM }
	if !hcGroupExists(500) {
		t.Error("EPERM should still mean the group exists")
	}
	killGroup = func(int, syscall.Signal) error { return syscall.ESRCH }
	if hcGroupExists(500) {
		t.Error("ESRCH should mean no group")
	}
	if hcGroupExists(1) {
		t.Error("pgid 1 rejected out of range")
	}
	if !hcIsNoSuchProcess(syscall.ESRCH) || !hcIsNoSuchProcess(os.ErrProcessDone) || hcIsNoSuchProcess(syscall.EPERM) {
		t.Error("isNoSuchProcess classification wrong")
	}
}

func TestProbeSocket(t *testing.T) {
	// Live listener: a real unix socket in a temp dir; the peer is this test process.
	dir := t.TempDir()
	addr := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if state, pid, _ := probeSocket(addr); state != hcSockLive || pid != os.Getpid() {
		t.Errorf("live socket: state=%d pid=%d, want %d and %d", state, pid, hcSockLive, os.Getpid())
	}

	// Error states via the hcDial seam.
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	cases := []struct {
		err  error
		want int
	}{
		{syscall.ENOENT, hcSockMissing},
		{syscall.ENOTDIR, hcSockMissing},
		{syscall.ECONNREFUSED, hcSockStale},
		{syscall.ENOTSOCK, hcSockNotSock},
		{syscall.EACCES, hcSockUnknown},
	}
	for _, c := range cases {
		hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: c.err} }
		if state, _, _ := probeSocket("/x"); state != c.want {
			t.Errorf("dial err %v: state=%d, want %d", c.err, state, c.want)
		}
	}
}

func TestLockFileHeldAndProbe(t *testing.T) {
	_, _, _ = fakeProc(t) // points procRoot at a temp tree; we add /proc/locks below
	dir := t.TempDir()
	lockPath := filepath.Join(dir, runDirLockName)
	rec := ownerRecord{Pid: 4321, Role: "serve", Node: "n"}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(lockPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	major, minor := hcDevMajorMinor(st.Dev)
	// A /proc/locks line that matches the file's dev:inode.
	locks := "1: POSIX ADVISORY WRITE 4321 " + fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino) + " 0 EOF\n"
	if err := os.WriteFile(filepath.Join(procRoot, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}
	if !lockFileHeld(fi) {
		t.Error("held lock not detected against /proc/locks")
	}
	if state, got, present := lockProbe(dir); state != hcLockHeld || got == nil || got.Pid != 4321 || !present {
		t.Errorf("lockProbe held: state=%d rec=%v present=%v", state, got, present)
	}
	// No matching lock line -> stale.
	if err := os.WriteFile(filepath.Join(procRoot, "locks"), []byte("1: POSIX ADVISORY WRITE 1 fd:00:1 0 EOF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, _, present := lockProbe(dir); state != hcLockStale || !present {
		t.Errorf("lockProbe stale: state=%d present=%v", state, present)
	}
	// Absent lock file -> stale, not present.
	if state, _, present := lockProbe(t.TempDir()); state != hcLockStale || present {
		t.Errorf("lockProbe absent: state=%d present=%v", state, present)
	}
	// Unparseable content -> unknown.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, runDirLockName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := lockProbe(bad); state != hcLockUnknown {
		t.Errorf("lockProbe bad json: state=%d, want unknown", state)
	}
}

func TestWaitGone(t *testing.T) {
	_, mk, _ := fakeProc(t)
	oldKill, oldClock, oldSleep := killGroup, hcClock, hcSleep
	t.Cleanup(func() { killGroup, hcClock, hcSleep = oldKill, oldClock, oldSleep })
	now := time.Unix(0, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	var killed []int
	killGroup = func(pid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			killed = append(killed, pid)
		}
		return nil
	}
	// Entry 700: gone, kill enabled, group still exists -> SIGKILL the group.
	// Entry 701: alive throughout -> survivor.
	mk(701, "stat", "701 (server) R 1 701 "+strings.Repeat("0 ", 16)+"5 x")
	// 700 is absent (gone). groupExists(700): our killGroup returns nil -> exists.
	entries := []waitEntry{
		{tracked: tracked{pid: 700, pgid: 700, startTicks: "1"}, kill: true},
		{tracked: tracked{pid: 701, pgid: 701, startTicks: "5"}, kill: true},
	}
	surv := waitGone(entries, now.Add(500*time.Millisecond))
	if len(killed) != 1 || killed[0] != 700 {
		t.Errorf("killed = %v, want a group SIGKILL to 700", killed)
	}
	if len(surv) != 1 || surv[0].pid != 701 {
		t.Errorf("survivors = %v, want [701]", surv)
	}
}

func TestHcSnapshot(t *testing.T) {
	root, mk, _ := fakeProc(t)
	for _, pid := range []int{10, 11, 12} {
		mk(pid, "stat", strconv.Itoa(pid)+" (server) R 1 "+strconv.Itoa(pid)+" x")
	}
	// A non-numeric entry is ignored.
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	// Our uid matches the temp dirs (created by this process), so all are included.
	hcGetuid = os.Getuid
	pids, overflow, ok := hcSnapshot()
	if !ok || overflow || len(pids) != 3 {
		t.Errorf("snapshot = %v overflow=%v ok=%v, want 3 pids, no overflow", pids, overflow, ok)
	}
	// A foreign uid excludes them all.
	hcGetuid = func() int { return os.Getuid() + 99999 }
	if pids, overflow, _ := hcSnapshot(); len(pids) != 0 || overflow {
		t.Errorf("snapshot with foreign uid = %v overflow=%v, want none", pids, overflow)
	}
}

// TestHcSnapshotOverflow proves the overflow flag, not the slice length, signals "too many
// processes". The slice is capped at hcMaxSnapshot; one process past the cap sets overflow.
func TestHcSnapshotOverflow(t *testing.T) {
	root, _, _ := fakeProc(t)
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	hcGetuid = os.Getuid
	for i := 1; i <= hcMaxSnapshot+1; i++ {
		if err := os.Mkdir(filepath.Join(root, strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pids, overflow, ok := hcSnapshot()
	if !ok || !overflow {
		t.Fatalf("overflow=%v ok=%v, want overflow with ok", overflow, ok)
	}
	if len(pids) != hcMaxSnapshot {
		t.Errorf("len(pids)=%d, want the cap %d", len(pids), hcMaxSnapshot)
	}
}

// TestEndGroupsTwoPhaseNonLeaderSingly proves a non-leader child (pid != pgid) is signalled by
// its single pid, while a group leader (pid == pgid) is signalled as a whole group. A blind
// kill(-pid) on a non-leader would target a non-existent group and miss the helper.
func TestEndGroupsTwoPhaseNonLeaderSingly(t *testing.T) {
	root, mk, _ := fakeProc(t)
	single, group := hcSeamSignals(t, root)
	// Both are live at signal time (present in procfs with matching pgid+start-ticks).
	mk(3001, "stat", "3001 (x) R 1 3001 "+strings.Repeat("0 ", 16)+"1 x")
	mk(3002, "stat", "3002 (x) R 1 4000 "+strings.Repeat("0 ", 16)+"1 x")
	groups := []tracked{
		{pid: 3001, pgid: 3001, startTicks: "1"}, // leader -> group signal
		{pid: 3002, pgid: 4000, startTicks: "1"}, // non-leader helper -> single pid
	}
	sig, _ := endGroupsTwoPhase(groups, "process left behind by a stranded daemon")
	if sig != 2 {
		t.Errorf("signalled=%d, want 2", sig)
	}
	if len(*group) != 1 || (*group)[0] != 3001 {
		t.Errorf("group signals = %v, want [3001] (the leader)", *group)
	}
	if len(*single) != 1 || (*single)[0] != 3002 {
		t.Errorf("single signals = %v, want [3002] (the non-leader helper)", *single)
	}
}

// TestEndGroupsTwoPhaseSkipsReusedPid proves the ender re-validates identity before signalling:
// a tracked child whose pid is now held by a different process (a different start-ticks) is not
// signalled, so a pid reused during the wait cannot get an unrelated process killed.
func TestEndGroupsTwoPhaseSkipsReusedPid(t *testing.T) {
	root, mk, _ := fakeProc(t)
	single, group := hcSeamSignals(t, root)
	// The tracked child recorded start-ticks "1"; the live pid now shows "999" (a reused pid).
	mk(5000, "stat", "5000 (x) R 1 5000 "+strings.Repeat("0 ", 16)+"999 x")
	sig, _ := endGroupsTwoPhase([]tracked{{pid: 5000, pgid: 5000, startTicks: "1"}}, "leftover")
	if sig != 0 {
		t.Errorf("signalled=%d, want 0 (the pid was reused)", sig)
	}
	if len(*single) != 0 || len(*group) != 0 {
		t.Errorf("a reused pid was signalled: single=%v group=%v", *single, *group)
	}
}

// TestEndGroupsTwoPhaseReapsDyingLeaderGroup proves the kill-enabled wait: a group leader that
// exits during the grace has its lingering process group SIGKILLed, even though the leader pid
// is now gone. Without the kill-enable, the group would leak.
func TestEndGroupsTwoPhaseReapsDyingLeaderGroup(t *testing.T) {
	root, mk, _ := fakeProc(t)
	oldGrp, oldClock, oldSleep := killGroup, hcClock, hcSleep
	t.Cleanup(func() { killGroup, hcClock, hcSleep = oldGrp, oldClock, oldSleep })
	now := time.Unix(3_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	var terms, kills []int
	killGroup = func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGTERM:
			terms = append(terms, pid)
			_ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid))) // leader exits during the grace
		case syscall.SIGKILL:
			kills = append(kills, pid)
		case 0:
			return nil // the leader pid is gone but its group still has a member
		}
		return nil
	}
	mk(7001, "stat", "7001 (x) R 1 7001 "+strings.Repeat("0 ", 16)+"1 x")
	sig, _ := endGroupsTwoPhase([]tracked{{pid: 7001, pgid: 7001, startTicks: "1"}}, "leftover")
	if sig != 1 {
		t.Errorf("signalled=%d, want 1", sig)
	}
	if len(kills) != 1 || kills[0] != 7001 {
		t.Errorf("group SIGKILL = %v, want [7001] (leader died in the grace, group lingered)", kills)
	}
}

// TestDeriveRoots covers the containment derivation: a valid run-dir socket yields the root
// and run dir; a bad basename or a non-"run" grandparent is rejected; with an executable the
// deployed-daemon check runs and a differing exe root is added.
func TestDeriveRoots(t *testing.T) {
	// Socket only.
	r, err := deriveRoots("/opt/claude/run/c0ffee01/rpc.sock", "", false)
	if err != nil {
		t.Fatalf("valid socket: %v", err)
	}
	if len(r.roots) != 1 || r.roots[0] != "/opt/claude" {
		t.Errorf("roots = %v, want [/opt/claude]", r.roots)
	}
	if r.runDir != "/opt/claude/run/c0ffee01" {
		t.Errorf("runDir = %q", r.runDir)
	}

	// Bad basename.
	if _, err := deriveRoots("/opt/claude/run/c0ffee01/other.sock", "", false); err == nil {
		t.Error("bad socket basename accepted")
	}
	// Grandparent is not "run".
	if _, err := deriveRoots("/opt/claude/xrun/c0ffee01/rpc.sock", "", false); err == nil {
		t.Error("non-run grandparent accepted")
	}

	// With a deployed daemon exe under the SAME root.
	r, err = deriveRoots("/opt/claude/run/c0ffee01/rpc.sock", "/opt/claude/srv/abc123/server", true)
	if err != nil {
		t.Fatalf("valid exe: %v", err)
	}
	if r.daemonBin != "server" {
		t.Errorf("daemonBin = %q, want server", r.daemonBin)
	}
	if len(r.roots) != 1 {
		t.Errorf("roots = %v, want a single root (exe root == socket root)", r.roots)
	}

	// Exe under a DIFFERENT root -> second root added.
	r, err = deriveRoots("/opt/claude/run/c0ffee01/rpc.sock", "/home/u/.claude/srv/abc123/server", true)
	if err != nil {
		t.Fatalf("valid exe (other root): %v", err)
	}
	if len(r.roots) != 2 || r.roots[1] != "/home/u/.claude" {
		t.Errorf("roots = %v, want the socket root plus /home/u/.claude", r.roots)
	}

	// Exe that is not a deployed daemon (not under srv) -> error.
	if _, err := deriveRoots("/opt/claude/run/c0ffee01/rpc.sock", "/usr/bin/evil", true); err == nil {
		t.Error("non-deployed executable accepted")
	}
}

func TestRootsMembership(t *testing.T) {
	r := &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}

	// isDaemonBinary
	if !r.isDaemonBinary("/opt/claude/srv/abc/server") {
		t.Error("deployed daemon not recognized")
	}
	if r.isDaemonBinary("/opt/claude/srv/abc/other") {
		t.Error("wrong basename accepted as daemon")
	}
	if r.isDaemonBinary("/opt/claude/srv/server") {
		t.Error("wrong depth accepted as daemon (needs srv/<id>/server)")
	}
	if r.isDaemonBinary("/other/srv/abc/server") {
		t.Error("out-of-root daemon accepted")
	}

	// isCliBinary: versioned and direct.
	if !r.isCliBinary("/opt/claude/ccd-cli/2.1.0/claude") {
		t.Error("versioned CLI not recognized")
	}
	if !r.isCliBinary("/opt/claude/ccd-cli/claude") {
		t.Error("direct CLI not recognized")
	}
	if r.isCliBinary("/opt/claude/srv/abc/server") {
		t.Error("a daemon was accepted as a CLI binary")
	}

	// isRunDirSocket
	if !r.isRunDirSocket("/opt/claude/run/c0ffee01/rpc.sock") {
		t.Error("run-dir socket not recognized")
	}
	if r.isRunDirSocket("/opt/claude/run/c0ffee01/other.sock") {
		t.Error("non-rpc.sock accepted")
	}
	if r.isRunDirSocket("/other/run/x/rpc.sock") {
		t.Error("out-of-root socket accepted")
	}

	// runRoots
	rr := r.runRoots()
	if len(rr) != 1 || rr[0] != "/opt/claude/run" {
		t.Errorf("runRoots = %v, want [/opt/claude/run]", rr)
	}
}

func TestUnderDepth(t *testing.T) {
	r := &hostRoots{roots: []string{"/root"}}
	cases := []struct {
		path  string
		sub   string
		depth int
		want  bool
	}{
		{"/root/srv/id/server", "srv", 2, true},
		{"/root/srv/server", "srv", 2, false},        // too shallow
		{"/root/srv/id/sub/server", "srv", 2, false}, // too deep
		{"/root/run/id/rpc.sock", "run", 2, true},
		{"relative/srv/id/server", "srv", 2, false}, // not absolute
		{"/other/srv/id/server", "srv", 2, false},   // wrong root
	}
	for _, c := range cases {
		if got := r.under(c.path, c.sub, c.depth); got != c.want {
			t.Errorf("under(%q,%q,%d) = %v, want %v", c.path, c.sub, c.depth, got, c.want)
		}
	}
}

func TestSameSocketPath(t *testing.T) {
	r := &hostRoots{roots: []string{"/opt/claude", "/home/u/.claude"}}
	if !r.sameSocketPath("/opt/claude/run/x/rpc.sock", "/opt/claude/run/x/rpc.sock") {
		t.Error("identical paths not equal")
	}
	// Same socket relative to two different roots.
	if !r.sameSocketPath("/opt/claude/run/x/rpc.sock", "/home/u/.claude/run/x/rpc.sock") {
		t.Error("same socket under different roots not equal")
	}
	if r.sameSocketPath("/opt/claude/run/x/rpc.sock", "/opt/claude/run/y/rpc.sock") {
		t.Error("different sockets reported equal")
	}
}

func TestServeArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		bin  string
		want string
	}{
		{"serve with socket flag", []string{"/opt/claude/srv/a/server", "--serve", "--socket", "/opt/claude/run/x/rpc.sock"}, "server", "/opt/claude/run/x/rpc.sock"},
		{"serve with socket=", []string{"/opt/claude/srv/a/server", "-serve", "-socket=/opt/claude/run/x/rpc.sock"}, "server", "/opt/claude/run/x/rpc.sock"},
		{"wrong binary name", []string{"/opt/claude/srv/a/other", "--serve", "--socket", "/x/rpc.sock"}, "server", ""},
		{"no serve flag", []string{"/opt/claude/srv/a/server", "--socket", "/x/rpc.sock"}, "server", ""},
		{"bridge invocation", []string{"/opt/claude/srv/a/server", "--bridge", "--socket", "/x/rpc.sock"}, "server", ""},
		{"stop invocation", []string{"/opt/claude/srv/a/server", "-serve", "-socket", "/x/rpc.sock", "-stop"}, "server", ""},
		{"relative socket rejected", []string{"/opt/claude/srv/a/server", "--serve", "--socket", "rel/rpc.sock"}, "server", ""},
	}
	for _, c := range cases {
		if got := serveArgv(c.argv, c.bin); got != c.want {
			t.Errorf("%s: serveArgv = %q, want %q", c.name, got, c.want)
		}
	}
}
