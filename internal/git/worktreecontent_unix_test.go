//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestContentCheckLinkToAFifo: a committed link goes on no hash-object list,
// so a link to a FIFO does not block the check. The log line counts the
// hashed files only, and the blob of the link (measured on Linux).
func TestContentCheckLinkToAFifo(t *testing.T) {
	f := newContentFixture(t, map[string]string{
		"CLAUDE.md": "c\n", "AGENTS.md": "a\n", ".mcp.json": "{}\n", ".claude/settings.json": "{}\n",
	})
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.repo, ".claude", "x")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "add", "-f", "--", ".claude/x")
	runGit(t, f.repo, "commit", "-q", "-m", "link")
	stdin := filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv("CLAUSTRUM_GITSTUB_STDINLOG", stdin)
	logs := captureLogBuf(t)
	start := time.Now()
	raw, _ := f.create(t, nil)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the create took %s, want no wait for the FIFO", took)
	}
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	b, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(b)); len(lines) != 4 || strings.Contains(string(b), filepath.Join(".claude", "x")) {
		t.Errorf("the stdin of hash-object --stdin-paths = %q, want the 4 files and no link", lines)
	}
	if want := checkedLine("4", "files", 4, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
}

// TestContentCheckUndoThatCannotFinish: a file of the check that cannot be
// read refuses with the retry code. When the rollback cannot remove an entry
// of the leaf, the frame says so after "Removing it could not finish for",
// and the registration and the branch stay (measured on Windows, with a held
// file. Here a folder of mode 0555 stands in for it).
func TestContentCheckUndoThatCannotFinish(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root removes entries of a folder of mode 0555")
	}
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".claude/settings.json": "{}\n"})
	dir := filepath.Join(f.leaf, ".claude")
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(filepath.Join(dir, "settings.json"), 0o644)
	})
	afterCheckout(t, []string{"chmod", filepath.Join(dir, "settings.json"), "000"}, []string{"chmod", dir, "555"})
	raw, calls := f.create(t, nil)
	if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--stdin-paths") }) {
		t.Error("a hash-object --stdin-paths call ran, want the refusal before it (measured on Windows)")
	}
	start := `"error":"Claude could not confirm that the new worktree matches its commit, so it did not keep it. Try again, and if this repeats check the permissions of the folder the worktree is made in: \".claude/settings.json\" in the new worktree couldn't be read`
	undo := `. Removing it could not finish for ` + strings.Trim(jsonQuoted(t, f.leaf), `"`) + `: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll .claude: `
	if !strings.Contains(raw, start) || !strings.Contains(raw, undo) || !strings.HasSuffix(raw, `).","errorCode":"worktree_verify_retry"}}`) {
		t.Errorf("git.worktree_create =\n%s\nwant the retry text, then %q, and a final period", raw, undo)
	}
	if !exists(f.reg) || !branchExists(t, f.repo, "w1") {
		t.Errorf("registration stays %v, branch stays %v, want both", exists(f.reg), branchExists(t, f.repo, "w1"))
	}
}
