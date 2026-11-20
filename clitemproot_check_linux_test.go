package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// afterCheck runs cond under tempRootChecking and tempRoots.mu. A check logs its line
// while it holds tempRootChecking and fits the folder after the line. So cond sees the
// line only once that check has ended.
func afterCheck(cond func() bool) func() bool {
	return func() bool {
		tempRootChecking.Lock()
		defer tempRootChecking.Unlock()
		tempRoots.mu.Lock()
		defer tempRoots.mu.Unlock()
		return cond()
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
// one. The reply after it lists it again (4c111e86, measured on Linux and macOS, the
// list on macOS).
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
	// The check ends the spawn before it logs the run-on line, so the test waits for
	// the end of that check.
	waitFor(t, "end", afterCheck(func() bool { return ends.Load() == 1 }))
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
	if d := runTempRootBound(t, x, runDir, &rel, nil); d == nil || len(d.Gone) != 1 || d.Gone[0].Name != a.Name || d.Gone[0].Why != "changed" {
		t.Errorf("the next answer %+v, want the lost %s again", d, a.Name)
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
	// The next reply lists both, oldest first, with the words "open" and "anothers"
	// (4c111e86, measured on macOS).
	c := runTempRootBound(t, x2, runDir2, &rel, nil)
	if c == nil || len(c.Gone) != 2 || c.Gone[0].Name != a.Name || c.Gone[0].Why != "open" || c.Gone[1].Name != b.Name || c.Gone[1].Why != "anothers" {
		t.Errorf("answer %+v, want the lost %s (open) and %s (anothers)", c, a.Name, b.Name)
	}
}

// The lost list keeps the last 16 folders, oldest first, and each later reply with a
// member carries all of them. The 17th loss cuts the oldest (4c111e86, measured on
// macOS).
func TestCLITempRootLostListKeepsTheLast16(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	for i := range 17 {
		addLostEntry(fmt.Sprintf("ccd-%010d", i), "changed")
	}
	for range 2 {
		res := addGoneList(&rpc.CLITempRoot{Name: "ccd-aaaaaaaaaa"})
		var got []string
		for _, g := range res.Gone {
			got = append(got, g.Name)
		}
		var want []string
		for i := 1; i <= 16; i++ {
			want = append(want, fmt.Sprintf("ccd-%010d", i))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("lost list %v, want %v", got, want)
		}
	}
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
	waitFor(t, "swap line", afterCheck(func() bool { return strings.Contains(logs.String(), line) }))
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
	waitFor(t, "second swap line", afterCheck(func() bool { return strings.Count(logs.String(), line) == 2 }))
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

// A held folder that flips between mode 0000 and 0500 64 times logs 49 change lines,
// then the cap line in place of the 50th, and no change line after (4c111e86, measured
// on Linux). Nobody is ended (claustrum's own here, not measured).
func TestCLITempRootChangeLinesStopAtTheCap(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root uses a folder of mode 0000")
	}
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	h := heldRecordOf(x, a.Name)
	f := filepath.Join(x, a.Name)
	underCheckLock(t, func() error {
		for i := range 64 {
			mode := os.FileMode(0o500)
			if i%2 == 0 {
				mode = 0
			}
			if err := os.Chmod(f, mode); err != nil {
				return err
			}
			checkTempRoot(h)
		}
		return nil
	})
	prefix := "[process.Manager] Claude Code's private temp folder " + a.Name + ": "
	shut := prefix + "its owner cannot use it (chmod 500 it); nobody is ended over that, and it is looked at again"
	well := prefix + "all is well with it again"
	capLine := prefix + "its state keeps changing, and is logged no more unless it is lost"
	holdsFor(t, 5*tempRootCheckEvery, "the capped lines", func() bool {
		s := logs.String()
		return ends.Load() == 0 && strings.Count(s, shut) == 25 && strings.Count(s, well) == 24 &&
			strings.Count(s, capLine) == 1 && strings.Index(s, capLine) > strings.LastIndex(s, shut) &&
			strings.Index(s, capLine) > strings.LastIndex(s, well)
	})
	// A loss after the cap still logs its line (claustrum's own, not measured).
	if err := os.Chmod(f, 0o777); err != nil {
		t.Fatal(err)
	}
	lost := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is open to other accounts"
	waitFor(t, "the loss line after the cap", func() bool { return strings.Contains(logs.String(), lost) })
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
	want := `{"refused":"a link","whyNot":"` + tempRootSteppedAround + `","lost":[{"name":"` + a.Name + `","why":"open","agoSeconds":`
	if !strings.HasPrefix(string(b), want) {
		t.Errorf("answer %s, want the prefix %s", b, want)
	}
}

// wantLostAfter holds a folder in a new temp dir, runs change on the temp dir x and
// the folder name, and waits for the end and the lost line with why.
func wantLostAfter(t *testing.T, change func(x, name string), why string) {
	t.Helper()
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	change(x, a.Name)
	waitFor(t, "end", func() bool { return ends.Load() == 1 })
	wantLinesInOrder(t, logs.String(), "[process.Manager] Claude Code's private temp folder "+a.Name+" counts as lost: "+why)
	holdsFor(t, 5*tempRootCheckEvery, "one end", func() bool { return ends.Load() == 1 })
}

// wantNoEndAfter holds a folder in a new temp dir, runs change, and checks for 5
// checks that nobody is ended and that the log holds line once, or no line of the
// folder for an empty line. It gives back the folder name.
func wantNoEndAfter(t *testing.T, change func(x, name string), line string) {
	t.Helper()
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	change(x, a.Name)
	if line != "" {
		line = "[process.Manager] Claude Code's private temp folder " + a.Name + line
		waitFor(t, "the line", func() bool { return strings.Contains(logs.String(), line) })
	}
	holdsFor(t, 5*tempRootCheckEvery, "no end", func() bool {
		if line == "" {
			return ends.Load() == 0 && !strings.Contains(logs.String(), "private temp folder "+a.Name)
		}
		return ends.Load() == 0 && strings.Count(logs.String(), line) == 1
	})
}

// underCheckLock runs f while no check runs.
func underCheckLock(t *testing.T, f func() error) {
	t.Helper()
	tempRootChecking.Lock()
	tempRoots.mu.Lock()
	err := f()
	tempRoots.mu.Unlock()
	tempRootChecking.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

// A file at the name, and a t that is a file or a link, count as lost (4c111e86,
// measured on macOS).
func TestCLITempRootFileAtTheNameOrInnerChangeIsLost(t *testing.T) {
	t.Run("file at the name", func(t *testing.T) {
		wantLostAfter(t, func(x, name string) {
			f := filepath.Join(x, name)
			underCheckLock(t, func() error {
				if err := os.Rename(f, f+".old"); err != nil {
					return err
				}
				return os.WriteFile(f, nil, 0o600)
			})
		}, "it is not a folder")
	})
	for _, c := range []struct {
		name, why string
		put       func(p string) error
	}{
		{"t is a file", "it is holding a t that is not a folder", func(p string) error { return os.WriteFile(p, nil, 0o600) }},
		{"t is a link", "it is holding a t that is a link", func(p string) error { return os.Symlink("/", p) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantLostAfter(t, func(x, name string) {
				f := filepath.Join(x, name)
				underCheckLock(t, func() error {
					_ = os.Chmod(f, 0o700)
					if err := os.Remove(filepath.Join(f, "t")); err != nil {
						return err
					}
					if err := c.put(filepath.Join(f, "t")); err != nil {
						return err
					}
					return os.Chmod(f, 0o500)
				})
			}, c.why)
		})
	}
}

// A mount on the held folder, on its t or on the temp dir counts as lost (4c111e86,
// measured on macOS). A test cannot mount, so the seam gives the one inode another
// device.
func TestCLITempRootMountIsLost(t *testing.T) {
	for _, c := range []struct {
		name, why string
		path      func(x, name string) string
	}{
		{"on the folder", "it is on another file system", func(x, name string) string { return filepath.Join(x, name) }},
		{"on t", "it is holding a t that is on another file system", func(x, name string) string { return filepath.Join(x, name, "t") }},
		{"on the temp dir", "it is in a temp dir whose path now leads to another file system (a new mount?)", func(x, name string) string { return x }},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantLostAfter(t, func(x, name string) {
				var st unix.Stat_t
				if err := unix.Lstat(c.path(x, name), &st); err != nil {
					t.Fatal(err)
				}
				ino := st.Ino
				underCheckLock(t, func() error {
					old := tempRootCheckDev
					tempRootCheckDev = func(s *unix.Stat_t) uint64 {
						if s.Ino == ino {
							return old(s) + 1
						}
						return old(s)
					}
					t.Cleanup(func() {
						tempRoots.mu.Lock()
						tempRootCheckDev = old
						tempRoots.mu.Unlock()
					})
					return nil
				})
			}, c.why)
		})
	}
}

// A temp dir that became a file or a link, or that others can write, counts as lost
// (4c111e86, measured on macOS, mode 0777 also of root).
func TestCLITempRootTempDirChangeIsLost(t *testing.T) {
	for _, c := range []struct {
		name, why string
		change    func(x string) error
	}{
		{"a file", "it is in a temp dir that is no longer a folder", func(x string) error {
			if err := os.Rename(x, x+".old"); err != nil {
				return err
			}
			return os.WriteFile(x, nil, 0o600)
		}},
		{"a link", "it is in a temp dir that is no longer a folder", func(x string) error {
			if err := os.Rename(x, x+".old"); err != nil {
				return err
			}
			return os.Symlink(x+".old", x)
		}},
		{"mode 0777", "it is in a temp dir that another account can rearrange", func(x string) error { return os.Chmod(x, 0o777) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantLostAfter(t, func(x, _ string) { underCheckLock(t, func() error { return c.change(x) }) }, c.why)
		})
	}
}

// A sticky temp dir of mode 1777 and a temp dir of mode 0770 change nothing. A temp dir that is gone and a held
// folder of mode 0 log their own line once and end nobody (4c111e86, measured on
// macOS).
func TestCLITempRootChangesThatEndNobody(t *testing.T) {
	t.Run("sticky 1777", func(t *testing.T) {
		wantNoEndAfter(t, func(x, _ string) {
			underCheckLock(t, func() error { return os.Chmod(x, os.ModeSticky|0o777) })
		}, "")
	})
	// A group write bit alone ends nobody: only mode 0777 is measured, and the test
	// of the other write bit is claustrum's own.
	t.Run("group write 0770", func(t *testing.T) {
		wantNoEndAfter(t, func(x, _ string) {
			underCheckLock(t, func() error { return os.Chmod(x, 0o770) })
		}, "")
	})
	t.Run("gone temp dir", func(t *testing.T) {
		wantNoEndAfter(t, func(x, _ string) {
			underCheckLock(t, func() error { return os.Rename(x, x+".old") })
		}, ": the temp dir is gone; nobody is ended over that, and it is looked at again")
	})
	t.Run("folder of mode 0", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root uses a folder of mode 0000")
		}
		wantNoEndAfter(t, func(x, name string) {
			underCheckLock(t, func() error { return os.Chmod(filepath.Join(x, name), 0) })
		}, ": its owner cannot use it (chmod 500 it); nobody is ended over that, and it is looked at again")
	})
}

