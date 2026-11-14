package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// runTempRootBound runs the step for a temp dir x and binds its end at once, as a
// spawn does after the start. A nil end stands for a spawn with no cliTempRootEnd.
func runTempRootBound(t *testing.T, x, runDir string, releases *[]func(), end func()) *rpc.CLITempRoot {
	t.Helper()
	_, res, release, bind := privateTempRoot([]string{"TMPDIR=" + x}, runDir)
	*releases = append(*releases, release)
	bind(end)
	return res
}

// waitFor polls cond until it holds, with a limit.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within 5 s", what)
		}
	}
}

// holdsFor checks cond again and again for d, and fails the test once it does not
// hold.
func holdsFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if !cond() {
			t.Fatalf("%s broke", what)
		}
	}
}

// heldRecordOf gives the held record of the folder name in x.
func heldRecordOf(x, name string) *heldTempRoot {
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	return tempRoots.held[filepath.Join(x, name)]
}

// wantFolderShape fails the test unless x/name is a folder of mode 0500 that holds t
// of mode 0700.
func wantFolderShape(t *testing.T, x, name string) {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(x, name))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o500 {
		t.Errorf("folder %s: %v %v, want mode 0500", name, fi, err)
	}
	fi, err = os.Lstat(filepath.Join(x, name, "t"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("folder %s/t: %v %v, want mode 0700", name, fi, err)
	}
}

// A link at the name counts as lost: the spawn that asked is ended, the other gets one
// run-on line, and the next reply with a member names a new folder and lists the lost
// one once (4c111e86, measured on Linux and macOS).
func TestCLITempRootLinkAtTheNameIsLost(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, runDir := tempRootDir(t, true)
	logs := captureLogBuf(t)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	if b := runTempRootBound(t, x, runDir, &rel, nil); b.Name != a.Name {
		t.Fatalf("second spawn %+v, want the folder %s", b, a.Name)
	}
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f+".old", f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "end", func() bool { return ends.Load() == 1 })
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] Claude Code's private temp folder "+a.Name+" counts as lost: it is a link",
		"[process.Manager] 1 process(es) run on in "+a.Name+", which may be another account's: their starts did not ask to be ended over it (cliTempRootEnd); end those sessions")
	c := runTempRootBound(t, x, runDir, &rel, nil)
	b, _ := json.Marshal(c)
	if c == nil || c.Name == a.Name || !tempRootNameRE.MatchString(c.Name) {
		t.Fatalf("answer after the loss %s, want a new folder", b)
	}
	ok := false
	for ago := range 3 { // a slow host can take a second or two to the next spawn
		ok = ok || string(b) == `{"refused":"not readable by its owner","name":"`+c.Name+`","lost":[{"name":"`+a.Name+`","why":"changed","agoSeconds":`+strconv.Itoa(ago)+`}]}`
	}
	if !ok {
		t.Errorf("answer %s, want the new name and the lost %s with agoSeconds 0 to 2", b, a.Name)
	}
	if d := runTempRootBound(t, x, runDir, &rel, nil); d == nil || d.Gone != nil {
		t.Errorf("the next answer %+v, want no lost list", d)
	}
	holdsFor(t, 5*tempRootCheckEvery, "one end", func() bool { return ends.Load() == 1 })
	if fi, err := os.Lstat(f); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link: %v %v, want it to stay", fi, err)
	}
}

// A folder that others can write, or that another account owns, counts as lost
// (4c111e86, measured on Linux and macOS for mode 0777 and uid 0). The owner case
// gives the held record another uid, as root cannot be the test.
func TestCLITempRootOpenOrForeignFolderIsLost(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	if err := os.Chmod(filepath.Join(x, a.Name), 0o777); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "end", func() bool { return ends.Load() == 1 })
	wantLinesInOrder(t, logs.String(), "[process.Manager] Claude Code's private temp folder "+a.Name+" counts as lost: it is open to other accounts")
	x2, runDir2 := tempRootDir(t, true)
	b := runTempRootBound(t, x2, runDir2, &rel, func() { ends.Add(1) })
	tempRoots.mu.Lock()
	tempRoots.held[filepath.Join(x2, b.Name)].uid = os.Getuid() + 1
	tempRoots.mu.Unlock()
	waitFor(t, "end", func() bool { return ends.Load() == 2 })
	wantLinesInOrder(t, logs.String(), "[process.Manager] Claude Code's private temp folder "+b.Name+" counts as lost: it is another account's (uid "+strconv.Itoa(os.Getuid())+")")
}

