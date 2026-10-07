//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cell A-10. A file symlink as path gets the hooks refusal with the start error of the
// listing, as a regular file does. The test skips when the user cannot make a symlink.
func TestStartErrorInFileSymlinkWindows(t *testing.T) {
	f := newNoRepoFixture(t)
	nFile := filepath.Join(f.n, "n.txt")
	writeFile(t, nFile, "n\n", 0o644)
	lnk := filepath.Join(f.n, "lnk")
	if err := os.Symlink(nFile, lnk); err != nil {
		t.Skipf("no symlink on this host: %v", err)
	}
	start := gitStartError(t, lnk)
	if start == "" {
		t.Fatalf("git started in the file symlink %s", lnk)
	}
	if got, want := f.frame(t, "git.info", map[string]any{"path": lnk}), errFrame(t, hooksRefusalPrefix+start); got != want {
		t.Errorf("A-10 = %s\nwant %s", got, want)
	}
}

// Cell A-13. A folder with a full path of more than 260 characters holds no
// repository, and git cannot start in it. git.info answers the hooks refusal with the
// start error of the listing. The test skips on a host that starts a process there.
func TestStartErrorInLongFolderWindows(t *testing.T) {
	f := newNoRepoFixture(t)
	long := filepath.Join(f.n, strings.Repeat("a", 90), strings.Repeat("b", 90), strings.Repeat("c", 90))
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	if len(long) <= 260 {
		t.Fatalf("the folder path has %d characters, want more than 260", len(long))
	}
	start := gitStartError(t, long)
	if start == "" {
		t.Skip("this host starts a process in a folder with a long path")
	}
	if got, want := f.frame(t, "git.info", map[string]any{"path": long}), errFrame(t, hooksRefusalPrefix+start); got != want {
		t.Errorf("A-13 = %s\nwant %s", got, want)
	}
}