// A temp dir whose path is a link at the hold, as /tmp is on macOS, ends nobody and
// logs no line of the folder (claustrum's own, not measured).
func TestCLITempRootTempDirLinkAtTheHoldEndsNobody(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	l := filepath.Join(filepath.Dir(x), "l")
	if err := os.Symlink(x, l); err != nil {
		t.Fatal(err)
	}
	var ends atomic.Int32
	a := runTempRootBound(t, l, runDir, &rel, func() { ends.Add(1) })
	if a == nil || !tempRootNameRE.MatchString(a.Name) {
		t.Fatalf("answer %+v, want a folder", a)
	}
	holdsFor(t, 5*tempRootCheckEvery, "no end and no line", func() bool {
		return ends.Load() == 0 && !strings.Contains(logs.String(), "private temp folder "+a.Name)
	})
}

// A folder gone from a temp dir that group can write, or that another account owns,
// is not made anew, and nobody is ended (claustrum's own, not measured). The owner
// case gives the held record another uid, as the test cannot chown.
func TestCLITempRootNoRemakeInAGroupWritableTempDir(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(x string, h *heldTempRoot) error
	}{
		{"mode 0770", func(x string, _ *heldTempRoot) error { return os.Chmod(x, 0o770) }},
		{"another owner", func(_ string, h *heldTempRoot) error { h.uid = os.Getuid() + 1; return nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var rel []func()
			cleanTempRoots(t, &rel)
			setTempRootCheckEvery(t, 20*time.Millisecond)
			logs := captureLogBuf(t)
			x, runDir := tempRootDir(t, true)
			var ends atomic.Int32
			a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
			h := heldRecordOf(x, a.Name)
			f := filepath.Join(x, a.Name)
			underCheckLock(t, func() error {
				_ = os.Chmod(f, 0o700)
				_ = os.Remove(filepath.Join(f, "t"))
				if err := os.Remove(f); err != nil {
					return err
				}
				return c.change(x, h)
			})
			holdsFor(t, 5*tempRootCheckEvery, "no remake, no end and no line", func() bool {
				_, err := os.Lstat(f)
				return errors.Is(err, os.ErrNotExist) && ends.Load() == 0 && !strings.Contains(logs.String(), "private temp folder "+a.Name)
			})
		})
	}
}

