//go:build linux

package leftsweep

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBase is the base of the fake pids of the fake /proc tree. It is above the
// pid limit of Linux, so no fake pid names a real process.
const fakeBase = 1 << 30

// fakeSelf and fakeParent stand for the pid of the daemon and of its parent in
// the fake tree.
const (
	fakeSelf   = fakeBase + 1000
	fakeParent = fakeBase + 1001
)

// fakeProc is a fake /proc tree with fake signals. A send records the pid and
// removes its entry, as if the process ended.
type fakeProc struct {
	t      *testing.T
	dir    string
	pass   int
	opens  map[int][]int // pass -> pids opened
	ended  []int
	pauses []time.Duration
	onWait func(pass int)
}

// newFakeProc points the seams of Run at a new fake tree for the rest of the test.
func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	f := &fakeProc{t: t, dir: t.TempDir(), pass: 1, opens: map[int][]int{}}
	oldProc, oldHold, oldWait, oldUID, oldPID, oldPPID := procDir, holdPID, waitPass, ownEUID, ownPID, ownPPID
	t.Cleanup(func() {
		procDir, holdPID, waitPass, ownEUID, ownPID, ownPPID = oldProc, oldHold, oldWait, oldUID, oldPID, oldPPID
	})
	procDir = func() string { return f.dir }
	ownPID = func() int { return fakeSelf }
	ownPPID = func() int { return fakeParent }
	if err := os.Symlink(strconv.Itoa(fakeSelf), filepath.Join(f.dir, "self")); err != nil {
		t.Fatal(err)
	}
	holdPID = func(pid int) (func(syscall.Signal) error, func(), error) {
		if pid < fakeBase {
			t.Errorf("hold of pid %d, which is no fake pid", pid)
			return nil, nil, syscall.EPERM
		}
		f.opens[f.pass] = append(f.opens[f.pass], pid)
		send := func(sig syscall.Signal) error {
			if sig != syscall.SIGKILL {
				t.Errorf("signal %v to pid %d, want SIGKILL", sig, pid)
			}
			f.ended = append(f.ended, pid)
			return os.RemoveAll(filepath.Join(f.dir, strconv.Itoa(pid)))
		}
		return send, func() {}, nil
	}
	waitPass = func(d time.Duration) {
		f.pauses = append(f.pauses, d)
		f.pass++
		if f.onWait != nil {
			f.onWait(f.pass)
		}
	}
	return f
}

// add makes the entry of pid with the environment env and a root link to root.
func (f *fakeProc) add(pid int, env []string, root string) {
	f.t.Helper()
	d := filepath.Join(f.dir, strconv.Itoa(pid))
	if err := os.Mkdir(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(d, "root")); err != nil {
		f.t.Fatal(err)
	}
}

