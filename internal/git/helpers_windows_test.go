//go:build windows

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// The test helpers of package main that the Windows tests of this package use,
// under the same names.

// resolveTestRoot canonicalizes a t.TempDir() root to the spelling the daemon
// will report for paths under it. On the GitHub Windows runners TEMP uses an
// 8.3 short name (C:\Users\RUNNER~1\...), while git rev-parse --show-toplevel
// reports the expanded long path — so expand it here before building fixtures.
func resolveTestRoot(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	pp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 512)
	n, err := windows.GetLongPathName(pp, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

// makeDirSymlink makes a directory symbolic link at link to target with
// mklink /D, or skips the test when the process cannot make one. That call
// needs the privilege to make symbolic links, or the developer mode.
func makeDirSymlink(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/D", link, target).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("mklink /D %s %s: %v\n%s", link, target, err, out)
		}
		t.Skipf("this process cannot make a directory symbolic link: mklink /D %s %s: %v\n%s", link, target, err, out)
	}
}

// makeJunction links link to target with mklink /J, or skips the test when the
// junction cannot be made.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
		}
		t.Skipf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
}
