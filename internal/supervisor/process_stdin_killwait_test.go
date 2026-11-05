package supervisor

import (
	"testing"
	"time"
)

// ClampKillWaitMs maps a caller's killAndWait timeoutMs onto the grace: non-positive
// → the default (probe-verified 0 and -100 both wait 3000ms), positive honored up
// to maxKillWaitMs.
//
// The {90000, 90000} row was removed on 2026-08-02: it asserted that 90 s passes
// through, which encoded the old 600000 ms ceiling as a fact. The reference stops
// at 30 s, so 90 s must now clamp — and a caller asking for it is a realistic
// client, not the "adversarial input" the old comment assumed.
func TestClampKillWaitMs(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultKillWaitMs},
		{-100, defaultKillWaitMs},
		{50, 50},
		{3000, 3000},
		{29500, 29500},         // below the ceiling, honored verbatim
		{90000, maxKillWaitMs}, // a plausible client value that now clamps
		{maxKillWaitMs, maxKillWaitMs},
		{maxKillWaitMs + 1, maxKillWaitMs},
		{10_000_000, maxKillWaitMs},
	}
	for _, tc := range cases {
		if got := ClampKillWaitMs(tc.in); got != tc.want {
			t.Errorf("clampKillWaitMs(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// The ceiling itself is the parity claim, so pin the value and not just the
	// clamping behaviour. Measured bracket (29500, 30500]; 30000 is the only
	// round value in it. See maxKillWaitMs in process.go.
	if maxKillWaitMs != 30000 {
		t.Errorf("maxKillWaitMs = %d, want 30000 (reference bracket (29500, 30500] at 5db5e4a)", maxKillWaitMs)
	}
	// Same argument for the post-SIGKILL reap grace, and it needs the pin MORE than the
	// ceiling does: the branch killReapGrace bounds is unreachable in this suite (it needs
	// a child SIGKILL cannot reap, which took a dm-delay device on a throwaway test system
	// to build), so every escalation test reaps promptly, p.done wins the select, and the
	// timeout arm is never taken. A refactor that puts this back to 5s — or folds it into
	// exitDrainGrace, which it equalled until the parity fix — would stay green with
	// nothing else to catch it.
	if killReapGrace != 7*time.Second {
		t.Errorf("killReapGrace = %v, want 7s (measured against the reference 2026-08-06: reply at 7.51s with timeoutMs 500)", killReapGrace)
	}
}
