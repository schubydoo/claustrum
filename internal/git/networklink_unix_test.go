//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// git.status, git.info and git.worktree_create of a repository through a link to
// a network path. b3081b51 refuses each one with no git call on the repository:
// -32603 for git.status and git.info, a failed add for the create (measured on
// Windows). This test turns the Windows test on and fakes the reparse read.
func TestNetworkLinkFrames(t *testing.T) {
	f := newStatusFixture(t)
	call := removeCallLog(t, f.root)
	link := filepath.Join(f.root, "jn")
	if err := os.Symlink(f.T, link); err != nil {
		t.Fatal(err)
	}
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	text := networkLinkText(link)
	worktree := filepath.Join(link, ".claude", "worktrees", "w1")
	for _, c := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.status", map[string]any{"path": f.W, "baseRepo": link}, errFrame(t, text)},
		{"git.info", map[string]any{"path": link}, errFrame(t, text)},
		{"git.worktree_create", map[string]any{"baseRepo": link, "branchName": "w1", "worktreePath": worktree},
			resultFrame(t, rpc.WorktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})},
	} {
		raw, calls := call(c.method, c.params)
		if raw != c.want {
			t.Errorf("%s = %s\nwant %s", c.method, raw, c.want)
		}
		for _, l := range calls {
			if !strings.Contains(l, "|config --includes --path core.excludesFile") {
				t.Errorf("%s ran git: %q", c.method, calls)
				break
			}
		}
	}
	// The create made nothing (from the code).
	if _, err := os.Lstat(filepath.Join(f.T, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the worktree folder: %v", err)
	}
	if out, _ := exec.Command("git", "-C", f.T, "branch", "--list", "w1").Output(); len(out) != 0 {
		t.Errorf("branch w1: %q", out)
	}
	// A link to a local drive goes on to git.
	fakeLinkTargets(t, map[string]string{link: `\??\C:\work\T`})
	for _, m := range []string{"git.status", "git.info"} {
		params := map[string]any{"path": link}
		if m == "git.status" {
			params = map[string]any{"path": f.W, "baseRepo": link}
		}
		if raw, calls := call(m, params); strings.Contains(raw, "doesn't follow") || len(calls) == 0 {
			t.Errorf("%s of a local link = %s, %d calls", m, raw, len(calls))
		}
	}
}

// With "shadowDirOff":true each of the three methods gives the same refusal, with
// no git call on the repository (claustrum's own, not measured: no row sends the
// switch through such a link).
func TestNetworkLinkFramesShadowDirOff(t *testing.T) {
	f := newStatusFixture(t)
	call := removeCallLog(t, f.root)
	link := filepath.Join(f.root, "jn")
	if err := os.Symlink(f.T, link); err != nil {
		t.Fatal(err)
	}
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	text := networkLinkText(link)
	worktree := filepath.Join(link, ".claude", "worktrees", "w1")
	for _, c := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.status", map[string]any{"path": f.W, "baseRepo": link, "shadowDirOff": true}, errFrame(t, text)},
		{"git.info", map[string]any{"path": link, "shadowDirOff": true}, errFrame(t, text)},
		{"git.worktree_create", map[string]any{"baseRepo": link, "branchName": "w1", "worktreePath": worktree, "shadowDirOff": true},
			resultFrame(t, rpc.WorktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})},
	} {
		raw, calls := call(c.method, c.params)
		if raw != c.want {
			t.Errorf("%s = %s\nwant %s", c.method, raw, c.want)
		}
		for _, l := range calls {
			if !strings.Contains(l, "|config --includes --path core.excludesFile") {
				t.Errorf("%s ran git: %q", c.method, calls)
				break
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(f.T, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the worktree folder: %v", err)
	}
}
