//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The frames of the five git methods for a repository whose .git objects folder
// cannot be looked at, byte for byte in the shape of 4c111e86 (measured on
// Windows), with the id 1. No git call runs on the repository. This test turns
// the Windows test on and fakes the look and its error.
func TestPathLookFrames(t *testing.T) {
	f := newStatusFixture(t)
	call := removeCallLog(t, f.root)
	objects := filepath.Join(f.T, ".git", "objects")
	fakeLstat(t, map[string]error{objects: errCannotAccess})
	oldAccess := lookCannotAccess
	lookCannotAccess = func(err error) bool { return errors.Is(err, errCannotAccess) }
	t.Cleanup(func() { lookCannotAccess = oldAccess })
	// fakeLstat answers with the device file for every other path. The .git
	// folder gets its real look, so the test sees a folder there.
	faked := lstatPath
	lstatPath = func(path string) (fs.FileInfo, error) {
		if path == filepath.Join(f.T, ".git") {
			return os.Lstat(path)
		}
		return faked(path)
	}
	msg := "Claude couldn't check that `" + objects + "` stays on a local drive, so git wasn't run. Try again."
	worktree := filepath.Join(f.T, ".claude", "worktrees", "w1")
	errReply := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + msg + `"}}`
	for _, m := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.info", map[string]any{"path": f.T}, errReply},
		{"git.list_branches", map[string]any{"path": f.T}, errReply},
		{"git.status", map[string]any{"path": f.T, "baseRepo": f.T}, errReply},
		{"git.worktree_remove", map[string]any{"baseRepo": f.T, "worktreePath": worktree, "branchName": "w1"},
			`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not check whether ` + worktree + ` is locked: ` + msg + `"}}`},
		{"git.worktree_create", map[string]any{"baseRepo": f.T, "branchName": "w1", "worktreePath": worktree},
			`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + msg + `","errorCode":"worktree_add_failed"}}`},
	} {
		raw, calls := call(m.method, m.params)
		if raw != m.want {
			t.Errorf("%s = %s\nwant %s", m.method, raw, m.want)
		}
		for _, l := range calls {
			if !strings.Contains(l, "|config --includes --path core.excludesFile") {
				t.Errorf("%s ran git: %q", m.method, calls)
				break
			}
		}
	}
}
