//go:build linux

package procinfo

import (
	"errors"
	"syscall"
	"testing"
)

// TestRootHooksSwapTheMount: the setter returns the old mount, and Root reads the new one.
func TestRootHooksSwapTheMount(t *testing.T) {
	old := procRoot
	t.Cleanup(func() { procRoot = old })
	procRoot = "/base"
	prev := SetRootForTest("/fake")
	during := Root()
	SetRootForTest(prev)
	if prev != "/base" || during != "/fake" || Root() != "/base" {
		t.Errorf("mount = %q before, %q during, %q after; want /base, /fake, /base", prev, during, Root())
	}
}

// TestLinuxSeamHooksReturnTheOldValue: each getter returns the seam itself, so a stand-in
// that wraps it ends. Each setter returns the seam that it replaced.
func TestLinuxSeamHooksReturnTheOldValue(t *testing.T) {
	oldLink, oldOpen, oldSend, oldKill := osReadlink, hcPidfdOpen, hcPidfdSend, hcKillPid
	t.Cleanup(func() { osReadlink, hcPidfdOpen, hcPidfdSend, hcKillPid = oldLink, oldOpen, oldSend, oldKill })
	errBase := errors.New("base")
	osReadlink = func(string) (string, error) { return "base", nil }
	hcPidfdOpen = func(int) (int, error) { return 7, nil }
	hcPidfdSend = func(int, syscall.Signal) error { return errBase }
	hcKillPid = func(int, syscall.Signal) error { return errBase }

	baseLink, baseOpen := ReadlinkForTest(), PidfdOpenForTest()
	baseSend, baseKill := PidfdSendForTest(), KillPidForTest()
	prevLink := SetReadlinkForTest(func(n string) (string, error) { s, err := baseLink(n); return "wrap " + s, err })
	prevOpen := SetPidfdOpenForTest(func(p int) (int, error) { fd, err := baseOpen(p); return fd + 1, err })
	prevSend := SetPidfdSendForTest(func(fd int, sig syscall.Signal) error { return baseSend(fd, sig) })
	prevKill := SetKillPidForTest(func(pid int, sig syscall.Signal) error { return baseKill(pid, sig) })
	if s, _ := prevLink("x"); s != "base" {
		t.Errorf("old link read = %q, want base", s)
	}
	if fd, _ := prevOpen(1); fd != 7 {
		t.Errorf("old pidfd_open = %d, want 7", fd)
	}
	if prevSend(1, 0) != errBase || prevKill(1, 0) != errBase {
		t.Error("the old pidfd_send_signal or kill is not the base one")
	}

	if s, _ := Readlink("x"); s != "wrap base" {
		t.Errorf("Readlink = %q, want wrap base", s)
	}
	if s, _ := ReadlinkForTest()("x"); s != "wrap base" {
		t.Errorf("ReadlinkForTest() = %q, want wrap base", s)
	}
	if fd, _ := PidfdOpenForTest()(1); fd != 8 {
		t.Errorf("PidfdOpenForTest() = %d, want 8", fd)
	}
	if err := PidfdSendForTest()(1, 0); err != errBase {
		t.Errorf("PidfdSendForTest() = %v, want base", err)
	}
	if err := KillPidForTest()(1, 0); err != errBase {
		t.Errorf("KillPidForTest() = %v, want base", err)
	}
	SetReadlinkForTest(prevLink)
	SetPidfdOpenForTest(prevOpen)
	if s, _ := Readlink("x"); s != "base" {
		t.Errorf("Readlink after the restore = %q, want base", s)
	}
	if fd, _ := hcPidfdOpen(1); fd != 7 {
		t.Errorf("pidfd_open after the restore = %d, want 7", fd)
	}
}
