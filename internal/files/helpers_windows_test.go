//go:build windows

package files

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// resolveTestRoot canonicalizes a t.TempDir() root to the spelling the daemon
// will report for paths under it. On the GitHub Windows runners TEMP uses an
// 8.3 short name (C:\Users\RUNNER~1\...), so expand it here before building
// fixtures.
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

// shortPathName is the 8.3 spelling of p. The test skips when the volume makes no
// short names.
func shortPathName(t *testing.T, p string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("no short name for %s: %v", p, err)
	}
	s := windows.UTF16ToString(buf[:n])
	if !strings.Contains(s, "~") {
		t.Skipf("the volume makes no 8.3 names: %s", s)
	}
	return s
}

// wipesHomeDir is homeguard.WipesHomeDir under the name that the tests of this
// package use.
func wipesHomeDir(p string) bool { return homeguard.WipesHomeDir(p) }
