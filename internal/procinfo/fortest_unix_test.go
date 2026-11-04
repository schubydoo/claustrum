//go:build linux || darwin

package procinfo

import (
	"syscall"
	"testing"
)

// noSuchPid is the pid of the fake group behind the killGroup seam. It is above the pid
// limit of Linux and macOS, so it names no group. KillGroup refuses a pid below 2.
const noSuchPid = 1 << 30

// TestKillGroupHooksSwapTheSeam: the getter returns the seam, so a stand-in that wraps it
// ends. The setter returns the old seam, and KillGroup goes through the new one.
func TestKillGroupHooksSwapTheSeam(t *testing.T) {
	old := killGroup
	t.Cleanup(func() { killGroup = old })
	var calls []string
	killGroup = func(int, syscall.Signal) error { calls = append(calls, "base"); return nil }
	record := KillGroupForTest()
	prev := SetKillGroupForTest(func(pid int, sig syscall.Signal) error {
		calls = append(calls, "wrap")
		return record(pid, sig)
	})
	if err := KillGroup(noSuchPid, 0); err != nil {
		t.Fatal(err)
	}
	if err := prev(noSuchPid, 0); err != nil {
		t.Fatal(err)
	}
	SetKillGroupForTest(prev)
	if err := KillGroup(noSuchPid, 0); err != nil {
		t.Fatal(err)
	}
	if want := []string{"wrap", "base", "base", "base"}; len(calls) != len(want) ||
		calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] || calls[3] != want[3] {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

// TestKillGroupRefusesASmallPid: KillGroup returns an error for pid 0 and pid 1 and never
// calls the seam. Through the seam, 0 names the group of the daemon and 1 names every
// process of the user.
func TestKillGroupRefusesASmallPid(t *testing.T) {
	old := killGroup
	t.Cleanup(func() { killGroup = old })
	var calls []int
	killGroup = func(pid int, _ syscall.Signal) error { calls = append(calls, pid); return nil }
	for _, pid := range []int{0, 1} {
		if err := KillGroup(pid, syscall.SIGKILL); err == nil {
			t.Errorf("KillGroup(%d) = nil, want an error", pid)
		}
	}
	if len(calls) != 0 {
		t.Errorf("seam calls = %v, want none", calls)
	}
	if err := KillGroup(noSuchPid, syscall.SIGKILL); err != nil || len(calls) != 1 || calls[0] != noSuchPid {
		t.Errorf("KillGroup(noSuchPid) = %v, calls %v, want nil and one call", err, calls)
	}
}
