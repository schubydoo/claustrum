package git

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// switchMacCreateOrderOn turns on the two macOS steps of the create order
// (createGateAsksAfterAdd, attachAddListsTwice) for the length of the test.
func switchMacCreateOrderOn(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the steps follow the macOS order, and Windows asks after its add by recheck")
	}
	oldAsk, oldList := createGateAsksAfterAdd, attachAddListsTwice
	createGateAsksAfterAdd, attachAddListsTwice = true, true
	t.Cleanup(func() { createGateAsksAfterAdd, attachAddListsTwice = oldAsk, oldList })
}

// TestCreateAfterAHoldAtTheAddAsksBeforeTheConfigRead: after a hold that ends at
// the add, the later gate of the create asks git about the repository after the
// start test and before the read of the config, and askAfterAdd makes no second
// call. 4c111e86 does so (measured on macOS). Mutation: drop the askInGate arm of
// settingsGate. The call then comes after the read.
func TestCreateAfterAHoldAtTheAddAsksBeforeTheConfigRead(t *testing.T) {
	switchMacCreateOrderOn(t)
	switchAskAfterAddOn(t)
	h := newLiveFixture(t)
	holdA(t, "worktree,add")
	params, leaf := h.createParams()
	if got, want := h.endsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Fatalf("create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	add := h.callAt(calls, 0, "worktree,add")
	test := h.callAt(calls, add+1, "init,-q,--template=")
	if add < 0 || test < 0 {
		t.Fatalf("the add is call %d and the start test starts at call %d, want both", add, test)
	}
	testAdd := h.callAt(calls, test+1, "worktree,add")
	if testAdd < 0 {
		t.Fatalf("no worktree add of the start test after call %d", test)
	}
	read := slices.IndexFunc(calls[testAdd+1:], envCall.configRead)
	probe := slices.IndexFunc(calls[testAdd+1:], envCall.isProbe)
	if read < 0 || probe < 0 || probe > read {
		t.Errorf("after the start test the call about the repository is call %d and the read of the config is call %d, want the call first", probe, read)
	}
	if n := len(probeCalls(calls[add+1:])); n != 1 {
		t.Errorf("%d calls about the repository after the add, want 1", n)
	}
}

// TestAttachAddListsTwiceBeforeItsAdd: on macOS a create in attach mode runs two
// listings in baseRepo between `rev-parse --absolute-git-dir` and its add, where
// a create of a new branch runs one. 4c111e86 does so after a git.info (measured
// on macOS). Mutation: drop the listing of attachAddListsTwice in
// gitWorktreeCreateLocked. The attach add then has one listing.
func TestAttachAddListsTwiceBeforeItsAdd(t *testing.T) {
	switchMacCreateOrderOn(t)
	for _, row := range []struct {
		name   string
		attach bool
		want   int
	}{
		{"a new branch", false, 1},
		{"an attach add", true, 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			runGit(t, f.repo, "branch", "ex")
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": filepath.Join(f.repo, ".claude", "worktrees", "w1")}
			if row.attach {
				params["existingBranch"] = "ex"
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			from := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "rev-parse,--absolute-git-dir") })
			add := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "worktree,add") })
			if from < 0 || add < from {
				t.Fatalf("rev-parse --absolute-git-dir is call %d and the add is call %d", from, add)
			}
			n := 0
			for _, c := range calls[from+1 : add] {
				if c.listing() && sameDir(c.cwd, f.repo) {
					n++
				}
			}
			if n != row.want {
				t.Errorf("%d listings in baseRepo between rev-parse --absolute-git-dir and the add, want %d", n, row.want)
			}
		})
	}
}
