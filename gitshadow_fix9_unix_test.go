//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// profileOf are the profile variables of a call, in the order of its
// environment.
func profileOf(c envCall) []string {
	var out []string
	for _, kv := range c.env {
		switch envName(kv) {
		case "GIT_ALLOW_PROTOCOL", "GIT_TERMINAL_PROMPT", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE", "GIT_NO_LAZY_FETCH", "GIT_ASKPASS":
			out = append(out, kv)
		}
	}
	return out
}

// TestNoRepositoryCallsWithAnUnreadableHead: the HEAD of the repository has
// mode 000, so the trust check finds no repository. git.list_branches then
// makes the listing and the heavy `rev-parse --git-dir`. A create makes the
// listing and the light `rev-parse --is-inside-work-tree`, both with the six
// values of the state shadow in their order. Each call has
// GIT_DIR=<null device>. On 5fd08069 the two requests make 2 calls each, and
// the replies are the "no repository" answers (cell h1-mode000 of
// val-s3p2b2-7-linux2, Linux VM). The daemon has no state in that cell. With
// the member the create is not measured: its calls keep the four light values.
func TestNoRepositoryCallsWithAnUnreadableHead(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	f := newShadowFixture(t)
	head := filepath.Join(f.gitDir, "HEAD")
	if err := os.Chmod(head, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(head, 0o644) })
	gitCalls := func(calls []envCall) []envCall {
		return slices.DeleteFunc(calls, func(c envCall) bool { return c.excludesRead() })
	}
	check := func(what string, calls []envCall, last []string, profile []string) {
		t.Helper()
		calls = gitCalls(calls)
		if len(calls) != 2 || !calls[0].listing() || !argvEndsWith(calls[1].argv, last) {
			t.Fatalf("%s made %d calls, want the listing and %q", what, len(calls), last)
		}
		for i, c := range calls {
			if v, ok := lookupExact(c.env, "GIT_DIR"); !ok || v != os.DevNull {
				t.Errorf("%s call %d: GIT_DIR = %q, %v, want the null device", what, i+1, v, ok)
			}
			if got := profileOf(c); !slices.Equal(got, profile) {
				t.Errorf("%s call %d: the profile variables are %q\nwant %q", what, i+1, got, profile)
			}
		}
	}
	six := []string{"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_ASKPASS="}

	raw, calls := f.call(t, "git.list_branches", map[string]any{"path": f.repo})
	if raw != `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}` {
		t.Fatalf("git.list_branches = %s", raw)
	}
	check("git.list_branches", calls, []string{"rev-parse", "--git-dir"}, profileEnv(true))
	if v, _ := optionList(gitCalls(calls)[1].argv); len(v) != len(gitHardenHeavy) {
		t.Errorf("the rev-parse call of git.list_branches has %d items, want the heavy list of %d", len(v), len(gitHardenHeavy))
	}

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
	raw, calls = f.call(t, "git.worktree_create", params)
	if raw != `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}` {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	check("git.worktree_create", calls, []string{"rev-parse", "--is-inside-work-tree"}, six)
	if v, _ := optionList(gitCalls(calls)[1].argv); len(v) == 0 || v[0] != gitHardenLight[0] {
		t.Errorf("the rev-parse call of the create starts its list with %q, want the light list", v)
	}

	params["shadowDirOff"] = true
	raw, calls = f.call(t, "git.worktree_create", params)
	if !strings.Contains(raw, `"errorCode":"not_a_repo"`) {
		t.Fatalf("git.worktree_create with the member = %s", raw)
	}
	check("git.worktree_create with the member", calls, []string{"rev-parse", "--is-inside-work-tree"}, profileEnv(false))
	if got := daemonFolders(t, f.run); len(got) != 0 {
		t.Errorf("git-shadow holds %v, want no folder of the daemon", got)
	}
}

// TestDaemonFolderThatTakesNoNewEntry: the folder of the daemon has mode 0500
// after the first request. git.status then answers isRepo:false, and a create
// answers the error of the mkdir with the full path of its index folder. On
// 5fd08069 the lines are `{"isRepo":false,"clean":false}` and `git worktree add
// failed (checkout): mkdir <S>/.index-<n>: permission denied` (cell f1-0500 of
// val-s3p2b2-7-linux2, Linux VM).
func TestDaemonFolderThatTakesNoNewEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if raw, _ := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}); raw != entryStatusNoRepo {
		t.Errorf("git.status = %s\nwant %s", raw, entryStatusNoRepo)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	start := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): mkdir ` +
		strings.Trim(jsonQuoted(t, filepath.Join(dir, shadowIndexDirPrefix)), `"`)
	rest, ok := strings.CutPrefix(raw, start)
	n, tail, _ := strings.Cut(rest, ":")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" || tail != ` permission denied","errorCode":"worktree_add_failed"}}` {
		t.Errorf("git.worktree_create = %s\nwant it to start %s<n>: permission denied", raw, start)
	}
}
