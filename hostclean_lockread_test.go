//go:build linux || darwin

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

// The lock read of the tidy, as a macOS VM measured it against 89cb6289 (rows C2 to C6).
// Every test here stages the holder query through hcLockHolderRead and the content rule
// through hcLockNeedsRecord, so each one runs on linux and on darwin. No real lsof runs, no
// signal goes out, and every run dir is under t.TempDir.

// hcLockReadFix is a tidy over run dirs that are 40 days old, with no socket.
type hcLockReadFix struct {
	t       *testing.T
	c       *hostCleaner
	runRoot string
	root    *os.Root
	entries []runDirEntry
	asked   []string // the paths the holder query was asked about
}

// newHcLockReadFix stages the content rule and the answer of the holder query.
func newHcLockReadFix(t *testing.T, needsRecord bool, answer func(path string) (held, asked bool)) *hcLockReadFix {
	t.Helper()
	f := &hcLockReadFix{t: t, c: &hostCleaner{selfPid: os.Getpid()}}
	f.runRoot, f.root = hcRunRoot(t)
	oldClock, oldDial, oldRead, oldNeeds := hcClock, hcDial, hcLockHolderRead, hcLockNeedsRecord
	t.Cleanup(func() { hcClock, hcDial, hcLockHolderRead, hcLockNeedsRecord = oldClock, oldDial, oldRead, oldNeeds })
	now := time.Now()
	hcClock = func() time.Time { return now }
	hcDial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT} }
	hcLockNeedsRecord = needsRecord
	hcLockHolderRead = func(path string, _ os.FileInfo) (bool, bool) {
		f.asked = append(f.asked, path)
		return answer(path)
	}
	return f
}

