//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The error arms of the linux host-cleaner primitives (hostclean_linux.go). Each test here
// picks a fixture that a DELETED guard would answer differently — a /proc read is trivial to
// make fail, but a test that only proves "a missing file reads not-ok" usually passes against
// the mutant too, because the parse below the guard fails on the same empty buffer. The two
// exceptions are grouped in TestHcPrimitivesOnAVanishedProcess and labelled as such.

// hcNSLinks gives pid the namespace links hcSameNamespaces compares, and gives /proc/self the
// matching ones, so a test can make the namespace read succeed and isolate a different arm.
func hcNSLinks(t *testing.T, root string, pid int) {
	t.Helper()
	for _, ns := range hcNamespaces {
		for _, dir := range []string{filepath.Join(root, "self", "ns"), filepath.Join(root, strconv.Itoa(pid), "ns")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(ns+":[4026531836]", filepath.Join(dir, ns)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// hcRunningStat is a /proc/<pid>/stat line for a running process: state R, the given ppid and
// pgid, and startTicks in field 22.
func hcRunningStat(pid, ppid, pgid int, startTicks string) string {
	return fmt.Sprintf("%d (server) R %d %d %s%s x", pid, ppid, pgid, strings.Repeat("0 ", 16), startTicks)
}

func TestHcReadStatUnparseableFields(t *testing.T) {
	_, mk, _ := fakeProc(t)

	// No ')' at all. The fixture's fields parse cleanly FROM INDEX 0 (state R, ppid 1,
	// pgid 42, 20 fields), so dropping the guard would return a populated, ok stat for a
	// line the kernel never writes.
	mk(60, "stat", "R 1 42 "+strings.Repeat("0 ", 16)+"9000 x")
	if s := hcReadStat(60); s.ok {
		t.Errorf("stat with no ')' parsed as ok: %+v", s)
	}
	// Non-numeric ppid. Atoi returns 0 on failure, so without the error check this reads as
	// a running process parented to pid 0.
	mk(61, "stat", "61 (server) R notanumber 61 "+strings.Repeat("0 ", 16)+"9000 x")
	if s := hcReadStat(61); s.ok {
		t.Errorf("stat with a non-numeric ppid parsed as ok: %+v", s)
	}
	// Non-numeric pgid, the other half of the same check.
	mk(62, "stat", "62 (server) R 1 notanumber "+strings.Repeat("0 ", 16)+"9000 x")
	if s := hcReadStat(62); s.ok {
		t.Errorf("stat with a non-numeric pgid parsed as ok: %+v", s)
	}
}

func TestHcUptimeEmptyFile(t *testing.T) {
	root, _, _ := fakeProc(t)
	// An empty /proc/uptime. Without the len(f)==0 guard, f[0] panics.
	if err := os.WriteFile(filepath.Join(root, "uptime"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if up, ok := hcUptime(); ok {
		t.Errorf("hcUptime on an empty file = %v ok=true, want not-ok", up)
	}
}

func TestHcReadUIDFieldlessLine(t *testing.T) {
	_, mk, _ := fakeProc(t)
	// "Uid:" with nothing after it. Without the len(f)==0 guard, f[0] panics.
	mk(63, "status", "Name:\tx\nUid:\n")
	if uid, ok := hcReadUID(63); ok {
		t.Errorf("hcReadUID on a fieldless Uid line = %d ok=true, want not-ok", uid)
	}
}

func TestHcSameNamespacesUnreadable(t *testing.T) {
	_, mk, _ := fakeProc(t)
	mk(64, "stat", hcRunningStat(64, 1, 64, "9000")) // the pid exists; only ns/ is absent
	// Neither /proc/self/ns nor /proc/64/ns exists. Both readlinks fail and return "", so
	// without the error check the two empty strings compare EQUAL and the pid is reported as
	// sharing our namespaces.
	same, readable := hcSameNamespaces(64)
	if readable {
		t.Errorf("hcSameNamespaces with no ns links: same=%v readable=true, want readable=false", same)
	}
	if same {
		t.Error("hcSameNamespaces reported a match it could not read")
	}
}

func TestInspectErrArms(t *testing.T) {
	root, mk, _ := fakeProc(t)

	// Uid unreadable. The namespace links are present and matching, so hcInspectErr can only
	// come from the missing status file: with that arm gone, inspect returns OK.
	mk(65, "stat", hcRunningStat(65, 1, 65, "9000"))
	hcNSLinks(t, root, 65)
	if tr, res := inspect(65, false); res != hcInspectErr {
		t.Errorf("inspect with no status = %d (%+v), want hcInspectErr", res, tr)
	}

	// Namespaces unreadable. Stat and status both read, so hcInspectErr can only come from
	// the missing ns links.
	mk(66, "stat", hcRunningStat(66, 1, 66, "9000"))
	mk(66, "status", fmt.Sprintf("Name:\tx\nUid:\t%d\t%d\t%d\t%d\n", hcGetuid(), hcGetuid(), hcGetuid(), hcGetuid()))
	if tr, res := inspect(66, false); res != hcInspectErr {
		t.Errorf("inspect with no ns links = %d (%+v), want hcInspectErr", res, tr)
	}
}

// TestInspectPidReuseMidRead is the linux twin of TestInspectDarwinPidReuseMidRead: a pid that
// exits and is reused while inspect is reading it must be reported gone, not acted on with a
// mixture of the two processes' facts.
//
// Linux's inspect reads /proc directly rather than through a per-read seam, so the fixture
// uses the one seam the function calls BETWEEN its two stat reads: hcClock, which the age
// calculation invokes after the first read and before the re-read. The stub rewrites the stat
// file there, which is exactly the kernel's behaviour under pid reuse. If a later edit moves
// that hcClock call above the first stat read, both reads see the rewritten file and this test
// fails rather than silently stopping covering the arm.
func TestInspectPidReuseMidRead(t *testing.T) {
	root, mk, _ := fakeProc(t)
	const pid = 71

	mk(pid, "stat", hcRunningStat(pid, 1, pid, "9000"))
	mk(pid, "status", fmt.Sprintf("Name:\tx\nUid:\t%d\t%d\t%d\t%d\n", hcGetuid(), hcGetuid(), hcGetuid(), hcGetuid()))
	hcNSLinks(t, root, pid)
	// An uptime is what makes inspect take the age branch, and with it the hcClock call.
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("100.5 50.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	rewrote := false
	hcClock = func() time.Time {
		if !rewrote { // a different process now owns the pid: same number, new start-ticks
			rewrote = true
			mk(pid, "stat", hcRunningStat(pid, 1, pid, "9999"))
		}
		return oldClock()
	}

	tr, res := inspect(pid, false)
	if !rewrote {
		t.Fatal("inspect never called hcClock, so the pid was not reused mid-read; the fixture no longer works")
	}
	if res != hcInspectGone {
		t.Errorf("inspect of a pid reused mid-read = %d (%+v), want hcInspectGone", res, tr)
	}
}

func TestHcSnapshotSkipsNonPidEntries(t *testing.T) {
	root, mk, _ := fakeProc(t)
	mk(67, "stat", hcRunningStat(67, 1, 67, "9000")) // a real pid, so the walk has work to do
	// "self" is not a number and "0" is not a valid pid. A dropped guard would turn BOTH into
	// pid 0: Atoi("self") returns 0 on error, and the "0" directory stats fine and is ours.
	for _, name := range []string{"self", "0"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A pid that exits between the ReadDir and the Stat leaves a numeric name that no longer
	// resolves. Without the stat error check the nil FileInfo is dereferenced on the next line.
	if err := os.Symlink(filepath.Join(root, "exited"), filepath.Join(root, "999")); err != nil {
		t.Fatal(err)
	}

	pids, overflow, ok := hcSnapshot()
	if !ok || overflow {
		t.Fatalf("hcSnapshot ok=%v overflow=%v, want ok with no overflow", ok, overflow)
	}
	for _, p := range pids {
		if p <= 0 {
			t.Errorf("hcSnapshot returned pid %d from a non-pid /proc entry (%v)", p, pids)
		}
		if p == 999 {
			t.Errorf("hcSnapshot returned pid 999, which no longer exists (%v)", pids)
		}
	}
}

func TestHcSnapshotUnreadableProc(t *testing.T) {
	root, _, _ := fakeProc(t)
	procRoot = filepath.Join(root, "does-not-exist")
	pids, overflow, ok := hcSnapshot()
	if ok {
		t.Errorf("hcSnapshot on an unreadable /proc = %v overflow=%v ok=true, want not-ok", pids, overflow)
	}
}

func TestHcSelfExeUnreadable(t *testing.T) {
	fakeProc(t) // a bare tree: no self/exe link
	if exe, ok := hcSelfExe(); ok {
		t.Errorf("hcSelfExe with no self/exe = %q ok=true, want not-ok", exe)
	}
}

func TestPeerOfErrorArms(t *testing.T) {
	// A zero-value UnixConn has no descriptor, so SyscallConn returns EINVAL and a nil
	// RawConn. Without that error check the next line dereferences nil.
	if pid, uid, ok := peerOf(&net.UnixConn{}); ok {
		t.Errorf("peerOf on a zero conn = pid %d uid %d ok=true, want not-ok", pid, uid)
	}
	// A closed connection: the fd is gone, so raw.Control fails. Without the check the nil
	// ucred is dereferenced.
	conn := newFakePeerConn(t).(*net.UnixConn)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if pid, uid, ok := peerOf(conn); ok {
		t.Errorf("peerOf on a closed conn = pid %d uid %d ok=true, want not-ok", pid, uid)
	}
}

// hcFakeFileInfo is an os.FileInfo whose Sys() the test controls, which is the only way to
// reach lockFileHeld's two device arms: a real stat always yields a *syscall.Stat_t, and the
// anonymous-filesystem case needs st_dev major 0, which a temp file does not have.
type hcFakeFileInfo struct{ sys any }

func (f hcFakeFileInfo) Name() string       { return "daemon.lock" }
func (f hcFakeFileInfo) Size() int64        { return 0 }
func (f hcFakeFileInfo) Mode() fs.FileMode  { return 0o600 }
func (f hcFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f hcFakeFileInfo) IsDir() bool        { return false }
func (f hcFakeFileInfo) Sys() any           { return f.sys }

func TestLockFileHeldDeviceArms(t *testing.T) {
	root, _, _ := fakeProc(t)

	// /proc/locks is written FIRST, before any assertion. The read of it is the guard
	// immediately after the type assertion, so with no locks file to read every call below
	// returns false at that second guard and proves nothing about the first one.
	const ino = 12345
	locks := "1: POSIX ADVISORY WRITE 4321 00:01:" + strconv.Itoa(ino) + " 0 EOF\n"
	if err := os.WriteFile(filepath.Join(root, "locks"), []byte(locks), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sys() is not a *syscall.Stat_t: without the type-assertion guard the nil st is
	// dereferenced when the device is encoded below the locks read.
	if lockFileHeld(hcFakeFileInfo{sys: nil}) {
		t.Error("lockFileHeld reported held for a FileInfo with no stat")
	}

	// An anonymous filesystem reports st_dev major 0, and /proc/locks then names the file by a
	// device the encoding cannot reproduce (00:01 here against the computed 00:00). Only the
	// inode-suffix fallback can match it.
	anon := hcFakeFileInfo{sys: &syscall.Stat_t{Dev: 0, Ino: ino}}
	if !lockFileHeld(anon) {
		t.Error("lockFileHeld missed the major-0 anonymous-filesystem lock line")
	}
	// The fallback is inode-exact, not a substring: a different inode with the same suffix
	// shape must not match.
	other := hcFakeFileInfo{sys: &syscall.Stat_t{Dev: 0, Ino: ino + 1}}
	if lockFileHeld(other) {
		t.Error("lockFileHeld matched a lock line for a different inode")
	}
}

// TestHcPrimitivesOnAVanishedProcess pins the contract for a pid that disappeared between the
// snapshot and the read. These arms are defensive: deleting them leaves the behaviour
// unchanged (the parse below simply finds nothing in the empty buffer), so unlike every other
// test in this file these assertions do NOT discriminate against a mutant. They are here for
// the contract, which the cleaner's callers rely on — a gone process is never busy, never has
// a file open and never looks like a daemon-spawned child.
func TestHcPrimitivesOnAVanishedProcess(t *testing.T) {
	_, mk, link := fakeProc(t)
	const gone = 68

	if hcStdioArePipes(gone) {
		t.Error("hcStdioArePipes reported pipes for a pid with no fd directory")
	}
	if hcHasFileOpen(gone, "/opt/claude/run/x/rpc.sock") {
		t.Error("hcHasFileOpen reported an open file for a pid with no fd directory")
	}
	if hcBusy(gone) {
		t.Error("hcBusy reported busy for a pid with no fd directory")
	}
	if uid, ok := hcReadUID(gone); ok {
		t.Errorf("hcReadUID for a pid with no status = %d ok=true, want not-ok", uid)
	}

	// A live process holding only pipes has no socket inode to look up, so it is not busy.
	link(69, "fd/0", "pipe:[111]")
	link(69, "fd/1", "pipe:[112]")
	if hcBusy(69) {
		t.Error("hcBusy reported busy for a process with no socket fds")
	}
	// A socket fd but no /proc/<pid>/net/unix to resolve it against.
	link(70, "fd/4", "socket:[555]")
	if hcBusy(70) {
		t.Error("hcBusy reported busy with no net/unix to read")
	}
	mk(70, "stat", hcRunningStat(70, 1, 70, "9000")) // keep the fixture a plausible process

	// lockFileHeld with no /proc/locks to read: a lock nobody can see is not held.
	lockPath := filepath.Join(t.TempDir(), runDirLockName)
	if err := os.WriteFile(lockPath, []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lockFileHeld(fi) {
		t.Error("lockFileHeld reported held with no /proc/locks to read")
	}
}
