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
