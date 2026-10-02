//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The cleaner lines and the retire call that a Linux VM measured against f6010b97 and
// 89cb6289 (slice E of the 89cb6289 reconciliation). Each test names its rows. Every signal
// in this file goes through a recording seam, with one exception. The pidfd test sends a
// real signal to a helper process that it started itself.

// TestPassLogsTheProcessLimit: with more than 4096 processes of this user the pass prints
// one line and tidies no run dir (rows HC24 and HC25). At 4096 it prints no line and the
// tidy runs. Row HC24f has 4097 processes. In one earlier run of it 89cb6289 printed no
// line. In five later runs both sides printed the line.
func TestPassLogsTheProcessLimit(t *testing.T) {
	const line = "[hostclean] more than 4096 processes for this user: this pass only acts on per-process facts (no run-dir retirement)\n"
	for _, n := range []int{hcMaxSnapshot, hcMaxSnapshot + 1} {
		f := hcNewPassFixture(t)
		// Bare pid folders: the snapshot counts them, and inspect reads each one as gone.
		for i := 0; i < n; i++ {
			if err := os.Mkdir(filepath.Join(f.proot, strconv.Itoa(1_000_000+i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stale := filepath.Join(f.runRoot, "Z0")
		if err := os.Mkdir(stale, 0o755); err != nil {
			t.Fatal(err)
		}
		hcBackdate(t, stale, 40*24*time.Hour)
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		over := n > hcMaxSnapshot
		if got := strings.Contains(buf.String(), line); got != over {
			t.Errorf("%d processes: limit line logged = %v, want %v\nlog: %s", n, got, over, buf.String())
		}
		wantRemoved := 1
		if over {
			wantRemoved = 0
		}
		if sum.runDirsRemoved != wantRemoved {
			t.Errorf("%d processes: runDirsRemoved = %d, want %d", n, sum.runDirsRemoved, wantRemoved)
		}
	}
}

// TestPassLogsTheOrphanGroupCap: 65 orphan groups give 64 SIGTERM and one line before the
// first SIGTERM line (rows HC22c, HC22e and HC26). 64 groups give no line (row HC22).
func TestPassLogsTheOrphanGroupCap(t *testing.T) {
	const line = "[hostclean] more orphaned Claude Code groups than one pass ends (64); leaving the rest for the next pass\n"
	for _, n := range []int{hcMaxGroups, hcMaxGroups + 1} {
		f := hcNewPassFixture(t)
		cli := filepath.Join(f.base, "ccd-cli", "2.1.0")
		for i := 0; i < n; i++ {
			pid := 2_000_000 + i
			hcFakeDaemonUID(t, f.proot, f.mk, f.link, pid, cli,
				[]string{"claude", "--output-format=stream-json"}, true, "1", f.uid)
			hcPipeStdio(f.link, pid)
		}
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		if len(f.group) != hcMaxGroups || sum.orphanSignalled != hcMaxGroups {
			t.Errorf("%d groups: %d group signals, summary %d, want %d", n, len(f.group), sum.orphanSignalled, hcMaxGroups)
		}
		got := buf.String()
		at := strings.Index(got, line)
		if (at >= 0) != (n > hcMaxGroups) {
			t.Errorf("%d groups: cap line logged = %v, want %v", n, at >= 0, n > hcMaxGroups)
		}
		if first := strings.Index(got, "[hostclean] ending orphaned Claude Code process group"); at >= 0 && first < at {
			t.Errorf("%d groups: the cap line comes after the first SIGTERM line", n)
		}
	}
}

// TestEndGroupsLogsTheSIGKILL: a group that is alive after the 3 s grace gets one line and
// SIGKILL (row HC21a). A group whose leader ended in the grace gets SIGKILL from the wait
// and no line (row HC21, the kid variant).
func TestEndGroupsLogsTheSIGKILL(t *testing.T) {
	root, mk, _ := fakeProc(t)
	oldGrp, oldClock, oldSleep := killGroup, hcClock, hcSleep
	t.Cleanup(func() { killGroup, hcClock, hcSleep = oldGrp, oldClock, oldSleep })
	now := time.Unix(3_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	var kills []int
	var killAt []time.Duration
	start := now
	killGroup = func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGTERM:
			if pid == 5339 {
				_ = os.RemoveAll(filepath.Join(root, "5339")) // the leader ends, a kid stays
			}
		case syscall.SIGKILL:
			kills = append(kills, pid)
			killAt = append(killAt, now.Sub(start))
			_ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid)))
		case 0: // the group exists until its SIGKILL
			for _, k := range kills {
				if k == pid {
					return syscall.ESRCH
				}
			}
		}
		return nil
	}
	mk(5338, "stat", hcRunningStat(5338, 1, 5338, "1")) // ignores SIGTERM
	mk(5339, "stat", hcRunningStat(5339, 1, 5339, "1")) // its leader ends on SIGTERM
	buf := captureLogBuf(t)

	endGroupsTwoPhase([]tracked{{pid: 5338, pgid: 5338, startTicks: "1"}, {pid: 5339, pgid: 5339, startTicks: "1"}},
		"orphaned process group", "")

	if len(kills) != 2 || kills[0] != 5339 || kills[1] != 5338 {
		t.Fatalf("SIGKILL = %v, want [5339 5338]", kills)
	}
	if killAt[1] != hcTermGrace {
		t.Errorf("the SIGKILL of the group that ignores SIGTERM came after %s, want %s", killAt[1], hcTermGrace)
	}
	got := buf.String()
	if want := "[hostclean] group 5338 outlived SIGTERM for 3s: SIGKILL\n"; !strings.Contains(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
	if strings.Contains(got, "group 5339 outlived") {
		t.Errorf("log = %q, want no line for the group that the wait ended", got)
	}
}

// TestTidyKeepsALockedDirWithTheMeasuredLine: a stale dir whose daemon.lock a live process
// holds stays, with the reference's line (row HC03).
func TestTidyKeepsALockedDirWithTheMeasuredLine(t *testing.T) {
	proot, _, _ := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRm := hcDial, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	hcRemoveAll = func(*os.Root, string) error { return nil }
	e := hcNewEntry(t, "zd", 40*24*time.Hour)
	lockPath := filepath.Join(e.dirPath, runDirLockName)
	if err := os.WriteFile(lockPath, []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	major, minor := hcDevMajorMinor(st.Dev)
	locks := "1: FLOCK ADVISORY WRITE 1 " + fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino) + " 0 EOF\n"
	if err := os.WriteFile(filepath.Join(proot, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}
	hcBackdate(t, e.dirPath, 40*24*time.Hour)
	buf := captureLogBuf(t)

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)

	want := fmt.Sprintf("[hostclean] run dir %q unused for 40 days: kept, a live process holds its daemon.lock\n", e.dirPath)
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
	if sum.runDirsRemoved != 0 {
		t.Errorf("runDirsRemoved = %d, want 0", sum.runDirsRemoved)
	}
}

// TestTidyRefusesAListenerOfAnotherBinary: a live listener that does not run the deployed
// daemon binary is not retired, with the reference's line, and gets no signal (row HC04).
func TestTidyRefusesAListenerOfAnotherBinary(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	e := hcNewEntry(t, "zd", 40*24*time.Hour)
	self, uid := os.Getpid(), os.Getuid()
	hcGetuid = func() int { return uid } // the fake peer reports the runner's real uid
	hcFakeDaemonUID(t, proot, mk, link, self, "/usr/bin/listener", []string{"/usr/bin/listener"}, false, "1", uid)

	oldDial, oldSig, oldRen, oldRm := hcDial, hcHoldPid, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcHoldPid, hcRename, hcRemoveAll = oldDial, oldSig, oldRen, oldRm })
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil }
	signals := 0
	hcHoldPid = holdWith(func(int, syscall.Signal) error { signals++; return nil })
	hcRename = func(*os.Root, string, string) error { return nil }
	hcRemoveAll = func(*os.Root, string) error { return nil }
	buf := captureLogBuf(t)

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)

	want := fmt.Sprintf("[hostclean] run dir %q unused for 40 days: its daemon (pid %d) was not retired: the listener could not be verified as a daemon of ours (not our daemon binary)\n", e.dirPath, self)
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
	if signals != 0 || sum.daemonsRetired != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("signals=%d summary=%+v, want no signal, no retire, no removal", signals, sum)
	}
}