func sortedInts(s []int) []int {
	out := append([]int(nil), s...)
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mkdirAll makes the folder p.
func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The cases of row K-1 of 13047a51 (Linux, run m25c-ubuntu2-20261128T060259Z) on a
// fake /proc tree. A, E and G end by one SIGKILL each. B, C and F stay. Two more
// cases are claustrum's own: a run folder value that differs, and a child mark with
// another value, stay alive. The daemon itself is passed over, as on 13047a51
// (row K-1, strace). The parent skip is claustrum's own.
func TestRunEndsTheMarkedCasesOfRowK1(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	// The run folder of a daemon holds a children entry. So only the identity
	// test lets A end.
	mkdirAll(t, filepath.Join(runDir, childrenEntry))
	run := runDirMark + runDir
	empty := t.TempDir()
	withChildren := t.TempDir()
	mkdirAll(t, filepath.Join(withChildren, runDir, childrenEntry))
	without := t.TempDir()
	mkdirAll(t, filepath.Join(without, runDir))

	const (
		pA = fakeBase + iota
		pB
		pC
		pE
		pF
		pG
		pValue
		pChild2
	)
	f.add(pA, []string{"HOME=/h", run, childMark}, "/")
	f.add(pB, []string{run}, "/")
	f.add(pC, []string{childMark}, "/")
	f.add(pE, []string{run, childMark}, empty)
	f.add(pF, []string{run, childMark}, withChildren)
	f.add(pG, []string{run, childMark}, without)
	f.add(pValue, []string{run + "x", childMark}, "/")
	f.add(pChild2, []string{run, "CLAUDE_SSH_DAEMON_CHILD=12"}, "/")
	f.add(fakeSelf, []string{run, childMark}, "/")
	f.add(fakeParent, []string{run, childMark}, "/")

	n, err := Run(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{pA, pE, pG}; n != 3 || !equalInts(sortedInts(f.ended), want) {
		t.Fatalf("Run = %d, ended %v, want 3 and %v", n, f.ended, want)
	}
	// The pass that ended three processes is followed by one more pass, which
	// ends none, and then the sweep stops (row K-1).
	if len(f.pauses) != 1 || f.pauses[0] != passPause {
		t.Fatalf("pauses = %v, want one of %v", f.pauses, passPause)
	}
	if want := []int{pB, pC, pF, pValue, pChild2}; !equalInts(sortedInts(f.opens[2]), want) {
		t.Fatalf("pass 2 opened %v, want %v", f.opens[2], want)
	}
	for _, p := range f.opens[1] {
		if p == fakeSelf || p == fakeParent {
			t.Fatalf("pid %d of the daemon or its parent was held", p)
		}
	}
}

// A process of another uid stays alive (row K-1 D). The fake tree belongs to the
// test user, so a different own uid stands for it.
func TestRunKeepsAProcessOfAnotherUID(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	f.add(fakeBase, []string{runDirMark + runDir, childMark}, "/")
	ownEUID = func() int { return os.Geteuid() + 1 }
	if n, err := Run(runDir); n != 0 || err != nil || len(f.ended) != 0 {
		t.Fatalf("Run = %d, %v, ended %v, want 0, nil and none", n, err, f.ended)
	}
}

// Each pass that ends a process is followed by a pause and one more pass, up to
// three passes. The bound is claustrum's own.
func TestRunStopsAfterMaxPasses(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	marks := []string{runDirMark + runDir, childMark}
	f.add(fakeBase, marks, "/")
	f.onWait = func(pass int) { f.add(fakeBase+pass, marks, "/") }
	n, err := Run(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(f.pauses) != 2 {
		t.Fatalf("Run = %d with %d pauses, want 3 and 2", n, len(f.pauses))
	}
}

// No run folder: nothing is held and nothing is sent.
func TestRunSendsNothingWithoutTheRunFolder(t *testing.T) {
	f := newFakeProc(t)
	runDir := filepath.Join(t.TempDir(), "gone")
	f.add(fakeBase, []string{runDirMark + runDir, childMark}, "/")
	if n, err := Run(runDir); n != 0 || !errors.Is(err, os.ErrNotExist) || len(f.opens) != 0 {
		t.Fatalf("Run = %d, %v, opens %v, want 0, not-exist and none", n, err, f.opens)
	}
}

// A failed open of the pid file descriptor sends nothing, ENOSYS too.
func TestRunSendsNothingWhenTheHoldFails(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	f.add(fakeBase, []string{runDirMark + runDir, childMark}, "/")
	holdPID = func(int) (func(syscall.Signal) error, func(), error) { return nil, nil, syscall.ENOSYS }
	if n, err := Run(runDir); n != 0 || err != nil || len(f.ended) != 0 {
		t.Fatalf("Run = %d, %v, ended %v, want 0, nil and none", n, err, f.ended)
	}
}

// The narrowing cases of claustrum's own, none measured. Each process has both
// marks and stays alive: a mark inside a longer entry, a link, a dangling link or
// a file at the run path, a loop or a closed folder above it, a dangling children
// link in another folder, an environment over the cap, and pid 1.
func TestRunKeepsTheNarrowedCases(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	mkdirAll(t, filepath.Join(runDir, childrenEntry))
	run := runDirMark + runDir
	marks := []string{run, childMark}
	parent := filepath.Dir(runDir)

	// root makes a fake root and calls build on the run path inside it.
	root := func(build func(at string)) string {
		r := t.TempDir()
		mkdirAll(t, filepath.Join(r, filepath.Dir(parent)))
		build(filepath.Join(r, runDir))
		return r
	}
	link := func(target string) func(string) {
		return func(at string) {
			mkdirAll(t, filepath.Dir(at))
			if err := os.Symlink(target, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	const (
		pInner = fakeBase + iota
		pSuffix
		pLinkOwn
		pLinkGone
		pFile
		pLoop
		pClosed
		pChildLink
		pBig
	)
	f.add(pInner, []string{run, "X=" + childMark}, "/")
	f.add(pSuffix, []string{"Y=" + run, childMark}, "/")
	f.add(pLinkOwn, marks, root(link(runDir)))
	f.add(pLinkGone, marks, root(link("/no/such/folder")))
	f.add(pFile, marks, root(func(at string) {
		mkdirAll(t, filepath.Dir(at))
		if err := os.WriteFile(at, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	// A link that names itself in place of the parent of the run path: ELOOP.
	f.add(pLoop, marks, root(func(at string) {
		if err := os.Symlink(filepath.Base(parent), filepath.Dir(at)); err != nil {
			t.Fatal(err)
		}
	}))
	closed := ""
	if os.Geteuid() != 0 {
		// A parent of mode 0 in place of the parent of the run path: EACCES.
		closed = root(func(at string) {
			mkdirAll(t, filepath.Dir(at))
			if err := os.Chmod(filepath.Dir(at), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(at), 0o755) })
		})
		f.add(pClosed, marks, closed)
	}
	f.add(pChildLink, marks, root(func(at string) {
		mkdirAll(t, at)
		if err := os.Symlink("/no/such/children", filepath.Join(at, childrenEntry)); err != nil {
			t.Fatal(err)
		}
	}))
	f.add(pBig, []string{run, childMark, strings.Repeat("z", maxEnviron)}, "/")
	if err := os.Mkdir(filepath.Join(f.dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "1", "environ"), []byte(run+"\x00"+childMark+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(f.dir, "1", "root")); err != nil {
		t.Fatal(err)
	}

	if n, err := Run(runDir); n != 0 || err != nil || len(f.ended) != 0 {
		t.Fatalf("Run = %d, %v, ended %v, want 0, nil and none", n, err, f.ended)
	}
}

// A send that fails is not counted, and no second pass follows.
func TestRunCountsNoFailedSend(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	f.add(fakeBase, []string{runDirMark + runDir, childMark}, "/")
	holdPID = func(int) (func(syscall.Signal) error, func(), error) {
		return func(syscall.Signal) error { return syscall.ESRCH }, func() {}, nil
	}
	if n, err := Run(runDir); n != 0 || err != nil || len(f.pauses) != 0 {
		t.Fatalf("Run = %d, %v with %d pauses, want 0, nil and none", n, err, len(f.pauses))
	}
}

// A /proc whose self is another process, a relative run folder and an unclean one
// each refuse the whole sweep before any hold.
func TestRunRefusesBeforeAnyHold(t *testing.T) {
	f := newFakeProc(t)
	runDir := t.TempDir()
	f.add(fakeBase, []string{runDirMark + runDir, childMark}, "/")
	for _, dir := range []string{"rel/run", runDir + "/../" + filepath.Base(runDir), runDir + "/"} {
		if n, err := Run(dir); n != 0 || err == nil {
			t.Errorf("Run(%q) = %d, %v, want 0 and an error", dir, n, err)
		}
	}
	ownPID = func() int { return fakeSelf + 7 }
	if n, err := Run(runDir); n != 0 || err == nil {
		t.Errorf("Run with another /proc/self = %d, %v, want 0 and an error", n, err)
	}
	if len(f.opens) != 0 {
		t.Fatalf("opens = %v, want none", f.opens)
	}
}
