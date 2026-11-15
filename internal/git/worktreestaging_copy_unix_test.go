//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStagedAddInAFolderWithSearchOnly: the folder of the daemon has mode 0100
// after the first request. A create answers privateCopyText with the error of
// the mkdir of `.s<n>`, as with mode 0500. 4c111e86 answers that text in both
// states (measured on Linux).
func TestStagedAddInAFolderWithSearchOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0100")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	start := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + strings.Trim(jsonQuoted(t, privateCopyText), `"`) + `mkdir ` +
		strings.Trim(jsonQuoted(t, filepath.Join(dir, addCopyPrefix)), `"`)
	rest, ok := strings.CutPrefix(raw, start)
	n, tail, _ := strings.Cut(rest, ":")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" || tail != ` permission denied","errorCode":"worktree_add_failed"}}` {
		t.Errorf("git.worktree_create = %s\nwant it to start %s<n>: permission denied", raw, start)
	}
}

// umaskModes are the modes that a new file of mode 0666 and a new folder of
// mode 0777 get in dir under the umask of the test.
func umaskModes(t *testing.T, dir string) (file, folder os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, "probe-file")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Mkdir(filepath.Join(dir, "probe-folder"), 0o777); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Join(dir, "probe-folder"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), di.Mode().Perm()
}

// TestStagedCopyModes: in `.s<n>`, HEAD, config and info/exclude are made
// with mode 0666 and info, objects and refs with 0777, less the umask. The
// folder, worktrees and each entry keep 0700, and the entry files keep 0600.
// Under umask 022 4c111e86 gives 0644 and 0755 (measured on Linux).
func TestStagedCopyModes(t *testing.T) {
	file, folder := umaskModes(t, t.TempDir())
	if file == 0o600 && folder == 0o700 {
		t.Skip("the umask of the test gives the old modes")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	want := map[string]os.FileMode{
		".": 0o700, "HEAD": file, "config": file, "info": folder, "info/exclude": file, "objects": folder, "refs": folder,
		"worktrees": 0o700, "worktrees/wr": 0o700, "worktrees/wr/HEAD": 0o600, "worktrees/wr/commondir": 0o600, "worktrees/wr/gitdir": 0o600,
	}
	for name, mode := range want {
		fi, err := os.Lstat(filepath.Join(st.dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if fi.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v", name, fi.Mode().Perm(), mode)
		}
	}
}
