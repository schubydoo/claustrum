//go:build linux

package install

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// honestLauncher makes a link-free launcher file in a temp folder.
func honestLauncher(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// A mount id that differs from the first look is a moved item, even when the device and
// the inode agree (a bind mount over a folder with the same device and inode).
func TestLauncherMovedMountIDCounts(t *testing.T) {
	target := honestLauncher(t)
	look, err := lookLauncherPath(target)
	if err != nil {
		t.Fatalf("lookLauncherPath: %v", err)
	}
	if f, r := launcherMoved(target, look); f != "" {
		t.Fatalf("an honest launcher was refused: %s %s", f, r)
	}
	bad := append([]launcherID(nil), look...)
	bad[len(bad)-1].mnt++
	if f, _ := launcherMoved(target, bad); f != FaultWritable {
		t.Fatalf("a differing mount id alone was not refused: fault %q", f)
	}
}

// Where statx is refused (an old kernel, or a seccomp filter that answers EPERM), the
// check falls back to fstatat and an honest launcher still passes.
func TestLauncherIDFallsBackWithoutStatx(t *testing.T) {
	for _, refusal := range []error{unix.ENOSYS, unix.EPERM} {
		old := statxCall
		statxCall = func(int, string, int, int, *unix.Statx_t) error { return refusal }
		target := honestLauncher(t)
		look, err := lookLauncherPath(target)
		if err != nil {
			statxCall = old
			t.Fatalf("%v: lookLauncherPath failed with no statx: %v", refusal, err)
		}
		f, r := launcherMoved(target, look)
		statxCall = old
		if f != "" {
			t.Fatalf("%v: an honest launcher was refused with no statx: %s %s", refusal, f, r)
		}
		for _, id := range look {
			if id.mnt != 0 || id.ino == 0 {
				t.Fatalf("%v: fallback id %+v, want an inode and no mount id", refusal, id)
			}
		}
	}
}
