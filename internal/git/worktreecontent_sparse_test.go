package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sparseContentFixture is a content fixture whose base repository has
// core.sparseCheckout set and a sparse-checkout file of only comment lines, so
// the checkout of a create writes no file of the commit.
func sparseContentFixture(t *testing.T, files map[string]string) checkoutRuleFixture {
	t.Helper()
	f := newContentFixture(t, files)
	runGit(t, f.repo, "config", "core.sparseCheckout", "true")
	writeFile(t, filepath.Join(f.repo, ".git", "info", "sparse-checkout"), "# no path\n", 0o644)
	return f
}

// lsFilesCalls counts the `ls-files -t -z` calls of the content check.
func lsFilesCalls(calls []envCall) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(c envCall) bool { return !containsSeq(c.argv, "ls-files", "-t") }))
}

// TestContentCheckSparseCheckout: with core.sparseCheckout and a
// sparse-checkout file of only comment lines, .claude/settings.json is not
// checked out. The create succeeds and logs 0 files (4c111e86, measured on
// Linux).
func TestContentCheckSparseCheckout(t *testing.T) {
	f := sparseContentFixture(t, map[string]string{".claude/settings.json": "{}\n"})
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if _, err := os.Lstat(filepath.Join(f.leaf, ".claude", "settings.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat of the sparse file = %v, want no such file", err)
	}
	commit := gitRevParse(t, f.repo, "HEAD")
	if want := checkedLine("0", "files", 3, commit, "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if n := lsFilesCalls(calls); n != 1 {
		t.Errorf("%d ls-files -t calls, want 1", n)
	}
}

// TestContentCheckSkippedFilePresent: of two files that the index marks
// skip-worktree, one stands in the new worktree with wrong bytes. It is still
// compared and refuses, while the missing one is left out (claustrum's own, not
// measured). With core.sparseCheckout on, git clears the bit of a file that
// stands in the worktree, so this test sets the bits with no sparse checkout.
func TestContentCheckSkippedFilePresent(t *testing.T) {
	f := newContentFixture(t, map[string]string{".claude/a.json": "a\n", ".claude/b.json": "b\n"})
	afterCheckout(t, []string{"git", "-C", f.leaf, "update-index", "--skip-worktree", ".claude/a.json", ".claude/b.json"},
		[]string{"rm", filepath.Join(f.leaf, ".claude", "a.json")}, []string{"write", filepath.Join(f.leaf, ".claude", "b.json"), "other\n"})
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Check whether a filter that rewrites ".claude/b.json" on checkout gives different output each time it runs: that file in the new worktree didn't match commit ` +
		commit[:12] + `. Less often, two committed names that differ only in letter case, or a damaged repository (run "git fsck" in ` + fmt.Sprintf("%q", f.repo) + ` to check), cause this.`
	if want := contentFrame(t, "worktree_verify_content", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
	if want := checkedLine("1", "file", 4, commit, fmt.Sprintf("refused: %q", loggedHint(hint))); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if n := lsFilesCalls(calls); n != 1 {
		t.Errorf("%d ls-files -t calls, want 1", n)
	}
	f.wantGone(t)
}

// TestContentCheckSparseCallFails: an ls-files call that fails drops no file,
// even with a skip-worktree line on its stdout. The missing file refuses with
// the retry code (claustrum's own, not measured).
func TestContentCheckSparseCallFails(t *testing.T) {
	f := sparseContentFixture(t, map[string]string{".claude/settings.json": "{}\n"})
	slowGit(t, "ls-files,-t", "fail", 0, "", "S .claude/settings.json")
	raw, _ := f.create(t, nil)
	if !strings.Contains(raw, `"errorCode":"worktree_verify_retry"`) {
		t.Errorf("git.worktree_create = %s, want worktree_verify_retry", raw)
	}
	f.wantGone(t)
}

// TestContentCheckMissingFileNotSparse: a file with no skip-worktree mark
// that is gone at the start of the check still refuses with the retry code,
// after the ls-files call (claustrum's own, not measured).
func TestContentCheckMissingFileNotSparse(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", "AGENTS.md": "a\n"})
	p := filepath.Join(f.leaf, "AGENTS.md")
	afterCheckout(t, []string{"rm", p})
	_, gone := os.Lstat(filepath.Join(t.TempDir(), "missing"))
	var pe *fs.PathError
	if !errors.As(gone, &pe) {
		t.Fatalf("Lstat of a missing file = %v", gone)
	}
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Try again, and if this repeats check the permissions of the folder the worktree is made in: "AGENTS.md" in the new worktree couldn't be read to check it against commit ` +
		commit[:12] + ` (` + pe.Err.Error() + `).`
	if want := contentFrame(t, "worktree_verify_retry", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
	if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", loggedHint(hint))); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if n := lsFilesCalls(calls); n != 1 {
		t.Errorf("%d ls-files -t calls, want 1", n)
	}
	f.wantGone(t)
}

// TestContentCheckNothingMissing: a create whose files all stand in the new
// worktree runs no ls-files call (claustrum's own, not measured).
func TestContentCheckNothingMissing(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".claude/settings.json": "{}\n"})
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if n := lsFilesCalls(calls); n != 0 {
		t.Errorf("%d ls-files -t calls, want 0", n)
	}
}