// A temp dir path with a trailing slash: a link in place of the temp dir still counts
// as lost, and a temp dir path that was a link at the hold still ends nobody.
func TestCLITempRootTempDirWithATrailingSlash(t *testing.T) {
	t.Run("replaced by a link", func(t *testing.T) {
		var rel []func()
		cleanTempRoots(t, &rel)
		setTempRootCheckEvery(t, 20*time.Millisecond)
		logs := captureLogBuf(t)
		x, runDir := tempRootDir(t, true)
		var ends atomic.Int32
		a := runTempRootBound(t, x+"/", runDir, &rel, func() { ends.Add(1) })
		if a == nil || !tempRootNameRE.MatchString(a.Name) {
			t.Fatalf("answer %+v, want a folder", a)
		}
		underCheckLock(t, func() error {
			if err := os.Rename(x, x+".old"); err != nil {
				return err
			}
			return os.Symlink(x+".old", x)
		})
		waitFor(t, "end", func() bool { return ends.Load() == 1 })
		wantLinesInOrder(t, logs.String(), "[process.Manager] Claude Code's private temp folder "+a.Name+" counts as lost: it is in a temp dir that is no longer a folder")
	})
	t.Run("a link at the hold", func(t *testing.T) {
		var rel []func()
		cleanTempRoots(t, &rel)
		setTempRootCheckEvery(t, 20*time.Millisecond)
		logs := captureLogBuf(t)
		x, runDir := tempRootDir(t, true)
		l := filepath.Join(filepath.Dir(x), "l")
		if err := os.Symlink(x, l); err != nil {
			t.Fatal(err)
		}
		var ends atomic.Int32
		a := runTempRootBound(t, l+"/", runDir, &rel, func() { ends.Add(1) })
		if a == nil || !tempRootNameRE.MatchString(a.Name) {
			t.Fatalf("answer %+v, want a folder", a)
		}
		holdsFor(t, 5*tempRootCheckEvery, "no end and no line", func() bool {
			return ends.Load() == 0 && !strings.Contains(logs.String(), "private temp folder "+a.Name)
		})
	})
}

// linkAtName puts a link at the name of the folder name in x, to the folder under a
// new name, as the rows of the cap do.
func linkAtName(t *testing.T, x, name string) {
	t.Helper()
	f := filepath.Join(x, name)
	if err := os.Rename(f, f+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f+".old", f); err != nil {
		t.Fatal(err)
	}
}

// checkTicks drives the loop of the checks by hand (fakeCheckTicks).
type checkTicks struct {
	ticks chan time.Time
	done  chan struct{} // gets a value at the end of each check
}

// fakeCheckTicks gives the checks a tick only when the test sends one, until the test
// ends. So no check comes between two steps of the test.
func fakeCheckTicks(t *testing.T) checkTicks {
	t.Helper()
	stopTempRootChecks()
	c := checkTicks{ticks: make(chan time.Time), done: make(chan struct{}, 1)}
	// The loop reads both seams under tempRoots.mu at its start.
	tempRoots.mu.Lock()
	oldTicks, oldAfter := tempRootCheckTicks, afterTempRootChecks
	tempRootCheckTicks = func(time.Duration) (<-chan time.Time, func()) { return c.ticks, func() {} }
	afterTempRootChecks = func() {
		select {
		case c.done <- struct{}{}:
		default:
		}
	}
	tempRoots.mu.Unlock()
	t.Cleanup(func() {
		tempRoots.mu.Lock()
		tempRootCheckTicks, afterTempRootChecks = oldTicks, oldAfter
		tempRoots.mu.Unlock()
	})
	return c
}

// check runs one check and waits for its end. With no loop of the checks it fails the
// test.
func (c checkTicks) check(t *testing.T) {
	t.Helper()
	select {
	case c.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("no loop of the checks runs")
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the check did not end within 5 s")
	}
}

