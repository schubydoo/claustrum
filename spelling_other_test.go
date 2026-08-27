//go:build !windows

package main

import "testing"

// Off Windows the spelling guard is a no-op: trailing dots/spaces and ":" are valid
// POSIX filename characters, and the reference applies no such check on unix (measured).
// (Containment and the ".." check still run in worktreePathRefusal.)
func TestSpellingHazardNoopOnUnix(t *testing.T) {
	for _, p := range []string{"/r/.claude/worktrees/wt.", "/r/.claude/worktrees/wt ", "/r/.claude/worktrees/wt:a"} {
		if windowsPathSpellingHazard(p) {
			t.Errorf("windowsPathSpellingHazard(%q) = true on unix, want false (POSIX allows these)", p)
		}
	}
}