// hcStartSleeper starts a helper of this test binary that sleeps, and returns its command.
// The cleanup ends it by its own handle.
func hcStartSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	cmd := exec.Command(exe, "60")
	cmd.Env = buildEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

// hcWaitSignal waits for the helper and returns the signal that ended it.
func hcWaitSignal(t *testing.T, cmd *exec.Cmd) syscall.Signal {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper ended with %v, want a signal", err)
		}
		return ee.Sys().(syscall.WaitStatus).Signal()
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not end within 10 s of the signal")
		return 0
	}
}

// holdWith makes a hold for the hcHoldPid seam out of a plain signal function. The hold
// itself does nothing, and its send calls fn with the pid.
func holdWith(fn func(pid int, sig syscall.Signal) error) func(int) (func(syscall.Signal) error, func()) {
	return func(pid int) (func(syscall.Signal) error, func()) {
		return func(sig syscall.Signal) error { return fn(pid, sig) }, func() {}
	}
}

// hcSignalDaemon runs the real hold in one step: take it, send sig through it, release it.
func hcSignalDaemon(pid int, sig syscall.Signal) error {
	send, release := hcHoldDaemon(pid)
	defer release()
	return send(sig)
}

// TestRetireChecksTheIdentityAfterThePidfdOpen: the retire opens the pid file descriptor
// first, then checks the identity of the pid, then sends through the descriptor. Here the
// pid changes hands right after the open: the fake open writes a new start time for it. The
// retire then sends no signal, by no call, and closes the descriptor. With the check
// before the open, the SIGTERM goes out. The control half has no change of hands, and
// there the SIGTERM goes through the descriptor that the open returned.
func TestRetireChecksTheIdentityAfterThePidfdOpen(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	const control, victim = 77, 78
	plant := func(pid int) {
		hcFakeDaemon(t, proot, mk, link, pid, "/opt/claude/srv/a/server",
			[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
		link(pid, "fd/1", "/dev/null") // readable connections, none a client
	}

	oldOpen, oldSend, oldKill := hcPidfdOpen, hcPidfdSend, hcKillPid
	t.Cleanup(func() { hcPidfdOpen, hcPidfdSend, hcKillPid = oldOpen, oldSend, oldKill })
	var opened, sentFds []int
	var sent []syscall.Signal
	kills, heldFd := 0, -1
	reuse := false
	hcPidfdOpen = func(p int) (int, error) {
		opened = append(opened, p)
		fd, err := syscall.Open(os.DevNull, syscall.O_RDONLY, 0)
		heldFd = fd
		if reuse {
			// The pid now names another process: same number, another start time.
			mk(p, "stat", hcRunningStat(p, 1, p, "424242"))
		}
		return fd, err
	}
	hcPidfdSend = func(fd int, sig syscall.Signal) error {
		sentFds, sent = append(sentFds, fd), append(sent, sig)
		return nil
	}
	hcKillPid = func(int, syscall.Signal) error { kills++; return nil }
	closed := func(fd int) bool {
		var st syscall.Stat_t
		return syscall.Fstat(fd, &st) == syscall.EBADF
	}

	// Control: the pid stays the same process, so the signal goes through the held fd.
	plant(control)
	hcRetire(c, control, socket)
	if len(opened) != 1 || opened[0] != control {
		t.Fatalf("pidfd_open calls = %v, want one for %d", opened, control)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGTERM || sentFds[0] != heldFd {
		t.Errorf("pidfd_send_signal calls = %v on fds %v, want one SIGTERM on fd %d", sent, sentFds, heldFd)
	}
	if kills != 0 {
		t.Errorf("kill was called %d times in the control, want 0", kills)
	}
	if !closed(heldFd) {
		t.Errorf("fd %d is still open after the control retire", heldFd)
	}

	// The pid changes hands between the open and the send.
	opened, sent, sentFds, kills, reuse = nil, nil, nil, 0, true
	plant(victim)
	e := runDirEntry{name: "x", dirPath: filepath.Dir(socket), socket: socket, idle: 40 * 24 * time.Hour}
	ok, reason := c.retireAbandoned(e, victim, uint32(hcGetuid()))
	if ok || reason != "it is no longer the process that was inspected" {
		t.Errorf("retireAbandoned = %v, %q, want a refusal for the changed identity", ok, reason)
	}
	if len(opened) != 1 || opened[0] != victim {
		t.Errorf("pidfd_open calls = %v, want one for %d", opened, victim)
	}
	if len(sent) != 0 || kills != 0 {
		t.Errorf("signals after the pid changed hands: pidfd_send_signal %v, kill %d, want none", sent, kills)
	}
	if !closed(heldFd) {
		t.Errorf("fd %d is still open after the refused retire", heldFd)
	}
}

// TestRetireSignalGoesThroughAPidfd: on linux the retire signal is pidfd_open and then
// pidfd_send_signal, and no kill (rows HC10, HC12 to HC15, strace). The target is a helper
// that this test started.
func TestRetireSignalGoesThroughAPidfd(t *testing.T) {
	cmd := hcStartSleeper(t)
	pid := cmd.Process.Pid

	oldOpen, oldSend, oldKill := hcPidfdOpen, hcPidfdSend, hcKillPid
	t.Cleanup(func() { hcPidfdOpen, hcPidfdSend, hcKillPid = oldOpen, oldSend, oldKill })
	var opened []int
	var sent []syscall.Signal
	kills := 0
	sentFd := -1
	hcPidfdOpen = func(p int) (int, error) { opened = append(opened, p); return oldOpen(p) }
	hcPidfdSend = func(fd int, sig syscall.Signal) error {
		sent = append(sent, sig)
		sentFd = fd
		return oldSend(fd, sig)
	}
	hcKillPid = func(int, syscall.Signal) error { kills++; return nil }

	if err := hcSignalDaemon(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("hcSignalDaemon = %v", err)
	}
	if len(opened) != 1 || opened[0] != pid {
		t.Errorf("pidfd_open calls = %v, want one for the helper %d", opened, pid)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGTERM || sentFd < 0 {
		t.Errorf("pidfd_send_signal calls = %v on fd %d, want one SIGTERM on an open fd", sent, sentFd)
	}
	if kills != 0 {
		t.Errorf("kill was called %d times, want 0: the kernel has the pidfd calls", kills)
	}
	if sig := hcWaitSignal(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("the helper ended with signal %v, want SIGTERM", sig)
	}
	// The descriptor does not stay open after the call.
	if target, _ := os.Readlink("/proc/self/fd/" + strconv.Itoa(sentFd)); strings.HasPrefix(target, "anon_inode:[pidfd]") {
		t.Errorf("fd %d is still a pidfd after the call", sentFd)
	}
}

// TestRetireSignalFallsBackToKill: a kernel without the pidfd calls answers ENOSYS, and the
// signal then goes out with kill. The fallback is claustrum's own choice. The reference on
// such a kernel is not measured.
func TestRetireSignalFallsBackToKill(t *testing.T) {
	oldOpen, oldSend, oldKill := hcPidfdOpen, hcPidfdSend, hcKillPid
	t.Cleanup(func() { hcPidfdOpen, hcPidfdSend, hcKillPid = oldOpen, oldSend, oldKill })
	type call struct {
		pid int
		sig syscall.Signal
	}
	var kills []call
	hcKillPid = func(pid int, sig syscall.Signal) error { kills = append(kills, call{pid, sig}); return nil }

	// No pidfd_open at all.
	hcPidfdOpen = func(int) (int, error) { return -1, syscall.ENOSYS }
	hcPidfdSend = func(int, syscall.Signal) error { t.Error("pidfd_send_signal ran without a descriptor"); return nil }
	if err := hcSignalDaemon(4242, syscall.SIGTERM); err != nil {
		t.Errorf("hcSignalDaemon = %v, want nil from the kill fallback", err)
	}
	// pidfd_open exists and pidfd_send_signal does not.
	hcPidfdOpen = func(int) (int, error) { return syscall.Open(os.DevNull, syscall.O_RDONLY, 0) }
	hcPidfdSend = func(int, syscall.Signal) error { return syscall.ENOSYS }
	if err := hcSignalDaemon(4243, syscall.SIGTERM); err != nil {
		t.Errorf("hcSignalDaemon = %v, want nil from the kill fallback", err)
	}
	if len(kills) != 2 || kills[0] != (call{4242, syscall.SIGTERM}) || kills[1] != (call{4243, syscall.SIGTERM}) {
		t.Errorf("kill calls = %v, want one SIGTERM for each pid", kills)
	}

	// An error that is not ENOSYS is returned, and no kill follows it.
	kills = nil
	hcPidfdOpen = func(int) (int, error) { return -1, syscall.ESRCH }
	if err := hcSignalDaemon(4244, syscall.SIGTERM); err != syscall.ESRCH {
		t.Errorf("hcSignalDaemon = %v, want ESRCH", err)
	}
	if len(kills) != 0 {
		t.Errorf("kill calls = %v, want none after ESRCH", kills)
	}
}

// TestHostCleaningOffLines: the cleaner stays off with one line that says why. A socket of
// another shape names the socket (rows EV01a, ST08). A run-shaped socket with a binary that
// is not under <root>/srv names the binary (every plain-layout row). A relative run-shaped
// socket names the socket (row EV01h).
func TestHostCleaningOffLines(t *testing.T) {
	exe, ok := hcSelfExe()
	if !ok {
		t.Skip("cannot read the path of this test binary")
	}
	oldSleep := hcSleep
	t.Cleanup(func() { hcSleep = oldSleep })
	hcSleep = func(time.Duration) { t.Error("a cleaner loop started") }

	cases := []struct{ socket, want string }{
		{"/tmp/e/b/s.sock", `[daemon] host cleaning off: socket path "/tmp/e/b/s.sock" is not <root>/run/<id>/rpc.sock`},
		{"/tmp/e/other/rpc.sock", `[daemon] host cleaning off: socket path "/tmp/e/other/rpc.sock" is not <root>/run/<id>/rpc.sock`},
		{"run/c1/rpc.sock", `[daemon] host cleaning off: socket path "run/c1/rpc.sock" is not <root>/run/<id>/rpc.sock`},
		{"/tmp/e/R/run/c1/rpc.sock", fmt.Sprintf(`[daemon] host cleaning off: executable %q is not a deployed daemon under /tmp/e/R/srv`, exe)},
	}
	for _, tc := range cases {
		buf := captureLogBuf(t)
		startHostCleaner(tc.socket)
		// Another test can leave a goroutine that logs into this buffer. Count the cleaner
		// lines only.
		if got := buf.String(); !strings.Contains(got, tc.want+"\n") || strings.Count(got, "[daemon] host cleaning off") != 1 {
			t.Errorf("socket %q: log = %q, want the one line %q", tc.socket, got, tc.want)
		}
	}
}

// TestHostCleaningStartRows is the truth table of the cleaner gate. It holds the 24 start
// rows that a Linux VM measured against 89cb6289. Nine socket shapes (a to i) run in the install layout
// and in the plain layout, and six more rows move the executable. Each row gives the
// reason of the "host cleaning off" line, or no reason when the cleaner runs. When it runs,
// its root is the root of the socket. In two rows an older gate ran a pass that the
// reference does not run: KREL (install layout, relative socket) and KXB (executable under
// the srv of another root).
//
// The executable here is what /proc/self/exe names, so rows l and u give the resolved path.
func TestHostCleaningStartRows(t *testing.T) {
	const R = "/tmp/e/R"
	// The paths of this table are names only. No symlink is behind them, so the
	// resolution answers each path as given. TestHostCleaningSymlinkRows has the rows
	// with a real symlink.
	oldEval := hcEvalSymlinks
	t.Cleanup(func() { hcEvalSymlinks = oldEval })
	hcEvalSymlinks = func(p string) (string, error) { return p, nil }
	install := R + "/srv/d1/claude-ssh"
	plain := "/tmp/e/bin/tag/claude-ssh"
	sockOff := func(s string) string {
		return fmt.Sprintf("socket path %q is not <root>/run/<id>/rpc.sock", s)
	}
	exeOff := func(exe string) string {
		return fmt.Sprintf("executable %q is not a deployed daemon under %s/srv", exe, R)
	}
	shapes := []struct{ name, socket string }{
		{"a", R + "/b/s.sock"},
		{"b", R + "/b/rpc.sock"},
		{"c", R + "/run/rpc.sock"},
		{"d", R + "/run/c1/x.sock"},
		{"e", R + "/run/c1/rpc.sock"},
		{"f", R + "/run/c1/d/rpc.sock"},
		{"g", R + "/RUN/c1/rpc.sock"},
		{"h", "run/c1/rpc.sock"},
		{"i", R + "/lnk/c1/rpc.sock"}, // lnk is a symlink to run on the VM: not resolved
	}
	type row struct{ name, socket, exe, off string }
	var rows []row
	for _, sh := range shapes {
		off := sockOff(sh.socket)
		if sh.name == "e" {
			off = ""
		}
		rows = append(rows, row{"install-" + sh.name, sh.socket, install, off})
		if sh.name == "e" {
			off = exeOff(plain)
		}
		rows = append(rows, row{"plain-" + sh.name, sh.socket, plain, off})
	}
	e := R + "/run/c1/rpc.sock"
	rows = append(rows,
		row{"s-binary-directly-in-srv", e, R + "/srv/claude-ssh", exeOff(R + "/srv/claude-ssh")},
		row{"t-binary-one-level-deeper", e, R + "/srv/d1/sub/claude-ssh", exeOff(R + "/srv/d1/sub/claude-ssh")},
		row{"l-symlink-in-srv-to-a-file-outside", e, R + "/ext/claude-ssh", exeOff(R + "/ext/claude-ssh")},
		row{"n-other-file-name", e, R + "/srv/d1/other-name", ""},
		row{"u-binary-through-a-symlink-to-the-root", e, install, ""},
		row{"x-binary-under-another-root", e, R + "o/srv/d1/claude-ssh", exeOff(R + "o/srv/d1/claude-ssh")},
	)
	// Two more measured rows: a socket path that is not clean keeps the cleaner on.
	rows = append(rows,
		row{"j-dot-in-the-socket-path", R + "/run/./c1/rpc.sock", install, ""},
		row{"k-doubled-slash-in-the-socket-path", R + "//run/c1/rpc.sock", install, ""},
	)
	if len(rows) != 26 {
		t.Fatalf("%d rows, want the 24 measured start rows and rows j and k", len(rows))
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			proot, _, _ := fakeProc(t)
			hcFakeSelf(t, proot, tc.exe, "4242")
			c, err := newHostCleaner(tc.socket)
			if tc.off != "" {
				if err == nil {
					t.Fatalf("the cleaner is on with roots %v, want it off: %s", c.roots.roots, tc.off)
				}
				if err.Error() != tc.off {
					t.Errorf("reason = %q, want %q", err, tc.off)
				}
				return
			}
			if err != nil {
				t.Fatalf("the cleaner is off (%v), want it on", err)
			}
			if len(c.roots.roots) != 1 || c.roots.roots[0] != R {
				t.Errorf("roots = %v, want only the root of the socket %s", c.roots.roots, R)
			}
			if rr := c.roots.runRoots(); len(rr) != 1 || rr[0] != R+"/run" {
				t.Errorf("run roots = %v, want only %s/run", rr, R)
			}
		})
	}
}

// hcSymlinkFixture makes a real root folder <tmp>/R with run/k1 and srv/d1, and a symlink
// <tmp>/Rl to it. It also makes <tmp>/real/ROOT of the same layout. The symlink <tmp>/L
// names <tmp>/real, one level above that root. It returns the four root paths.
func hcSymlinkFixture(t *testing.T) (R, Rl, anc, ancReal string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	R, Rl = filepath.Join(tmp, "R"), filepath.Join(tmp, "Rl")
	ancReal, anc = filepath.Join(tmp, "real", "ROOT"), filepath.Join(tmp, "L", "ROOT")
	for _, root := range []string{R, ancReal, filepath.Join(tmp, "Rx")} {
		for _, sub := range []string{"run/k1", "srv/d1"} {
			if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "srv", "d1", "claude-ssh"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(R, Rl); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "L")); err != nil {
		t.Fatal(err)
	}
	return R, Rl, anc, ancReal
}

// TestHostCleaningSymlinkRows holds the rows with a real symlink that a Linux VM measured
// against 89cb6289. The cleaner is on when the socket path goes through a symlink to the
// root (rows KS-m-e and KS-q-e). It is on when the binary does (row KS-u-e). It is also on with
// a symlink one level above the root. The root list of claustrum then holds the root as
// given and its real path. A binary under the srv of another folder keeps the cleaner off (row KXB).
// So does a root whose resolution fails while the binary sits under the real path.
//
// On Linux /proc/self/exe names the real path of the binary. macOS gives the path as it
// was started. The rows with a binary path through the symlink hand in that form, and the
// start check resolves it. The row "KS-u-e" fails without that resolution.
func TestHostCleaningSymlinkRows(t *testing.T) {
	R, Rl, anc, ancReal := hcSymlinkFixture(t)
	Rx := filepath.Join(filepath.Dir(R), "Rx")
	exe := func(root string) string { return filepath.Join(root, "srv", "d1", "claude-ssh") }
	sock := func(root string) string { return filepath.Join(root, "run", "k1", "rpc.sock") }
	rows := []struct {
		name, socket, exe string
		failEval          bool
		wantRoots         []string // nil: the cleaner is off
		wantOff           string
	}{
		{name: "KS-m-e and KS-q-e socket through the symlink and the real binary path", socket: sock(Rl), exe: exe(R), wantRoots: []string{Rl, R}},
		{name: "KS-m-e socket and binary path through the symlink", socket: sock(Rl), exe: exe(Rl), wantRoots: []string{Rl, R}},
		{name: "KS-u-e binary path through the symlink and the real socket", socket: sock(R), exe: exe(Rl), wantRoots: []string{R}},
		{name: "symlink one level above the root", socket: sock(anc), exe: exe(ancReal), wantRoots: []string{anc, ancReal}},
		{name: "KXB binary under the srv of another folder", socket: sock(Rl), exe: exe(Rx),
			wantOff: fmt.Sprintf("executable %q is not a deployed daemon under %s", exe(Rx), filepath.Join(Rl, "srv"))},
		{name: "root resolution fails", socket: sock(Rl), exe: exe(R), failEval: true,
			wantOff: fmt.Sprintf("executable %q is not a deployed daemon under %s", exe(R), filepath.Join(Rl, "srv"))},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			proot, _, _ := fakeProc(t)
			hcFakeSelf(t, proot, tc.exe, "4242")
			if tc.failEval {
				oldEval := hcEvalSymlinks
				t.Cleanup(func() { hcEvalSymlinks = oldEval })
				hcEvalSymlinks = func(string) (string, error) { return "", os.ErrNotExist }
			}
			c, err := newHostCleaner(tc.socket)
			if tc.wantRoots == nil {
				if err == nil {
					t.Fatalf("the cleaner is on with roots %v, want it off: %s", c.roots.roots, tc.wantOff)
				}
				if err.Error() != tc.wantOff {
					t.Errorf("reason = %q, want %q", err, tc.wantOff)
				}
				return
			}
			if err != nil {
				t.Fatalf("the cleaner is off (%v), want it on", err)
			}
			if fmt.Sprint(c.roots.roots) != fmt.Sprint(tc.wantRoots) {
				t.Errorf("roots = %v, want %v", c.roots.roots, tc.wantRoots)
			}
			if c.ownSocket != tc.socket {
				t.Errorf("own socket = %q, want it as given %q", c.ownSocket, tc.socket)
			}
		})
	}
}

