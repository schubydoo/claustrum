//go:build linux

package main

import (
	"bytes"
	"fmt"
	"log"
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
// directories, so every test here drives it through the hcRename / hcRemoveAll seams except
// the one case that needs a real rename, which works inside its own t.TempDir.
//
// Three arms of hostclean.go are deliberately left uncovered, because no fixture can reach
// them rather than because no test was written:
//
//   - runDirs' name == "." || ".." skip: os.ReadDir never yields those entries.
//   - judgeDaemon's argv == nil spare: serveArgv already returned a socket for this
//     candidate, and it returns "" for an argv shorter than two entries, so argv cannot be
//     nil by the time that line is reached.
//   - retireAbandoned's hcSettledBusy arm: hcSettledBusy is a constant false on linux (linux
//     answers from hcBusy at judge time). The darwin implementation is a real probe, and the
//     darwin tests cover it.

func TestRunDirsEnumerationArms(t *testing.T) {
	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	hcClock = time.Now

	t.Run("a non-directory entry is not a run dir", func(t *testing.T) {
		base := t.TempDir()
		runRoot := filepath.Join(base, "run")
		if err := os.MkdirAll(filepath.Join(runRoot, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A plain file beside it. Without the stat/IsDir check it becomes a run-dir entry
		// and the tidy pass tries to rename and delete it.
		if err := os.WriteFile(filepath.Join(runRoot, "notes.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := &hostCleaner{roots: &hostRoots{roots: []string{base}}}
		dirs := c.runDirs()
		if len(dirs) != 1 || dirs[0].name != "a" {
			t.Errorf("runDirs = %+v, want only the directory entry", dirs)
		}
	})

	t.Run("the same directory reached by two roots is listed once", func(t *testing.T) {
		base := t.TempDir()
		runRoot := filepath.Join(base, "run")
		if err := os.MkdirAll(filepath.Join(runRoot, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A second root that is a symlink to the first: both runRoots() entries list the
		// same directory, and only the device+inode key can tell that.
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(base, alias); err != nil {
			t.Fatal(err)
		}
		c := &hostCleaner{roots: &hostRoots{roots: []string{base, alias}}}
		if dirs := c.runDirs(); len(dirs) != 1 {
			t.Errorf("runDirs = %d entries, want 1: the same dir under two roots", len(dirs))
		}
	})

	t.Run("the enumeration stops at the cap", func(t *testing.T) {
		base := t.TempDir()
		runRoot := filepath.Join(base, "run")
		for i := 0; i < hcMaxRunDirs+1; i++ {
			if err := os.MkdirAll(filepath.Join(runRoot, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		c := &hostCleaner{roots: &hostRoots{roots: []string{base}}}
		if dirs := c.runDirs(); len(dirs) != hcMaxRunDirs {
			t.Errorf("runDirs = %d entries, want the cap of %d", len(dirs), hcMaxRunDirs)
		}
	})
}

// hcRetireFixture seams a live-then-dead socket and a signal that "replaces" the daemon's pid,
// so retireAbandoned can succeed repeatedly. It returns the pid the fake peer reports.
func hcRetireFixture(t *testing.T, proot string, mk func(int, string, string), link func(int, string, string), socketArgv string) int {
	t.Helper()
	self := os.Getpid()
	hcFakeDaemon(t, proot, mk, link, self, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socketArgv}, true, "1")

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
	socket := "/opt/claude/run/x/rpc.sock"
	hcRetireFixture(t, proot, mk, link, socket)

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
	hcRename = func(string, string) error { return nil }
	hcRemoveAll = func(p string) error { removed = append(removed, p); return nil }

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{{name: "x", dirPath: "/opt/claude/run/x", socket: socket, idle: 40 * 24 * time.Hour}}, &sum)

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
	hcRename = func(string, string) error { renames++; return nil }
	hcRemoveAll = func(string) error { removes++; return nil }

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{{name: "x", dirPath: "/opt/claude/run/x", socket: "/opt/claude/run/x/rpc.sock", idle: 40 * 24 * time.Hour}}, &sum)

	if sum.daemonsRetired != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("summary = %+v, want no retire and no removal", sum)
	}
	if renames != 0 || removes != 0 {
		t.Errorf("a still-answering run dir was touched (renames=%d removes=%d)", renames, removes)
	}
}

// TestTidyRunDirsStopsAtTheRetireBudget: the per-pass retirement budget is a cap on how many
// daemons one sweep may SIGTERM. The entry past the budget is skipped entirely — not probed,
// not retired, not removed.
func TestTidyRunDirsStopsAtTheRetireBudget(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	hcRetireFixture(t, proot, mk, link, socket)

	oldDial, oldRen, oldRm := hcDial, hcRename, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRename, hcRemoveAll = oldDial, oldRen, oldRm })
	// Each entry probes its socket twice: live, then dead once its daemon is retired. The
	// removal's own probe of the staged copy is keyed by address, not by that alternation.
	dials := 0
	hcDial = func(addr string) (net.Conn, error) {
		if strings.Contains(addr, hcStagingPrefix) {
			return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT}
		}
		dials++
		if dials%2 == 1 {
			return newFakePeerConn(t), nil
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT}
	}
	hcRename = func(string, string) error { return nil }
	hcRemoveAll = func(string) error { return nil }

	// Every entry names the same socket (the same fake daemon answers each time), which is
	// what lets one fixture stand in for a host with many abandoned run dirs.
	var entries []runDirEntry
	for i := 0; i < hcMaxRetire+1; i++ {
		name := fmt.Sprintf("d%d", i)
		entries = append(entries, runDirEntry{
			name: name, dirPath: "/opt/claude/run/" + name, socket: socket, idle: 40 * 24 * time.Hour,
		})
	}
	var sum hcSummary
	c.tidyRunDirs(entries, &sum)

	if sum.daemonsRetired != hcMaxRetire {
		t.Errorf("daemonsRetired = %d, want the budget of %d", sum.daemonsRetired, hcMaxRetire)
	}
	if sum.runDirsRemoved != hcMaxRetire {
		t.Errorf("runDirsRemoved = %d, want %d: the entry past the budget is skipped", sum.runDirsRemoved, hcMaxRetire)
	}
}

// TestRemoveRunDirLockReacquiredMidRemoval uses a REAL rename so the staged directory carries
// the same lock file (same device and inode) the run dir had. A daemon that takes the lock in
// the rename window must get its directory back rather than have it deleted underneath it.
func TestRemoveRunDirLockReacquiredMidRemoval(t *testing.T) {
	proot, _, _ := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")

	parent := t.TempDir()
	dir := filepath.Join(parent, "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	hcRename = os.Rename // a real rename: the staged dir must carry the real lock file
	removed := 0
	hcRemoveAll = func(string) error { removed++; return nil }

	// removeRunDir is called directly: the tidy pass has its own lock check, which refuses
	// this dir before the removal starts. The arm under test is the SECOND look, after the
	// dir has already been renamed aside — a lock taken inside that window.
	e := runDirEntry{name: "x", dirPath: dir, socket: filepath.Join(dir, "rpc.sock"), idle: 40 * 24 * time.Hour}
	if c.removeRunDir(e, false) {
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
	dials := 0
	hcDial = func(string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} // dead when the tidy looks
		}
		return newFakePeerConn(t), nil // a listener reappeared inside the rename window
	}
	renames := 0
	hcRename = func(string, string) error {
		renames++
		if renames == 1 {
			return nil // staged
		}
		return syscall.EACCES // and now it cannot be put back
	}
	removed := 0
	hcRemoveAll = func(string) error { removed++; return nil }

	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{{name: "x", dirPath: "/opt/claude/run/x", socket: "/opt/claude/run/x/rpc.sock", idle: 40 * 24 * time.Hour}}, &sum)

	if removed != 0 || sum.runDirsRemoved != 0 {
		t.Errorf("the dir was removed although a listener reappeared (removed=%d)", removed)
	}
	got := buf.String()
	if !strings.Contains(got, `run dir "x" could not be restored`) {
		t.Errorf("log = %q, want the could-not-be-restored line naming the dir", got)
	}
	if !strings.Contains(got, "a listener reappeared mid-removal") {
		t.Errorf("log = %q, want the reason the removal was aborted", got)
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
	// The self-probe succeeds (the socket leads to this process), and only then the process
	// list turns out to be unreadable. A mutant that carried on sweeps with no snapshot.
	procRoot = filepath.Join(root, "gone")
	c := &hostCleaner{
		roots:     &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"},
		ownSocket: ownSock, ownRunDir: filepath.Dir(ownSock), selfPid: os.Getpid(),
	}
	if sum := c.Pass(); sum != (hcSummary{}) {
		t.Errorf("summary = %+v, want the zero summary when /proc cannot be read", sum)
	}
}

// TestPassClassifiesAWholeHost drives one sweep over a fake host carrying every shape the
// classification loops branch on: a stranded daemon with a child, a daemon that must be
// spared, a process group with no leader, an orphan that must be spared, an orphan that must
// be reaped, a plain process, and a pid that vanishes mid-read. The counters and the signal
// lists together pin which arm handled which.
func TestPassClassifiesAWholeHost(t *testing.T) {
	proot, mk, link := fakeProc(t)

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
	cliExe := "/opt/claude/ccd-cli/v1/claude"
	deadSock := filepath.Join(t.TempDir(), "gone", "rpc.sock")
	stream := []string{"claude", "--output-format=stream-json"}

	stranded := self + 1 // reaped: marked, old, its own socket is gone, no lock, not busy
	hcFakeDaemonUID(t, proot, mk, link, stranded, daemonExe,
		[]string{daemonExe, "--serve", "--socket", deadSock}, true, "1", uid)

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
	link(orphan, "fd/0", "pipe:[1]")

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
