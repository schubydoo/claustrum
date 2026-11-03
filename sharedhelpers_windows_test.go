//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"
)

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
