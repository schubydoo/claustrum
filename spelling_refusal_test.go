package main

import (
	"runtime"
	"strings"
	"testing"
)

// The spelling refusal inside worktreePathRefusal fires only when
// windowsPathSpellingHazard reports true, which off Windows it never does — so stub the
// hazard to exercise that branch (and its exact message) on the linux coverage cell,
// for both the create and remove verbs.
func TestWorktreePathRefusalSpellingBranch(t *testing.T) {
	orig := windowsPathSpellingHazard
	t.Cleanup(func() { windowsPathSpellingHazard = orig })
	windowsPathSpellingHazard = func(string) bool { return true }

	// worktreePathRefusal's first check is filepath.IsAbs, so the worktreePath must be
	// absolute on the host OS or it short-circuits to "is a relative path" before the
	// spelling branch (a "/..." path is not absolute on Windows).
	repo, wtp := "/repo", "/repo/.claude/worktrees/x"
	if runtime.GOOS == "windows" {
		repo, wtp = `C:\repo`, `C:\repo\.claude\worktrees\x`
	}
	for _, verb := range []string{"create", "remove"} {
		msg := worktreePathRefusal(repo, wtp, verb)
		if !strings.HasPrefix(msg, "refusing to "+verb+" worktree:") ||
			!strings.Contains(msg, "has a component Windows reads as a different name (trailing dot or space, or a colon)") {
			t.Errorf("verb %s: spelling branch not taken; got %q", verb, msg)
		}
	}
}
