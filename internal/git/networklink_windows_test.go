//go:build windows

package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// setJunction makes an empty folder at dir and gives it the reparse data of a
// junction with the substitute name sub, as the junctions of the rows were made.
// Windows makes no junction to a UNC path by mklink /J.
func setJunction(t *testing.T, dir, sub string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	data := reparseData(reparseTagMountPoint, sub, sub)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &n, nil); err != nil {
		t.Fatal(err)
	}
}

// The reparse read of a junction to a UNC path, to \\.\NUL and to a local folder,
// and the refusal of a path through each (measured on Windows for the first two).
// The local junction passes. This test needs the Windows VM or a Windows runner.
func TestNetworkLinkJunctions(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	unc, nul, loc := filepath.Join(root, "jn_unc"), filepath.Join(root, "jn_nul"), filepath.Join(root, "jn_loc")
	setJunction(t, unc, `\??\UNC\localhost\nosuchshare`)
	setJunction(t, nul, `\??\NUL`)
	makeJunction(t, loc, filepath.Join(root, "local"))
	if got, ok := linkTarget(unc); !ok || got != `\??\UNC\localhost\nosuchshare` {
		t.Errorf("read of the UNC junction: %q, %v", got, ok)
	}
	if got, ok := linkTarget(root); ok {
		t.Errorf("read of a plain folder: %q", got)
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(unc, "R"), true},
		{filepath.Join(nul, "R"), true},
		{filepath.Join(loc, "R"), false},
	} {
		want := ""
		if c.want {
			want = networkLinkText(c.path)
		}
		if got := networkLinkRefusal(c.path); got != want {
			t.Errorf("%s: got %q, want %q", c.path, got, want)
		}
	}
	h := newTestGitDeps()
	path := filepath.Join(unc, "R")
	if raw, want := gitDispatch(t, h, rpcLine(t, "git.info", map[string]any{"path": path})), errFrame(t, networkLinkText(path)); raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
}

// The frames of the five git methods for a path on a share that cannot be found
// and for a \\?\UNC\ path, byte for byte against the frames of b3081b51 on
// Windows (measured on Windows), with the id 1. The look at the share path is
// faked and the link chain test is off, so no request reaches the network.
func TestUnlookablePathFramesWindows(t *testing.T) {
	old := objectSourceChecks
	objectSourceChecks = false
	t.Cleanup(func() { objectSourceChecks = old })
	share, long := `\\127.0.0.1\nosuchshare\repo`, `\\?\UNC\localhost\c$\repo\R`
	fakeLstat(t, map[string]error{share: errors.New("network name not found")})
	h := newTestGitDeps()
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
			if raw := gitDispatch(t, h, rpcLine(t, m.method, m.params)); raw != m.want {
				t.Errorf("%s of %s = %s\nwant %s", m.method, c.repo, raw, m.want)
			}
		}
	}
}
