//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Helpers shared by the host-cleaner tests.

// hcRetire calls retireAbandoned for pid on socket, the way the tidy does for a run dir idle
// 40 days, with the peer uid the cleaner itself runs as. It returns only whether the daemon
// was retired.
func hcRetire(c *hostCleaner, pid int, socket string) bool {
	e := runDirEntry{
		name: filepath.Base(filepath.Dir(socket)), dirPath: filepath.Dir(socket),
		socket: socket, idle: 40 * 24 * time.Hour,
	}
	ok, _ := c.retireAbandoned(e, pid, uint32(hcGetuid()))
	return ok
}

// hcEntry makes the run dir <runRoot>/<name>, sets its mtime idle before hcClock(), and
// returns the entry runDirs would list for it.
func hcEntry(t *testing.T, runRoot string, root *os.Root, name string, idle time.Duration) runDirEntry {
	t.Helper()
	dir := filepath.Join(runRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hcBackdate(t, dir, idle)
	return runDirEntry{root: root, name: name, dirPath: dir, socket: filepath.Join(dir, rpcSockBasename), idle: idle}
}

// hcNewEntry is hcEntry in a run root of its own.
func hcNewEntry(t *testing.T, name string, idle time.Duration) runDirEntry {
	t.Helper()
	runRoot, root := hcRunRoot(t)
	return hcEntry(t, runRoot, root, name, idle)
}

// hcPipeStdio gives pid pipes on fds 0, 1 and 2, the stdio a daemon gives its child.
func hcPipeStdio(link func(int, string, string), pid int) {
	link(pid, "fd/0", "pipe:[1]")
	link(pid, "fd/1", "pipe:[2]")
	link(pid, "fd/2", "pipe:[3]")
}

// hcBackdate sets path's mtime idle before hcClock().
func hcBackdate(t *testing.T, path string, idle time.Duration) {
	t.Helper()
	at := hcClock().Add(-idle)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// ---- Pass fixture ----

// hcPassFix is a cleaner over a real temp root with a live own socket, a fake /proc, a clock
// the sleeps drive, and signals that only record (and make the fake pid exit). dial answers
// every dial except the own-socket one.
type hcPassFix struct {
	c       *hostCleaner
	base    string
	runRoot string
	proot   string
	mk      func(int, string, string)
	link    func(int, string, string)
	uid     int
	dial    func(addr string) (net.Conn, error)
	single  []int // single-pid signals
	group   []int // group signals
}

func hcNewPassFixture(t *testing.T) *hcPassFix {
	t.Helper()
	f := &hcPassFix{uid: os.Getuid()}
	f.proot, f.mk, f.link = fakeProc(t)
	f.base = t.TempDir()
	f.runRoot = filepath.Join(f.base, "run")
	own := filepath.Join(f.runRoot, "C")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	ownSock := filepath.Join(own, rpcSockBasename)
	ln, err := net.Listen("unix", ownSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			conn.Close()
		}
	}()

	oldDial, oldUID, oldClock, oldSleep := hcDial, hcGetuid, hcClock, hcSleep
	oldSig, oldGrp, oldRen, oldRm := hcSignalPid, killGroup, hcRename, hcRemoveAll
	t.Cleanup(func() {
		hcDial, hcGetuid, hcClock, hcSleep = oldDial, oldUID, oldClock, oldSleep
		hcSignalPid, killGroup, hcRename, hcRemoveAll = oldSig, oldGrp, oldRen, oldRm
	})
	hcGetuid = func() int { return f.uid }
	now := time.Now()
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }
	die := func(pid int) { _ = os.RemoveAll(filepath.Join(f.proot, strconv.Itoa(pid))) }
	hcSignalPid = func(pid int, _ syscall.Signal) error { f.single = append(f.single, pid); die(pid); return nil }
	killGroup = func(pid int, sig syscall.Signal) error {
		if sig == 0 {
			if _, err := os.Stat(filepath.Join(f.proot, strconv.Itoa(pid))); err != nil {
				return syscall.ESRCH
			}
			return nil
		}
		f.group = append(f.group, pid)
		die(pid)
		return nil
	}
	f.dial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	hcDial = func(addr string) (net.Conn, error) {
		if addr == ownSock {
			return net.DialTimeout("unix", addr, hcDialTimeout)
		}
		return f.dial(addr)
	}
	f.c = &hostCleaner{
		roots:     &hostRoots{roots: []string{f.base}, daemonBin: "server"},
		ownSocket: ownSock, ownRunDir: own, selfPid: os.Getpid(),
	}
	return f
}

