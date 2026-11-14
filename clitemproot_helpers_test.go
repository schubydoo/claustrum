//go:build linux || darwin

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The helpers of the temp-folder tests on Linux and macOS.

// tempRootDir makes a temp dir X of mode 0700 whose chain passes the account test,
// and a run dir for the registry. With unfit, X holds claude-<uid> as an own folder
// of mode 0000, which is unfit and gets a folder (not readable by its owner).
func tempRootDir(t *testing.T, unfit bool) (x, runDir string) {
	t.Helper()
	base, err := os.MkdirTemp("", "ctr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { openTree(base); _ = os.RemoveAll(base) })
	// The precondition is tested here on its own, not by the code under test.
	for d := base; ; d = filepath.Dir(d) {
		var st unix.Stat_t
		if err := unix.Stat(d, &st); err != nil {
			t.Fatal(err)
		}
		if (int(st.Uid) != os.Getuid() && st.Uid != 0) || (st.Mode&0o022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			t.Skipf("the folder %s does not pass the account test on this host", d)
		}
		if d == "/" {
			break
		}
	}
	x, runDir = filepath.Join(base, "X"), filepath.Join(base, "run")
	for _, d := range []string{x, runDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if unfit {
		if err := os.Mkdir(filepath.Join(x, tempEntryName()), 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(x, tempEntryName()), 0); err != nil {
			t.Fatal(err)
		}
	}
	return x, runDir
}

// openTree gives each folder under root mode 0700, so that RemoveAll can remove a
// new folder of mode 0500 and its t.
func openTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if d != nil && d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
}

// unfitKind is the refused kind of the entry that tempRootDir makes with unfit.
const unfitKind = "not readable by its owner"

func sha256Hex8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func tempEntryName() string { return "claude-" + strconv.Itoa(os.Getuid()) }

// cleanTempRoots gives back each folder that a test left, at its end, and fails a
// test that left a live count.
func cleanTempRoots(t *testing.T, releases *[]func()) {
	t.Helper()
	t.Cleanup(func() {
		for _, r := range *releases {
			r()
		}
		// A release can hand its give-back to a goroutine while the lock is busy.
		deadline := time.Now().Add(2 * time.Second)
		live, held := 0, 0
		for {
			tempRoots.mu.Lock()
			live, held = tempRoots.live, len(tempRoots.held)
			tempRoots.mu.Unlock()
			if (live == 0 && held == 0) || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if live != 0 || held != 0 {
			t.Errorf("after the test: live %d, held %d, want 0 and 0", live, held)
		}
		tempRootsLost.mu.Lock()
		tempRootsLost.list = nil
		tempRootsLost.mu.Unlock()
	})
}

func ccdFolders(t *testing.T, x string) []string {
	t.Helper()
	ents, err := os.ReadDir(x)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "ccd-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// setTempRootCheckEvery shortens the time between two checks until the test ends.
func setTempRootCheckEvery(t *testing.T, d time.Duration) {
	t.Helper()
	old := tempRootCheckEvery
	tempRootCheckEvery = d
	t.Cleanup(func() { tempRootCheckEvery = old })
}

func tempRootsLive() int {
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	return tempRoots.live
}

// waitTempRootsLive polls the live count until it is n, with a limit.
func waitTempRootsLive(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for tempRootsLive() != n {
		if time.Now().After(deadline) {
			t.Fatalf("live count %d, want %d", tempRootsLive(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