// An own folder that took the place of the held one is held instead, with t and mode
// 0500, and nobody is ended. The old folder stays as it is (4c111e86, measured on
// Linux and macOS).
func TestCLITempRootSwappedFolderIsHeldInstead(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f, 0o700); err != nil {
		t.Fatal(err)
	}
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " was no longer at its name: this account's folder that stands there is held instead"
	waitFor(t, "swap line", func() bool { return strings.Contains(logs.String(), line) })
	wantFolderShape(t, x, a.Name)
	wantFolderShape(t, x, a.Name+".old")
	h := heldRecordOf(x, a.Name)
	tempRoots.mu.Lock()
	var st unix.Stat_t
	_ = unix.Fstat(int(h.f.Fd()), &st)
	tempRoots.mu.Unlock()
	var now unix.Stat_t
	if err := unix.Stat(f, &now); err != nil || now.Ino != st.Ino {
		t.Errorf("held inode %d, folder at the name %d (%v), want the new folder held", st.Ino, now.Ino, err)
	}
	holdsFor(t, 5*tempRootCheckEvery, "no end and no loss", func() bool {
		return ends.Load() == 0 && !strings.Contains(logs.String(), "counts as lost")
	})
	// A new folder that has its own t gets mode 0500 too.
	tempRoots.mu.Lock()
	err := os.Rename(f, f+".older")
	if err == nil {
		err = os.Mkdir(f, 0o700)
	}
	if err == nil {
		err = os.Mkdir(filepath.Join(f, "t"), 0o700)
	}
	tempRoots.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second swap line", func() bool { return strings.Count(logs.String(), line) == 2 })
	wantFolderShape(t, x, a.Name)
	if n := ends.Load(); n != 0 {
		t.Errorf("ends %d, want 0", n)
	}
}

// With no entry at the name the folder is made anew, with t and mode 0500, and nobody
// is ended (4c111e86, measured on macOS).
func TestCLITempRootGoneFolderIsMadeAnew(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	f := filepath.Join(x, a.Name)
	tempRoots.mu.Lock() // no check between the steps of the remove
	_ = os.Chmod(f, 0o700)
	_ = os.Remove(filepath.Join(f, "t"))
	err := os.Remove(f)
	tempRoots.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " was no longer at its name: it is made anew"
	waitFor(t, "made-anew line", func() bool { return strings.Contains(logs.String(), line) })
	waitFor(t, "the new folder", func() bool {
		fi, err := os.Lstat(filepath.Join(f, "t"))
		return err == nil && fi.IsDir()
	})
	wantFolderShape(t, x, a.Name)
	if n := ends.Load(); n != 0 {
		t.Errorf("ends %d, want 0", n)
	}
}

// A held folder whose t was removed gets t and mode 0500 again with no line. A t of
// mode 0777 changes nothing. Nobody is ended (4c111e86, measured on macOS, and the t
// of mode 0777 on Linux too).
func TestCLITempRootInnerFolderIsMadeAgain(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	f := filepath.Join(x, a.Name)
	tempRoots.mu.Lock()
	_ = os.Chmod(f, 0o700)
	err := os.Remove(filepath.Join(f, "t"))
	tempRoots.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "t again", func() bool {
		fi, err := os.Lstat(f)
		return err == nil && fi.Mode().Perm() == 0o500
	})
	wantFolderShape(t, x, a.Name)
	tempRoots.mu.Lock()
	_ = os.Chmod(f, 0o700)
	err = os.Chmod(filepath.Join(f, "t"), 0o777)
	_ = os.Chmod(f, 0o500)
	tempRoots.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	holdsFor(t, 5*tempRootCheckEvery, "t of mode 0777, no end and no line", func() bool {
		fi, err := os.Lstat(filepath.Join(f, "t"))
		return err == nil && fi.Mode().Perm() == 0o777 && ends.Load() == 0 && !strings.Contains(logs.String(), "private temp folder")
	})
}

