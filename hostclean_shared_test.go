//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Host-cleaner tests that need no /proc and no lsof, so they run on linux and darwin alike.

// hcRunRoot makes <tmp>/run and opens it as the tidy's run root. The root closes at cleanup.
func hcRunRoot(t *testing.T) (runRoot string, root *os.Root) {
	t.Helper()
	runRoot = filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return runRoot, root
}

// TestRunDirsSkipsOwnDirAndSymlinks pins two runDirs rules (issue 429): the cleaner's
// own run dir is never listed, so the tidy never dials its own socket, and a symlinked entry
// is not a run dir, so it is never probed or removed.
func TestRunDirsSkipsOwnDirAndSymlinks(t *testing.T) {
	base := t.TempDir()
	runRoot := filepath.Join(base, "run")
	for _, name := range []string{"own", "D"} {
		if err := os.MkdirAll(filepath.Join(runRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(base, "outside", "S")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runRoot, "S")); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside the run root. os.Root would follow this one, so only the
	// Lstat rule keeps it out.
	if err := os.Symlink("D", filepath.Join(runRoot, "T")); err != nil {
		t.Fatal(err)
	}
	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	hcClock = time.Now

	c := &hostCleaner{roots: &hostRoots{roots: []string{base}}, ownRunDir: filepath.Join(runRoot, "own")}
	dirs, done := c.runDirs()
	defer done()
	var names []string
	for _, d := range dirs {
		names = append(names, d.name)
	}
	if len(names) != 1 || names[0] != "D" {
		t.Errorf("runDirs = %v, want only [D]: not the own dir, not the symlink", names)
	}
}

// TestHcDirIdleIgnoresANonRegularLog: a log that is not a regular file (a symlink here) does
// not count as activity.
// Not measured: the Linux run did not stage this rule.
func TestHcDirIdleIgnoresANonRegularLog(t *testing.T) {
	runRoot, root := hcRunRoot(t)
	dir := filepath.Join(runRoot, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(runRoot, "fresh.log")
	if err := os.WriteFile(fresh, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fresh, filepath.Join(dir, hcLogName)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	idle, ok := hcDirIdle(root, "d", now)
	if !ok || idle < 39*24*time.Hour {
		t.Errorf("idle=%v ok=%v, want ~40 days: a symlinked log is not activity", idle, ok)
	}
}

// TestRunDirsCapLogNeedsA65thDir: the cap line fires only when a 65th candidate exists.
//
// Not measured: the Linux run did not stage this rule.
func TestRunDirsCapLogNeedsA65thDir(t *testing.T) {
	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	hcClock = time.Now
	for _, n := range []int{hcMaxRunDirs, hcMaxRunDirs + 1} {
		base := t.TempDir()
		for i := 0; i < n; i++ {
			if err := os.MkdirAll(filepath.Join(base, "run", fmt.Sprintf("d%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		buf := captureLogBuf(t)
		c := &hostCleaner{roots: &hostRoots{roots: []string{base}}}
		dirs, done := c.runDirs()
		done()
		logged := strings.Contains(buf.String(), fmt.Sprintf("run-dir count over %d: capping this sweep at the first %d", hcMaxRunDirs, hcMaxRunDirs))
		if len(dirs) != hcMaxRunDirs || logged != (n > hcMaxRunDirs) {
			t.Errorf("%d dirs: listed %d, cap logged %v", n, len(dirs), logged)
		}
	}
}

func TestSummaryMeasuredText(t *testing.T) {
	s := hcSummary{daemonsRetired: 1, runDirsRemoved: 1}
	want := "stranded daemons signalled 0 (ended 0, survived 0) plus 0 process(es)/group(s) they left behind; orphaned Claude Code groups signalled 0 (survived 0); abandoned daemons retired 1; dead run dirs removed 1; candidates left undecided 0"
	if s.String() != want {
		t.Errorf("summary = %q, want %q", s.String(), want)
	}
}

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
		dirs, done := c.runDirs()
		defer done()
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
		dirs, done := c.runDirs()
		defer done()
		if len(dirs) != 1 {
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
		dirs, done := c.runDirs()
		defer done()
		if len(dirs) != hcMaxRunDirs {
			t.Errorf("runDirs = %d entries, want the cap of %d", len(dirs), hcMaxRunDirs)
		}
	})
}
