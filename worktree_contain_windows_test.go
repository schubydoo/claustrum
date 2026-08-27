//go:build windows

package main

import "testing"

// On Windows the filesystem is case-insensitive, and filepath.Rel compares path
// components case-insensitively (its sameWord is strings.EqualFold), so a worktreePath
// that differs from baseRepo only in drive-letter or directory-name case is still
// inside the repo. 7d193f89 accepts such a path (measured on the Windows build); the
// earlier case-sensitive strings.HasPrefix wrongly refused it. This test runs only on
// the Windows CI leg — on a case-sensitive Linux/macOS filesystem these would be
// distinct directories and the behaviour is deliberately different (pinned by the
// battery and the Linux containment tests).
func TestPathStrictlyUnderCaseInsensitiveOnWindows(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{`C:\Repo\.claude\worktrees\w`, `C:\REPO`, true}, // case-variant prefix -> inside
		{`c:\users\x\repo\.claude\worktrees\w`, `C:\Users\X\Repo`, true},
		{`C:\Repo`, `C:\REPO`, false},    // equal (case-insensitive) -> not strictly under
		{`C:\Other\w`, `C:\Repo`, false}, // genuinely outside
	}
	for _, c := range cases {
		if got := pathStrictlyUnder(c.p, c.root); got != c.want {
			t.Errorf("pathStrictlyUnder(%q, %q) = %v, want %v", c.p, c.root, got, c.want)
		}
	}
}