// dir adds the run dir name. A nil lock gives no daemon.lock.
func (f *hcLockReadFix) dir(name string, lock []byte) string {
	f.t.Helper()
	dir := filepath.Join(f.runRoot, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if lock != nil {
		if err := os.WriteFile(filepath.Join(dir, runDirLockName), lock, 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	const idle = 40 * 24 * time.Hour
	at := hcClock().Add(-idle)
	if err := os.Chtimes(dir, at, at); err != nil {
		f.t.Fatal(err)
	}
	f.entries = append(f.entries, runDirEntry{root: f.root, name: name, dirPath: dir, socket: filepath.Join(dir, rpcSockBasename), idle: idle})
	return dir
}

// tidy runs the tidy and returns its summary and its log.
func (f *hcLockReadFix) tidy() (hcSummary, string) {
	buf := captureLogBuf(f.t)
	var sum hcSummary
	f.c.tidyRunDirs(f.entries, &sum)
	return sum, buf.String()
}

func hcDirThere(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Lstat(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func hcRemovedLine(dir string) string {
	return fmt.Sprintf("[hostclean] removed run dir %q: unused for 40 days, nothing answers on its socket, no process holds its lock\n", dir)
}

var hcDeadRecord = []byte(`{"pid":999999,"role":"daemon"}`)

// TestTidyKeepsALockWhoseHolderQueryDidNotStart pins row C3. The holder query does not
// start. A run dir with a lock file stays, with the line of the reference. A run dir with no
// lock file needs no query, and it goes. The control is row C3c: the query starts and finds
// no holder, and all three go.
func TestTidyKeepsALockWhoseHolderQueryDidNotStart(t *testing.T) {
	for _, started := range []bool{false, true} {
		f := newHcLockReadFix(t, false, func(string) (bool, bool) { return false, started })
		z0 := f.dir("z0", nil)
		z1 := f.dir("z1", []byte{})
		z2 := f.dir("z2", hcDeadRecord)
		sum, log := f.tidy()
		for _, p := range f.asked {
			if strings.HasPrefix(p, z0) {
				t.Errorf("started=%v: the holder query ran for %q, a dir with no lock file", started, p)
			}
		}
		if hcDirThere(t, z0) || !strings.Contains(log, hcRemovedLine(z0)) {
			t.Errorf("started=%v: the dir with no lock file was kept; log %q", started, log)
		}
		for _, z := range []string{z1, z2} {
			kept := fmt.Sprintf("[hostclean] run dir %q unused for 40 days: kept, its daemon.lock could not be examined\n", z)
			if started {
				if hcDirThere(t, z) || !strings.Contains(log, hcRemovedLine(z)) || strings.Contains(log, kept) {
					t.Errorf("control: %q was kept; log %q", z, log)
				}
				continue
			}
			if !hcDirThere(t, z) || !strings.Contains(log, kept) {
				t.Errorf("%q: there=%v, want it kept with the line %q; log %q", z, hcDirThere(t, z), kept, log)
			}
		}
		want := hcSummary{runDirsRemoved: 1}
		if started {
			want.runDirsRemoved = 3
		}
		if sum != want {
			t.Errorf("started=%v: summary = %+v, want %+v", started, sum, want)
		}
	}
}

// TestTidyAsksAboutALockThatIsNoRecord pins row C5. Lock content that is no record does not
// stop the holder query. With no holder the run dir goes. The second arm is the linux
// answer, which this change keeps: there such content keeps the dir, and no query runs.
func TestTidyAsksAboutALockThatIsNoRecord(t *testing.T) {
	t.Run("the query runs and the dir goes", func(t *testing.T) {
		f := newHcLockReadFix(t, false, func(string) (bool, bool) { return false, true })
		z3 := f.dir("z3", []byte("garbage"))
		sum, log := f.tidy()
		if hcDirThere(t, z3) || sum.runDirsRemoved != 1 || !strings.Contains(log, hcRemovedLine(z3)) {
			t.Errorf("there=%v summary=%+v log=%q, want the dir removed", hcDirThere(t, z3), sum, log)
		}
		if len(f.asked) == 0 || f.asked[0] != filepath.Join(z3, runDirLockName) {
			t.Errorf("holder queries = %q, want the first one on the lock file", f.asked)
		}
	})
	t.Run("with the record rule the dir stays", func(t *testing.T) {
		f := newHcLockReadFix(t, true, func(string) (bool, bool) { return false, true })
		z3 := f.dir("z3", []byte("garbage"))
		sum, log := f.tidy()
		want := fmt.Sprintf("[hostclean] run dir %q kept: whether a process holds its run-dir lock could not be determined\n", z3)
		if !hcDirThere(t, z3) || sum.runDirsRemoved != 0 || !strings.Contains(log, want) || len(f.asked) != 0 {
			t.Errorf("there=%v summary=%+v queries=%q log=%q, want the dir kept and no query", hcDirThere(t, z3), sum, f.asked, log)
		}
	})
}

// TestTidyLockReadControls pins the two rows that stay equal. Row C2: a live process holds
// the lock, and the dir stays with the held line. Row C6: the lock holds the record of a dead
// pid and has no holder, and the dir goes.
func TestTidyLockReadControls(t *testing.T) {
	t.Run("C2: a holder keeps the dir", func(t *testing.T) {
		f := newHcLockReadFix(t, false, func(string) (bool, bool) { return true, true })
		z1 := f.dir("z1", hcDeadRecord)
		sum, log := f.tidy()
		want := fmt.Sprintf("[hostclean] run dir %q unused for 40 days: kept, a live process holds its daemon.lock\n", z1)
		if !hcDirThere(t, z1) || sum != (hcSummary{}) || !strings.Contains(log, want) {
			t.Errorf("there=%v summary=%+v log=%q, want the dir kept with %q", hcDirThere(t, z1), sum, log, want)
		}
	})
	t.Run("C6: a dead record with no holder goes", func(t *testing.T) {
		for _, needsRecord := range []bool{false, true} {
			f := newHcLockReadFix(t, needsRecord, func(string) (bool, bool) { return false, true })
			z6 := f.dir("z6", hcDeadRecord)
			sum, log := f.tidy()
			if hcDirThere(t, z6) || sum.runDirsRemoved != 1 || !strings.Contains(log, hcRemovedLine(z6)) {
				t.Errorf("needsRecord=%v: there=%v summary=%+v log=%q, want the dir removed", needsRecord, hcDirThere(t, z6), sum, log)
			}
		}
	})
}
