//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestInstallWorktreeIndexSetgidGroup pins row A8 (Linux VM). The registration has a
// second group of the user and the setgid bit, and the index gets that group. The
// temporary index keeps the primary group, so a moved file does not pass.
func TestInstallWorktreeIndexSetgidGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	second := -1
	for _, g := range groups {
		if g != os.Getegid() {
			second = g
			break
		}
	}
	if second < 0 {
		t.Skip("the test user has one group only, so no second group can own the registration")
	}
	src, srcInfo := indexSource(t)
	if gid := srcInfo.Sys().(*syscall.Stat_t).Gid; int(gid) == second {
		t.Skipf("the temporary folder already gives group %d, so the index group tells nothing", second)
	}
	adminDir := filepath.Join(t.TempDir(), "worktrees", "w1")
	mkdirForTest(t, adminDir)
	if err := os.Chown(adminDir, -1, second); err != nil {
		t.Skipf("chown to group %d: %v", second, err)
	}
	// Chmod after Chown, because a chown clears the setgid bit.
	if err := os.Chmod(adminDir, 0o755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if err := installWorktreeIndex(src, adminDir); err != nil {
		t.Fatalf("installWorktreeIndex: %v", err)
	}
	fi, err := os.Stat(filepath.Join(adminDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if gid := fi.Sys().(*syscall.Stat_t).Gid; int(gid) != second {
		t.Errorf("index group = %d, want %d, the group of the setgid registration", gid, second)
	}
}
