//go:build unix

package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// flockSeam replaces flockNoWait for one test with answer.
func flockSeam(t *testing.T, answer func(f *os.File) error) {
	t.Helper()
	old := flockNoWait
	flockNoWait = answer
	t.Cleanup(func() { flockNoWait = old })
}

// goesOnErr is the flock error that c9125eb8 goes on after on this system:
// ENOLCK on Linux, ENOTSUP on macOS (measured).
func goesOnErr() error {
	if runtime.GOOS == "darwin" {
		return syscall.ENOTSUP
	}
	return syscall.ENOLCK
}

// Measured on c9125eb8: on a mount whose flock answers ENOLCK, git.info
// succeeds, and the sweep removes a folder of another host's name of 8 days
// and keeps one of 1 hour (Linux). On macOS a FIFO lock gives the same at 10
// days for a folder of this host's name form. The 7 days are claustrum's own
// (not measured).
func TestShadowGoesOnAfterAFlockError(t *testing.T) {
	f := newShadowFixture(t)
	hostOld := plantFolder(t, f.run, deadFolderName(), true)
	hostMid := plantFolder(t, f.run, hostForm()+"-5-0123456789abcdef", true)
	hostNew := plantFolder(t, f.run, hostForm()+"-2-0123456789abcdef", true)
	otherOld := plantFolder(t, f.run, "otherhost-3-0123456789abcdef", true)
	otherMid := plantFolder(t, f.run, "otherhost-6-0123456789abcdef", true)
	otherNew := plantFolder(t, f.run, "otherhost-4-0123456789abcdef", true)
	ageEntry(t, hostOld, 8*24*time.Hour)
	ageEntry(t, hostMid, 5*24*time.Hour)
	ageEntry(t, hostNew, time.Hour)
	ageEntry(t, otherOld, 8*24*time.Hour)
	ageEntry(t, otherMid, 5*24*time.Hour)
	ageEntry(t, otherNew, time.Hour)
	flockSeam(t, func(*os.File) error { return goesOnErr() })
	raw, _ := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info answered %s, want a result in the state shadow", raw)
	}
	sweptGone(t, hostOld)
	sweptThere(t, hostMid)
	sweptThere(t, hostNew)
	sweptGone(t, otherOld)
	sweptThere(t, otherMid)
	sweptThere(t, otherNew)
}

// Another flock error keeps the refusal of the request, and an old folder of
// this host stays (claustrum's own, not measured).
func TestShadowRefusesAnotherFlockError(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	ageEntry(t, dead, 10*24*time.Hour)
	flockSeam(t, func(*os.File) error { return syscall.EIO })
	raw, _ := f.info(t, f.repo, nil)
	if !strings.Contains(raw, "flock ") || !strings.Contains(raw, "input/output error") {
		t.Errorf("git.info answered %s, want the flock refusal", raw)
	}
	sweptThere(t, dead)
}

// A flock that answers EINTR is tried again (claustrum's own, not measured).
// Here the second try finds the lock held, so an old folder of this host stays.
func TestShadowSweepTriesALockAgainAfterEINTR(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	ageEntry(t, dead, 10*24*time.Hour)
	lockRel := filepath.Join(filepath.Base(dead), shadowLockName)
	tries := 0
	old := flockNoWait
	flockSeam(t, func(file *os.File) error {
		if !strings.HasSuffix(file.Name(), lockRel) {
			return old(file)
		}
		tries++
		if tries == 1 {
			return syscall.EINTR
		}
		return syscall.EWOULDBLOCK
	})
	f.info(t, f.repo, nil)
	sweptThere(t, dead)
	if tries != 2 {
		t.Errorf("the lock of the old folder had %d tries, want 2", tries)
	}
}

// After flockTries answers of EINTR the lock counts as held, and an old
// folder of this host stays (claustrum's own, not measured).
func TestShadowSweepStopsAfterEINTRTries(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	ageEntry(t, dead, 10*24*time.Hour)
	lockRel := filepath.Join(filepath.Base(dead), shadowLockName)
	tries := 0
	old := flockNoWait
	flockSeam(t, func(file *os.File) error {
		if !strings.HasSuffix(file.Name(), lockRel) {
			return old(file)
		}
		tries++
		return syscall.EINTR
	})
	f.info(t, f.repo, nil)
	sweptThere(t, dead)
	if tries != flockTries {
		t.Errorf("the lock of the old folder had %d tries, want %d", tries, flockTries)
	}
}

// Measured on c9125eb8 (macOS): a stale folder whose lock is a FIFO goes at 10
// days and a new one stays. On Linux a FIFO lock keeps the folder (not
// measured).
func TestShadowSweepFIFOLock(t *testing.T) {
	f := newShadowFixture(t)
	oldDir := plantFolder(t, f.run, deadFolderName(), false)
	newDir := plantFolder(t, f.run, hostForm()+"-2-0123456789abcdef", false)
	for _, dir := range []string{oldDir, newDir} {
		if err := syscall.Mkfifo(filepath.Join(dir, shadowLockName), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ageEntry(t, oldDir, 10*24*time.Hour)
	f.info(t, f.repo, nil)
	sweptThere(t, newDir)
	if runtime.GOOS == "darwin" {
		sweptGone(t, oldDir)
		return
	}
	sweptThere(t, oldDir)
}

// Measured on c9125eb8 (Linux): on a FUSE mount whose flock answers ENOLCK, a
// folder of another host's name of 8 days goes. There the folder of a lock
// file that is open is not empty at its rmdir. So the sweep closes a lock that
// it did not take before the remove (claustrum's own).
func TestShadowSweepClosesALockNotTakenBeforeTheRemove(t *testing.T) {
	f := newShadowFixture(t)
	old := plantFolder(t, f.run, "otherhost-3-0123456789abcdef", true)
	ageEntry(t, old, 8*24*time.Hour)
	lockRel := filepath.Join(filepath.Base(old), shadowLockName)
	var lockFile *os.File
	realFlock := flockNoWait
	flockSeam(t, func(file *os.File) error {
		if !strings.HasSuffix(file.Name(), lockRel) {
			return realFlock(file)
		}
		lockFile = file
		return goesOnErr()
	})
	realRemove := sweepRemove
	openAtRemove := false
	sweepRemove = func(h *ShadowHost, name string) error {
		if name == filepath.Base(old) && lockFile != nil {
			_, err := lockFile.Stat()
			openAtRemove = err == nil
		}
		return realRemove(h, name)
	}
	t.Cleanup(func() { sweepRemove = realRemove })
	f.info(t, f.repo, nil)
	if lockFile == nil {
		t.Fatal("the sweep took no lock of the old folder")
	}
	if openAtRemove {
		t.Error("the lock file of the old folder was open at its remove")
	}
	sweptGone(t, old)
}