// givenCount is the count of the given-back folders that are still checked.
func givenCount() int {
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	return len(tempRoots.given)
}

// A loss in the time that the child ran still counts after the give-back: the next
// check finds it, and the next reply lists it (4c111e86, measured on macOS). The
// flock goes at the give-back (claustrum's own).
func TestCLITempRootLossBeforeTheGiveBackCounts(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	linkAtName(t, x, a.Name)
	rel[0]()
	if n := givenCount(); n != 1 {
		t.Fatalf("%d given-back folders are checked, want 1", n)
	}
	f, err := os.Open(filepath.Join(x, a.Name+".old"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("flock after the give-back: %v, want none held", err)
	}
	ticks.check(t)
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is a link"
	if n := strings.Count(logs.String(), line); n != 1 {
		t.Errorf("%d loss lines, want 1\nlog:\n%s", n, logs.String())
	}
	if n := givenCount(); n != 0 {
		t.Errorf("%d given-back folders are still checked after the loss, want 0", n)
	}
	c := runTempRootBound(t, x, runDir, &rel, nil)
	if c == nil || c.Name == a.Name || len(c.Gone) != 1 || c.Gone[0].Name != a.Name || c.Gone[0].Why != "changed" {
		t.Errorf("the next answer %+v, want a new folder and the lost %s", c, a.Name)
	}
}

// A given-back folder stays checked: a loss after several checks still counts
// (4c111e86, measured on macOS: a loss 5 s after the end of the child).
func TestCLITempRootGivenBackFolderStaysChecked(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	for range 3 {
		ticks.check(t)
	}
	if n := givenCount(); n != 1 || strings.Contains(logs.String(), "private temp folder "+a.Name) {
		t.Fatalf("after three checks %d given-back folders and log %q, want 1 and no line", n, logs.String())
	}
	linkAtName(t, x, a.Name)
	ticks.check(t)
	if line := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is a link"; !strings.Contains(logs.String(), line) {
		t.Errorf("log %q, want %q", logs.String(), line)
	}
	if c := runTempRootBound(t, x, runDir, &rel, nil); c == nil || len(c.Gone) != 1 || c.Gone[0].Name != a.Name {
		t.Errorf("the next answer %+v, want the lost %s", c, a.Name)
	}
}

// A gone temp dir ends the checks of a given-back folder, with no line (claustrum's
// own, not measured).
func TestCLITempRootGivenBackFolderOfAGoneTempDir(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	if err := os.Rename(x, x+".away"); err != nil {
		t.Fatal(err)
	}
	ticks.check(t)
	if n := givenCount(); n != 0 || strings.Contains(logs.String(), "private temp folder "+a.Name) {
		t.Errorf("%d given-back folders and log %q, want 0 and no line", n, logs.String())
	}
}

