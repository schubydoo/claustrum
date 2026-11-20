//go:build linux || darwin

package git

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

// TestStagedRecordNamesLeaf: a registration with no record, or with a
// record that names another path than the staged leaf, keeps the record of
// git. A record of the staged leaf gets a new record. Without the test of the
// record each record gets a new one.
func TestStagedRecordNamesLeaf(t *testing.T) {
	base := t.TempDir()
	st := &addStage{dir: filepath.Join(base, ".s1"), leafName: "w1", git: openTestRoot(t, base)}
	reg := filepath.Join(base, "reg")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	if st.stagedRecordNamesLeaf(openTestRoot(t, reg)) {
		t.Error("no record names the staged leaf")
	}
	mustWrite(t, filepath.Join(reg, "gitdir"), "/elsewhere/.git\n")
	if st.stagedRecordNamesLeaf(openTestRoot(t, reg)) {
		t.Error("a record of another path names the staged leaf")
	}
	mustWrite(t, filepath.Join(reg, "gitdir"), filepath.Join(st.dir, stagedLeafFolder, "w1", ".git")+"\n")
	if !st.stagedRecordNamesLeaf(openTestRoot(t, reg)) {
		t.Error("the record of the staged leaf does not name it")
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
