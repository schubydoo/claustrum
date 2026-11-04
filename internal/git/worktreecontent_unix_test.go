//go:build unix

package git

import (
	"os"
	"path/filepath"
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
