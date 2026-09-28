//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The run-dir tidy pass and the sweep orchestration. The tidy pass renames and deletes
// directories. A test either counts those calls through the hcRename / hcRemoveAll seams, or
// lets them run for real through os.Root inside its own t.TempDir. No test renames or
// removes anything outside a temp dir.
//
// One arm of hostclean.go is deliberately left uncovered, because no fixture can reach it
// rather than because no test was written: runDirs' name == "." || ".." skip.
// Readdirnames never yields those entries.
//
// judgeDaemon's argv == nil spare used to be listed here too. It is reachable now, because
// that gate runs before serveArgv, and TestJudgeDaemonBranches drives it.
//
// retireAbandoned's hcSettledBusy arm used to be listed here as well. It is reachable
// now: hcSettledBusy is one shared implementation that samples for real on linux too, so
// TestRetireAbandonedSparesABusyDaemon drives that branch.

// hcRetireFixture seams a live-then-dead socket and a signal that "replaces" the daemon's pid,
// so retireAbandoned can succeed repeatedly. It returns the pid the fake peer reports.
func hcRetireFixture(t *testing.T, proot string, mk func(int, string, string), link func(int, string, string), socketArgv string) int {
	t.Helper()
	self := os.Getpid()
	// SO_PEERCRED reports the runner's real uid for the fake peer, and the retire refuses a
	// peer whose uid is not the cleaner's. So the cleaner and the fixture both use that uid.
	uid := os.Getuid()
	hcGetuid = func() int { return uid }
	hcFakeDaemonUID(t, proot, mk, link, self, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socketArgv}, true, "1", uid)
	link(self, "fd/1", "/dev/null") // readable connections, none of them a client

	oldSig, oldSleep := hcSignalPid, hcSleep
	t.Cleanup(func() { hcSignalPid, hcSleep = oldSig, oldSleep })
	hcSleep = func(time.Duration) {}
	rounds := 0
	hcSignalPid = func(pid int, _ syscall.Signal) error {
		// The daemon exits and its pid is taken by something else: the tracked identity no
		// longer matches, which is how waitGone reports it gone.
		rounds++
		mk(pid, "stat", hcRunningStat(pid, 1, pid, strconv.Itoa(100+rounds)))
		return nil
	}
	return self
}

// TestTidyRunDirsRetiresAnAnsweringDaemon covers the live-socket arm: a run dir whose socket
// still answers is not removed outright — the answering daemon is retired first, and only a
// re-probe showing the socket dead lets the removal proceed. A mutant without the retire call
// keeps every answered dir forever; one without the re-probe removes a dir that still answers.
func TestTidyRunDirsRetiresAnAnsweringDaemon(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	e := hcNewEntry(t, "x", 40*24*time.Hour)
	hcRetireFixture(t, proot, mk, link, e.socket)

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	dials := 0
	hcDial = func(string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return newFakePeerConn(t), nil // the daemon still answers
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} // it exited
	}
	var removed []string
	hcRename = func(*os.Root, string, string) error { return nil }
	hcRemoveAll = func(_ *os.Root, p string) error { removed = append(removed, p); return nil }

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)

	if sum.daemonsRetired != 1 {
		t.Errorf("daemonsRetired = %d, want 1", sum.daemonsRetired)
	}
	if sum.runDirsRemoved != 1 || len(removed) != 1 {
		t.Errorf("run dir not removed after the retire: removed=%v sum=%d", removed, sum.runDirsRemoved)
	}
}

// TestTidyRunDirsKeepsADaemonItCannotRetire is the other half: the socket answers and the
// answerer cannot be verified as ours, so the dir stays. Nothing is renamed or removed.
func TestTidyRunDirsKeepsADaemonItCannotRetire(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	// The answering process serves a DIFFERENT socket, so verifyListener refuses it.
	hcRetireFixture(t, proot, mk, link, "/opt/claude/run/other/rpc.sock")

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil }
	renames, removes := 0, 0
	hcRename = func(*os.Root, string, string) error { renames++; return nil }
	hcRemoveAll = func(*os.Root, string) error { removes++; return nil }

	buf := captureLogBuf(t)

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{hcNewEntry(t, "x", 40*24*time.Hour)}, &sum)

	if sum.daemonsRetired != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("summary = %+v, want no retire and no removal", sum)
	}
	if renames != 0 || removes != 0 {
		t.Errorf("a still-answering run dir was touched (renames=%d removes=%d)", renames, removes)
	}
	// "Nothing happened" is also true of a mutant that never tried to retire. The log is what
	// distinguishes a REFUSED retire from a skipped one, so it is asserted here.
	if got := buf.String(); !strings.Contains(got, "was not retired: it serves a different socket") {
		t.Errorf("log = %q, want the refusal that shows the retire was attempted and declined", got)
	}
}

