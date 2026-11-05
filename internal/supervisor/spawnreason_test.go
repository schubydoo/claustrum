package supervisor

import (
	"errors"
	"testing"
)

// TestSpawnFailureReasonFallback pins the reason of an error that no rule lists.
func TestSpawnFailureReasonFallback(t *testing.T) {
	if got := SpawnFailureReason(errors.New("x"), ""); got.Reason != "other" {
		t.Fatalf("reason = %q, want other", got.Reason)
	}
}
