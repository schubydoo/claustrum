//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The in-place files of a Windows create: `config.worktree` and the index of
// the registration that the staging step moved (round task087-01, Windows VM).
// No test of this file ran on the host of the build: a Windows run must match
// the cell that each test names.

// TestCreateWithAFolderAtConfigWorktreeOnWindows must match cells S9 and S9b
// (C6 of round task007-02): a folder `config.worktree` in the staged
// registration, empty or with a file, fails the create with `openat
// config.worktree: is a directory`. Without the open in place an empty folder
// is removed and the create succeeds.
func TestCreateWithAFolderAtConfigWorktreeOnWindows(t *testing.T) {
	for _, row := range []struct {
		cell string
		file bool
	}{{"S9 empty", false}, {"S9b with a file", true}} {
		t.Run(row.cell, func(t *testing.T) {
			f := newConfigStepFixture(t, "[core]\n\tfilemode = false\n")
			s := stepCreate{shadowFixture: f.shadowFixture, leaf: f.leaf}
			atStep(t, "staged", func() {
				at := filepath.Join(s.staged(t), "w1", worktreeConfigName)
				if err := os.Mkdir(at, 0o755); err != nil {
					t.Error(err)
				}
				if row.file {
					mustWrite(t, filepath.Join(at, "f.txt"), "x\n")
				}
			})
			raw, _ := f.create(t)
			if want := failedFrame(t, completingAddPrefix+"openat config.worktree: is a directory"); raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			s.fullUndo(t)
		})
	}
}

// fileTimes are the creation, access and write times of path.
func fileTimes(t *testing.T, path string) (created, accessed, written time.Time) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d := fi.Sys().(*syscall.Win32FileAttributeData)
	return time.Unix(0, d.CreationTime.Nanoseconds()), time.Unix(0, d.LastAccessTime.Nanoseconds()), time.Unix(0, d.LastWriteTime.Nanoseconds())
}

// wantEarlierTimes tests the time rule of cells S10 and S13: the access time
// is the write time, and both are before the creation time. Without the set
// of the times the write time is at or after the creation time. A write time
// equal to the creation time passes too: the file clock of a CI runner can
// give both steps the same tick.
func wantEarlierTimes(t *testing.T, path string) {
	t.Helper()
	created, accessed, written := fileTimes(t, path)
	if !accessed.Equal(written) || written.After(created) {
		t.Errorf("%s: creation %v, access %v, write %v, want access = write, not after the creation", path, created, accessed, written)
	}
}

// TestConfigWorktreeTimesOnWindows must match cell S10: the access time equals
// the write time, and both are before the creation time. That is the
// `config.worktree` file of the registration.
func TestConfigWorktreeTimesOnWindows(t *testing.T) {
	f := newConfigStepFixture(t, "[core]\n\tfilemode = false\n")
	raw, _ := f.create(t)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	wantEarlierTimes(t, filepath.Join(f.reg, worktreeConfigName))
}

// TestCreateWithAFolderAtTheIndexOnWindows must match cell S12: a folder
// `index` with a file in the registration fails the create with `openat
// index: is a directory`, and the rollback is full. Without the error of the
// placement the create succeeds with no index.
func TestCreateWithAFolderAtTheIndexOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "index", func() { mustWrite(t, filepath.Join(f.gitDir, "worktrees", "w1", "index", "f.txt"), "x\n") })
	raw, _ := f.create(t)
	wantFailed(t, raw, "git worktree add failed (checkout): openat index: is a directory", "worktree_add_failed")
	f.fullUndo(t)
}

// TestIndexTimesOnWindows must match cell S13: the access time equals the
// write time, and both are before the creation time. That is the index of the
// registration.
func TestIndexTimesOnWindows(t *testing.T) {
	f := newStepCreate(t)
	raw, _ := f.create(t)
	if !strings.Contains(raw, `"result":{"success":true,`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	wantEarlierTimes(t, filepath.Join(f.gitDir, "worktrees", "w1", "index"))
}

// TestRegistrationFileLeavesAHardLink: a hard link at `config.worktree` or at
// `index` of the moved registration goes as a link, and the linked file
// outside keeps its bytes. The create succeeds with the new file. Without
// createRegistrationFile an open with O_TRUNC writes into the linked file.
func TestRegistrationFileLeavesAHardLink(t *testing.T) {
	outside := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "keep.txt")
		mustWrite(t, p, "keep\n")
		return p
	}
	link := func(t *testing.T, target, at string) {
		if err := os.Link(target, at); err != nil {
			t.Errorf("the hard link: %v", err)
		}
	}
	t.Run("config.worktree", func(t *testing.T) {
		f := newConfigStepFixture(t, "[core]\n\tfilemode = false\n")
		keep := outside(t)
		at := filepath.Join(f.reg, worktreeConfigName)
		atStep(t, "registration", func() { link(t, keep, at) })
		raw, _ := f.create(t)
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		if got := mustRead(t, keep); got != "keep\n" {
			t.Errorf("the linked file = %q, want its bytes", got)
		}
		if got := mustRead(t, at); got != "[core]\n\tfilemode = false\n" {
			t.Errorf("config.worktree = %q, want the copy", got)
		}
	})
	t.Run("index", func(t *testing.T) {
		f := newStepCreate(t)
		keep := outside(t)
		at := filepath.Join(f.gitDir, "worktrees", "w1", "index")
		atStep(t, "index", func() { link(t, keep, at) })
		raw, _ := f.create(t)
		if !strings.Contains(raw, `"result":{"success":true,`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		if got := mustRead(t, keep); got != "keep\n" {
			t.Errorf("the linked file = %q, want its bytes", got)
		}
		if got := mustRead(t, at); got == "keep\n" {
			t.Error("the index has the bytes of the linked file")
		}
	})
}

// TestCreateRegistrationFile pins the steps of createRegistrationFile: a
// folder gives `openat <name>: is a directory` and stays, and a file there is
// removed and made again.
func TestCreateRegistrationFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	mustWrite(t, filepath.Join(dir, "d", "f.txt"), "x\n")
	if _, err := createRegistrationFile(root, "d"); err == nil || err.Error() != "openat d: is a directory" {
		t.Errorf("createRegistrationFile on a folder = %v, want openat d: is a directory", err)
	}
	if got := mustRead(t, filepath.Join(dir, "d", "f.txt")); got != "x\n" {
		t.Errorf("the file in the folder = %q", got)
	}
	mustWrite(t, filepath.Join(dir, "f"), "old\n")
	out, err := createRegistrationFile(root, "f")
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got := mustRead(t, filepath.Join(dir, "f")); got != "" {
		t.Errorf("the new file = %q, want an empty new file", got)
	}
}
