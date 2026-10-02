//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// The cells of install_homeguard_test.go with a junction as the link. Measured
// on a Windows VM against 89cb6289: the reference removes the fixture home in
// the cells of these shapes (HJ1 to HJ4, HJ10 and HJ12), and it replaces the
// folder of the control (HJ9).
//
// A junction needs no privilege, so a failed mklink fails the cell. The shared
// makeJunction helper skips on that failure, and this test does not use it.
func TestInstallHomeGuardResolvesJunctions(t *testing.T) {
	homeGuardLinkCells(t, func(t *testing.T, link, target string) {
		t.Helper()
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
		}
	})
}
