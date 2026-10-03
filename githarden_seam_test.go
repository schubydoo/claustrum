package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gitStatusFixture builds the minimum that buildStatusGitDir needs to reach the
// seamed call: an entry folder with a HEAD and an index, and a common directory.
// Nothing here has to be a valid repository: buildStatusGitDir runs no git.
func gitStatusFixture(t *testing.T) (e statusEntry, commonDir string) {
	t.Helper()
	root := t.TempDir()
	entry := filepath.Join(root, "entry")
	commonDir = filepath.Join(root, "common")
	for _, d := range []string{entry, commonDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"HEAD", "index"} {
		if err := os.WriteFile(filepath.Join(entry, f), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return statusEntryAt(t, entry), commonDir
}

// statusEntryAt is the entry at dir with its root open. The root is closed when the
// test ends, before the temporary folders are removed.
func statusEntryAt(t *testing.T, dir string) statusEntry {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return statusEntry{dir: dir, root: root}
}

// buildFails runs buildStatusGitDir, removes its folder and returns its error.
func buildFails(t *testing.T, e statusEntry, common string) error {
	t.Helper()
	tmp, err := buildStatusGitDir(e, common)
	if tmp != "" {
		_ = os.RemoveAll(tmp)
	}
	return err
}

// Every assertion below is errors.Is against the test's own sentinel, not err != nil.

// TestGitStatusPropagatesACopyFailure covers a file of the entry that exists and
// cannot be copied. buildStatusGitDir must return that error and not go on: a status
// with HEAD or index missing reports deletions and untracked entries that do not
// exist. Only a file that is absent is left out.
func TestGitStatusPropagatesACopyFailure(t *testing.T) {
	errRead := errors.New("read HEAD: input/output error")
	old := copyStatusFile
	t.Cleanup(func() { copyStatusFile = old })
	copyStatusFile = func(*os.Root, string, string, int64) error { return errRead }

	e, common := gitStatusFixture(t)
	if err := buildFails(t, e, common); !errors.Is(err, errRead) {
		t.Fatalf("buildStatusGitDir with a failing copy = %v, want %v", err, errRead)
	}
}

// TestGitStatusPropagatesAChtimesFailure covers the copy of the modification time.
// That Chtimes keeps git's racy-clean check armed (see
// TestGitStatusDetectsSameSizeModification). It targets a file that the copy just
// made, so only a seam makes it fail.
func TestGitStatusPropagatesAChtimesFailure(t *testing.T) {
	errChtimes := errors.New("chtimes index: operation not permitted")
	old := chtimesStatusFile
	t.Cleanup(func() { chtimesStatusFile = old })
	chtimesStatusFile = func(string, time.Time, time.Time) error { return errChtimes }

	e, common := gitStatusFixture(t)
	if err := buildFails(t, e, common); !errors.Is(err, errChtimes) {
		t.Fatalf("buildStatusGitDir with a failing chtimes = %v, want %v", err, errChtimes)
	}
}

// TestGitStatusPropagatesACommondirWriteFailure covers the commondir write. Without
// commondir git cannot resolve the branch HEAD of a worktree and reports every
// tracked file as a new one.
func TestGitStatusPropagatesACommondirWriteFailure(t *testing.T) {
	errCommondir := errors.New("write commondir: no space left on device")
	old := writeStatusFile
	t.Cleanup(func() { writeStatusFile = old })
	writeStatusFile = func(string, []byte, os.FileMode) error { return errCommondir }

	e, common := gitStatusFixture(t)
	if err := buildFails(t, e, common); !errors.Is(err, errCommondir) {
		t.Fatalf("buildStatusGitDir with a failing commondir write = %v, want %v", err, errCommondir)
	}
}

// A file of the entry that grew past its bound after the gate looked at it is an
// error of the copy, and the copy reads no more than the bound plus one byte.
func TestStatusCopyFileStopsAtItsBound(t *testing.T) {
	e, _ := gitStatusFixture(t)
	root := e.root
	dst := filepath.Join(t.TempDir(), "index")
	if err := statusCopyFile(root, "index", dst, int64(len("fixture\n"))-1); err == nil {
		t.Fatal("statusCopyFile of a file over its bound = nil, want an error")
	}
	if err := statusCopyFile(root, "index", dst+"2", int64(len("fixture\n"))); err != nil {
		t.Fatalf("statusCopyFile of a file at its bound = %v", err)
	}
}
