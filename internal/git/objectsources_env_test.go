package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment refuses git.status
// and git.list_branches with no git call, also with "ownGitConfigOff":true. With
// "shadowDirOff":true both run git (4c111e86, measured on Windows). This test runs
// the checks of Windows on every system.
func TestAlternateEnvRefusalOfStatusAndBranches(t *testing.T) {
	f := newListingFixture(t)
	checksOn(t)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(t.TempDir(), "objects"))
	refusal := `{"code":-32603,"message":"` + alternateEnvText + `"}`
	for _, row := range []struct {
		method  string
		params  map[string]any
		refused bool
	}{
		{"git.list_branches", map[string]any{"path": f.top}, true},
		{"git.list_branches", map[string]any{"path": f.top, "ownGitConfigOff": true}, true},
		{"git.status", map[string]any{"path": f.top, "baseRepo": f.top, "ownGitConfigOff": true}, true},
		{"git.list_branches", map[string]any{"path": f.top, "shadowDirOff": true}, false},
		{"git.status", map[string]any{"path": f.top, "baseRepo": f.top, "shadowDirOff": true}, false},
	} {
		raw, calls := f.call(t, row.method, row.params)
		got := replyPart(t, raw)
		if row.refused {
			if got != refusal {
				t.Errorf("%s %v: %s, want %s", row.method, row.params, got, refusal)
			}
			for _, c := range calls {
				if !slicesHasExcludesRead(c.argv) {
					t.Errorf("%s %v: git call %q, want none but the excludes read", row.method, row.params, c.argv)
				}
			}
			continue
		}
		if strings.Contains(got, "GIT_ALTERNATE_OBJECT_DIRECTORIES") || len(calls) == 0 {
			t.Errorf("%s %v: %s with %d git calls, want git to run", row.method, row.params, got, len(calls))
		}
	}
}

// GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment refuses git.info
// after at most its excludes read, also with "ownGitConfigOff":true, and
// git.worktree_create and git.worktree_remove with no git call. The create makes
// no leaf, and the remove keeps the worktree, its entry and its branch. git.info
// with "shadowDirOff":true runs git, and git.list_branches on a folder with no
// repository answers that shape. The daemon logs one line (4c111e86, measured on
// Windows). This test runs the checks of Windows on every system.
func TestAlternateEnvRefusalOfInfoCreateRemove(t *testing.T) {
	f := newListingFixture(t)
	wt2 := filepath.Join(f.top, ".claude", "worktrees", "w2")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w2", wt2)
	noRepo := t.TempDir()
	checksOn(t)
	alternateEnvLogged.Store(false)
	t.Cleanup(func() { alternateEnvLogged.Store(false) })
	logs := captureLogBuf(t)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(t.TempDir(), "objects"))
	wantNoGit := func(what string, calls []envCall) {
		t.Helper()
		for _, c := range calls {
			if !slicesHasExcludesRead(c.argv) {
				t.Errorf("%s: git call %q, want none but the excludes read", what, c.argv)
			}
		}
	}
	for _, params := range []map[string]any{{"path": f.top}, {"path": f.top, "ownGitConfigOff": true}} {
		raw, calls := f.call(t, "git.info", params)
		if got, want := replyPart(t, raw), errPart(t, alternateEnvText); got != want {
			t.Errorf("git.info %v: %s, want %s", params, got, want)
		}
		wantNoGit("git.info", calls)
	}
	if raw, calls := f.call(t, "git.info", map[string]any{"path": f.top, "shadowDirOff": true}); !strings.Contains(raw, `"isRepo":true`) || len(calls) == 0 {
		t.Errorf("git.info with shadowDirOff: %s with %d git calls, want git to run", raw, len(calls))
	}
	if raw, _ := f.call(t, "git.list_branches", map[string]any{"path": noRepo}); replyPart(t, raw) != `{"isRepo":false,"branches":[]}` {
		t.Errorf("git.list_branches on a folder with no repository: %s", raw)
	}

	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": f.leaf()})
	if want := `{"success":false,"error":` + quotedJSON(t, alternateEnvText) + `,"errorCode":"worktree_add_failed"}`; replyPart(t, raw) != want {
		t.Errorf("git.worktree_create: %s, want %s", raw, want)
	}
	if len(calls) != 0 {
		t.Errorf("git.worktree_create made %d git calls, want 0", len(calls))
	}
	if exists(f.leaf()) || branchExists(t, f.top, "w1") {
		t.Errorf("git.worktree_create made the leaf %v or the branch %v", exists(f.leaf()), branchExists(t, f.top, "w1"))
	}

	raw, calls = f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": wt2, "branchName": "w2"})
	want := `{"success":false,"error":` + quotedJSON(t, "failed to remove worktree: could not check whether "+wt2+" is locked: "+alternateEnvText) + `}`
	if replyPart(t, raw) != want {
		t.Errorf("git.worktree_remove: %s, want %s", raw, want)
	}
	if len(calls) != 0 {
		t.Errorf("git.worktree_remove made %d git calls, want 0", len(calls))
	}
	if !exists(filepath.Join(wt2, ".git")) || !exists(filepath.Join(f.regDir, "w2")) || !branchExists(t, f.top, "w2") {
		t.Errorf("git.worktree_remove deleted the leaf %v, the entry %v or the branch %v",
			!exists(filepath.Join(wt2, ".git")), !exists(filepath.Join(f.regDir, "w2")), !branchExists(t, f.top, "w2"))
	}
	if n := strings.Count(logs.String(), alternateEnvLine); n != 1 {
		t.Errorf("%d lines of GIT_ALTERNATE_OBJECT_DIRECTORIES, want 1:\n%s", n, logs.String())
	}
}

// An empty GIT_ALTERNATE_OBJECT_DIRECTORIES refuses no git.info and logs the line
// once (4c111e86, measured on Windows).
func TestAlternateEnvEmptyLogsOnce(t *testing.T) {
	f := newListingFixture(t)
	checksOn(t)
	alternateEnvLogged.Store(false)
	t.Cleanup(func() { alternateEnvLogged.Store(false) })
	logs := captureLogBuf(t)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "")
	for range 2 {
		if raw, _ := f.call(t, "git.info", map[string]any{"path": f.top}); !strings.Contains(raw, `"isRepo":true`) {
			t.Errorf("git.info with an empty value: %s", raw)
		}
	}
	if n := strings.Count(logs.String(), alternateEnvLine); n != 1 {
		t.Errorf("%d lines of GIT_ALTERNATE_OBJECT_DIRECTORIES, want 1:\n%s", n, logs.String())
	}
}

// slicesHasExcludesRead reports whether argv is the excludes read of git.status,
// which runs before the check (measured on Windows).
func slicesHasExcludesRead(argv []string) bool {
	return strings.Contains(strings.Join(argv, " "), "core.excludesFile")
}