// The checks of a given-back folder write nothing: a missing folder is not made anew,
// an own folder at the name is not held instead, and a missing t is not made again.
// The first two end the checks of the folder (claustrum's own, not measured).
func TestCLITempRootGivenBackCheckWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		given  int // the given-back folders that are checked after the check
		change func(t *testing.T, f string)
		want   func(t *testing.T, f string)
	}{
		{"missing folder", 0,
			func(t *testing.T, f string) {
				if err := os.Rename(f, f+".away"); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, f string) {
				if _, err := os.Lstat(f); !os.IsNotExist(err) {
					t.Errorf("the name: %v, want no folder made anew", err)
				}
			}},
		{"own folder at the name", 0,
			func(t *testing.T, f string) {
				if err := os.Rename(f, f+".away"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, f string) {
				if fi, err := os.Lstat(f); err != nil || fi.Mode().Perm() != 0o700 {
					t.Errorf("the new folder: %v %v, want mode 0700", fi, err)
				}
				if _, err := os.Lstat(filepath.Join(f, "t")); !os.IsNotExist(err) {
					t.Errorf("t in the new folder: %v, want none", err)
				}
			}},
		{"missing t", 1,
			func(t *testing.T, f string) {
				if err := os.Chmod(f, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(f, "t")); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(f, 0o500); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, f string) {
				if _, err := os.Lstat(filepath.Join(f, "t")); !os.IsNotExist(err) {
					t.Errorf("t: %v, want none made again", err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rel []func()
			cleanTempRoots(t, &rel)
			ticks := fakeCheckTicks(t)
			logs := captureLogBuf(t)
			x, runDir := tempRootDir(t, true)
			a := runTempRootBound(t, x, runDir, &rel, nil)
			rel[0]()
			f := filepath.Join(x, a.Name)
			c.change(t, f)
			ticks.check(t)
			c.want(t, f)
			if n := givenCount(); n != c.given {
				t.Errorf("%d given-back folders after the check, want %d", n, c.given)
			}
			if strings.Contains(logs.String(), "private temp folder "+a.Name) {
				t.Errorf("log %q, want no line of %s", logs.String(), a.Name)
			}
		})
	}
}

// One check looks at all held folders together. The losses of folders held at other
// times come at the same check, in the order of the hold, and the next reply lists
// them in that order (4c111e86, measured on macOS: batches of 3). The order of the hold
// is claustrum's own.
func TestCLITempRootLossesOfOneCheckComeTogether(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	var names, xs []string
	var runDir0 string
	for i := range 6 {
		x, runDir := tempRootDir(t, true)
		a := runTempRootBound(t, x, runDir, &rel, nil)
		if a == nil || !tempRootNameRE.MatchString(a.Name) {
			t.Fatalf("spawn %d: %+v, want a folder", i+1, a)
		}
		names, xs = append(names, a.Name), append(xs, x)
		if i == 0 {
			runDir0 = runDir
		}
	}
	for i := range names {
		linkAtName(t, xs[i], names[i])
	}
	ticks.check(t)
	line := func(n string) string {
		return "[process.Manager] Claude Code's private temp folder " + n + " counts as lost: it is a link"
	}
	all := false
	waitFor(t, "first loss line", afterCheck(func() bool {
		s := logs.String()
		if !strings.Contains(s, line(names[0])) {
			return false
		}
		all = true
		for _, n := range names {
			all = all && strings.Contains(s, line(n))
		}
		return true
	}))
	if !all {
		t.Fatalf("the check that found %s did not find all six\nlog:\n%s", names[0], logs.String())
	}
	var want []string
	for _, n := range names {
		want = append(want, line(n))
	}
	wantLinesInOrder(t, logs.String(), want...)
	c := runTempRootBound(t, xs[0], runDir0, &rel, nil)
	var got []string
	if c != nil {
		for _, g := range c.Gone {
			got = append(got, g.Name)
		}
	}
	if !slices.Equal(got, names) {
		t.Errorf("lost list %v, want %v", got, names)
	}
}

// A held folder and a folder given back before it, lost at one check, come in the order
// of the hold: the folder held first comes first in the loss lines and in the lost list
// (claustrum's own).
func TestCLITempRootGivenBackAndHeldLossesKeepTheOrderOfTheHold(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x1, runDir1 := tempRootDir(t, true)
	x2, runDir2 := tempRootDir(t, true)
	a := runTempRootBound(t, x1, runDir1, &rel, nil)
	rel[0]()
	b := runTempRootBound(t, x2, runDir2, &rel, nil)
	if a == nil || b == nil || givenCount() != 1 {
		t.Fatalf("answers %+v and %+v with %d given-back folders, want two folders and 1", a, b, givenCount())
	}
	linkAtName(t, x1, a.Name)
	linkAtName(t, x2, b.Name)
	ticks.check(t)
	line := func(n string) string {
		return "[process.Manager] Claude Code's private temp folder " + n + " counts as lost: it is a link"
	}
	wantLinesInOrder(t, logs.String(), line(a.Name), line(b.Name))
	x3, runDir3 := tempRootDir(t, true)
	c := runTempRootBound(t, x3, runDir3, &rel, nil)
	var got []string
	if c != nil {
		for _, g := range c.Gone {
			got = append(got, g.Name)
		}
	}
	if want := []string{a.Name, b.Name}; !slices.Equal(got, want) {
		t.Errorf("lost list %v, want %v", got, want)
	}
}

// A held folder whose handle cannot be copied for a check logs the failed-check line,
// and a later good check logs that all is well again. Nobody is ended (claustrum's own).
func TestCLITempRootHandleThatCannotBeCopiedLogsTheFailedCheck(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	h := heldRecordOf(x, a.Name)
	if h == nil {
		t.Fatal("no held record")
	}
	closed, err := os.Open(x)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	tempRoots.mu.Lock()
	f := h.f
	h.f = closed // a copy of a closed handle fails with EBADF
	tempRoots.mu.Unlock()
	ticks.check(t)
	tempRoots.mu.Lock()
	h.f = f
	tempRoots.mu.Unlock()
	ticks.check(t)
	pre := "[process.Manager] Claude Code's private temp folder " + a.Name
	wantLinesInOrder(t, logs.String(),
		pre+": the temp dir could not be looked at: "+unix.EBADF.Error()+"; nobody is ended over that, and it is looked at again",
		pre+": all is well with it again")
	if ends.Load() != 0 {
		t.Errorf("%d ends, want none", ends.Load())
	}
}

// A new hold of a given-back folder ends its old record and closes the old handle, so
// a later loss gives one line and one entry of the list (claustrum's own).
func TestCLITempRootHeldAgainEndsTheGivenRecord(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	tempRoots.mu.Lock()
	old := tempRoots.given[0]
	tempRoots.mu.Unlock()
	if b := runTempRootBound(t, x, runDir, &rel, nil); b == nil || b.Name != a.Name {
		t.Fatalf("second answer %+v, want the folder %s again", b, a.Name)
	}
	if n := givenCount(); n != 0 || old.f.Fd() != ^uintptr(0) {
		t.Errorf("after the new hold %d given-back folders, old handle %d, want 0 and closed", n, old.f.Fd())
	}
	rel[1]()
	linkAtName(t, x, a.Name)
	ticks.check(t)
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is a link"
	if n := strings.Count(logs.String(), line); n != 1 {
		t.Errorf("%d loss lines, want 1", n)
	}
	if c := runTempRootBound(t, x, runDir, &rel, nil); c == nil || len(c.Gone) != 1 {
		t.Errorf("the next answer %+v, want one lost entry", c)
	}
}

// At most tempRootGivenKeep given-back folders are checked, and each for
// tempRootGivenFor. The oldest goes first (claustrum's own, not measured).
func TestCLITempRootGivenBackBounds(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	advance := fakeTempRootClock(t)
	var first *heldTempRoot
	for i := range tempRootGivenKeep + 1 {
		x, runDir := tempRootDir(t, true)
		if a := runTempRootBound(t, x, runDir, &rel, nil); a == nil || a.Name == "" {
			t.Fatalf("spawn %d: %+v, want a folder", i+1, a)
		}
		rel[i]()
		advance(tempRootMakeWindow)
		if i == 0 {
			tempRoots.mu.Lock()
			first = tempRoots.given[0]
			tempRoots.mu.Unlock()
		}
	}
	tempRoots.mu.Lock()
	n, firstIn := len(tempRoots.given), slices.Contains(tempRoots.given, first)
	tempRoots.given[0].givenAt = time.Now().Add(-tempRootGivenFor)
	tempRoots.mu.Unlock()
	if n != tempRootGivenKeep || firstIn || first.f.Fd() != ^uintptr(0) {
		t.Fatalf("%d given-back folders, the first kept %v, want %d and the first closed", n, firstIn, tempRootGivenKeep)
	}
	ticks.check(t)
	if n := givenCount(); n != tempRootGivenKeep-1 {
		t.Errorf("%d given-back folders after the check, want %d: the old one ends", n, tempRootGivenKeep-1)
	}
}

// setAfterOneCheck runs fn after each folder of a check, until the test ends. The loop
// reads the seam under tempRoots.mu at its start.
func setAfterOneCheck(t *testing.T, fn func()) {
	t.Helper()
	tempRoots.mu.Lock()
	old := afterTempRootOneCheck
	afterTempRootOneCheck = fn
	tempRoots.mu.Unlock()
	t.Cleanup(func() {
		tempRoots.mu.Lock()
		afterTempRootOneCheck = old
		tempRoots.mu.Unlock()
	})
}

// The losses of one check reach the lost list in one step at its end: between two
// folders of the check a reply sees none of them (claustrum's own).
func TestCLITempRootLossesOfOneCheckReachTheListTogether(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	var seen []int
	setAfterOneCheck(t, func() {
		tempRootsLost.mu.Lock()
		seen = append(seen, len(tempRootsLost.list))
		tempRootsLost.mu.Unlock()
	})
	for range 2 {
		x, runDir := tempRootDir(t, true)
		a := runTempRootBound(t, x, runDir, &rel, nil)
		linkAtName(t, x, a.Name)
	}
	ticks.check(t)
	tempRootsLost.mu.Lock()
	n := len(tempRootsLost.list)
	tempRootsLost.mu.Unlock()
	if !slices.Equal(seen, []int{0, 0}) || n != 2 {
		t.Errorf("list sizes between the folders %v and after the check %d, want [0 0] and 2", seen, n)
	}
}

// The check of a given-back folder holds no lock while it looks at the folder, so a
// spawn in another temp dir gets its folder meanwhile (claustrum's own).
func TestCLITempRootGivenBackCheckHoldsNoLock(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	x, runDir := tempRootDir(t, true)
	x2, runDir2 := tempRootDir(t, true)
	runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	in, free := make(chan struct{}), make(chan struct{})
	var once sync.Once
	old := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		once.Do(func() {
			close(in)
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		})
		return statDev(st)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticks.ticks <- time.Now()
		<-ticks.done
	}()
	<-in
	tempRoots.mu.Lock()
	pass := tempRoots.givenPass // the look that the seam holds up
	tempRoots.mu.Unlock()
	type answer struct {
		res     *rpc.CLITempRoot
		release func()
	}
	got := make(chan answer, 1)
	go func() {
		_, res, release, _ := privateTempRoot([]string{"TMPDIR=" + x2}, runDir2)
		got <- answer{res, release}
	}()
	select {
	case a := <-got:
		rel = append(rel, a.release)
		if a.res == nil || !tempRootNameRE.MatchString(a.res.Name) {
			t.Errorf("answer %+v during the check, want a folder", a.res)
		}
	case <-time.After(5 * time.Second):
		t.Error("a spawn waited for the check of a given-back folder")
		close(free)
		rel = append(rel, (<-got).release)
		free = nil
	}
	if free != nil {
		close(free)
	}
	<-done
	// The loop can end its check before the look answers, so the seam goes back only
	// once the look has answered too.
	if pass != nil {
		<-pass.done
	}
	tempRootCheckDev = old
}

// The check of a held folder holds no lock that a spawn takes while it looks at the
// folder, so a spawn in another temp dir that answers gets its folder at once while that
// check does not answer (4c111e86,
// measured on Linux on a dead NFS mount). A loss that the check finds still ends the
// child that asked, once the check answers.
func TestCLITempRootHeldCheckHoldsNoLock(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	old := tempRootLimit
	tempRootLimit = time.Second
	t.Cleanup(func() { tempRootLimit = old })
	x, runDir := tempRootDir(t, true)
	x2, runDir2 := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	linkAtName(t, x, a.Name)
	in, free := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldDev := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		once.Do(func() {
			close(in)
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		})
		return statDev(st)
	}
	t.Cleanup(func() { tempRootCheckDev = oldDev })
	t.Cleanup(func() {
		select {
		case <-free:
		default:
			close(free)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticks.ticks <- time.Now()
		<-ticks.done
	}()
	<-in
	start := time.Now()
	_, res, release, _ := privateTempRoot([]string{"TMPDIR=" + x2}, runDir2)
	rel = append(rel, release)
	if d := time.Since(start); res == nil || !tempRootNameRE.MatchString(res.Name) || d >= tempRootLimit {
		t.Errorf("answer %+v after %v during the check of a held folder, want a folder at once", res, d)
	}
	if ends.Load() != 0 {
		t.Error("an end before the check answered")
	}
	close(free)
	<-done
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is a link"
	if n := strings.Count(logs.String(), line); n != 1 || ends.Load() != 1 {
		t.Errorf("%d loss lines and %d ends after the check, want 1 and 1\nlog:\n%s", n, ends.Load(), logs.String())
	}
}

// A folder given back while its check does not answer takes only the loss that the
// check finds: one loss line, and its given-back record ends (claustrum's own).
func TestCLITempRootGivenBackDuringItsCheckTakesTheLoss(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	linkAtName(t, x, a.Name)
	in, free := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldDev := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		once.Do(func() {
			close(in)
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		})
		return statDev(st)
	}
	t.Cleanup(func() { tempRootCheckDev = oldDev })
	t.Cleanup(func() {
		select {
		case <-free:
		default:
			close(free)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticks.ticks <- time.Now()
		<-ticks.done
	}()
	<-in
	rel[0]()
	if n := givenCount(); n != 1 {
		t.Fatalf("%d given-back folders during the check, want 1", n)
	}
	close(free)
	<-done
	line := "[process.Manager] Claude Code's private temp folder " + a.Name + " counts as lost: it is a link"
	if n := strings.Count(logs.String(), line); n != 1 || givenCount() != 0 {
		t.Errorf("%d loss lines, %d given-back folders, want 1 and 0\nlog:\n%s", n, givenCount(), logs.String())
	}
}

// A loss that the check of one held folder finds ends the child that asked at once,
// while the check of a later held folder does not answer yet (claustrum's own).
func TestCLITempRootLossEndsBeforeALaterCheckAnswers(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	x, runDir := tempRootDir(t, true)
	x2, runDir2 := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	runTempRootBound(t, x2, runDir2, &rel, nil)
	var st2 unix.Stat_t
	if err := unix.Stat(x2, &st2); err != nil {
		t.Fatal(err)
	}
	linkAtName(t, x, a.Name)
	free := make(chan struct{})
	oldDev := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		if st.Ino == st2.Ino && st.Dev == st2.Dev { // the look at the temp dir of the second folder
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		}
		return statDev(st)
	}
	t.Cleanup(func() { tempRootCheckDev = oldDev })
	t.Cleanup(func() {
		select {
		case <-free:
		default:
			close(free)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticks.ticks <- time.Now()
		<-ticks.done
	}()
	waitFor(t, "end of the child of the lost folder", func() bool { return ends.Load() == 1 })
	close(free)
	<-done
}

// A folder given back while its check runs gets no write from that check: a missing
// folder is not made anew at its name (claustrum's own).
func TestCLITempRootGivenBackDuringItsCheckGetsNoWrite(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	in, free := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldDev := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		once.Do(func() {
			close(in)
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		})
		return statDev(st)
	}
	t.Cleanup(func() { tempRootCheckDev = oldDev })
	t.Cleanup(func() {
		select {
		case <-free:
		default:
			close(free)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticks.ticks <- time.Now()
		<-ticks.done
	}()
	<-in
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".away"); err != nil {
		t.Fatal(err)
	}
	rel[0]()
	close(free)
	<-done
	if _, err := os.Lstat(f); !os.IsNotExist(err) {
		t.Errorf("the name: %v, want no folder made anew", err)
	}
	if strings.Contains(logs.String(), "made anew") {
		t.Errorf("log %q, want no line that the folder is made anew", logs.String())
	}
}

// onLogLine is a log sink that runs fn once, at the first line that holds sub, before
// it writes that line.
type onLogLine struct {
	buf  syncBuffer
	sub  string
	once sync.Once
	fn   func()
}

func (w *onLogLine) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.sub) {
		w.once.Do(w.fn)
	}
	return w.buf.Write(p)
}

// A folder given back right after the write test of its check, while the check makes it
// anew at its name, loses that flock when the check gives its result: a spawn of the same
// temp dir right after gets that folder again (claustrum's own).
func TestCLITempRootGivenBackDuringItsRemakeLosesTheFlock(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	x, runDir := tempRootDir(t, true)
	given := func() bool {
		tempRoots.mu.Lock()
		defer tempRoots.mu.Unlock()
		return len(tempRoots.held) == 0 && len(tempRoots.given) == 1
	}
	// The line that the folder is made anew comes after the write test and before the
	// mkdir, so the give-back lands in that window.
	w := &onLogLine{sub: "made anew", fn: func() {
		rel[0]()
		for end := time.Now().Add(5 * time.Second); !given() && time.Now().Before(end); {
			time.Sleep(time.Millisecond)
		}
	}}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(w)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	var again *rpc.CLITempRoot
	var againRel func()
	var calls atomic.Int32
	setAfterOneCheck(t, func() {
		if calls.Add(1) == 1 {
			var bind func(func())
			_, again, againRel, bind = privateTempRoot([]string{"TMPDIR=" + x}, runDir)
			bind(nil)
		}
	})
	a := runTempRootBound(t, x, runDir, &rel, nil)
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".away"); err != nil {
		t.Fatal(err)
	}
	ticks.check(t)
	if againRel != nil {
		rel = append(rel, againRel)
	}
	if !strings.Contains(w.buf.String(), "made anew") {
		t.Fatalf("log %q, want the line that the folder is made anew", w.buf.String())
	}
	if again == nil || again.Name != a.Name {
		t.Errorf("the spawn right after the check got %+v, want the folder %s again", again, a.Name)
	}
}

// A check of a given-back folder that does not answer holds up no check of a held
// folder: each tick still checks the held folders, their losses reach the list at that
// tick, and no new look at the given-back folders starts meanwhile (claustrum's own).
func TestCLITempRootStuckGivenBackCheckDelaysNoHeldCheck(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	x, runDir := tempRootDir(t, true)
	runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	var heldNames []string
	var heldDirs []string
	for range 2 {
		x2, runDir2 := tempRootDir(t, true)
		b := runTempRootBound(t, x2, runDir2, &rel, nil)
		heldNames, heldDirs = append(heldNames, b.Name), append(heldDirs, x2)
	}
	free := make(chan struct{})
	var calls atomic.Int32
	old := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		if calls.Add(1) == 1 { // the first call is the look at the given-back folder
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		}
		return statDev(st)
	}
	for i, n := range heldNames {
		linkAtName(t, heldDirs[i], n)
		ticks.check(t)
		if line := "[process.Manager] Claude Code's private temp folder " + n + " counts as lost: it is a link"; !strings.Contains(logs.String(), line) {
			t.Errorf("tick %d: log lacks %q", i+1, line)
		}
		tempRootsLost.mu.Lock()
		got := len(tempRootsLost.list)
		tempRootsLost.mu.Unlock()
		if got != i+1 {
			t.Errorf("tick %d: %d lost entries, want %d", i+1, got, i+1)
		}
	}
	tempRoots.mu.Lock()
	pass := tempRoots.givenPass
	tempRoots.mu.Unlock()
	if pass == nil || pass.answered {
		t.Errorf("look at the given-back folder %+v, want the first one still running", pass)
	}
	close(free)
	if pass != nil {
		<-pass.done
	}
	tempRootCheckDev = old
}

