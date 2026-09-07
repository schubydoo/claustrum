package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gitStatusFixture builds the minimum hardenedGitStatus's temp-gitdir assembly
// needs to REACH the seamed call: a gitDir carrying a real HEAD and a real index,
// so the copy loop stats both and proceeds instead of skipping them as absent.
// Nothing here has to be a valid repository — every arm under test returns before
// git is executed.
func gitStatusFixture(t *testing.T) (worktree, gitDir, commonDir string) {
	t.Helper()
	root := t.TempDir()
	worktree = filepath.Join(root, "wt")
	gitDir = filepath.Join(root, "gitdir")
	commonDir = filepath.Join(root, "common")
	for _, d := range []string{worktree, gitDir, commonDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"HEAD", "index"} {
		if err := os.WriteFile(filepath.Join(gitDir, f), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return worktree, gitDir, commonDir
}

// Every assertion below is errors.Is against the test's OWN sentinel rather than
// err != nil. With this fixture git itself would fail too (it is not a real
// repository), so err != nil cannot tell the arm under test from git's own exit
// status — it would pass with the arm deleted.

// TestGitStatusPropagatesAReadFailureAfterStat covers the TOCTOU arm: the file
// existed at Stat and then failed to read. hardenedGitStatus must propagate that
// rather than continue, because a status run with HEAD or index missing
// fabricates deletions and untracked entries. A mode-0000 HEAD stages it on Unix as
// non-root (see git_status_stderr_unix_test.go); the seam covers root and Windows too.
func TestGitStatusPropagatesAReadFailureAfterStat(t *testing.T) {
	errRead := errors.New("read HEAD: input/output error")
	old := readStatusFile
	t.Cleanup(func() { readStatusFile = old })
	readStatusFile = func(string) ([]byte, error) { return nil, errRead }

	wt, gitDir, common := gitStatusFixture(t)
	if _, err := hardenedGitStatus(wt, gitDir, common, "status", "--porcelain"); !errors.Is(err, errRead) {
		t.Fatalf("hardenedGitStatus with a failing read = %v, want %v", err, errRead)
	}
}

// TestGitStatusPropagatesAMetadataWriteFailure covers the write of the copied
// HEAD/index into the temp gitdir. It fails only on a full or failing filesystem,
// and the destination directory was created by os.MkdirTemp moments earlier.
func TestGitStatusPropagatesAMetadataWriteFailure(t *testing.T) {
	errWrite := errors.New("write HEAD: no space left on device")
	old := writeStatusFile
	t.Cleanup(func() { writeStatusFile = old })
	writeStatusFile = func(string, []byte, os.FileMode) error { return errWrite }

	wt, gitDir, common := gitStatusFixture(t)
	if _, err := hardenedGitStatus(wt, gitDir, common, "status", "--porcelain"); !errors.Is(err, errWrite) {
		t.Fatalf("hardenedGitStatus with a failing write = %v, want %v", err, errWrite)
	}
}

// TestGitStatusPropagatesAChtimesFailure covers the mtime copy. That Chtimes is
// what keeps git's racy-clean check armed (see
// TestGitStatusDetectsSameSizeModification), so silently continuing past a
// failure would report a same-size unstaged edit as clean. It targets the file
// os.WriteFile just created, so nothing short of a seam makes it fail.
func TestGitStatusPropagatesAChtimesFailure(t *testing.T) {
	errChtimes := errors.New("chtimes index: operation not permitted")
	old := chtimesStatusFile
	t.Cleanup(func() { chtimesStatusFile = old })
	chtimesStatusFile = func(string, time.Time, time.Time) error { return errChtimes }

	wt, gitDir, common := gitStatusFixture(t)
	if _, err := hardenedGitStatus(wt, gitDir, common, "status", "--porcelain"); !errors.Is(err, errChtimes) {
		t.Fatalf("hardenedGitStatus with a failing chtimes = %v, want %v", err, errChtimes)
	}
}

// TestGitStatusPropagatesACommondirWriteFailure covers the commondir write, which
// is a SEPARATE arm from the metadata write above — the fake is path-sensitive so
// the HEAD and index copies still succeed and the failure lands after the loop.
// Without commondir git cannot resolve a branch worktree's symref HEAD and
// reports every tracked file as a fresh add, so this arm must not be skipped
// either.
func TestGitStatusPropagatesACommondirWriteFailure(t *testing.T) {
	errCommondir := errors.New("write commondir: no space left on device")
	old := writeStatusFile
	t.Cleanup(func() { writeStatusFile = old })
	writeStatusFile = func(name string, b []byte, mode os.FileMode) error {
		if filepath.Base(name) == "commondir" {
			return errCommondir
		}
		return old(name, b, mode)
	}

	wt, gitDir, common := gitStatusFixture(t)
	if _, err := hardenedGitStatus(wt, gitDir, common, "status", "--porcelain"); !errors.Is(err, errCommondir) {
		t.Fatalf("hardenedGitStatus with a failing commondir write = %v, want %v", err, errCommondir)
	}
}
