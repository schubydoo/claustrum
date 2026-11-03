//go:build linux

package main

import (
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// errProcUnreadable is the sentinel the /proc stubs return, so the arms below
// fail for a reason this test owns rather than for "some error".
var errProcUnreadable = errors.New("seam: /proc entry unreadable")

// seamNS is a plausible pid-namespace link target.
const seamNS = "pid:[4026531836]"

// When our OWN pid namespace cannot be read there is nothing to compare a holder
// against, so pidNamespaceRefusal refuses with its own distinct reason — not the
// cross-namespace one, which is what a dropped guard would produce here (the
// stub answers the holder's link successfully).
func TestPidNamespaceRefusalSelfUnreadableLinux(t *testing.T) {
	selfLink := "/proc/self/ns/pid"
	holderLink := "/proc/" + strconv.Itoa(os.Getpid()) + "/ns/pid"
	prev := procinfo.SetReadlinkForTest(func(path string) (string, error) {
		switch path {
		case selfLink:
			return "partial-self", errProcUnreadable
		case holderLink:
			return seamNS, nil
		}
		return "", errProcUnreadable
	})
	t.Cleanup(func() { procinfo.SetReadlinkForTest(prev) })

	const want = "this process's pid namespace is unreadable"
	if got := pidNamespaceRefusal(os.Getpid()); got != want {
		t.Errorf("pidNamespaceRefusal with an unreadable own namespace = %q, want %q", got, want)
	}
}