// A look at the given-back folders that a loop gave up still counts until it answers.
// A new loop starts no second look meanwhile (claustrum's own: the cap of 1).
func TestCLITempRootGivenUpLookBlocksANewOne(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	x, runDir := tempRootDir(t, true)
	x2, runDir2 := tempRootDir(t, true)
	runTempRootBound(t, x, runDir, &rel, nil)
	rel[0]()
	free := make(chan struct{})
	var calls atomic.Int32
	old := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		if calls.Add(1) == 1 { // the first look does not answer
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
		}
		return statDev(st)
	}
	ticks.check(t) // the look starts and does not answer
	tempRoots.mu.Lock()
	first := tempRoots.givenPass
	tempRoots.given[0].givenAt = time.Now().Add(-tempRootGivenFor)
	tempRoots.mu.Unlock()
	if first == nil {
		t.Fatal("no look at the given-back folder")
	}
	ticks.check(t) // the old record ends, so the loop ends and gives the look up
	tempRoots.mu.Lock()
	running := tempRoots.checks != nil
	tempRoots.mu.Unlock()
	if running {
		t.Fatal("the loop still runs, want its end")
	}
	runTempRootBound(t, x2, runDir2, &rel, nil) // a new loop
	rel[1]()
	ticks.check(t)
	tempRoots.mu.Lock()
	second, open := tempRoots.givenPass, tempRoots.givenOpen
	tempRoots.mu.Unlock()
	if n := calls.Load(); second != nil || open != 1 || n != 1 {
		t.Errorf("look %+v, %d open and %d looks at a folder, want none new, 1 open and 1", second, open, n)
	}
	close(free)
	<-first.done
	tempRootCheckDev = old
}

