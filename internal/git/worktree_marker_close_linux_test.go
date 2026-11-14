package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// A close of the new .claude-managed-worktrees marker that fails stops the
// create with mkdir_failed. The marker stays with its 285 bytes, and no leaf,
// no registration and no branch is made. c9125eb8 and 4c111e86 answer so on
// Linux, with a FUSE folder whose flush fails (measured).
func TestWorktreeCreateMarkerCloseFails(t *testing.T) {
	base, repo := createModesRepo(t)
	requireTempOutsideCheckout(t, base)
	root := filepath.Join(base, "R")
	mkdirForTest(t, root)
	wp := filepath.Join(root, "cp", "w1")
	old := closeMarkerFile
	t.Cleanup(func() { closeMarkerFile = old })
	closeMarkerFile = func(f *os.File) error {
		_ = f.Close()
		return &fs.PathError{Op: "close", Path: f.Name(), Err: syscall.EIO}
	}
	raw := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp, "worktreeRoot": root}))
	dir := filepath.Join(root, "cp")
	want := regexp.MustCompile(`^\{"jsonrpc":"2\.0","id":1,"result":\{"success":false,"error":"failed to create parent directory: cannot mark ` +
		regexp.QuoteMeta(dir) + ` as a worktree location: close /dev/fd/[0-9]+/\.claude-managed-worktrees: input/output error","errorCode":"mkdir_failed"\}\}$`)
	if !want.MatchString(raw) {
		t.Errorf("create frame\n got %s\nwant %s", raw, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, managedWorktreesMarker)); err != nil || string(b) != managedWorktreesMarkerBody {
		t.Errorf("the marker = %q, %v, want its 285 bytes", b, err)
	}
	requireAbsent(t, wp)
	if out := gitOut(t, repo, "branch", "--list", "b"); out != "" {
		t.Errorf("branch b = %q, want none", out)
	}
	if out := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
		t.Errorf("worktree list = %q, want the main checkout only", out)
	}
}
