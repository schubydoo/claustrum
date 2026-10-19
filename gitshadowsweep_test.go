package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests of the sweep of git-shadow and of the limit of repository folders.
// The cells are those of round task075-01 (Linux VM). Each test stages its
// entries in its own temp socket folder.

// deadFolderName is a name of a folder of this host whose daemon is gone.
func deadFolderName() string {
	return hostForm() + "-1-0123456789abcdef"
}

// plantFolder makes the folder name in <run>/git-shadow with a file and a
// subfolder with a file. With lock it also has an empty lock file that no one
// holds, as the folder of a daemon that is gone has it.
func plantFolder(t *testing.T, run, name string, lock bool) string {
	t.Helper()
	dir := filepath.Join(run, shadowRootName, name)
	writeFile(t, filepath.Join(dir, "sub", "f2"), "f2", 0o600)
	writeFile(t, filepath.Join(dir, "f1"), "f1", 0o600)
	if lock {
		writeFile(t, filepath.Join(dir, shadowLockName), "", 0o600)
	}
	return dir
}

// ageEntry sets the mtime of path to age before now.
func ageEntry(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// symlinkOrSkip makes the link, or skips the test where the system gives no
// link to this user.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic link here: %v", err)
	}
}

// ownFolderName is the name of the folder of the daemon of f.
func ownFolderName(f shadowFixture) string {
	f.s.gitShadow.mu.Lock()
	defer f.s.gitShadow.mu.Unlock()
	return filepath.Base(f.s.gitShadow.dir)
}

// sweptGone fails the test when path is still there.
func sweptGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s: %v, want it gone", path, err)
	}
}

// sweptThere fails the test when path is not there.
func sweptThere(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s: %v, want it there", path, err)
	}
}

// Cell G1: the folder of a daemon that is gone goes at the first request that
// makes the folder of this daemon, and not at the start of the daemon.
func TestShadowSweepRemovesDeadFolder(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	// The daemon started (newShadowFixture), and no request ran: the folder stays.
	sweptThere(t, dead)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	sweptGone(t, dead)
	if got := daemonFolders(t, f.run); !slices.Equal(got, []string{ownFolderName(f)}) {
		t.Errorf("git-shadow holds %v, want the folder of the daemon only", got)
	}
}

// Cells G2 and G3: a folder whose lock is held stays. Here the test holds the
// lock as a live daemon does.
func TestShadowSweepKeepsHeldFolder(t *testing.T) {
	f := newShadowFixture(t)
	live := plantFolder(t, f.run, deadFolderName(), false)
	lock, err := openShadowLockFile(filepath.Join(live, shadowLockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	f.info(t, f.repo, nil)
	sweptThere(t, filepath.Join(live, "sub", "f2"))
	sweptThere(t, filepath.Join(live, "f1"))
}

// Cells G7, G7b and G7c: a folder with another host form goes when it is 8
// days old and stays at 6 days (the bound of 7 days is not measured). On Linux and macOS the age is read after the lock step:
// a folder with no lock file gets one, so it is new then and stays. On Windows
// the age is read before, and that folder goes (Windows VM).
func TestShadowSweepAgeOfOtherHosts(t *testing.T) {
	f := newShadowFixture(t)
	noLock := plantFolder(t, f.run, "otherhost-1-0123456789abcdef", false)
	ageEntry(t, noLock, 8*24*time.Hour)
	oldLock := plantFolder(t, f.run, "otherhost-2-0123456789abcdef", true)
	ageEntry(t, oldLock, 8*24*time.Hour)
	young := plantFolder(t, f.run, "otherhost-3-0123456789abcdef", true)
	ageEntry(t, young, 6*24*time.Hour)

	f.info(t, f.repo, nil)
	if runtime.GOOS == "windows" {
		// G7 on Windows: removed.
		sweptGone(t, noLock)
	} else {
		// G7: kept, with a new lock file of mode 0600.
		sweptThere(t, filepath.Join(noLock, "f1"))
		if fi, err := os.Stat(filepath.Join(noLock, shadowLockName)); err != nil || fi.Size() != 0 {
			t.Errorf("the lock of the folder with no lock: %v, %v, want a new empty file", fi, err)
		}
	}
	// G7b: removed.
	sweptGone(t, oldLock)
	// G7c: kept.
	sweptThere(t, filepath.Join(young, "f1"))
}

// Cell G8: a regular file with a name of this host stays.
func TestShadowSweepKeepsAFile(t *testing.T) {
	f := newShadowFixture(t)
	file := filepath.Join(f.run, shadowRootName, deadFolderName())
	writeFile(t, file, "x", 0o600)
	f.info(t, f.repo, nil)
	if b, err := os.ReadFile(file); err != nil || string(b) != "x" {
		t.Errorf("the file: %q, %v, want it unchanged", b, err)
	}
}

// Cell G9: a folder whose lock is a link stays, and the target of the link is
// unchanged. The second folder has a link to a file inside git-shadow.
func TestShadowSweepKeepsALinkedLock(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), false)
	v := filepath.Join(resolveTestRoot(t, t.TempDir()), "V")
	writeFile(t, v, "V content\n", 0o600)
	symlinkOrSkip(t, v, filepath.Join(dead, shadowLockName))
	inner := plantFolder(t, f.run, hostForm()+"-2-0123456789abcdef", false)
	vIn := filepath.Join(f.run, shadowRootName, "Vin")
	writeFile(t, vIn, "", 0o600)
	symlinkOrSkip(t, filepath.Join("..", filepath.Base(vIn)), filepath.Join(inner, shadowLockName))
	f.info(t, f.repo, nil)
	sweptThere(t, filepath.Join(dead, "f1"))
	sweptThere(t, filepath.Join(inner, "f1"))
	if b, err := os.ReadFile(v); err != nil || string(b) != "V content\n" {
		t.Errorf("V: %q, %v, want it unchanged", b, err)
	}
}

