//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// On Windows the reference (7d193f89) refuses a worktree path whose leaf ends in "."
// or " " or contains ":" — Windows reads those as a different name. A drive-letter
// volume (C:) must NOT trip the colon check, and an internal dot is fine.
func TestWindowsPathSpellingHazard(t *testing.T) {
	repo := `C:\repo`
	base := filepath.Join(repo, ".claude", "worktrees")
	cases := []struct {
		leaf   string
		hazard bool
	}{
		{"ok", false},
		{"a.b", false},       // internal dot is fine
		{"wt.", true},        // trailing dot
		{"wt ", true},        // trailing space
		{"wt:a", true},       // colon (ADS / drive-relative)
		{`nested\ok`, false}, // multi-component, clean
		{`nested\wt.`, true}, // hazard in a deeper component
	}
	for _, c := range cases {
		p := filepath.Join(base, c.leaf)
		if got := windowsPathSpellingHazard(p); got != c.hazard {
			t.Errorf("windowsPathSpellingHazard(%q) = %v, want %v", p, got, c.hazard)
		}
	}
	if windowsPathSpellingHazard(`C:\repo\ok`) {
		t.Error("drive-letter volume ':' was wrongly flagged as a hazard")
	}
	// End-to-end: worktreePathRefusal returns the reference's spelling message.
	msg := worktreePathRefusal(repo, filepath.Join(base, "wt."), "create")
	if !strings.Contains(msg, "has a component Windows reads as a different name") {
		t.Errorf("worktreePathRefusal(create, wt.) = %q, want the spelling refusal", msg)
	}
}
