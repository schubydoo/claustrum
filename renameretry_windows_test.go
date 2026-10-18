//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The Windows side of the retry of a held rename (round task009-01, Windows
// VM). No test of this file ran on the host of the build: a Windows run must
// match the cell that each test names.

// holdOpen opens path with no delete sharing, as the holder of cells F1a and
// F1b does, and returns the release. A folder is opened with backup semantics.
func holdOpen(t *testing.T, path string, dir bool) (release func()) {
	t.Helper()
	release, err := holdOpenErr(path, dir)
	if err != nil {
		t.Fatalf("the hold of %s: %v", path, err)
	}
	t.Cleanup(release)
	return release
}

// holdOpenErr is holdOpen for a goroutine that is not the one of the test.
func holdOpenErr(path string, dir bool) (release func(), err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var flags uint32
	if dir {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = windows.CloseHandle(h) }) }, nil
}

// setRenameRetryBound sets renameRetryBound until the test ends.
func setRenameRetryBound(t *testing.T, d time.Duration) {
	t.Helper()
	old := renameRetryBound
	renameRetryBound = d
	t.Cleanup(func() { renameRetryBound = old })
}

// jsonText is s as it is inside a JSON string.
func jsonText(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[1 : len(b)-1])
}

// TestRenameHeldOnWindows pins the two errors that the retry tries again:
// access denied and a sharing violation. Another error fails at once.
func TestRenameHeldOnWindows(t *testing.T) {
	for _, row := range []struct {
		err  syscall.Errno
		want bool
	}{
		{syscall.ERROR_ACCESS_DENIED, true},
		{windows.ERROR_SHARING_VIOLATION, true},
		{syscall.ERROR_FILE_NOT_FOUND, false},
		{syscall.ERROR_ALREADY_EXISTS, false},
		{windows.ERROR_DIR_NOT_EMPTY, false},
	} {
		if got := renameHeld(&os.LinkError{Op: "renameat", Old: "a", New: "b", Err: row.err}); got != row.want {
			t.Errorf("renameHeld(%v) = %v, want %v", row.err, got, row.want)
		}
	}
}

// TestCopyRenameErrorOnWindows pins the text form of cell F1b: `rename <full
// path of the temp file> <full path of the copy>: Access is denied.`.
func TestCopyRenameErrorOnWindows(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	err = copyRenameError(root, &os.LinkError{Op: "renameat", Old: ".tmp-1", New: "HEAD", Err: syscall.ERROR_ACCESS_DENIED})
	want := "rename " + filepath.Join(dir, ".tmp-1") + " " + filepath.Join(dir, "HEAD") + ": Access is denied."
	if err == nil || err.Error() != want || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Errorf("copyRenameError = %v, want %s", err, want)
	}
	other := errors.New("other")
	if got := copyRenameError(root, other); got != other {
		t.Errorf("copyRenameError of another error = %v, want it unchanged", got)
	}
}

// TestShadowCopyHeldOnWindows must match cells F1a, F1b and F1c of round
// task009-01: the copy of HEAD is held open while git.info writes a new copy.
// Released after 300 ms, the reply is the normal result and the copy has the
// new bytes. Held past the bound, the reply names both full paths, the old copy
// stays and no temp file is left. Without the retry the first case fails at
// once. Without copyRenameError the text says `renameat .tmp-<n> HEAD`.
func TestShadowCopyHeldOnWindows(t *testing.T) {
	prepare := func(t *testing.T) (shadowFixture, string) {
		f := newSeededShadowFixture(t)
		if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
			t.Fatalf("git.info = %s", raw)
		}
		runGit(t, f.repo, "checkout", "-q", "-b", "x")
		return f, filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "HEAD")
	}
	noTempLeft := func(t *testing.T, copyOf string) {
		ents, err := os.ReadDir(filepath.Dir(copyOf))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("the temp file %s is left", e.Name())
			}
		}
	}
	t.Run("released", func(t *testing.T) {
		f, copyOf := prepare(t)
		release := holdOpen(t, copyOf, false)
		time.AfterFunc(300*time.Millisecond, release)
		start := time.Now()
		raw, _ := f.info(t, f.repo, nil)
		if !strings.Contains(raw, `"branch":"x"`) || !strings.Contains(raw, `"settingsControl":"shadow"`) {
			t.Errorf("git.info = %s, want the result on the branch x", raw)
		}
		if took := time.Since(start); took < 250*time.Millisecond {
			t.Errorf("git.info took %v, want the wait for the release", took)
		}
		if got := mustRead(t, copyOf); got != "ref: refs/heads/x\n" {
			t.Errorf("the copy of HEAD = %q, want the new bytes", got)
		}
		noTempLeft(t, copyOf)
	})
	t.Run("held past the bound", func(t *testing.T) {
		f, copyOf := prepare(t)
		setRenameRetryBound(t, 300*time.Millisecond)
		release := holdOpen(t, copyOf, false)
		start := time.Now()
		raw, _ := f.info(t, f.repo, nil)
		took := time.Since(start)
		release()
		want := regexp.MustCompile(`"error":\{"code":-32603,"message":"Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: rename ` +
			regexp.QuoteMeta(jsonText(t, filepath.Join(filepath.Dir(copyOf), ".tmp-"))) + `[0-9]+ ` + regexp.QuoteMeta(jsonText(t, copyOf)) + `: Access is denied\."\}\}$`)
		if !want.MatchString(raw) {
			t.Errorf("git.info = %s\nwant a match of %s", raw, want)
		}
		if took < 300*time.Millisecond {
			t.Errorf("git.info took %v, want the retry of the bound", took)
		}
		if got := mustRead(t, copyOf); got != "ref: refs/heads/main\n" {
			t.Errorf("the copy of HEAD = %q, want the old copy", got)
		}
		noTempLeft(t, copyOf)
		if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"branch":"x"`) {
			t.Errorf("git.info after the release = %s", raw)
		}
	})
}
