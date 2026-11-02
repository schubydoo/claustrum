//go:build windows

package install

import (
	"os/exec"
	"testing"
)

// The cells of install_homeguard_test.go with a junction as the link. Measured
// on Windows against 89cb6289: the reference removes the fixture home in the
// cells of these shapes, and it replaces the
// folder of the control.
//
// A junction needs no privilege, so a failed mklink fails the cell. The shared
// makeJunction helper of package main skips on that failure, and this test does not use it.
func TestInstallHomeGuardResolvesJunctions(t *testing.T) {
	homeGuardLinkCells(t, func(t *testing.T, link, target string) {
		t.Helper()
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
		}
	})
}