// plantDaemon plants a marked, old daemon of ours serving socket. Its descriptor list reads and
// holds neither a lock nor a client.
func (f *hcPassFix) plantDaemon(t *testing.T, pid int, socket string) {
	t.Helper()
	exe := filepath.Join(f.base, "srv", "a", "server")
	hcFakeDaemonUID(t, f.proot, f.mk, f.link, pid, exe, []string{exe, "--serve", "--socket", socket}, true, "1", f.uid)
	f.link(pid, "fd/1", "/dev/null")
}

// ---- Order: the run dirs are listed before any daemon is dialled ----

// TestPassListsRunDirsBeforeTheDaemonPhase pins the pass order (issue 429). The
// sibling N holds no daemon.lock by name, so the daemon phase dials it, and that dial writes
// its log, as a real daemon does. The run dir was listed before the dial, with its old age, so
// the tidy sees it, re-reads it, and keeps it with the measured line. Listing the dirs after
// the daemon phase reads the fresh log instead and skips N without a word.
func TestPassListsRunDirsBeforeTheDaemonPhase(t *testing.T) {
	f := hcNewPassFixture(t)
	nDir := filepath.Join(f.runRoot, "N")
	if err := os.MkdirAll(nDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(nDir, hcLogName)
	if err := os.WriteFile(logPath, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hcBackdate(t, logPath, 454*24*time.Hour)
	hcBackdate(t, nDir, 454*24*time.Hour)
	nSock := filepath.Join(nDir, rpcSockBasename)
	pid := os.Getpid() + 1
	f.plantDaemon(t, pid, nSock)
	f.link(pid, "fd/3", filepath.Join(nDir, "daemon.lock.aside")) // the lockaside case

	dials := 0
	f.dial = func(addr string) (net.Conn, error) {
		if addr == nSock {
			dials++
			now := hcClock()
			_ = os.Chtimes(logPath, now, now) // the daemon logs "New connection"
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.EIO} // inconclusive: spared
	}
	buf := captureLogBuf(t)

	f.c.Pass()

	want := fmt.Sprintf("[hostclean] run dir %q: kept, in use again since this pass listed it", nDir)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
	if dials != 1 {
		t.Errorf("N's socket was dialled %d times, want 1 (the judge's dial, and no tidy probe)", dials)
	}
}

// ---- The dir goes in the same pass after a retire, a 0-byte lock included ----

// TestTidyRemovesADirWithAnEmptyLock: after a graceful exit a daemon leaves a 0-byte
// daemon.lock and no socket. The reference removes that dir in the same pass, both right after
// it retires the daemon and when it finds the dir dead (issue 429).
func TestTidyRemovesADirWithAnEmptyLock(t *testing.T) {
	t.Run("right after the retire", func(t *testing.T) {
		proot, mk, link := fakeProc(t)
		c := hcTestCleaner(t, "/opt/claude")
		e := hcNewEntry(t, "N", 454*24*time.Hour)
		if err := os.WriteFile(filepath.Join(e.dirPath, runDirLockName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		hcBackdate(t, e.dirPath, 454*24*time.Hour)
		hcRetireFixture(t, proot, mk, link, e.socket)

		oldDial := hcDial
		t.Cleanup(func() { hcDial = oldDial })
		dials := 0
		hcDial = func(string) (net.Conn, error) {
			dials++
			if dials == 1 {
				return newFakePeerConn(t), nil // the daemon answers
			}
			return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} // it unlinked its socket
		}
		buf := captureLogBuf(t)

		var sum hcSummary
		c.tidyRunDirs([]runDirEntry{e}, &sum)
		if sum.daemonsRetired != 1 || sum.runDirsRemoved != 1 {
			t.Errorf("summary = %+v, want 1 retired and 1 removed in the same pass", sum)
		}
		if _, err := os.Lstat(e.dirPath); !os.IsNotExist(err) {
			t.Errorf("the run dir is still there: %v", err)
		}
		for _, want := range []string{
			fmt.Sprintf("[hostclean] retiring abandoned daemon pid %d: its run dir %q has seen no connection for 454 days: SIGTERM", os.Getpid(), e.dirPath),
			fmt.Sprintf("[hostclean] removed run dir %q: unused for 454 days, nothing answers on its socket, no process holds its lock", e.dirPath),
		} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("log = %q, want %q", buf.String(), want)
			}
		}
	})

	t.Run("a dead dir", func(t *testing.T) {
		fakeProc(t)
		c := hcTestCleaner(t, "/opt/claude")
		e := hcNewEntry(t, "E0", 454*24*time.Hour)
		if err := os.WriteFile(filepath.Join(e.dirPath, runDirLockName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		hcBackdate(t, e.dirPath, 454*24*time.Hour)
		oldDial := hcDial
		t.Cleanup(func() { hcDial = oldDial })
		hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
		var sum hcSummary
		c.tidyRunDirs([]runDirEntry{e}, &sum)
		if sum.runDirsRemoved != 1 {
			t.Errorf("a dead dir with a 0-byte lock was kept (removed %d)", sum.runDirsRemoved)
		}
	})
}

// TestTidyKeepsADirWhoseSocketStaysAfterTheRetire: after a retire, only a MISSING socket lets
// the dir go this pass. A stale socket left behind keeps it, with its own line.
// Not measured: the Linux run did not stage this rule.
func TestTidyKeepsADirWhoseSocketStaysAfterTheRetire(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	e := hcNewEntry(t, "N", 40*24*time.Hour)
	hcRetireFixture(t, proot, mk, link, e.socket)
	oldDial, oldRm := hcDial, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
	dials := 0
	hcDial = func(string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return newFakePeerConn(t), nil
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED} // a stale socket stays
	}
	removes := 0
	hcRemoveAll = func(*os.Root, string) error { removes++; return nil }
	buf := captureLogBuf(t)

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)
	if sum.daemonsRetired != 1 || removes != 0 {
		t.Errorf("retired=%d removes=%d, want 1 retired and the dir kept", sum.daemonsRetired, removes)
	}
	want := fmt.Sprintf("run dir %q kept: its socket is still there after its daemon was retired", e.dirPath)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

// TestTidyRetireLogUsesTheFreshAge: the retire line gives the age read just before the probe,
// not the one listed at the start of the pass. Not measured: the Linux run showed the same age
// either way.
func TestTidyRetireLogUsesTheFreshAge(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	e := hcNewEntry(t, "N", 40*24*time.Hour)
	e.idle = 454 * 24 * time.Hour // what the listing saw
	hcRetireFixture(t, proot, mk, link, e.socket)
	oldDial, oldRm := hcDial, hcRemoveAll
	t.Cleanup(func() { hcDial, hcRemoveAll = oldDial, oldRm })
	dials := 0
	hcDial = func(string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return newFakePeerConn(t), nil
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT}
	}
	hcRemoveAll = func(*os.Root, string) error { return nil }
	buf := captureLogBuf(t)
	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)
	if want := "has seen no connection for 40 days: SIGTERM"; !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

// ---- The staging name, and leftovers of both namings ----

func TestRemoveRunDirStagingName(t *testing.T) {
	fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	oldRen, oldRm := hcRename, hcRemoveAll
	t.Cleanup(func() { hcRename, hcRemoveAll = oldRen, oldRm })
	var to string
	hcRename = func(r *os.Root, from, dst string) error { to = dst; return r.Rename(from, dst) }
	e := hcNewEntry(t, "D2", 40*24*time.Hour)
	if !c.removeRunDir(e, false, hcSockMissing) {
		t.Fatal("removeRunDir failed")
	}
	re := regexp.MustCompile(`^D2\.removing-` + strconv.Itoa(c.selfPid) + `-[0-9a-z]+$`)
	if !re.MatchString(to) {
		t.Errorf("staging name = %q, want <name>.removing-<pid>-<base36>", to)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(e.dirPath), to)); !os.IsNotExist(err) {
		t.Errorf("the staged dir was not removed: %v", err)
	}
}

