//go:build windows

package main

import "testing"

// On Windows the reference gates the custom-worktree-location (worktreeRoot) capability
// off, refusing worktree_create and worktree_remove with a fixed message naming the
// root. Assert claustrum emits that exact refusal for both verbs.
func TestExternalWorktreeUnsupportedOnWindows(t *testing.T) {
	for _, verb := range []string{"create", "remove"} {
		got := externalWorktreeUnsupportedRefusal(`C:\ext`, verb)
		want := "refusing to " + verb + " worktree: C:\\ext cannot be used: a custom worktree location is not supported on Windows hosts yet"
		if got != want {
			t.Errorf("verb %s: got %q, want %q", verb, got, want)
		}
	}
}