// A temp dir that cannot be read logs the failed-check line once, and a later good check
// logs that all is well. Nobody is ended (4c111e86, measured on Linux).
func TestCLITempRootFailedCheckEndsNobody(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 0000")
	}
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	if err := os.Chmod(x, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(x, 0o700) })
	failed := "[process.Manager] Claude Code's private temp folder " + a.Name + ": the temp dir could not be looked at: permission denied; nobody is ended over that, and it is looked at again"
	waitFor(t, "failed-check line", func() bool { return strings.Contains(logs.String(), failed) })
	holdsFor(t, 5*tempRootCheckEvery, "one failed-check line", func() bool { return strings.Count(logs.String(), failed) == 1 })
	if err := os.Chmod(x, 0o700); err != nil {
		t.Fatal(err)
	}
	well := "[process.Manager] Claude Code's private temp folder " + a.Name + ": all is well with it again"
	waitFor(t, "well line", func() bool { return strings.Contains(logs.String(), well) })
	if n := ends.Load(); n != 0 {
		t.Errorf("ends %d, want 0", n)
	}
}

// A spawn that binds after its folder was found lost is ended at once (claustrum's
// own, not measured).
func TestCLITempRootBindAfterALossEnds(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, runDir := tempRootDir(t, true)
	_, a, release, bind := privateTempRoot([]string{"TMPDIR=" + x}, runDir)
	rel = append(rel, release)
	h := heldRecordOf(x, a.Name)
	if err := os.Chmod(filepath.Join(x, a.Name), 0o777); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "loss", h.lost.Load)
	var ends atomic.Int32
	bind(func() { ends.Add(1) })
	if n := ends.Load(); n != 1 {
		t.Errorf("ends %d, want 1", n)
	}
}

// TestCLITempRootCheckEveryValue pins the check at 2 s.
func TestCLITempRootCheckEveryValue(t *testing.T) {
	if tempRootCheckEvery != 2*time.Second {
		t.Errorf("check every %v, want 2s", tempRootCheckEvery)
	}
}

// A lost folder is not given again, also when the old folder comes back to its name
// and its last spawn gave it back (claustrum's own, not measured).
func TestCLITempRootLostFolderIsNotGivenAgain(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	h := heldRecordOf(x, a.Name)
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f+".old", f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "loss", h.lost.Load)
	tempRoots.mu.Lock()
	_ = os.Remove(f)
	err := os.Rename(f+".old", f)
	tempRoots.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rel[0]()
	waitTempRootsLive(t, 0)
	if b := runTempRootBound(t, x, runDir, &rel, nil); b == nil || b.Name == a.Name {
		t.Errorf("answer %+v, want a new folder in place of the lost %s", b, a.Name)
	}
}

// A reply with whyNot carries the lost list after it (claustrum's own place, not
// measured: the measured reply had a name).
func TestCLITempRootGoneListAfterWhyNot(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	h := heldRecordOf(x, a.Name)
	if err := os.Chmod(filepath.Join(x, a.Name), 0o777); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "loss", h.lost.Load)
	x2, runDir2 := tempRootDir(t, false)
	if err := os.Symlink(x2, filepath.Join(x2, tempEntryName())); err != nil {
		t.Fatal(err)
	}
	res := runTempRootBound(t, x2, runDir2, &rel, nil)
	b, _ := json.Marshal(res)
	want := `{"refused":"a link","whyNot":"` + tempRootSteppedAround + `","lost":[{"name":"` + a.Name + `","why":"changed","agoSeconds":`
	if !strings.HasPrefix(string(b), want) {
		t.Errorf("answer %s, want the prefix %s", b, want)
	}
}