// The prefix of this host: each character outside A-Z, a-z, 0-9 and "." is
// "_". A "-" and a kept "." are measured (round task075-01). The other
// characters are not measured.
func TestHostFormRule(t *testing.T) {
	old := shadowHostname
	shadowHostname = func() (string, error) { return "Ab-9_c d+e.local", nil }
	t.Cleanup(func() { shadowHostname = old })
	if got, want := hostForm(), "Ab_9_c_d_e.local"; got != want {
		t.Errorf("hostForm() = %q, want %q", got, want)
	}
}

// Cell G10: a link in git-shadow with a name of this host goes as a link. The
// content of its target stays, and claustrum makes no lock file there (D8).
func TestShadowSweepRemovesAPlantedLink(t *testing.T) {
	f := newShadowFixture(t)
	target := filepath.Join(resolveTestRoot(t, t.TempDir()), "F")
	writeFile(t, filepath.Join(target, "f1"), "f1", 0o600)
	writeFile(t, filepath.Join(target, "sub", "f2"), "f2", 0o600)
	if err := os.MkdirAll(filepath.Join(f.run, shadowRootName), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.run, shadowRootName, deadFolderName())
	symlinkOrSkip(t, target, link)
	f.info(t, f.repo, nil)
	sweptGone(t, link)
	if got := entryNames(t, target); !slices.Equal(got, []string{"f1", "sub"}) {
		t.Errorf("the target holds %v, want f1 and sub only", got)
	}
	sweptThere(t, filepath.Join(target, "sub", "f2"))
}

// A link whose target holds a lock in use stays, and so does a link to a file
// (claustrum's own, not measured).
func TestShadowSweepKeepsHeldLinks(t *testing.T) {
	f := newShadowFixture(t)
	top := resolveTestRoot(t, t.TempDir())
	held := filepath.Join(top, "held")
	writeFile(t, filepath.Join(held, "f1"), "f1", 0o600)
	lock, err := openShadowLockFile(filepath.Join(held, shadowLockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	file := filepath.Join(top, "file")
	writeFile(t, file, "x", 0o600)
	if err := os.MkdirAll(filepath.Join(f.run, shadowRootName), 0o700); err != nil {
		t.Fatal(err)
	}
	toHeld := filepath.Join(f.run, shadowRootName, hostForm()+"-2-0123456789abcdef")
	toFile := filepath.Join(f.run, shadowRootName, hostForm()+"-3-0123456789abcdef")
	symlinkOrSkip(t, held, toHeld)
	symlinkOrSkip(t, file, toFile)
	f.info(t, f.repo, nil)
	sweptThere(t, toHeld)
	sweptThere(t, toFile)
}

// Cell G11: with git-shadow a link to a folder, the folder of the daemon is in
// that folder and the sweep removes a dead folder there.
func TestShadowSweepThroughALinkedRoot(t *testing.T) {
	f := newShadowFixture(t)
	target := filepath.Join(resolveTestRoot(t, t.TempDir()), "shadowT")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, target, filepath.Join(f.run, shadowRootName))
	plantFolder(t, f.run, deadFolderName(), true)
	deadIn := filepath.Join(target, deadFolderName())
	sweptThere(t, deadIn)
	f.info(t, f.repo, nil)
	sweptGone(t, deadIn)
	sweptThere(t, filepath.Join(target, ownFolderName(f)))
}

// Cell G12: when the folder of the daemon is gone, the request that makes it
// again sweeps again.
func TestShadowSweepAgainAtANewFolder(t *testing.T) {
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	first := daemonFolder(t, f.run)
	f.s.gitShadow.mu.Lock()
	f.s.gitShadow.dropLock()
	f.s.gitShadow.mu.Unlock()
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	dead := plantFolder(t, f.run, deadFolderName(), true)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info after the folder went = %s", raw)
	}
	sweptGone(t, dead)
	if got := daemonFolders(t, f.run); len(got) != 1 || got[0] == filepath.Base(first) {
		t.Errorf("git-shadow holds %v, want one new folder", got)
	}
}

