package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin how git.worktree_create picks the start commit for a
// sourceBranch. Every expectation below was measured side by side against
// f6010b97 on a Linux VM. Each fixture is a bare origin (O.git), a helper clone
// that pushes to it (W), and a clone of it that is the baseRepo (T).

type sourceFixture struct {
	t       *testing.T
	root    string
	origin  string
	helper  string
	repo    string
	base    string // the baseRepo sent: repo, or a linked worktree of it
	counter int
}

func newSourceFixture(t *testing.T) *sourceFixture {
	t.Helper()
	requireGit(t)
	isolateGitConfig(t)
	root := resolveTestRoot(t, t.TempDir())
	fx := &sourceFixture{
		t:      t,
		root:   root,
		origin: filepath.Join(root, "O.git"),
		helper: filepath.Join(root, "W"),
		repo:   filepath.Join(root, "T"),
	}
	fx.base = fx.repo
	runGit(t, root, "init", "-q", "--bare", "-b", "main", "O.git")
	runGit(t, root, "init", "-q", "-b", "main", "W")
	runGit(t, fx.helper, "remote", "add", "origin", fx.origin)
	fx.commit(fx.helper, map[string]string{".gitignore": ".claude/worktrees/\n", "t.txt": "t\n"})
	runGit(t, fx.helper, "push", "-q", "origin", "main")
	runGit(t, root, "clone", "-q", fx.origin, "T")
	return fx
}

// out runs git in dir with the same isolated identity and config as runGit and
// returns its trimmed stdout.
func (fx *sourceFixture) out(dir string, args ...string) string {
	fx.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	b, err := cmd.Output()
	if err != nil {
		fx.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(b))
}

// commit writes files (an empty value removes that path) on the branch checked
// out in dir, commits, and returns the new commit id. A unique counter file
// keeps two otherwise-identical commits distinct.
func (fx *sourceFixture) commit(dir string, files map[string]string) string {
	fx.t.Helper()
	fx.counter++
	files["n.txt"] = strings.Repeat("n", fx.counter) + "\n"
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if body == "" {
			runGit(fx.t, dir, "rm", "-q", "-r", "--", name)
			continue
		}
		writeFile(fx.t, p, body, 0o644)
	}
	runGit(fx.t, dir, "add", "-A")
	runGit(fx.t, dir, "commit", "-q", "-m", "c")
	return fx.out(dir, "rev-parse", "HEAD")
}

// originBranch creates feat on origin with one commit on top of main and fetches
// it into T. It returns the commit.
func (fx *sourceFixture) originBranch(name string) string {
	fx.t.Helper()
	runGit(fx.t, fx.helper, "checkout", "-q", "-b", name, "main")
	c := fx.commit(fx.helper, map[string]string{"marker": "base\n"})
	fx.push(name)
	return c
}

// pushOrigin adds a commit to branch name in W, pushes it, and fetches T.
func (fx *sourceFixture) pushOrigin(name string, files map[string]string) string {
	fx.t.Helper()
	runGit(fx.t, fx.helper, "checkout", "-q", name)
	c := fx.commit(fx.helper, files)
	fx.push(name)
	return c
}

func (fx *sourceFixture) push(name string) {
	fx.t.Helper()
	runGit(fx.t, fx.helper, "push", "-q", "origin", name)
	runGit(fx.t, fx.repo, "fetch", "-q", "origin")
}

// localCommit adds a commit to local branch name in T (creating it at from when
// from is non-empty) and returns to main.
func (fx *sourceFixture) localCommit(name, from string, files map[string]string) string {
	fx.t.Helper()
	if from != "" {
		runGit(fx.t, fx.repo, "branch", "-q", "--no-track", name, from)
	}
	runGit(fx.t, fx.repo, "checkout", "-q", name)
	c := fx.commit(fx.repo, files)
	runGit(fx.t, fx.repo, "checkout", "-q", "main")
	return c
}

