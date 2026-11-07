//go:build linux

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// linkChain makes n links in d, each to the one before, the first to target. It
// returns the last link.
func linkChain(t *testing.T, d, name, target string, n int) string {
	t.Helper()
	prev := target
	for i := range n {
		next := filepath.Join(d, fmt.Sprintf("%s%02d", name, i+1))
		if err := os.Symlink(prev, next); err != nil {
			t.Fatal(err)
		}
		prev = next
	}
	return prev
}

// TestCheckLauncherFileLinkLoop pins the links fault of c9125eb8 (measured on Linux):
// a chain of 41 links, a loop of 2 and a self loop get links, with a right or a wrong
// digest. Chains of 5, 39 and 40 pass. Linux only, because the link limit of the
// system differs elsewhere.
func TestCheckLauncherFileLinkLoop(t *testing.T) {
	d := launcherDir(t)
	ok := writeLauncher(t, filepath.Join(d, "ok", "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, nil)
	right := LauncherCheck{SHA256: []string{digestOf(t, ok)}}
	for _, n := range []int{5, 39, 40} {
		L := linkChain(t, d, fmt.Sprintf("pass%d-", n), ok, n)
		checkFile(t, fmt.Sprintf("chain of %d", n), L, right, "", "")
	}
	links := func(L string) string {
		return "too many symbolic links (or a loop) lead to launcher `" + L + "`"
	}
	chain41 := linkChain(t, d, "chain41-", ok, 41)
	checkFile(t, "chain of 41", chain41, right, FaultLinks, links(chain41))
	loopa, loopb := filepath.Join(d, "loopa"), filepath.Join(d, "loopb")
	if err := os.Symlink(loopb, loopa); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loopa, loopb); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "loop of 2", loopa, right, FaultLinks, links(loopa))
	checkFile(t, "loop of 2, wrong digest", loopa, LauncherCheck{SHA256: []string{zeroDigest}}, FaultLinks, links(loopa))
	self := filepath.Join(d, "loopself")
	if err := os.Symlink(self, self); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "self loop", self, right, FaultLinks, links(self))
}
