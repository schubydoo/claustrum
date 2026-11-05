//go:build unix

package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBrokenPairGoneRemoveFrames: the daemon has GIT_CONFIG_KEY_0 with no value, and
// git.worktree_remove names a gone worktree. b3081b51 answers these frames (measured on
// Linux and macOS, and on Windows without worktreeRoot). A plain folder and a
// missing baseRepo get the count text after a colon. A repository gets the text of the
// failed start test, whose listing of the config fails under the pair. No branch step
// runs. Mutation: restore lockCheck(msg) at the count check of removeGoneWorktree, or
// drop either call of the start test there and in externalWorkTreeRefusal.
func TestBrokenPairGoneRemoveFrames(t *testing.T) {
	f := newWTFixture(t, false)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	base := t.TempDir()
	requireTempOutsideCheckout(t, base)
	plain := filepath.Join(base, "F")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	commonDir := filepath.Join(base, "gc")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "does", "not", "exist")
	root := filepath.Join(base, "wroot")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	plantEnv(t, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.foo")

	// The cause is the text of the host's git for the listing of call 7.
	list := exec.Command(f.realGit, "config", "--no-includes", "--file", "-", "--list", "-z")
	list.Env = repoConfigListEnv()
	var errOut bytes.Buffer
	list.Stdin, list.Stderr = bytes.NewReader(nil), &errOut
	if err := list.Run(); err == nil {
		t.Fatal("git listed a config under the broken pair")
	}
	failed := failedTestText(repoConfigPrefix + stderrLines(errOut.Bytes()))
	const count = "config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair 0 is incomplete"
	locked := func(leaf, reason string) string {
		return "failed to remove worktree: could not check whether " + leaf + " is locked: " + reason
	}
	const workTree = "failed to remove worktree: cannot determine the repository's work tree: "
	leafOf := func(repo string) string { return filepath.Join(repo, ".claude", "worktrees", "w1") }
	for _, r := range []struct {
		row    string
		env    []string
		params map[string]any
		want   string
	}{
		{"plain folder", nil, map[string]any{"baseRepo": plain, "branchName": "w1", "worktreePath": leafOf(plain)},
			locked(leafOf(plain), count)},
		{"plain folder with a daemon GIT_COMMON_DIR", []string{"GIT_COMMON_DIR=" + commonDir},
			map[string]any{"baseRepo": plain, "branchName": "w1", "worktreePath": leafOf(plain)}, locked(leafOf(plain), count)},
		{"missing baseRepo", nil, map[string]any{"baseRepo": missing, "branchName": "w1", "worktreePath": leafOf(missing)},
			locked(leafOf(missing), count)},
		{"repository", nil, map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": leafOf(f.top)},
			locked(leafOf(f.top), failed)},
		{"repository with worktreeRoot", nil,
			map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": filepath.Join(root, "d", "w1"), "worktreeRoot": root},
			workTree + failed},
		{"plain folder with worktreeRoot", nil,
			map[string]any{"baseRepo": plain, "branchName": "w1", "worktreePath": filepath.Join(root, "d", "w1"), "worktreeRoot": root},
			workTree + count},
	} {
		t.Run(r.row, func(t *testing.T) {
			plantEnv(t, r.env...)
			steps := branchStepRuns(t)
			d, _ := newShadowDeps(t)
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, r.want) + `}}`
			if got := gitDispatch(t, d, rpcLine(t, "git.worktree_remove", r.params)); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if n := steps.Load(); n != 0 {
				t.Errorf("the branch step ran %d times, want 0", n)
			}
		})
	}
}
