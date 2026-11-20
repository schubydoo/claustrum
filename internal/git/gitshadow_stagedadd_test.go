package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The staged add of a create holds no folder of a repository: its child names
// `.s<n>`. So with all other folders of the limit in use, a new repository
// removes the folder of the copy during the add. 4c111e86 does so (measured on
// Linux, macOS and Windows, a create held in its add and git.status on a new
// repository). The create then goes on: its next calls make the folder again.
//
// Mutation: hold the folder of the copy from the staging step until `.s<n>`
// goes (the hold before TASK-705). The git.info on B then gets the limit text,
// and the folder of A stays.
func TestStagedAddHoldsNoRepoFolder(t *testing.T) {
	lowerRepoLimit(t, 1)
	h := newOffHoldFixture(t, "worktree,add")
	leaf := filepath.Join(h.repo, ".claude", "worktrees", "w1")
	h.send(t, 1, "git.worktree_create", map[string]any{"baseRepo": h.repo, "branchName": "w1", "worktreePath": leaf})
	h.held(t)
	folderA := filepath.Join(daemonFolder(t, h.run), shadowRepoName(filepath.Join(h.repo, ".git")))
	sweptThere(t, folderA)
	h.send(t, 2, "git.info", map[string]any{"path": h.repoB})
	if raw := h.g.reply(t, 2); !strings.Contains(raw, `"isRepo":true`) {
		t.Errorf("git.info on B during the add = %s\nwant a result with isRepo true", raw)
	}
	sweptGone(t, folderA)
	h.release()
	want := `{"success":true,"path":"` + jsonInner(t, leaf) + `","sourceBranch":"main","branch":"w1"}`
	if got := string(h.reply(t, 1)); got != want {
		t.Errorf("git.worktree_create = %s\nwant %s", got, want)
	}
	sweptThere(t, folderA)
}

// statGoneText is the error of the stat of a folder that is not there, with
// the name `.`, as Go and 4c111e86 print it (measured on Linux, macOS and
// Windows).
func statGoneText() string {
	if runtime.GOOS == "windows" {
		return "GetFileAttributesEx .: The system cannot find the file specified."
	}
	return "stat .: no such file or directory"
}

// TestStagedAddFillsNoGoneCopy: the folder of the copy goes right before the
// staging step. The create answers privateCopyText and the error of the stat of
// that folder, with errorCode worktree_add_failed, and runs no add. 4c111e86
// answers so for a create whose folder the limit removed during the add of an
// earlier create (measured on Linux, macOS and Windows). `.s<n>` does not
// stay, and neither does the leaf or the branch.
//
// Mutation: drop the stat at the start of addStage.fill. The open of the
// folder then fails, and the create answers prepareWorktreesPrefix and that
// error.
func TestStagedAddFillsNoGoneCopy(t *testing.T) {
	f := newHoldTrial(t, argsHave("worktree", "add"))
	f.warm(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + jsonInner(t, privateCopyText+statGoneText()) + `","errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if !f.didFire() {
		t.Fatal("the folder of the copy did not go before the staging step")
	}
	for _, call := range calls {
		if argvHas(call.argv, "worktree", "add") {
			t.Errorf("a worktree add ran after the failed fill: %q", call.argv)
		}
	}
	ents, err := os.ReadDir(daemonFolder(t, f.run))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), addCopyPrefix) {
			t.Errorf("the staging folder %s stays", e.Name())
		}
	}
	sweptGone(t, leaf)
	if branchThere(t, f.repo, "w1") {
		t.Error("the branch w1 stays")
	}
}
