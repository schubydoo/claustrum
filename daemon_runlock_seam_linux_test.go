//go:build linux

package main

import (
	"errors"
	"os"
	"strconv"
	"testing"
)

// errProcUnreadable is the sentinel the /proc stubs return, so the arms below
// fail for a reason this test owns rather than for "some error".
var errProcUnreadable = errors.New("seam: /proc entry unreadable")

// seamNS is a plausible pid-namespace link target; the exact bytes only matter
// in that nodeID must join them to the boot id with a single "/".
const seamNS = "pid:[4026531836]"

// stubProcReads swaps the two /proc seams for the rest of the test. A nil
// readBoot or readLink leaves that seam untouched.
func stubProcReads(t *testing.T, readBoot func() ([]byte, error), readLink func(string) (string, error)) {
	t.Helper()
	oldBoot, oldLink := readBootID, osReadlink
	t.Cleanup(func() { readBootID, osReadlink = oldBoot, oldLink })
	if readBoot != nil {
		readBootID = readBoot
	}
	if readLink != nil {
		osReadlink = readLink
	}
}

// selfNSOnly answers the pid-namespace link for every path with ns, which is
// what a single-namespace host looks like.
func selfNSOnly(ns string) func(string) (string, error) {
	return func(string) (string, error) { return ns, nil }
}

// nodeID returns "" — the value that makes every signal guard refuse eviction —
// on each of its three unavailable-part arms. Each stub deliberately returns a
// NON-empty payload alongside the failure, so dropping the arm would produce a
// visibly different node string rather than "" by accident.
func TestNodeIDUnavailablePartsLinux(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readBoot func() ([]byte, error)
		readLink func(string) (string, error)
		want     string
	}{
		{
			name:     "boot_id unreadable",
			readBoot: func() ([]byte, error) { return []byte("partial-boot"), errProcUnreadable },
			readLink: selfNSOnly(seamNS),
			want:     "",
		},
		{
			name:     "boot_id blank",
			readBoot: func() ([]byte, error) { return []byte("  \n\t "), nil },
			readLink: selfNSOnly(seamNS),
			want:     "",
		},
		{
			name:     "pid-namespace link unreadable",
			readBoot: func() ([]byte, error) { return []byte("boot-abc\n"), nil },
			readLink: func(string) (string, error) { return "partial-ns", errProcUnreadable },
			want:     "",
		},
		{
			name:     "both parts present (control)",
			readBoot: func() ([]byte, error) { return []byte("  boot-abc\n"), nil },
			readLink: selfNSOnly(seamNS),
			want:     "boot-abc/" + seamNS,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubProcReads(t, tc.readBoot, tc.readLink)
			if got := nodeID(); got != tc.want {
				t.Errorf("nodeID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// When our OWN pid namespace cannot be read there is nothing to compare a holder
// against, so pidNamespaceRefusal refuses with its own distinct reason — not the
// cross-namespace one, which is what a dropped guard would produce here (the
// stub answers the holder's link successfully).
func TestPidNamespaceRefusalSelfUnreadableLinux(t *testing.T) {
	selfLink := "/proc/self/ns/pid"
	holderLink := "/proc/" + strconv.Itoa(os.Getpid()) + "/ns/pid"
	stubProcReads(t, nil, func(path string) (string, error) {
		switch path {
		case selfLink:
			return "partial-self", errProcUnreadable
		case holderLink:
			return seamNS, nil
		}
		return "", errProcUnreadable
	})

	const want = "this process's pid namespace is unreadable"
	if got := pidNamespaceRefusal(os.Getpid()); got != want {
		t.Errorf("pidNamespaceRefusal with an unreadable own namespace = %q, want %q", got, want)
	}
}
