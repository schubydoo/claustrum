package supervisor

import (
	"testing"
	"time"
)

// TestUnattendedTTLAndTick pins the clamp of 683cc6a9 (measured on Linux for each
// value here but -3) and the tick, which is claustrum's own.
func TestUnattendedTTLAndTick(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
		tick time.Duration
	}{
		{-3, "1m0s", 15 * time.Second},
		{0, "1m0s", 15 * time.Second},
		{1, "1s", 250 * time.Millisecond},
		{2, "2s", 500 * time.Millisecond},
		{4, "4s", time.Second},
		{120, "2m0s", 30 * time.Second},
		{599, "9m59s", time.Minute},
		{600, "10m0s", time.Minute},
		{100000, "10m0s", time.Minute},
	} {
		ttl := UnattendedTTL(c.n)
		if ttl.String() != c.want {
			t.Errorf("unattendedTTL(%d) = %s, want %s", c.n, ttl, c.want)
		}
		if got := unattendedTick(ttl); got != c.tick {
			t.Errorf("unattendedTick(%s) = %s, want %s", ttl, got, c.tick)
		}
	}
}

// TestUnattendedEndGraceIsSeven pins the wait between the SIGHUP and the SIGKILL.
// 683cc6a9 logs the SIGKILL exit about 7 s after the ending line (Linux).
func TestUnattendedEndGraceIsSeven(t *testing.T) {
	if unattendedEndGrace != 7*time.Second {
		t.Errorf("unattendedEndGrace = %s, want 7s", unattendedEndGrace)
	}
}
