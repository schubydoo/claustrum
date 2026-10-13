//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTidyKeepsARunDirWhenProcLocksCannotBeRead pins cell Q6 (`5fd08069`, Linux VM). The
// lock table of the fake /proc is gone, so the holder of daemon.lock is not known. The run
// dir stays with the line of the reference, and the remove seam is not called. The control
// is the same run dir with a lock table that reads and names no holder: the run dir goes
// through the seam. Every path is under t.TempDir.
func TestTidyKeepsARunDirWhenProcLocksCannotBeRead(t *testing.T) {
	for _, readable := range []bool{false, true} {
		proot, _, _ := fakeProc(t)
		if !readable {
			if err := os.Remove(filepath.Join(proot, "locks")); err != nil {
				t.Fatal(err)
			}
		}
		oldDial, oldRm := hcDial, hcRemoveAll
		t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
		hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
		var removed []string
		hcRemoveAll = func(root *os.Root, name string) error {
			removed = append(removed, name)
			return root.RemoveAll(name)
		}
		e := hcNewEntry(t, "dead", 40*24*time.Hour)
		if err := os.WriteFile(filepath.Join(e.dirPath, runDirLockName), hcDeadRecord, 0o600); err != nil {
			t.Fatal(err)
		}
		hcBackdate(t, e.dirPath, 40*24*time.Hour)
		state, _, present := lockProbe(e.dirPath)
		buf := captureLogBuf(t)
		var sum hcSummary
		(&hostCleaner{selfPid: os.Getpid()}).tidyRunDirs([]runDirEntry{e}, &sum)
		log := buf.String()

		if readable {
			if state != hcLockStale || len(removed) != 1 || hcDirThere(t, e.dirPath) || !strings.Contains(log, hcRemovedLine(e.dirPath)) {
				t.Errorf("control: state=%d removed=%q there=%v log=%q, want a stale lock and the dir removed through the seam", state, removed, hcDirThere(t, e.dirPath), log)
			}
			continue
		}
		if state != hcLockUnexamined || !present {
			t.Errorf("lockProbe = %d present=%v, want %d (not examined) for a lock file that is there", state, present, hcLockUnexamined)
		}
		want := fmt.Sprintf("[hostclean] run dir %q unused for 40 days: kept, its daemon.lock could not be examined\n", e.dirPath)
		if len(removed) != 0 || !hcDirThere(t, e.dirPath) || sum != (hcSummary{}) || !strings.Contains(log, want) || strings.Count(log, "\n") != 1 {
			t.Errorf("removed=%q there=%v summary=%+v log=%q, want the dir kept with only %q", removed, hcDirThere(t, e.dirPath), sum, log, want)
		}
	}
}

// hcStoppedStat is a /proc/<pid>/stat line for a stopped process: state T.
func hcStoppedStat(pid int, startTicks string) string {
	return strings.Replace(hcRunningStat(pid, 1, pid, startTicks), ") R ", ") T ", 1)
}

// TestRetireRefusesAStoppedDaemon pins cell Q2 (`5fd08069`, Linux VM). A daemon of ours in
// state T fails the listener check with the text of the reference, and the retire takes no
// hold and sends no signal. The control is the same daemon in state R: the check passes and
// one SIGTERM goes to the seam. The daemon is a fake pid in a temp /proc, and the hold is a
// seam that only records.
func TestRetireRefusesAStoppedDaemon(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		proot, mk, link := fakeProc(t)
		oldSig, oldClock, oldSleep, oldUID := hcHoldPid, hcClock, hcSleep, hcGetuid
		t.Cleanup(func() { hcHoldPid, hcClock, hcSleep, hcGetuid = oldSig, oldClock, oldSleep, oldUID })
		now := time.Unix(3_000_000, 0)
		hcClock = func() time.Time { return now }
		hcSleep = func(d time.Duration) { now = now.Add(d) }
		hcGetuid = func() int { return 1000 }
		holds := 0
		var sent []syscall.Signal
		hcHoldPid = func(int) (func(syscall.Signal) error, func()) {
			holds++
			return func(sig syscall.Signal) error { sent = append(sent, sig); return nil }, func() {}
		}
		const pid = 4600
		socket := "/opt/claude/run/x/rpc.sock"
		exe := "/opt/claude/srv/a/server"
		hcFakeDaemon(t, proot, mk, link, pid, exe, []string{exe, "--serve", "--socket", socket}, true, "1")
		link(pid, "fd/1", "/dev/null") // readable connections, none a client
		if stopped {
			mk(pid, "stat", hcStoppedStat(pid, "1"))
		}
		c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
		e := runDirEntry{name: "x", dirPath: filepath.Dir(socket), socket: socket, idle: 40 * 24 * time.Hour}

		detail := c.verifyListener(pid, socket)
		retired, reason := c.retireAbandoned(e, pid, 1000)

		if !stopped {
			if detail != "" || holds != 1 || len(sent) != 1 || sent[0] != syscall.SIGTERM {
				t.Errorf("control: verify=%q holds=%d sent=%v, want a verified daemon and one SIGTERM", detail, holds, sent)
			}
			continue
		}
		const want = "the listener could not be verified as a daemon of ours (not inspectable)"
		if detail != want || retired || reason != want {
			t.Errorf("verify=%q retired=%v reason=%q, want the refusal %q", detail, retired, reason, want)
		}
		if holds != 0 || len(sent) != 0 {
			t.Errorf("holds=%d sent=%v, want no hold and no signal for a stopped daemon", holds, sent)
		}
	}
}

// TestPassLeavesAStoppedDaemonAlone pins the first line and the count of cell Q2 (`5fd08069`,
// Linux VM): an old daemon of ours in state T on an idle run dir gets the "left alone" line
// and counts as one undecided candidate. No signal goes out. The control is the same daemon
// in state R: no line and no count.
func TestPassLeavesAStoppedDaemonAlone(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		f := hcNewPassFixture(t)
		pid := os.Getpid() + 1
		hcSiblingDaemon(t, f, pid, "s1", "1", 40*24*time.Hour)
		if stopped {
			f.mk(pid, "stat", hcStoppedStat(pid, "1"))
		}
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		log := buf.String()
		if !strings.Contains(log, "[hostclean] removed run dir ") {
			t.Fatalf("stopped=%v: the pass did not reach the tidy; log %q", stopped, log)
		}
		if len(f.single) != 0 || len(f.group) != 0 {
			t.Errorf("stopped=%v: signals single=%v group=%v, want none", stopped, f.single, f.group)
		}
		line := fmt.Sprintf("[hostclean] daemon pid %d left alone this pass: it could not be inspected (or is stopped)\n", pid)
		if !stopped {
			if strings.Contains(log, "left alone this pass") || sum.undecided != 0 {
				t.Errorf("control: undecided=%d log=%q, want no line and no count", sum.undecided, log)
			}
			continue
		}
		if strings.Count(log, line) != 1 || sum.undecided != 1 {
			t.Errorf("undecided=%d log=%q, want one %q and undecided 1", sum.undecided, log, line)
		}
	}
}
