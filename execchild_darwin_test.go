//go:build darwin

package main

import (
	"os"
	"strconv"
	"testing"
)

// TestOwnStartTokenAndIdentityDarwin proves the darwin start-token source feeds the
// CLAUDE_SSH_CHILD marker: ownStartToken returns the process start-time string, and
// childIdentity composes "<pid>:<start>" from it. darwinProcStart is seamed so the test
// never shells out to ps. The composed marker must match what the child registry records
// for the same process, so the reap's marker check compares equal.
func TestOwnStartTokenAndIdentityDarwin(t *testing.T) {
	old := darwinProcStart
	t.Cleanup(func() { darwinProcStart = old })
	const start = "Sun Sep 13 08:32:51 2026"
	darwinProcStart = func(pid int) string {
		if pid == os.Getpid() {
			return start
		}
		return ""
	}

	if got := ownStartToken(); got != start {
		t.Errorf("ownStartToken() = %q, want %q", got, start)
	}
	want := strconv.Itoa(os.Getpid()) + ":" + start
	if got := childIdentity(); got != want {
		t.Errorf("childIdentity() = %q, want %q", got, want)
	}
}
