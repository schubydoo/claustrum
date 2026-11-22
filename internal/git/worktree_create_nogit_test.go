package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitNotFoundCreate is the reply of a create with no git that the daemon runs
// (87375cc7, measured on Linux and macOS, and on Windows for a missing baseRepo).
const gitNotFoundCreate = `{"success":false,"error":"not a git repository","errorCode":"git_not_found"}`

// With no git on PATH, a create answers after the marker test and before the count
// check, and makes nothing (4c111e86, measured on Linux and macOS, and on Windows
// for a missing baseRepo). On Windows the rows of an existing baseRepo are
// claustrum's own, not measured. The code is git_not_found, as on 87375cc7
// (measured on Linux and macOS, and on Windows for a missing baseRepo). Mutation: drop the
// PATH test of gitWorktreeCreateLocked, and each row but the marker row fails, or
// keep the code not_a_repo.
func TestCreateWithNoGitOnPathAnswersNotARepo(t *testing.T) {
	f := newNoRepoFixture(t)
	// The daemon state of -serve, with a settings control, as in the measurements.
	f.d, _ = newShadowDeps(t)
	marker := filepath.Join(filepath.Dir(f.top), "MK")
	if err := os.MkdirAll(filepath.Join(marker, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, ".claude-managed-worktrees"), []byte("marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, row := range []struct {
		name   string
		env    []string
		params map[string]any
		want   string
	}{
		{"nogit_cnt_nokey_value.missing", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.foo"},
			map[string]any{"baseRepo": filepath.Join(marker, "missing"), "branchName": "w1", "worktreePath": f.leaf()}, gitNotFoundCreate},
		{"nogit_none.missing", nil,
			map[string]any{"baseRepo": f.m, "branchName": "w1", "worktreePath": f.leaf()}, gitNotFoundCreate},
		{"nogit_none.plain", nil,
			map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": f.leaf()}, gitNotFoundCreate},
		{"nogit_none.root_plain", nil,
			map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": f.external(), "worktreeRoot": f.r}, gitNotFoundCreate},
		{"nogit_none.real below a marker", nil,
			map[string]any{"baseRepo": filepath.Join(marker, "sub"), "branchName": "w1", "worktreePath": filepath.Join(marker, "sub", "w1")},
			`{"success":false,"error":"` + managedWorktreesRefusal + `","errorCode":"nested_base_repo"}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			plantEnv(t, row.env...)
			raw, _ := f.call(t, "git.worktree_create", row.params)
			if got := replyPart(t, raw); got != row.want {
				t.Fatalf("reply = %s, want %s", got, row.want)
			}
			if _, err := os.Lstat(row.params["worktreePath"].(string)); !os.IsNotExist(err) {
				t.Fatalf("leaf: %v, want none", err)
			}
		})
	}
}

// With "shadowDirOff":true a `rev-parse --absolute-git-dir` that exits 1 with no
// answer stops the create before the add, and no leaf stays (4c111e86, measured
// on Linux, macOS and Windows). Mutation: test the state shadow alone at that
// step, and the create succeeds.
func TestCreateWithTheOffSwitchStopsOnNoGitDirAnswer(t *testing.T) {
	requireGit(t)
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	stubRule2(t, "rev-parse,--absolute-git-dir", "", 0, "1", "", "")
	raw, _ := f.createWith(t, newTestGitDeps(), "w1", 0, map[string]any{"shadowDirOff": true})
	want := `{"success":false,"error":"git worktree add failed: cannot locate the repository's git directory: exit status 1","errorCode":"worktree_add_failed"}`
	if got := replyPart(t, raw); got != want {
		t.Fatalf("reply = %s, want %s", got, want)
	}
	if _, err := os.Lstat(f.leaf()); !os.IsNotExist(err) {
		t.Fatalf("leaf: %v, want none", err)
	}
	if f.hasRef(t, "w1") {
		t.Fatal("refs/heads/w1 was made, want no add")
	}
}

// Only exit 1 is measured for that stop. A call that exits 128 with the off switch
// keeps the old path, and the create goes on (not measured). Mutation: stop on any
// failure with the off switch, and the create fails.
func TestCreateWithTheOffSwitchGoesOnAfterOtherGitDirFailure(t *testing.T) {
	requireGit(t)
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	stubRule2(t, "rev-parse,--absolute-git-dir", "", 0, "128", "", "")
	raw, _ := f.createWith(t, newTestGitDeps(), "w1", 0, map[string]any{"shadowDirOff": true})
	if got := replyPart(t, raw); !strings.HasPrefix(got, `{"success":true`) {
		t.Fatalf("reply = %s, want a success", got)
	}
}
