//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStagedEntryModesSteps runs stagedEntryModes on a small tree, and on a
// staging folder that is gone. The tree gets the modes of its source,
// through a sub folder. Each failed step logs its line, and the create goes
// on. Without the copy of the modes the file keeps the mode of its create.
func TestStagedEntryModesSteps(t *testing.T) {
	src, git := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "sub", "f"), "f\n")
	if err := os.Chmod(filepath.Join(src, "sub", "f"), 0o640); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(git, "stg", "sub", "f"), "f\n")
	if err := os.Chmod(filepath.Join(git, "stg", "sub", "f"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(git, worktreesSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(git, worktreesSubdir), 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	srcRoot, gitRoot := openTestRoot(t, src), openTestRoot(t, git)
	stagedEntryModes(srcRoot, gitRoot, "stg")
	fi, err := os.Lstat(filepath.Join(git, "stg", "sub", "f"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("sub/f has mode %o, want 640", got)
	}

	logs := captureLogBuf(t)
	stagedEntryModes(srcRoot, gitRoot, "gone")
	if !strings.Contains(logs.String(), "the modes of the new worktree's files were not set") {
		t.Errorf("the log = %q, want the line of the modes", logs.String())
	}
	if stagedEntryTakesWorktreesGroup && !strings.Contains(logs.String(), "the group of the new worktree's entry was not set") {
		t.Errorf("the log = %q, want the line of the group", logs.String())
	}
	mustWrite(t, filepath.Join(git, "half", "sub", "x"), "x\n")
	if err := copyStagedModes(srcRoot, gitRoot, "half", ""); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy into a folder with no sub/f = %v, want a missing file", err)
	}
	if err := copyStagedModes(srcRoot, gitRoot, "stg", "nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy of a missing source folder = %v, want a missing folder", err)
	}
}

// TestWriteStagedRecordKeepsOtherRecords: writeStagedRecord changes nothing
// when the registration has no record, or a record that names another path
// than the staged leaf. A record of the staged leaf gets the text of the
// leaf. Without the test of the record each record gets that text.
func TestWriteStagedRecordKeepsOtherRecords(t *testing.T) {
	base := t.TempDir()
	st := &addStage{dir: filepath.Join(base, ".s1"), leafName: "w1", git: openTestRoot(t, base)}
	leaf := filepath.Join(base, "leaf", "w1")
	reg := filepath.Join(base, "reg")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.writeStagedRecord(openTestRoot(t, reg), "stg", leaf); err != nil {
		t.Errorf("no record = %v, want nil", err)
	}
	mustWrite(t, filepath.Join(reg, "gitdir"), "/elsewhere/.git\n")
	mustWrite(t, filepath.Join(base, "stg", "gitdir"), "/elsewhere/.git\n")
	if err := st.writeStagedRecord(openTestRoot(t, reg), "stg", leaf); err != nil {
		t.Errorf("another record = %v, want nil", err)
	}
	if got := mustRead(t, filepath.Join(base, "stg", "gitdir")); got != "/elsewhere/.git\n" {
		t.Errorf("the record = %q, want it as it was", got)
	}
	staged := filepath.Join(st.dir, stagedLeafFolder, "w1", ".git")
	mustWrite(t, filepath.Join(reg, "gitdir"), staged+"\n")
	mustWrite(t, filepath.Join(base, "stg", "gitdir"), staged+"\n")
	if err := st.writeStagedRecord(openTestRoot(t, reg), "stg", leaf); err != nil {
		t.Fatal(err)
	}
	if got, want := mustRead(t, filepath.Join(base, "stg", "gitdir")), stagedRecordText(leaf); got != want {
		t.Errorf("the record = %q, want %q", got, want)
	}
	if err := st.writeStagedRecord(openTestRoot(t, reg), "gone", leaf); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a staging folder with no record = %v, want a missing file", err)
	}
}

// TestStagingSmallErrors pins the wrapped error of requestDirError and a
// staged leaf with no `.git` file.
func TestStagingSmallErrors(t *testing.T) {
	inner := errors.New("inner")
	e := &requestDirError{err: inner}
	if e.Error() != "inner" || !errors.Is(e, inner) {
		t.Errorf("requestDirError = %q, want the inner error", e.Error())
	}
	if (&addOwnFiles{text: "own"}).Error() != "own" {
		t.Error("addOwnFiles.Error lost its text")
	}
	if stagedLeafNamesEntry(openTestRoot(t, t.TempDir()), "w1", ".s1", "w1", "/g") {
		t.Error("a staged leaf with no .git file names the entry")
	}
}