// The sweep skips the folder of this daemon. A second daemon of
// this host in the same socket folder keeps the folder of the first, whose
// lock is held (cell G2).
func TestShadowSweepKeepsALiveDaemon(t *testing.T) {
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	first := daemonFolder(t, f.run)
	other := newTestServer(t)
	other.gitShadow = newShadowHost(f.run)
	t.Cleanup(other.gitShadow.close)
	dispatchRaw(t, other, rpcLine(t, "git.info", map[string]any{"path": f.repo}))
	sweptThere(t, filepath.Join(first, shadowLockName))
	sweptThere(t, filepath.Join(first, shadowRepoName(filepath.Join(f.repo, ".git"))))
}

// A folder of this host with no lock file that is less than a minute old
// stays, and it gets no lock file. Another daemon can be between its mkdir
// and its lock. At 2 minutes it goes. Both are claustrum's own (not measured).
func TestShadowSweepKeepsANewFolderWithNoLock(t *testing.T) {
	f := newShadowFixture(t)
	young := plantFolder(t, f.run, deadFolderName(), false)
	old := plantFolder(t, f.run, hostForm()+"-2-0123456789abcdef", false)
	ageEntry(t, old, 2*time.Minute)
	f.info(t, f.repo, nil)
	if got := entryNames(t, young); !slices.Equal(got, []string{"f1", "sub"}) {
		t.Errorf("the new folder holds %v, want f1 and sub only", got)
	}
	sweptGone(t, old)
}

// lockSeam replaces openShadowLock for one test. first runs at the first call
// in place of the open, and each later call opens as production does. The
// count of calls is returned.
func lockSeam(t *testing.T, first func(path string) (*os.File, error)) *int {
	t.Helper()
	old := openShadowLock
	calls := 0
	openShadowLock = func(path string) (*os.File, error) {
		calls++
		if calls == 1 {
			return first(path)
		}
		return old(path)
	}
	t.Cleanup(func() { openShadowLock = old })
	return &calls
}

// makeTestFolder runs makeFolder on a new host in a temp socket folder.
func makeTestFolder(t *testing.T) (*shadowHost, error) {
	t.Helper()
	h := newShadowHost(t.TempDir())
	t.Cleanup(h.close)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h, h.makeFolder()
}

// The sweep of another daemon holds the lock of the new folder before this
// daemon takes it. The folder is made again with a new name, and the first
// folder is left to that sweep (claustrum's own, not measured).
func TestShadowFolderAgainAfterABusyLock(t *testing.T) {
	var first string
	var held *os.File
	calls := lockSeam(t, func(path string) (*os.File, error) {
		first = filepath.Dir(path)
		var err error
		if held, err = openShadowLockFile(path); err != nil {
			t.Fatal(err)
		}
		return openShadowLockFile(path)
	})
	h, err := makeTestFolder(t)
	if held != nil {
		t.Cleanup(func() { _ = held.Close() })
	}
	if err != nil {
		t.Fatalf("makeFolder = %v, want nil", err)
	}
	if *calls != 2 || h.dir == first || h.lock == nil {
		t.Errorf("calls %d, folder %s (first %s), want a second folder", *calls, h.dir, first)
	}
	sweptThere(t, filepath.Join(first, shadowLockName))
}

// The sweep of another daemon removed the new folder before its lock was
// opened. The folder is made again (claustrum's own, not measured).
func TestShadowFolderAgainAfterItWent(t *testing.T) {
	calls := lockSeam(t, func(path string) (*os.File, error) {
		if err := os.RemoveAll(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		return openShadowLockFile(path)
	})
	h, err := makeTestFolder(t)
	if err != nil {
		t.Fatalf("makeFolder = %v, want nil", err)
	}
	if *calls != 2 {
		t.Errorf("%d lock steps, want 2", *calls)
	}
	sweptThere(t, filepath.Join(h.dir, shadowLockName))
}
