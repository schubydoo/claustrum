package git

import (
	"strings"
	"testing"
)

// The log line of the content check quotes the first 400 bytes of the refusal
// text, with no final period. The long text has the shape of the macOS row on
// 4c111e86: 406 bytes after the period goes, of which the line keeps the part up
// to "try ".
func TestLoggedHintCut(t *testing.T) {
	w := "/private/tmp/cl/w/worktree-cut-sample/m"
	long := `Run "git fsck" in "` + w + `/R-a2" to check the repository: git couldn't read what it stores for commit 0123456789ab (git and Claude disagree about which repository holds this folder: from "` +
		w + `/R-a2" git finds "` + w + `/decoy/.git", Claude finds "` + w + `/R-a2/.git"; try again).`
	if got, want := loggedHint(long), strings.TrimSuffix(long, "again).")[:400]; got != want || !strings.HasSuffix(got, "; try ") {
		t.Errorf("loggedHint of %d bytes = %q, want %q", len(long), got, want)
	}
	if got := loggedHint("short text."); got != "short text" {
		t.Errorf("loggedHint of a short text = %q", got)
	}
	// A rune that the cut splits goes.
	split := strings.Repeat("a", 399) + "é tail"
	if got := loggedHint(split); got != strings.Repeat("a", 399) {
		t.Errorf("loggedHint of a split rune = %q", got)
	}
}