// dropLooseObject deletes one loose object from T, to make a commit or tree
// unreadable. Objects are read-only on Windows, so the mode is widened first.
func (fx *sourceFixture) dropLooseObject(sha string) {
	fx.t.Helper()
	p := filepath.Join(fx.repo, ".git", "objects", sha[:2], sha[2:])
	_ = os.Chmod(p, 0o644)
	if err := os.Remove(p); err != nil {
		fx.t.Fatalf("remove object %s: %v", sha, err)
	}
}

func (fx *sourceFixture) wtPath() string {
	return filepath.Join(fx.base, ".claude", "worktrees", "w1")
}

// create sends git.worktree_create for w1 and returns the reply's result member.
func (fx *sourceFixture) create(extra map[string]any) string {
	fx.t.Helper()
	params := map[string]any{"baseRepo": fx.base, "branchName": "w1", "worktreePath": fx.wtPath()}
	for k, v := range extra {
		params[k] = v
	}
	got := dispatchRaw(fx.t, newTestServer(fx.t), rpcLine(fx.t, "git.worktree_create", params))
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil || env.Result == nil {
		fx.t.Fatalf("reply %s: no result (%v)", got, err)
	}
	return string(env.Result)
}

func successFrame(t *testing.T, path, source, branch string) string {
	t.Helper()
	b, err := json.Marshal(worktreeResult{Success: true, Path: path, SourceBranch: source, Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wantCreated asserts the success frame, the commit refs/heads/w1 points at, and
// the reflog line of the new branch.
func (fx *sourceFixture) wantCreated(got, wantSource, wantCommit, wantReflog string) {
	fx.t.Helper()
	if want := successFrame(fx.t, fx.wtPath(), wantSource, "w1"); got != want {
		fx.t.Fatalf("result = %s\nwant     %s", got, want)
	}
	if c := fx.out(fx.repo, "rev-parse", "refs/heads/w1"); c != wantCommit {
		fx.t.Errorf("w1 = %s, want %s", c, wantCommit)
	}
	if c := fx.out(fx.wtPath(), "rev-parse", "HEAD"); c != wantCommit {
		fx.t.Errorf("worktree HEAD = %s, want %s", c, wantCommit)
	}
	if r := fx.out(fx.repo, "reflog", "show", "--format=%gs", "refs/heads/w1"); r != wantReflog {
		fx.t.Errorf("w1 reflog = %q, want %q", r, wantReflog)
	}
}

// wantFromCommit is wantCreated for a start commit that a candidate supplied:
// sourceBranch is echoed as sent, and the reflog names the commit id.
func (fx *sourceFixture) wantFromCommit(source, commit string) {
	fx.t.Helper()
	fx.wantCreated(fx.create(map[string]any{"sourceBranch": source}), source, commit, "branch: Created from "+commit)
}

// Only the local branch resolves. The start commit is passed as a
// full id, so the reflog reads "Created from <sha>", not "refs/heads/feat".
func TestWorktreeSourceLocalOnly(t *testing.T) {
	fx := newSourceFixture(t)
	l := fx.localCommit("feat", "main", map[string]string{"marker": "local\n"})
	fx.wantFromCommit("feat", l)
}

// Only refs/remotes/origin/<s> resolves, for a plain and a slash name.
func TestWorktreeSourceOriginOnly(t *testing.T) {
	fx := newSourceFixture(t)
	r := fx.originBranch("feat")
	fx.wantFromCommit("feat", r)

	fx2 := newSourceFixture(t)
	r2 := fx2.originBranch("team/feat")
	fx2.wantFromCommit("team/feat", r2)
}

// The local branch is behind origin → origin.
func TestWorktreeSourceLocalBehindTakesOrigin(t *testing.T) {
	fx := newSourceFixture(t)
	fx.originBranch("feat")
	runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	r := fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	fx.wantFromCommit("feat", r)
}

// Every case below has the local branch NOT an ancestor of
// origin, and a merge base. The net diff from the merge base to the local commit
// over the root .claude and .mcp.json (case-insensitive) decides.
func TestWorktreeSourceClaudeConfigRule(t *testing.T) {
	cases := []struct {
		name       string
		local      []map[string]string // local commits, in order, on top of origin/feat's base
		remote     map[string]string   // one origin commit on feat, or nil for "local ahead"
		wantOrigin bool
	}{
		{"ahead plain", []map[string]string{{"local.txt": "l\n"}}, nil, false},
		{"ahead .claude", []map[string]string{{".claude/settings.json": "{}\n"}}, nil, true},
		{"diverged plain", []map[string]string{{"local.txt": "l\n"}}, map[string]string{"marker": "remote\n"}, false},
		{"diverged root .mcp.json", []map[string]string{{".mcp.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, true},
		{"diverged upper case .Claude", []map[string]string{{".Claude/settings.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, true},
		{"diverged upper case .MCP.json", []map[string]string{{".MCP.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, true},
		{"diverged root file named .claude", []map[string]string{{".claude": "x\n"}}, map[string]string{"marker": "remote\n"}, true},
		{"diverged nested sub/.claude", []map[string]string{{"sub/.claude/settings.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, false},
		{"diverged nested sub/.mcp.json", []map[string]string{{"sub/.mcp.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, false},
		{"diverged .claude.json", []map[string]string{{".claude.json": "{}\n"}}, map[string]string{"marker": "remote\n"}, false},
		{"diverged .claudex dir", []map[string]string{{".claudex/x": "x\n"}}, map[string]string{"marker": "remote\n"}, false},
		// net tree difference, not history: added then removed on the local side.
		{"diverged .claude added then removed", []map[string]string{{".claude/settings.json": "{}\n"}, {".claude/settings.json": ""}}, map[string]string{"marker": "remote\n"}, false},
		// against the merge base, not against origin: both sides add the same file.
		{"diverged both add identical .claude", []map[string]string{{".claude/settings.json": "{}\n"}}, map[string]string{".claude/settings.json": "{}\n"}, true},
		// a .claude change on the origin side only does not count.
		{"diverged origin-only .claude", []map[string]string{{"local.txt": "l\n"}}, map[string]string{".claude/settings.json": "{}\n"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newSourceFixture(t)
			fx.originBranch("feat")
			runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
			var l string
			for _, files := range c.local {
				l = fx.localCommit("feat", "", files)
			}
			r := fx.out(fx.repo, "rev-parse", "refs/remotes/origin/feat")
			if c.remote != nil {
				r = fx.pushOrigin("feat", c.remote)
			}
			want := l
			if c.wantOrigin {
				want = r
			}
			fx.wantFromCommit("feat", want)
		})
	}
}

// rootCommit makes a parentless commit in T with the given message.
func (fx *sourceFixture) rootCommit(msg string) string {
	fx.t.Helper()
	return fx.out(fx.repo, "commit-tree", fx.out(fx.repo, "rev-parse", "main^{tree}"), "-m", msg)
}

// No merge base (unrelated histories) → origin. The origin ref is written
// with update-ref.
func TestWorktreeSourceNoMergeBaseTakesOrigin(t *testing.T) {
	fx := newSourceFixture(t)
	l := fx.rootCommit("local root")
	r := fx.rootCommit("origin root")
	runGit(t, fx.repo, "update-ref", "refs/heads/feat", l)
	runGit(t, fx.repo, "update-ref", "refs/remotes/origin/feat", r)
	fx.wantFromCommit("feat", r)
}

// The .claude diff fails (the local .claude tree object is gone) → origin.
func TestWorktreeSourceDiffErrorTakesOrigin(t *testing.T) {
	fx := newSourceFixture(t)
	fx.originBranch("feat")
	runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	fx.localCommit("feat", "", map[string]string{".claude/settings.json": "{}\n"})
	r := fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	fx.dropLooseObject(fx.out(fx.repo, "rev-parse", "feat:.claude"))
	fx.wantFromCommit("feat", r)
}

// checkoutFailureFixture builds a repo whose local feat is chosen (it diverged
// from origin with no .claude change) but whose tree cannot be checked out,
// because the tree of uniq/ is gone. The tree sits outside the .claude pathspec,
// so the diff passes. It returns the missing tree's id.
func checkoutFailureFixture(fx *sourceFixture) string {
	fx.t.Helper()
	fx.originBranch("feat")
	runGit(fx.t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	fx.localCommit("feat", "", map[string]string{"uniq/u.txt": "u\n"})
	fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	tree := fx.out(fx.repo, "rev-parse", "feat:uniq")
	fx.dropLooseObject(tree)
	return tree
}

// wantCheckoutFailure sends the create and asserts the one-line checkout-failure
// frame and the rollback end state: no leaf, no w1 branch or reflog, and exactly
// wantRegs left in the main repository's .git/worktrees directory, which exists.
func (fx *sourceFixture) wantCheckoutFailure(tree string, wantRegs []string) {
	fx.t.Helper()
	got := fx.create(map[string]any{"sourceBranch": "feat"})
	var res worktreeResult
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		fx.t.Fatal(err)
	}
	if res.Success || res.ErrorCode != "worktree_add_failed" ||
		!strings.HasPrefix(res.Error, "git worktree add failed (checkout): ") ||
		!strings.Contains(res.Error, tree) || strings.Contains(res.Error, "\n") {
		fx.t.Fatalf("result = %s, want a one-line worktree_add_failed (checkout) naming tree %s", got, tree)
	}
	if _, err := os.Lstat(fx.wtPath()); !os.IsNotExist(err) {
		fx.t.Errorf("worktree directory left behind (err=%v)", err)
	}
	if out, ok := git(fx.repo, "rev-parse", "--verify", "--quiet", "refs/heads/w1"); ok {
		fx.t.Errorf("branch w1 left behind at %s", out)
	}
	if _, ok := git(fx.repo, "reflog", "exists", "refs/heads/w1"); ok {
		fx.t.Errorf("reflog of w1 left behind")
	}
	entries, err := os.ReadDir(filepath.Join(fx.repo, ".git", "worktrees"))
	if err != nil {
		fx.t.Fatalf(".git/worktrees after the rollback: %v, want a directory", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != strings.Join(wantRegs, ",") {
		fx.t.Errorf(".git/worktrees holds %v after the rollback, want %v", names, wantRegs)
	}
}

// A checkout (read-tree) failure fails the request and rolls back. With the
// repo's first linked worktree, .git/worktrees/ stays behind, empty, as measured
// against f6010b97. A retry fails the same way.
func TestWorktreeSourceCheckoutFailureFailsRequest(t *testing.T) {
	fx := newSourceFixture(t)
	tree := checkoutFailureFixture(fx)
	if _, err := os.Lstat(filepath.Join(fx.repo, ".git", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("fixture: .git/worktrees exists before the call (err=%v)", err)
	}
	fx.wantCheckoutFailure(tree, nil)
	fx.wantCheckoutFailure(tree, nil)
}

// With a linked worktree as baseRepo, the new worktree's registration lives in
// the main repository's .git/worktrees, not under the baseRepo (whose .git is a
// file). The rollback removes it and keeps the linked worktree's own entry, so a
// retry at the same path fails the same way, as measured against f6010b97.
func TestWorktreeSourceCheckoutFailureLinkedBaseRepo(t *testing.T) {
	fx := newSourceFixture(t)
	tree := checkoutFailureFixture(fx)
	runGit(t, fx.repo, "worktree", "add", "-q", "-b", "lw", filepath.Join(fx.root, "LW"), "main")
	fx.base = filepath.Join(fx.root, "LW")
	fx.wantCheckoutFailure(tree, []string{"LW"})
	fx.wantCheckoutFailure(tree, []string{"LW"})
}

// A local ref that points at a missing object counts as absent → origin.
func TestWorktreeSourceLocalMissingObjectTakesOrigin(t *testing.T) {
	fx := newSourceFixture(t)
	r := fx.originBranch("feat")
	dead := fx.rootCommit("gone")
	runGit(t, fx.repo, "update-ref", "refs/heads/feat", dead)
	fx.dropLooseObject(dead)
	fx.wantFromCommit("feat", r)
}

// Revision syntax resolves on the local side and on the origin side, and
// sourceBranch is echoed exactly as sent.
func TestWorktreeSourceRevisionSyntax(t *testing.T) {
	fx := newSourceFixture(t)
	l1 := fx.localCommit("feat", "main", map[string]string{"marker": "l1\n"})
	fx.localCommit("feat", "", map[string]string{"marker": "l2\n"})
	fx.wantFromCommit("feat~1", l1)

	fx2 := newSourceFixture(t)
	r1 := fx2.originBranch("feat")
	fx2.pushOrigin("feat", map[string]string{"marker": "r2\n"})
	fx2.wantFromCommit("feat~1", r1)
}

// An annotated tag object stored in the origin ref is peeled, and the
// peeled commit (not the tag object) is what the reflog names.
func TestWorktreeSourcePeelsAnnotatedTag(t *testing.T) {
	fx := newSourceFixture(t)
	fx.originBranch("feat")
	runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	r := fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	runGit(t, fx.repo, "tag", "-a", "-m", "t", "tg", r)
	tagObj := fx.out(fx.repo, "rev-parse", "refs/tags/tg")
	runGit(t, fx.repo, "update-ref", "refs/remotes/origin/feat", tagObj)
	fx.wantFromCommit("feat", r)
}

// SourceBranch "HEAD" follows the refs/remotes/origin/HEAD symref, and
// "HEAD" is echoed.
func TestWorktreeSourceHEADFollowsOriginHEAD(t *testing.T) {
	fx := newSourceFixture(t)
	runGit(t, fx.repo, "remote", "set-head", "origin", "main")
	r := fx.pushOrigin("main", map[string]string{"marker": "remote\n"})
	fx.wantFromCommit("HEAD", r)
}

// Only the namespace named origin is read.
func TestWorktreeSourceIgnoresOtherRemotes(t *testing.T) {
	fx := newSourceFixture(t)
	l := fx.localCommit("feat", "main", map[string]string{"marker": "local\n"})
	ahead := fx.localCommit("feat", "", map[string]string{"marker": "ahead\n"})
	runGit(t, fx.repo, "update-ref", "refs/heads/feat", l)
	runGit(t, fx.repo, "update-ref", "refs/remotes/upstream/feat", ahead)
	fx.wantFromCommit("feat", l)
}

// With no usable source, and with sourceBranch omitted or "",
// the worktree starts at HEAD even though origin/main is ahead, and the current
// branch is echoed.
func TestWorktreeSourceFallbackUsesHEAD(t *testing.T) {
	for _, extra := range []map[string]any{nil, {"sourceBranch": ""}, {"sourceBranch": "nosuch"}} {
		fx := newSourceFixture(t)
		head := fx.out(fx.repo, "rev-parse", "HEAD")
		fx.pushOrigin("main", map[string]string{"marker": "remote\n"})
		fx.wantCreated(fx.create(extra), "main", head, "branch: Created from HEAD")
	}
}

// On a detached HEAD with no usable source, sourceBranch is omitted.
func TestWorktreeSourceDetachedHEADOmitsSource(t *testing.T) {
	fx := newSourceFixture(t)
	head := fx.out(fx.repo, "rev-parse", "HEAD")
	runGit(t, fx.repo, "checkout", "-q", "--detach")
	fx.wantCreated(fx.create(nil), "", head, "branch: Created from HEAD")
}

// ExistingBranch attaches even when sourceBranch resolves, and the reply
// still echoes the sourceBranch. A missing existingBranch creates w1 from the
// chosen candidate.
func TestWorktreeSourceWithExistingBranch(t *testing.T) {
	fx := newSourceFixture(t)
	ex := fx.localCommit("ex", "main", map[string]string{"marker": "ex\n"})
	fx.originBranch("feat")
	got := fx.create(map[string]any{"sourceBranch": "feat", "existingBranch": "ex"})
	if want := successFrame(t, fx.wtPath(), "feat", "ex"); got != want {
		t.Fatalf("result = %s\nwant     %s", got, want)
	}
	if c := fx.out(fx.wtPath(), "rev-parse", "HEAD"); c != ex {
		t.Errorf("worktree HEAD = %s, want ex %s", c, ex)
	}

	fx2 := newSourceFixture(t)
	r := fx2.originBranch("feat")
	fx2.wantCreated(fx2.create(map[string]any{"sourceBranch": "feat", "existingBranch": "nope"}),
		"feat", r, "branch: Created from "+r)
}