// A check whose look at the folder does not answer logs once that the name does not
// answer, and the check that then returns logs that all is well again. Nobody is ended.
// This holds for a held folder and for one given back (4c111e86, measured on Linux on a
// dead NFS mount).
func TestCLITempRootNameThatDoesNotAnswerHeld(t *testing.T) { nameDoesNotAnswer(t, false) }

func TestCLITempRootNameThatDoesNotAnswerGivenBack(t *testing.T) { nameDoesNotAnswer(t, true) }

func nameDoesNotAnswer(t *testing.T, given bool) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	oldWait := tempRootNameWait
	tempRootNameWait = 20 * time.Millisecond
	t.Cleanup(func() { tempRootNameWait = oldWait })
	x, runDir := tempRootDir(t, true)
	var ends atomic.Int32
	a := runTempRootBound(t, x, runDir, &rel, func() { ends.Add(1) })
	if given {
		rel[0]()
	}
	free, answered := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	old := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		if calls.Add(1) == 1 { // the first look does not answer until free
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
			defer close(answered)
		}
		return statDev(st)
	}
	t.Cleanup(func() {
		// The seam goes back only once the look at a given-back folder has answered too.
		tempRoots.mu.Lock()
		pass := tempRoots.givenPass
		tempRoots.mu.Unlock()
		if pass != nil {
			<-pass.done
		}
		tempRootCheckDev = old
	})
	go func() { ticks.ticks <- time.Now() }()
	prefix := "[process.Manager] Claude Code's private temp folder " + a.Name + ": "
	stuck := prefix + "its name does not answer; nobody is ended over that, and it is looked at again"
	well := prefix + "all is well with it again"
	waitFor(t, "the line that the name does not answer", func() bool { return strings.Contains(logs.String(), stuck) })
	holdsFor(t, 5*tempRootNameWait, "one such line and no line that all is well", func() bool {
		s := logs.String()
		return strings.Count(s, stuck) == 1 && !strings.Contains(s, well)
	})
	close(free)
	<-answered
	waitFor(t, "the line that all is well again", func() bool { return strings.Contains(logs.String(), well) })
	if n, w := strings.Count(logs.String(), stuck), strings.Count(logs.String(), well); n != 1 || w != 1 || ends.Load() != 0 {
		t.Errorf("%d stuck lines, %d well lines and %d ends, want 1, 1 and 0", n, w, ends.Load())
	}
}

