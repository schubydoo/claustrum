//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
)

// TestRenameHeldIsWindowsOnly: on Linux and macOS no rename error is tried
// again, also not a denied one.
func TestRenameHeldIsWindowsOnly(t *testing.T) {
	for _, err := range []error{syscall.EACCES, syscall.EBUSY, syscall.EPERM} {
		if renameHeld(&os.LinkError{Op: "renameat", Old: "a", New: "b", Err: err}) {
			t.Errorf("renameHeld(%v) = true, want false", err)
		}
	}
}

// TestShadowCopyRenameTextOnLinuxAndMacOS: a failed rename of the copy write
// keeps the text of the root on Linux and macOS, `renameat .tmp-<n> HEAD:
// <cause>`. The form with both full paths is measured on Windows only (cell
// F1b of round task009-01). Here a folder with content is at the place of
// the copy of HEAD.
func TestShadowCopyRenameTextOnLinuxAndMacOS(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !regexp.MustCompile(`"settingsControl":"shadow"`).MatchString(raw) {
		t.Fatalf("git.info = %s", raw)
	}
	copyOf := filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "HEAD")
	if err := os.Remove(copyOf); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(copyOf, "f"), "f\n")
	runGit(t, f.repo, "checkout", "-q", "-b", "x")
	raw, _ := f.info(t, f.repo, nil)
	want := regexp.MustCompile(`"code":-32603,"message":"Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: renameat \.tmp-[0-9]+ HEAD: [^"]+"`)
	if !want.MatchString(raw) {
		t.Errorf("git.info = %s\nwant a match of %s", raw, want)
	}
}
