//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// replaceDaemonFolder runs one git.info, then puts an entry in place of the
// folder of the daemon by make. It returns the old folder path.
func replaceDaemonFolder(t *testing.T, f shadowFixture, make func(path string) error) string {
	t.Helper()
	f.info(t, f.repo, nil)
	first := daemonFolder(t, f.run)
	f.d.Shadow.mu.Lock()
	f.d.Shadow.dropLock()
	f.d.Shadow.mu.Unlock()
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if err := make(first); err != nil {
		t.Fatal(err)
	}
	return first
}

// requestsWithLimit runs git.info, git.status, git.list_branches and
// git.worktree_create, each within 15 s. A request that hangs fails the test.
func requestsWithLimit(t *testing.T, f shadowFixture) {
	t.Helper()
	for _, m := range f.fiveRequests(nil)[:4] {
		done := make(chan string, 1)
		go func() {
			raw, _ := f.call(t, m.method, m.params)
			done <- raw
		}()
		select {
		case raw := <-done:
			if strings.Contains(raw, `"error"`) {
				t.Errorf("%s = %s", m.method, raw)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("%s gave no reply in 15 s", m.method)
		}
	}
}

// TestShadowFolderFIFOIsMadeAFolderAgain: a FIFO at the folder of the daemon
// goes, and each request succeeds with no wait, as on 4c111e86 (measured on
// Linux and macOS).
func TestShadowFolderFIFOIsMadeAFolderAgain(t *testing.T) {
	f := newShadowFixture(t)
	first := replaceDaemonFolder(t, f, func(p string) error { return syscall.Mkfifo(p, 0o600) })
	requestsWithLimit(t, f)
	if _, err := os.Lstat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the FIFO: %v, want it gone", err)
	}
	if fi, err := os.Lstat(daemonFolder(t, f.run)); err != nil || !fi.IsDir() {
		t.Errorf("the folder of the daemon: %v, %v, want a folder", fi, err)
	}
}

// TestShadowFolderLinkGoesAsALink: a link at the folder of the daemon goes as
// a link, a new folder is made, and its target gets nothing. Each request
// succeeds, also with a link to nothing (4c111e86, measured on Linux and
// macOS).
func TestShadowFolderLinkGoesAsALink(t *testing.T) {
	for _, tc := range []struct {
		name string
		file bool
		gone bool
	}{{"empty target", false, false}, {"target with a file", true, false}, {"no target", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShadowFixture(t)
			target := filepath.Join(t.TempDir(), "T")
			if !tc.gone {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.file {
				mustWrite(t, filepath.Join(target, "keep"), "x")
			}
			first := replaceDaemonFolder(t, f, func(p string) error { return os.Symlink(target, p) })
			requestsWithLimit(t, f)
			if _, err := os.Lstat(first); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the link: %v, want it gone", err)
			}
			if fi, err := os.Lstat(daemonFolder(t, f.run)); err != nil || !fi.IsDir() {
				t.Errorf("the folder of the daemon: %v, %v, want a folder", fi, err)
			}
			switch {
			case tc.gone:
				if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("the target: %v, want nothing there", err)
				}
			case tc.file:
				if got := entryNames(t, target); len(got) != 1 || got[0] != "keep" {
					t.Errorf("the target holds %v, want keep only", got)
				}
			default:
				if got := entryNames(t, target); len(got) != 0 {
					t.Errorf("the target holds %v, want nothing", got)
				}
			}
		})
	}
}

// TestShadowFolderEntryHealsAtRetest: a FIFO, a link to a folder and a link
// to nothing at the folder of the daemon go at the test after a kept failure.
// The requests succeed, the link's target gets nothing and a missing target
// is not made (4c111e86, measured on Linux). Mutation: keep h.dir in
// startTest when its Lstat succeeds.
func TestShadowFolderEntryHealsAtRetest(t *testing.T) {
	for _, tc := range []struct {
		name string
		link bool
		gone bool
	}{{"FIFO", false, false}, {"link to a folder", true, false}, {"link to nothing", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShadowFixture(t)
			target := filepath.Join(t.TempDir(), "T")
			if tc.link && !tc.gone {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(target, "keep"), "x")
			}
			first := replaceAfterKeptFailure(t, f, func(p string) error {
				if tc.link {
					return os.Symlink(target, p)
				}
				return syscall.Mkfifo(p, 0o600)
			})
			wantHealedAtRetest(t, f, first)
			switch {
			case tc.gone:
				if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("the target: %v, want nothing there", err)
				}
			case tc.link:
				if got := entryNames(t, target); len(got) != 1 || got[0] != "keep" {
					t.Errorf("the target holds %v, want keep only", got)
				}
			}
		})
	}
}

// TestUnsearchableDaemonFolderTexts: with mode 0000 4c111e86 answers the
// mkdir of the folder of the repository, and at `.probe` the mkdir of
// `.probe` (measured on Linux, and in `shadow` on macOS).
func TestUnsearchableDaemonFolderTexts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0000")
	}
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	want := "cannot write its copy of the repository's git config: mkdir " + filepath.Join(dir, shadowRepoName(f.gitDir)) + ": permission denied"
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, want) {
		t.Errorf("git.info = %s, want %s", raw, want)
	}
	probeWant := "mkdir " + filepath.Join(dir, shadowProbeName) + ": permission denied"
	if err := f.d.Shadow.prepareProbeFolder(dir); err == nil || err.Error() != probeWant {
		t.Errorf("prepareProbeFolder = %v, want %s", err, probeWant)
	}
}

// TestSearchOnlyDaemonFolderProbe: with mode 0100 the call about the
// repository runs with the `.probe` that is there, and git.info succeeds, as
// on 4c111e86 after the kept answer is over (measured on Linux, and in `shadow` on macOS).
func TestSearchOnlyDaemonFolderProbe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0100")
	}
	f := newShadowFixture(t)
	now := setProbeClock(t)
	f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	*now = now.Add(probeKeep + 1)
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Errorf("git.info = %s, want a good reply", raw)
	}
	if n := len(probeCalls(calls)); n != 1 {
		t.Errorf("git.info made %d calls about the repository, want 1", n)
	}
}

// TestRemoveOwnFolderFileHomeGuard: a path that the home guard refuses stays.
func TestRemoveOwnFolderFileHomeGuard(t *testing.T) {
	run := resolveTestRoot(t, t.TempDir())
	h := NewShadowHost(run)
	file := filepath.Join(run, shadowRootName, "home")
	mustWrite(t, file, "")
	t.Setenv("HOME", file)
	if err := h.removeOwnFolderFile(file); err == nil {
		t.Error("remove of the home path: nil, want an error")
	}
	if _, err := os.Lstat(file); err != nil {
		t.Errorf("the home path: %v, want it kept", err)
	}
}

// TestRemoveOwnFolderFileAtKeepsAFolder: the remove itself never takes a
// folder, also an empty one that took the place of the file after the test.
func TestRemoveOwnFolderFileAtKeepsAFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := removeOwnFolderFileAt(root, "empty"); err == nil {
		t.Error("remove of an empty folder: nil, want an error")
	}
	if _, err := os.Lstat(filepath.Join(dir, "empty")); err != nil {
		t.Errorf("the empty folder: %v, want it kept", err)
	}
}
