//go:build unix

package git

import (
	"errors"
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

// The frames of the five git methods for a path on a share that cannot be found
// and for a \\?\UNC\ path, byte for byte against the frames of b3081b51 on Windows
// (measured on Windows), with the id 1. No git call runs on the repository. This
// test turns the Windows test on and fakes the look at the share path.
func TestUnlookablePathFrames(t *testing.T) {
	f := newStatusFixture(t)
	call := removeCallLog(t, f.root)
	share, long := `\\127.0.0.1\nosuchshare\repo`, `\\?\UNC\localhost\c$\repo\R`
	fakeLstat(t, map[string]error{share: errors.New("network name not found")})
	const bt = "`"
	for _, c := range []struct{ repo, msg string }{
		{share, `Claude couldn't look at ` + bt + `\\\\127.0.0.1\\nosuchshare\\repo` + bt + ` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again.`},
		{long, `Claude can't check where ` + bt + `\\\\?\\UNC\\localhost\\c$\\repo\\R` + bt + ` leads because it doesn't start with a drive letter (C:\\...) or a share (\\\\server\\share\\...), so git wasn't run. Open the folder again using a path that does.`},
	} {
		worktree := c.repo + `\.claude\worktrees\w1`
		errReply := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + c.msg + `"}}`
		for _, m := range []struct {
			method string
			params map[string]any
			want   string
		}{
			{"git.info", map[string]any{"path": c.repo}, errReply},
			{"git.list_branches", map[string]any{"path": c.repo}, errReply},
			{"git.status", map[string]any{"path": c.repo, "baseRepo": c.repo}, errReply},
			{"git.worktree_remove", map[string]any{"baseRepo": c.repo, "worktreePath": worktree, "branchName": "w1"},
				`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + c.msg + `"}}`},
			{"git.worktree_create", map[string]any{"baseRepo": c.repo, "branchName": "w1", "worktreePath": worktree},
				`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + c.msg + `","errorCode":"worktree_add_failed"}}`},
		} {
			raw, calls := call(m.method, m.params)
			if raw != m.want {
				t.Errorf("%s of %s = %s\nwant %s", m.method, c.repo, raw, m.want)
			}
			for _, l := range calls {
				if !strings.Contains(l, "|config --includes --path core.excludesFile") {
					t.Errorf("%s of %s ran git: %q", m.method, c.repo, calls)
					break
				}
			}
		}
	}
	// A refused remove deletes nothing (from the code). On Linux and macOS the
	// share form //<repo> names the real repository, so the worktree can go.
	leaf := filepath.Join(f.T, ".claude", "worktrees", "w1")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "w1", leaf)
	slashed := "/" + f.T
	fakeLstat(t, map[string]error{slashed: errors.New("network name not found")})
	params := map[string]any{"baseRepo": slashed, "worktreePath": "/" + leaf, "branchName": "w1"}
	want := resultFrame(t, rpc.WorktreeRemoveResult{Success: false, Error: unlookableShareText(slashed)})
	if raw, _ := call("git.worktree_remove", params); raw != want {
		t.Errorf("git.worktree_remove of %s = %s\nwant %s", slashed, raw, want)
	}
	if fi, err := os.Lstat(leaf); err != nil || !fi.IsDir() {
		t.Errorf("the worktree folder after the refused remove: %v", err)
	}
	// The same remove by the plain path deletes the folder: the test can see a delete.
	params["baseRepo"], params["worktreePath"] = f.T, leaf
	if raw, _ := call("git.worktree_remove", params); !strings.Contains(raw, `"success":true`) {
		t.Errorf("git.worktree_remove of %s = %s", f.T, raw)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the worktree folder after the plain remove: %v", err)
	}
}
