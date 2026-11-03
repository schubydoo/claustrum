//go:build windows

package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/schubydoo/claustrum/internal/renameretry"
)

// The Windows side of the retry of a held rename (measured on Windows). No
// test of this file ran on the host of the build.

// holdOpen opens path with no delete sharing, as the holder does, and returns
// the release. A folder is opened with backup semantics.
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

// setRenameRetryBound sets renameretry.Bound until the test ends.
func setRenameRetryBound(t *testing.T, d time.Duration) {
	t.Helper()
	old := renameretry.Bound
	renameretry.Bound = d
	t.Cleanup(func() { renameretry.Bound = old })
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

// TestShadowCopyHeldOnWindows must match the measured cases: the copy of HEAD
// is held open while git.info writes a new copy. Released after 300 ms, the
// reply is the normal result and the copy has the new bytes. Held past the
// bound, the reply names both full paths, the old copy stays and no temp file
// is left. Without the retry the first case fails at once. Without
// renameretry.CopyError the text says `renameat .tmp-<n> HEAD`.
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
