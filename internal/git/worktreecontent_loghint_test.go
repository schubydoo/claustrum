package git

import (
	"path/filepath"
	"runtime"
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

// A refused create whose refusal text is over 400 bytes logs the first 400 bytes
// of it. A long file name puts the cut before the path of the repository.
func TestContentCheckLogCutsALongRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the long name goes past the path limit of Windows")
	}
	name := ".claude/" + strings.Repeat("n", 200) + ".md"
	f := newContentFixture(t, map[string]string{name: "c\n"})
	afterCheckout(t, []string{"write", filepath.Join(f.leaf, filepath.FromSlash(name)), "c\nmore\n"})
	logs := captureLogBuf(t)
	raw, _ := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")[:12]
	if !strings.Contains(raw, `"errorCode":"worktree_verify_content"`) {
		t.Fatalf("git.worktree_create = %s, want the content refusal", raw)
	}
	want := `refused: "Check whether a filter that rewrites \"` + name + `\" on checkout gives different output each time it runs: that file in the new worktree didn't match commit ` +
		commit + `. Less often, two committed names"` + "\n"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log:\n%s\nwant the end %q", logs, want)
	}
}
