package sysproc

import "testing"

// TestLeaderGateDoRunsOnlyWhileOpen: Do runs its kill on an open gate and
// runs nothing after Close.
func TestLeaderGateDoRunsOnlyWhileOpen(t *testing.T) {
	var g LeaderGate
	n := 0
	g.Do(func() { n++ })
	g.Close()
	g.Do(func() { n++ })
	if n != 1 {
		t.Errorf("kills = %d, want 1: one before Close, none after", n)
	}
}
