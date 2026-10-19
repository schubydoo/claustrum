//go:build windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Cell G10j: a junction in git-shadow with a name of this host goes as a
// junction. The content of its target stays, and claustrum makes no lock file
// there (D8, cell G10j of round task075-02, Windows VM).
func TestShadowSweepRemovesAPlantedJunction(t *testing.T) {
	f := newShadowFixture(t)
	target := filepath.Join(resolveTestRoot(t, t.TempDir()), "F")
	writeFile(t, filepath.Join(target, "f1"), "f1", 0o600)
	if err := os.MkdirAll(filepath.Join(f.run, shadowRootName), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.run, shadowRootName, deadFolderName())
	makeJunction(t, link, target)
	f.info(t, f.repo, nil)
	sweptGone(t, link)
	if got := entryNames(t, target); !slices.Equal(got, []string{"f1"}) {
		t.Errorf("the target holds %v, want f1 only", got)
	}
}

// Cell G3 on Windows: a lock that another process holds open with no sharing
// keeps its folder (Windows VM). The test holds the lock in that way.
func TestShadowSweepKeepsANoShareLock(t *testing.T) {
	f := newShadowFixture(t)
	dead := plantFolder(t, f.run, deadFolderName(), true)
	held, err := openNoShare(filepath.Join(dead, shadowLockName), 0x80000000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	f.info(t, f.repo, nil)
	sweptThere(t, filepath.Join(dead, "f1"))
}
