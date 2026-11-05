//go:build windows

package git

import "testing"

// On Windows the reference gates the custom-worktree-location (worktreeRoot) capability
// off, refusing worktree_create and worktree_remove with a fixed message naming the
// root. Assert claustrum emits that exact refusal for both verbs. A remove puts the
// root in backticks (b3081b51, measured on Windows). A create keeps it plain
// (claustrum's own, not measured).
func TestExternalWorktreeUnsupportedOnWindows(t *testing.T) {
	for verb, shown := range map[string]string{"create": `C:\ext`, "remove": "`C:\\ext`"} {
		got := externalWorktreeUnsupportedRefusal(`C:\ext`, verb)
		want := "refusing to " + verb + " worktree: " + shown + " cannot be used: a custom worktree location is not supported on Windows hosts yet"
		if got != want {
			t.Errorf("verb %s: got %q, want %q", verb, got, want)
		}
	}
}