// TestTidyRunDirsStopsAtTheRetireBudget: the per-pass retirement budget is a cap on how many
// daemons one sweep may SIGTERM. The entry past the budget is skipped, with a log line: not
// probed, not retired, not removed.
func TestTidyRunDirsStopsAtTheRetireBudget(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	runRoot, root := hcRunRoot(t)
	socket := filepath.Join(runRoot, "d0", rpcSockBasename)
	hcRetireFixture(t, proot, mk, link, socket)

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	// Each entry probes its socket twice: live, then dead once its daemon is retired.
	dials := 0
	hcDial = func(addr string) (net.Conn, error) {
		dials++
		if dials%2 == 1 {
			return newFakePeerConn(t), nil
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT}
	}
	hcRename = func(*os.Root, string, string) error { return nil }
	hcRemoveAll = func(*os.Root, string) error { return nil }
	buf := captureLogBuf(t)

	// Every entry names the same socket (the same fake daemon answers each time), which is
	// what lets one fixture stand in for a host with many abandoned run dirs.
	var entries []runDirEntry
	for i := 0; i < hcMaxRetire+1; i++ {
		e := hcEntry(t, runRoot, root, fmt.Sprintf("d%d", i), 40*24*time.Hour)
		e.socket = socket
		entries = append(entries, e)
	}
	var sum hcSummary
	c.tidyRunDirs(entries, &sum)

	if sum.daemonsRetired != hcMaxRetire {
		t.Errorf("daemonsRetired = %d, want the budget of %d", sum.daemonsRetired, hcMaxRetire)
	}
	if sum.runDirsRemoved != hcMaxRetire {
		t.Errorf("runDirsRemoved = %d, want %d: the entry past the budget is skipped", sum.runDirsRemoved, hcMaxRetire)
	}
	last := entries[hcMaxRetire].dirPath
	if want := fmt.Sprintf("run dir %q not examined this sweep: the retire budget of %d attempts is spent", last, hcMaxRetire); !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

// TestTidyRunDirsBudgetCountsAttempts: the budget counts retire attempts. Eight
// refused retires spend it, and the ninth dir is left for a later pass without a dial.
// Not measured: the Linux run did not stage this rule.
func TestTidyRunDirsBudgetCountsAttempts(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	// The answerer serves a DIFFERENT socket, so every retire is refused.
	hcRetireFixture(t, proot, mk, link, "/opt/claude/run/other/rpc.sock")

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	dials := 0
	hcDial = func(string) (net.Conn, error) { dials++; return newFakePeerConn(t), nil }
	hcRename = func(*os.Root, string, string) error { return nil }
	hcRemoveAll = func(*os.Root, string) error { return nil }
	buf := captureLogBuf(t)

	runRoot, root := hcRunRoot(t)
	var entries []runDirEntry
	for i := 0; i < hcMaxRetire+1; i++ {
		entries = append(entries, hcEntry(t, runRoot, root, fmt.Sprintf("d%d", i), 40*24*time.Hour))
	}
	var sum hcSummary
	c.tidyRunDirs(entries, &sum)

	if dials != hcMaxRetire {
		t.Errorf("dials = %d, want %d: the ninth dir is not probed once eight attempts failed", dials, hcMaxRetire)
	}
	if want := "the retire budget of 8 attempts is spent"; !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

// TestRemoveRunDirLockReacquiredMidRemoval uses a REAL rename so the staged directory carries
// the same lock file (same device and inode) the run dir had. A daemon that takes the lock in
// the rename window must get its directory back rather than have it deleted underneath it.
func TestRemoveRunDirLockReacquiredMidRemoval(t *testing.T) {
	proot, _, _ := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")

	e := hcNewEntry(t, "x", 40*24*time.Hour)
	dir := e.dirPath
	lockPath := filepath.Join(dir, runDirLockName)
	if err := os.WriteFile(lockPath, []byte(`{"pid":4321,"role":"serve"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	major, minor := hcDevMajorMinor(st.Dev)
	locks := "1: POSIX ADVISORY WRITE 4321 " + fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino) + " 0 EOF\n"
	if err := os.WriteFile(filepath.Join(proot, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	hcRename = func(r *os.Root, from, to string) error { return r.Rename(from, to) } // a real rename: the staged dir must carry the real lock file
	removed := 0
	hcRemoveAll = func(*os.Root, string) error { removed++; return nil }

	// removeRunDir is called directly: the tidy pass has its own lock check, which refuses
	// this dir before the removal starts. The arm under test is the SECOND look, after the
	// dir has already been renamed aside — a lock taken inside that window.
	if c.removeRunDir(e, false, hcSockMissing) {
		t.Error("removeRunDir reported success for a dir whose lock was re-acquired")
	}
	if removed != 0 {
		t.Errorf("a re-locked run dir was deleted (RemoveAll calls: %d)", removed)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the run dir was not renamed back: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("the lock file did not come back with the dir: %v", err)
	}
}

// TestUndoRenameFailureIsLogged covers the arm where the restore itself fails. The directory
// is then stranded under its staging name, and the log line is the only trace an operator
// gets, so the log is what this test asserts.
func TestUndoRenameFailureIsLogged(t *testing.T) {
	proot, _, _ := fakeProc(t)
	_ = proot
	c := hcTestCleaner(t, "/opt/claude")

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} } // dead when the tidy looks
	renames := 0
	hcRename = func(r *os.Root, from, to string) error {
		renames++
		if renames == 1 {
			if err := r.Rename(from, to); err != nil { // staged for real
				return err
			}
			// A socket appears in the staged dir inside the rename window.
			return os.WriteFile(filepath.Join(r.Name(), to, rpcSockBasename), nil, 0o600)
		}
		return syscall.EACCES // and now it cannot be put back
	}
	removed := 0
	hcRemoveAll = func(*os.Root, string) error { removed++; return nil }
	buf := captureLogBuf(t)

	var sum hcSummary
	e := hcNewEntry(t, "x", 40*24*time.Hour)
	c.tidyRunDirs([]runDirEntry{e}, &sum)

	if removed != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("the dir was removed although a socket appeared (removed=%d)", removed)
	}
	got := buf.String()
	if want := fmt.Sprintf("run dir %q could not be restored (a socket appeared in it during removal)", e.dirPath); !strings.Contains(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestPassBailsWhenTheProcessListIsUnreadable(t *testing.T) {
	root, _, _ := fakeProc(t)
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
	// An empty summary alone would not prove the pass stopped: with no snapshot the two
	// classification loops have nothing to iterate either. The root therefore holds ONE run
	// dir the tidy sweep would remove — old enough, no listener, no lock — so carrying on is
	// visible as a removal. Both destructive calls are seamed, so the dir is only counted.
	base := t.TempDir()
	stale := filepath.Join(base, "run", "stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	oldRen, oldRm := hcRename, hcRemoveAll
	t.Cleanup(func() { hcRename, hcRemoveAll = oldRen, oldRm })
	renames, removes := 0, 0
	hcRename = func(*os.Root, string, string) error { renames++; return nil }
	hcRemoveAll = func(*os.Root, string) error { removes++; return nil }

	// The self-probe succeeds (the socket leads to this process), and only then the process
	// list turns out to be unreadable. The pass must stop there.
	procRoot = filepath.Join(root, "gone")
	c := &hostCleaner{
		roots:     &hostRoots{roots: []string{base}, daemonBin: "server"},
		ownSocket: ownSock, ownRunDir: filepath.Dir(ownSock), selfPid: os.Getpid(),
	}

	if sum := c.Pass(); sum != (hcSummary{}) {
		t.Errorf("summary = %+v, want the zero summary when /proc cannot be read", sum)
	}
	if renames != 0 || removes != 0 {
		t.Errorf("the pass tidied a run dir after failing to read the process list (renames=%d removes=%d)", renames, removes)
	}
}

// TestPassClassifiesAWholeHost drives one sweep over a fake host carrying every shape the
// classification loops branch on: a stranded daemon with a child, a daemon that must be
// spared, a process group with no leader, an orphan that must be spared, an orphan that must
// be reaped, a plain process, and a pid that vanishes mid-read. The counters and the signal
// lists together pin which arm handled which.
func TestPassClassifiesAWholeHost(t *testing.T) {
	proot, mk, link := fakeProc(t)

	// Pass ends with a real tidy sweep over the roots, and this cleaner's root is the
	// product's own install path. The fake clock sits in 1970, so every ordinary run dir
	// under it reads as fresh and is skipped — but a leftover <name>.removing-<pid>-<ts> staging
	// dir SKIPS the idle gate by design, and removeRunDir then calls hcRemoveAll on it with
	// no rename. Measured against the same shape in TestPassReapsStrandedDaemon: a staging
	// dir holding a file was really deleted with these seams absent, while an ordinary
	// 40-day-idle dir was left alone. So both destructive calls are seamed before Pass runs.
	oldRen, oldRm := hcRename, hcRemoveAll
	t.Cleanup(func() { hcRename, hcRemoveAll = oldRen, oldRm })
	hcRename = func(*os.Root, string, string) error { return nil }
	hcRemoveAll = func(*os.Root, string) error { return nil }

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

	single, group := hcSeamSignals(t, proot) // clock, sleep, and signals that make a pid exit
	oldUID := hcGetuid
	t.Cleanup(func() { hcGetuid = oldUID })
	uid := os.Getuid()
	hcGetuid = func() int { return uid }

	self := os.Getpid()
	daemonExe := "/opt/claude/srv/a/server"
	cliExe := "/opt/claude/ccd-cli/2.1.0"
	deadSock := "/opt/claude/run/claustrum-test-gone/rpc.sock"
	stream := []string{"claude", "--output-format=stream-json"}

	stranded := self + 1 // reaped: marked, old, its own socket is gone, no lock, not busy
	hcFakeDaemonUID(t, proot, mk, link, stranded, daemonExe,
		[]string{daemonExe, "--serve", "--socket", deadSock}, true, "1", uid)
	link(stranded, "fd/1", "/dev/null") // readable descriptors, no daemon.lock, no client

	child := self + 2 // its child: ended in phase B once the daemon is dead
	hcFakeDaemonUID(t, proot, mk, link, child, cliExe, stream, true, "1", uid)
	mk(child, "stat", hcRunningStat(child, stranded, child, "1")) // ppid = the stranded daemon

	unmarked := self + 3 // spared: our --serve daemon, old, but no daemon-child marker
	hcFakeDaemonUID(t, proot, mk, link, unmarked, daemonExe,
		[]string{daemonExe, "--serve", "--socket", "/opt/claude/run/u/rpc.sock"}, false, "1", uid)

	plain := self + 4 // neither a daemon nor a CLI: skipped in both loops
	hcFakeDaemonUID(t, proot, mk, link, plain, "/usr/bin/unrelated", []string{"unrelated"}, false, "1", uid)

	leaderless := self + 5 // its group leader is not in the snapshot
	hcFakeDaemonUID(t, proot, mk, link, leaderless, cliExe, stream, true, "1", uid)
	mk(leaderless, "stat", hcRunningStat(leaderless, 1, 999_001, "1")) // pgid nobody owns

	stopped := self + 6 // spared: a stream-json leader that is stopped, so it cannot be read
	hcFakeDaemonUID(t, proot, mk, link, stopped, cliExe, stream, true, "1", uid)
	mk(stopped, "stat", strconv.Itoa(stopped)+" (claude) T 1 "+strconv.Itoa(stopped)+" "+strings.Repeat("0 ", 16)+"1 x")

	orphan := self + 7 // reaped: a full stream-json orphan whose descriptors read
	hcFakeDaemonUID(t, proot, mk, link, orphan, cliExe, stream, true, "1", uid)
	hcPipeStdio(link, orphan)

	vanished := self + 8 // enumerated, then unreadable: skipped
	if err := os.MkdirAll(filepath.Join(proot, strconv.Itoa(vanished)), 0o755); err != nil {
		t.Fatal(err)
	}

	c := &hostCleaner{
		roots:     &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"},
		ownSocket: ownSock, ownRunDir: filepath.Dir(ownSock), selfPid: self,
	}
	sum := c.Pass()

	if sum.strandedEnded != 1 {
		t.Errorf("strandedEnded = %d, want 1", sum.strandedEnded)
	}
	if sum.strandedLeftover != 1 {
		t.Errorf("strandedLeftover = %d, want 1: the stranded daemon's child", sum.strandedLeftover)
	}
	// One spared daemon (unmarked) and one spared orphan (stopped).
	if sum.undecided != 2 {
		t.Errorf("undecided = %d, want 2 (the unmarked daemon and the stopped orphan)", sum.undecided)
	}
	if sum.orphanSignalled != 1 {
		t.Errorf("orphanSignalled = %d, want 1", sum.orphanSignalled)
	}
	if len(*single) != 1 || (*single)[0] != stranded {
		t.Errorf("single-pid signals = %v, want exactly the stranded daemon %d", *single, stranded)
	}
	// The child and the orphan are both group leaders, so both are signalled as groups.
	var groups []int
	groups = append(groups, *group...)
	wantGroups := map[int]bool{child: true, orphan: true}
	for _, pid := range groups {
		if !wantGroups[pid] {
			t.Errorf("group signal to pid %d, which should not have been ended (%v)", pid, groups)
		}
		delete(wantGroups, pid)
	}
	if len(wantGroups) != 0 {
		t.Errorf("these pids were never ended: %v (group signals: %v)", wantGroups, groups)
	}
}
