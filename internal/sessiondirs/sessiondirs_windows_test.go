//go:build windows

package sessiondirs

import "testing"

// The refusal text on Windows, byte for byte.
func TestNoOtherGitTextWindows(t *testing.T) {
	want := `For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as C:\Program Files\Git`
	if got := (NoOtherGitError{}).Error(); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}
