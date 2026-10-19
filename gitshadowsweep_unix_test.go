//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// Cell G4: a lock file that another process holds open with no flock does not
// stop the sweep.
func TestShadowSweepIgnoresAnOpenFile(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	open, err := os.Open(filepath.Join(dead, shadowLockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = open.Close() })
	f.info(t, f.repo, nil)
	sweptGone(t, dead)
}

// Cell G5: a folder of mode 000 stays, and it gets no lock file.
func TestShadowSweepKeepsAClosedFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 000")
	}
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), false)
	if err := os.Chmod(dead, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dead, 0o700) })
	f.info(t, f.repo, nil)
	if err := os.Chmod(dead, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := entryNames(t, dead); !slices.Equal(got, []string{"f1", "sub"}) {
		t.Errorf("the folder of mode 000 holds %v, want f1 and sub", got)
	}
}

// Cell G6: a remove that fails at a subfolder of mode 000 removes the rest, and
// the folder and that subfolder stay. Each request answers. 5fd08069 makes a
// lock file there again at the next sweep and removes it again. claustrum
// skips that folder while it is new and has no lock file (shadowSweepNewAge).
func TestShadowSweepLeavesWhatItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a folder of mode 000")
	}
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	sub0 := filepath.Join(dead, "sub0")
	writeFile(t, filepath.Join(sub0, "x"), "x", 0o600)
	if err := os.Chmod(sub0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub0, 0o700) })
	for i := range 2 {
		if raw, _ := f.info(t, f.repo, nil); raw == "" {
			t.Fatalf("request %d gave no reply", i)
		}
		if got := entryNames(t, dead); !slices.Equal(got, []string{"sub0"}) {
			t.Errorf("sweep %d: the folder holds %v, want sub0 only", i, got)
		}
		// The own folder goes, so the next request makes it and sweeps again.
		f.s.gitShadow.mu.Lock()
		f.s.gitShadow.dropLock()
		own := f.s.gitShadow.dir
		f.s.gitShadow.mu.Unlock()
		if err := os.RemoveAll(own); err != nil {
			t.Fatal(err)
		}
	}
}

// The lock file of the daemon is opened with no follow of the last name, so a
// link there fails and its target stays (`O_NOFOLLOW` in round task075-01,
// Linux VM).
func TestShadowLockFollowsNoLink(t *testing.T) {
	dir := t.TempDir()
	v := filepath.Join(dir, "V")
	writeFile(t, v, "V content\n", 0o600)
	link := filepath.Join(dir, shadowLockName)
	mustSymlink(t, v, link)
	if f, err := openShadowLockFile(link); !errors.Is(err, syscall.ELOOP) {
		if f != nil {
			_ = f.Close()
		}
		t.Errorf("open of a lock that is a link: %v, want ELOOP", err)
	}
	if b, err := os.ReadFile(v); err != nil || string(b) != "V content\n" {
		t.Errorf("V: %q, %v", b, err)
	}
}

// After the lock step the folder and the lock file at the path must be the
// ones that were made and locked. A lock file that went and a folder that was
// replaced each make the folder again (claustrum's own, not measured).
func TestShadowFolderAgainAfterASwap(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, path string)
	}{
		{"the lock file went", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"the folder was replaced", func(t *testing.T, path string) {
			dir := filepath.Dir(path)
			// The held lock file moves into the new folder, so that only the
			// folder differs.
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(dir+".old", shadowLockName), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first string
			calls := lockSeam(t, func(path string) (*os.File, error) {
				first = filepath.Dir(path)
				lock, err := openShadowLockFile(path)
				if err == nil {
					tc.swap(t, path)
				}
				return lock, err
			})
			h, err := makeTestFolder(t)
			if err != nil {
				t.Fatalf("makeFolder = %v, want nil", err)
			}
			if *calls != 2 || h.dir == first {
				t.Errorf("calls %d, folder %s (first %s), want a second folder", *calls, h.dir, first)
			}
			sweptThere(t, filepath.Join(h.dir, shadowLockName))
		})
	}
}