// TestTwoSpellingRootListTouchesEachRunDirOnce: the socket path goes through a symlink to
// the root, so the root list holds two spellings of one folder (row KS-m-e). The pass lists
// each run folder once, in the spelling of the socket as given, and removes the stale one
// once. 89cb6289 logged `removed run dir "<R>l/run/z1"` in that row. Every folder here is
// under the temp dir of the test.
func TestTwoSpellingRootListTouchesEachRunDirOnce(t *testing.T) {
	R, Rl, _, _ := hcSymlinkFixture(t)
	proot, _, _ := fakeProc(t)
	hcFakeSelf(t, proot, filepath.Join(R, "srv", "d1", "claude-ssh"), "4242")
	old := time.Now().Add(-40 * 24 * time.Hour)
	for _, name := range []string{"z1", "f1"} {
		if err := os.Mkdir(filepath.Join(R, "run", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(R, "run", "z1"), old, old); err != nil { // z1 is stale, f1 is fresh
		t.Fatal(err)
	}
	oldDial, oldRm := hcDial, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	var removed []string
	hcRemoveAll = func(root *os.Root, name string) error {
		removed = append(removed, name)
		return root.RemoveAll(name)
	}

	c, err := newHostCleaner(filepath.Join(Rl, "run", "k1", "rpc.sock"))
	if err != nil {
		t.Fatalf("newHostCleaner: %v", err)
	}
	if len(c.roots.roots) != 2 {
		t.Fatalf("roots = %v, want the two spellings of one folder", c.roots.roots)
	}
	entries, done := c.runDirs()
	defer done()
	var listed []string
	for _, e := range entries {
		listed = append(listed, e.dirPath)
	}
	want := []string{filepath.Join(Rl, "run", "f1"), filepath.Join(Rl, "run", "z1")}
	slices.Sort(listed)
	if fmt.Sprint(listed) != fmt.Sprint(want) {
		t.Fatalf("listed run dirs = %v, want each folder once in the spelling of the socket: %v", listed, want)
	}
	buf := captureLogBuf(t)
	var sum hcSummary
	c.tidyRunDirs(entries, &sum)

	if sum.runDirsRemoved != 1 || len(removed) != 1 {
		t.Errorf("removed %v (summary %d), want the stale folder once", removed, sum.runDirsRemoved)
	}
	if wantLine := fmt.Sprintf("[hostclean] removed run dir %q: unused for 40 days, nothing answers on its socket, no process holds its lock\n", filepath.Join(Rl, "run", "z1")); !strings.HasSuffix(buf.String(), wantLine) {
		t.Errorf("log = %q, want it to end with %q", buf.String(), wantLine)
	}
	if _, err := os.Stat(filepath.Join(R, "run", "f1")); err != nil {
		t.Errorf("the fresh folder is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(R, "run", "z1")); !os.IsNotExist(err) {
		t.Errorf("the stale folder is still there: %v", err)
	}
}