// TestTidyRemovesLeftoversOfBothNamings: a leftover staging dir is recognised by the marker
// anywhere in its name, so both claustrum's old ".removing-<name>-…" and the reference's
// "<name>.removing-<pid>-…" leftovers go, fresh as they are, without a second rename.
func TestTidyRemovesLeftoversOfBothNamings(t *testing.T) {
	fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	oldDial, oldRen := hcDial, hcRename
	t.Cleanup(func() { hcDial, hcRename = oldDial, oldRen })
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	renames := 0
	hcRename = func(*os.Root, string, string) error { renames++; return nil }
	runRoot, root := hcRunRoot(t)
	var entries []runDirEntry
	for _, n := range []string{".removing-L1-abc123", "L2.removing-999-abc123"} {
		e := hcEntry(t, runRoot, root, n, 0)
		if err := os.WriteFile(filepath.Join(e.dirPath, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	var sum hcSummary
	c.tidyRunDirs(entries, &sum)
	if sum.runDirsRemoved != 2 || renames != 0 {
		t.Errorf("removed=%d renames=%d, want both leftovers removed with no rename", sum.runDirsRemoved, renames)
	}
	for _, e := range entries {
		if _, err := os.Lstat(e.dirPath); !os.IsNotExist(err) {
			t.Errorf("leftover %q is still there: %v", e.name, err)
		}
	}
}

// ---- Rules the Linux run did not stage ----

// TestRemoveRunDirUndoesOnAnUnexaminableLock: after the rename, any lock state other than
// stale undoes the removal, an unexaminable one included.
// Not measured: the Linux run did not stage this rule.
func TestRemoveRunDirUndoesOnAnUnexaminableLock(t *testing.T) {
	fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	oldRen, oldRm := hcRename, hcRemoveAll
	t.Cleanup(func() { hcRename, hcRemoveAll = oldRen, oldRm })
	renames := 0
	hcRename = func(r *os.Root, from, to string) error {
		renames++
		if err := r.Rename(from, to); err != nil {
			return err
		}
		if renames == 1 { // a directory named daemon.lock: the lock cannot be examined
			return os.Mkdir(filepath.Join(r.Name(), to, runDirLockName), 0o755)
		}
		return nil
	}
	removes := 0
	hcRemoveAll = func(*os.Root, string) error { removes++; return nil }
	e := hcNewEntry(t, "x", 40*24*time.Hour)
	if c.removeRunDir(e, false, hcSockMissing) || removes != 0 || renames != 2 {
		t.Errorf("removes=%d renames=%d, want the removal undone", removes, renames)
	}
}

// TestRemoveRunDirActsInsideTheRoot: the rename and the remove go through the run root and
// take bare names inside it, never a path.
// Not measured: the Linux run did not stage this rule.
func TestRemoveRunDirActsInsideTheRoot(t *testing.T) {
	fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	oldRen, oldRm := hcRename, hcRemoveAll
	t.Cleanup(func() { hcRename, hcRemoveAll = oldRen, oldRm })
	var renamedFrom, renamedTo, removed string
	hcRename = func(r *os.Root, from, to string) error {
		renamedFrom, renamedTo = from, to
		return r.Rename(from, to)
	}
	hcRemoveAll = func(r *os.Root, name string) error {
		removed = name
		return r.RemoveAll(name)
	}
	e := hcNewEntry(t, "x", 40*24*time.Hour)
	if !c.removeRunDir(e, false, hcSockMissing) {
		t.Fatal("removeRunDir failed")
	}
	if renamedFrom != "x" || strings.ContainsRune(renamedTo, filepath.Separator) || removed != renamedTo {
		t.Errorf("rename %q -> %q, remove %q: want bare names inside the run root", renamedFrom, renamedTo, removed)
	}
	if _, err := os.Lstat(e.dirPath); !os.IsNotExist(err) {
		t.Errorf("the run dir is still there: %v", err)
	}
}

// TestEndDaemonsRechecksIdentity: a target whose pid now belongs to another process is not
// signalled. The judging loop can take seconds, and the pid can be reused in that span.
func TestEndDaemonsRechecksIdentity(t *testing.T) {
	root, mk, _ := fakeProc(t)
	single, _ := hcSeamSignals(t, root)
	mk(3200, "stat", hcRunningStat(3200, 1, 3200, "999")) // judged with start-ticks 1, now 999
	var sum hcSummary
	(&hostCleaner{}).endDaemons([]daemonTarget{{tracked: tracked{pid: 3200, pgid: 3200, startTicks: "1"}, socket: "/r/run/X/rpc.sock"}}, &sum)
	if len(*single) != 0 {
		t.Errorf("SIGKILL sent to a reused pid: %v", *single)
	}
	if sum.strandedSignalled != 0 {
		t.Errorf("strandedSignalled = %d, want 0", sum.strandedSignalled)
	}
}

// TestHasOpenNamedDeletedLock pins that on linux a lock whose file was deleted does not count
// as open by name (the kernel shows it as "<path> (deleted)"). The reference side is not
// measured. This test makes a change to that choice a deliberate one.
func TestHasOpenNamedDeletedLock(t *testing.T) {
	_, _, link := fakeProc(t)
	link(3300, "fd/3", "/opt/claude/run/x/daemon.lock (deleted)")
	if open, canRead := hcHasOpenNamed(3300, runDirLockName); open || !canRead {
		t.Errorf("deleted lock: open=%v canRead=%v, want false true", open, canRead)
	}
	link(3301, "fd/3", "/opt/claude/run/x/daemon.lock")
	if open, _ := hcHasOpenNamed(3301, runDirLockName); !open {
		t.Error("a live lock did not count as open by name")
	}
}

// TestRetireAbandonedRefusesParentAndForeignUID: the retire refuses this daemon's parent and
// a peer of another uid before anything else, with no reason.
// Not measured: the Linux run did not stage this rule.
func TestRetireAbandonedRefusesParentAndForeignUID(t *testing.T) {
	proot, mk, link := fakeProc(t)
	single, _ := hcSeamSignals(t, proot)
	oldUID, oldPpid := hcGetuid, hcGetppid
	t.Cleanup(func() { hcGetuid, hcGetppid = oldUID, oldPpid })
	hcGetuid = func() int { return 1000 }
	socket := "/opt/claude/run/x/rpc.sock"
	const pid = 4500
	hcFakeDaemon(t, proot, mk, link, pid, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	link(pid, "fd/1", "/dev/null")
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	e := runDirEntry{name: "x", dirPath: "/opt/claude/run/x", socket: socket, idle: 40 * 24 * time.Hour}

	hcGetppid = func() int { return pid }
	if ok, reason := c.retireAbandoned(e, pid, 1000); ok || reason != "" {
		t.Errorf("parent pid: ok=%v reason=%q, want a silent refusal", ok, reason)
	}
	hcGetppid = func() int { return 1 }
	if ok, reason := c.retireAbandoned(e, pid, 4242); ok || reason != "" {
		t.Errorf("foreign uid: ok=%v reason=%q, want a silent refusal", ok, reason)
	}
	if len(*single) != 0 {
		t.Errorf("signals = %v, want none", *single)
	}
	// Control: the same daemon with our uid and another parent is retired.
	if ok, reason := c.retireAbandoned(e, pid, 1000); !ok {
		t.Errorf("control: not retired (%q)", reason)
	}
}

// TestRetireAbandonedReasonForAYoungDaemon: a daemon younger than hcMinAge is not retired, and
// the reason is the line the macOS run captured from the reference.
func TestRetireAbandonedReasonForAYoungDaemon(t *testing.T) {
	proot, mk, link := fakeProc(t)
	single, _ := hcSeamSignals(t, proot)
	oldUID, oldPpid := hcGetuid, hcGetppid
	t.Cleanup(func() { hcGetuid, hcGetppid = oldUID, oldPpid })
	hcGetuid = func() int { return 1000 }
	hcGetppid = func() int { return 1 }
	socket := "/opt/claude/run/y/rpc.sock"
	const pid = 4501
	hcFakeDaemon(t, proot, mk, link, pid, "/opt/claude/srv/a/server",
		// The fake uptime is 100000 s and a tick is 10 ms, so 9994000 ticks is 60 s old.
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "9994000")
	link(pid, "fd/1", "/dev/null")
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	e := runDirEntry{name: "y", dirPath: "/opt/claude/run/y", socket: socket, idle: 40 * 24 * time.Hour}
	if ok, reason := c.retireAbandoned(e, pid, 1000); ok || reason != "it is too young to judge" {
		t.Errorf("young daemon: ok=%v reason=%q, want the too-young reason", ok, reason)
	}
	if len(*single) != 0 {
		t.Errorf("signals = %v, want none", *single)
	}
}

// ---- Orphan groups ----

// TestPassEndsOnlyTheMeasuredOrphanGroups drives the four groups of the macOS VM orphan
// fixture (issue 429). Only the ppid-1 group with pipe stdio on the flat ccd-cli/<version>
// binary is ended. A live parent, non-pipe stdio, and the nested layout are all spared.
func TestPassEndsOnlyTheMeasuredOrphanGroups(t *testing.T) {
	f := hcNewPassFixture(t)
	flat := filepath.Join(f.base, "ccd-cli", "2.1.0")
	nested := filepath.Join(f.base, "ccd-cli", "1.0", "claude")
	argv := []string{"claude", "-c", "sleep 100000; :", "stream-json"}
	self := os.Getpid()
	ended, liveParent, notPipes, nestedPid := self+11, self+12, self+13, self+14
	for _, p := range []int{ended, liveParent, notPipes} {
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, p, flat, argv, true, "1", f.uid)
	}
	hcFakeDaemonUID(t, f.proot, f.mk, f.link, nestedPid, nested, argv, true, "1", f.uid)
	hcPipeStdio(f.link, ended)
	hcPipeStdio(f.link, liveParent)
	hcPipeStdio(f.link, nestedPid)
	f.mk(liveParent, "stat", hcRunningStat(liveParent, 4242, liveParent, "1")) // a live python parent
	f.link(notPipes, "fd/0", "pipe:[1]")
	f.link(notPipes, "fd/1", "/dev/null")
	f.link(notPipes, "fd/2", "/dev/null")
	buf := captureLogBuf(t)

	sum := f.c.Pass()
	if len(f.group) != 1 || f.group[0] != ended {
		t.Errorf("group signals = %v, want only [%d]", f.group, ended)
	}
	if sum.undecided != 0 {
		t.Errorf("undecided = %d, want 0: the spared groups are spared silently", sum.undecided)
	}
	want := fmt.Sprintf("[hostclean] ending orphaned Claude Code process group %d (its daemon is gone): SIGTERM", ended)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

// ---- Log lines captured on the Linux VM (issue 429) ----

// TestPassOwnSocketLines pins the pass-starting line and the own-socket line for a socket that
// another process answers. Both were captured on the VM. The line for a socket that does not
// lead anywhere is claustrum's own (origin/main wording); it is asserted separately below.
func TestPassOwnSocketLines(t *testing.T) {
	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}}, ownSocket: "/opt/claude/run/self/rpc.sock", selfPid: 1}
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })

	buf := captureLogBuf(t)
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil }
	if sum := c.Pass(); sum != (hcSummary{}) {
		t.Errorf("summary = %+v, want zero", sum)
	}
	got := buf.String()
	for _, want := range []string{
		"[hostclean] pass starting (the next connection logged is this pass probing its own socket)\n",
		fmt.Sprintf(`[hostclean] own socket "/opt/claude/run/self/rpc.sock" is answered by pid %d, not this daemon; skipping this pass`, os.Getpid()),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want %q", got, want)
		}
	}

	// claustrum's own wording, not a captured text: a socket that is missing or cannot be
	// probed also stops the pass.
	buf = captureLogBuf(t)
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	if sum := c.Pass(); sum != (hcSummary{}) {
		t.Errorf("summary = %+v, want zero", sum)
	}
	if want := `own socket "/opt/claude/run/self/rpc.sock" does not lead to this daemon (state 1, pid 0); skipping this pass`; !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

func TestEndDaemonsMeasuredLine(t *testing.T) {
	root, mk, _ := fakeProc(t)
	hcSeamSignals(t, root)
	mk(3100, "stat", hcRunningStat(3100, 1, 3100, "1"))
	buf := captureLogBuf(t)
	var sum hcSummary
	(&hostCleaner{}).endDaemons([]daemonTarget{{tracked: tracked{pid: 3100, pgid: 3100, startTicks: "1"}, socket: "/r/run/X/rpc.sock"}}, &sum)
	want := `[hostclean] ending stranded daemon pid 3100: it was started for "/r/run/X/rpc.sock", which no longer leads to it, and it holds no run-dir lock: SIGKILL`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}
