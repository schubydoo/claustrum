//go:build linux || darwin

package procinfo

import (
	"syscall"
	"testing"
)

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
	if err := KillGroup(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := prev(1, 0); err != nil {
		t.Fatal(err)
	}
	SetKillGroupForTest(prev)
	if err := KillGroup(1, 0); err != nil {
		t.Fatal(err)
	}
	if want := []string{"wrap", "base", "base", "base"}; len(calls) != len(want) ||
		calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] || calls[3] != want[3] {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}