// A held folder past the cap of the change lines logs no line that all is well after a
// check that did not answer: that line counts toward tempRootChangeCap (claustrum's own).
func TestCLITempRootNoAnswerWellLineKeepsTheCap(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	ticks := fakeCheckTicks(t)
	logs := captureLogBuf(t)
	oldWait := tempRootNameWait
	tempRootNameWait = 20 * time.Millisecond
	t.Cleanup(func() { tempRootNameWait = oldWait })
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	h := heldRecordOf(x, a.Name)
	tempRoots.mu.Lock()
	h.changes = tempRootChangeCap
	tempRoots.mu.Unlock()
	free, answered := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	old := tempRootCheckDev
	tempRootCheckDev = func(st *unix.Stat_t) uint64 {
		if calls.Add(1) == 1 { // the first look does not answer until free
			select {
			case <-free:
			case <-time.After(10 * time.Second):
			}
			defer close(answered)
		}
		return statDev(st)
	}
	t.Cleanup(func() { tempRootCheckDev = old })
	go func() { ticks.ticks <- time.Now() }()
	prefix := "[process.Manager] Claude Code's private temp folder " + a.Name + ": "
	stuck := prefix + "its name does not answer; nobody is ended over that, and it is looked at again"
	waitFor(t, "the line that the name does not answer", func() bool { return strings.Contains(logs.String(), stuck) })
	close(free)
	<-answered
	<-ticks.done
	if s := logs.String(); strings.Contains(s, prefix+"all is well with it again") {
		t.Errorf("log = %q, want no line that all is well past the cap", s)
	}
}
