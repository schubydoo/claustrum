//go:build unix

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests cover a worktreeRoot in the repository. Each row name is a row of the
// side-by-side run against f6010b97 and 89cb6289 on a Linux VM. A macOS VM measured
// the same texts in rows MX3, MX4 and MX10 to MX15. In the rows, <B> is
// the temp dir and <T> the repository in it. <T2> is a second repository and <X> a
// plain folder. <B>/L is a symlink to <T>, and <B>/M a symlink to <T>/sub.
// worktreeRoot is unix-only.

type r0Fixture struct{ B, T, T2, X string }

func newR0Fixture(t *testing.T) *r0Fixture {
	t.Helper()
	requireGit(t)
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requireTempOutsideCheckout(t, b)
	f := &r0Fixture{B: b, T: filepath.Join(b, "T"), T2: filepath.Join(b, "T2"), X: filepath.Join(b, "X")}
	for _, d := range []string{filepath.Join(f.T, "sub"), f.X} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r0Repo(t, f.T)
	for link, target := range map[string]string{"L": f.T, "M": filepath.Join(f.T, "sub")} {
		if err := os.Symlink(target, filepath.Join(b, link)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// requireWorktreeListZ skips the test when git is older than 2.36, which has no
// `git worktree list -z`. The checkout tests need a working call.
func requireWorktreeListZ(t *testing.T) {
	t.Helper()
	requireGit(t)
	out, err := gitVersionCmd(t.Context()).Output()
	if err != nil {
		t.Fatalf("git version: %v", err)
	}
	m := regexp.MustCompile(`git version (\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("git version: does not parse %q", out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || major == 2 && minor < 36 {
		t.Skipf("git %s.%s has no `worktree list -z`, which needs git 2.36 or later", m[1], m[2])
	}
}

// r0Repo makes a repository at dir with one commit on main that adds a.txt.
func r0Repo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n", 0o644)
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "init")
}

// expand replaces the placeholders of a row with the paths of the fixture.
func (f *r0Fixture) expand(s string) string {
	return strings.NewReplacer("<B>", f.B, "<T2>", f.T2, "<T>", f.T, "<X>", f.X).Replace(s)
}

// dirtyWorktree adds a worktree of repo at leaf on a new branch. Then it modifies
// a.txt and adds an untracked new.txt in it.
func dirtyWorktree(t *testing.T, repo, leaf, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "add", "-q", "-b", branch, leaf)
	writeFile(t, filepath.Join(leaf, "a.txt"), "modified\n", 0o644)
	writeFile(t, filepath.Join(leaf, "new.txt"), "untracked\n", 0o644)
}

// keptDirty fails the test when the worktree at leaf lost its modified or its
// untracked file. It also fails it when the entry or the branch of leaf is gone.
func keptDirty(t *testing.T, repo, leaf, branch string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(leaf, "a.txt")); err != nil || string(b) != "modified\n" {
		t.Errorf("modified file of %s = %q, %v; a refusal deletes nothing", leaf, b, err)
	}
	if _, err := os.Stat(filepath.Join(leaf, "new.txt")); err != nil {
		t.Errorf("untracked file of %s: %v; a refusal deletes nothing", leaf, err)
	}
	if fi, err := os.Stat(filepath.Join(repo, ".git", "worktrees", filepath.Base(leaf))); err != nil || !fi.IsDir() {
		t.Errorf("entry of %s: %v; a refusal deletes nothing", leaf, err)
	}
	if !branchExists(t, repo, branch) {
		t.Errorf("branch %s of %s is gone; a refusal deletes nothing", branch, repo)
	}
}

// gitSteps returns the logged git calls without the configuration calls (the
// listings and the excludes read), each call as one string without its -c options.
// An empty log gives an empty slice.
func gitSteps(calls [][]string) []string {
	var steps []string
	for _, c := range calls {
		for len(c) >= 2 && c[0] == "-c" {
			c = c[2:]
		}
		if len(c) == 0 || c[0] == "" || c[0] == "config" {
			continue
		}
		steps = append(steps, strings.Join(c, " "))
	}
	return steps
}

const r0Tail = "; a worktree location must be outside the repository"

type r0RemoveRow struct {
	name, base, root, wp string
	// leaf is where the dirty worktree of <T> with branch wt1 is made. The remove
	// sends wp, which is absent unless it leads to leaf.
	leaf  string
	setup func(t *testing.T, f *r0Fixture)
	want  string
}

func (r r0RemoveRow) run(t *testing.T, wantSteps []string) {
	f := newR0Fixture(t)
	if r.setup != nil {
		r.setup(t, f)
	}
	leaf := f.expand(r.leaf)
	dirtyWorktree(t, f.T, leaf, "wt1")
	base := "<T>"
	if r.base != "" {
		base = r.base
	}
	calls := logGitArgv(t)
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": f.expand(base), "worktreePath": f.expand(r.wp), "branchName": "wt1",
		"worktreeRoot": f.expand(r.root)}))
	if want := refusalFrame(t, f.expand(r.want)); got != want {
		t.Errorf("remove =\n  %s\nwant\n  %s", got, want)
	}
	steps := gitSteps(calls())
	if wantSteps == nil && len(steps) != 0 {
		t.Errorf("git calls = %q, want none", steps)
	} else if strings.Join(steps, "\n") != strings.Join(wantSteps, "\n") {
		t.Errorf("git calls = %q, want %q", steps, wantSteps)
	}
	keptDirty(t, f.T, leaf, "wt1")
}

// A worktreeRoot that is baseRepo or lies beneath it is refused on remove, with no
// git call. Before, claustrum answered {"success":true} to each destructive row and
// deleted the worktree with its files, its entry and branch wt1. With the leaf
// absent it deleted wt1 while the live worktree had it checked out (rows Q8, Q5s).
// Rows Q2, Q10 and Q16 pin the order: the shape and baseRepo checks come first, and
// the <directory> symlink check comes after.
func TestWorktreeRemoveRootInRepo(t *testing.T) {
	const w1 = "<T>/.claude/worktrees/w1"
	for _, r := range []r0RemoveRow{
		{name: "Q1", root: "<T>/.claude", wp: w1, leaf: w1,
			want: "refusing to remove worktree: <T>/.claude is the repository <T> or inside it" + r0Tail},
		{name: "Q7", root: "<T>/.claude/", wp: w1, leaf: w1,
			want: "refusing to remove worktree: <T>/.claude/ is the repository <T> or inside it" + r0Tail},
		{name: "Q8", root: "<T>/.claude", wp: "<T>/.claude/cp/w1", leaf: w1,
			want: "refusing to remove worktree: <T>/.claude is the repository <T> or inside it" + r0Tail},
		{name: "Q8b", root: "<T>/.claude", wp: "<T>/.claude/cp/w1", leaf: "<T>/.claude/cp/w1",
			want: "refusing to remove worktree: <T>/.claude is the repository <T> or inside it" + r0Tail},
		{name: "Q2s", root: "<T>", wp: "<T>/cp/w1", leaf: "<T>/cp/w1",
			want: "refusing to remove worktree: <T> is the repository <T> or inside it" + r0Tail},
		{name: "Q3s", root: "<T>/.claude/worktrees", wp: "<T>/.claude/worktrees/cp/w1", leaf: "<T>/.claude/worktrees/cp/w1",
			want: "refusing to remove worktree: <T>/.claude/worktrees is the repository <T> or inside it" + r0Tail},
		{name: "Q4s", root: "<T>/sub", wp: "<T>/sub/cp/w1", leaf: "<T>/sub/cp/w1",
			want: "refusing to remove worktree: <T>/sub is the repository <T> or inside it" + r0Tail},
		{name: "Q5s", root: "<T>/missing", wp: "<T>/missing/cp/w1", leaf: w1,
			want: "refusing to remove worktree: <T>/missing is the repository <T> or inside it" + r0Tail},
		{name: "Q16", root: "<T>/.claude", wp: "<T>/.claude/cp/w1", leaf: w1,
			setup: func(t *testing.T, f *r0Fixture) {
				if err := os.MkdirAll(filepath.Join(f.T, ".claude"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.X, filepath.Join(f.T, ".claude", "cp")); err != nil {
					t.Fatal(err)
				}
			},
			want: "refusing to remove worktree: <T>/.claude is the repository <T> or inside it" + r0Tail},
		{name: "Q2", root: "<T>", wp: w1, leaf: w1,
			want: "refusing to remove worktree: <T>/.claude/worktrees/w1 is not <worktree location>/<directory>/<name> beneath <T>"},
		{name: "Q10", base: "<T>/sub/..", root: "<T>/.claude", wp: w1, leaf: w1,
			want: `refusing to remove worktree: <T>/sub/.. contains a ".." component; choose the session folder by its absolute path, without ".."`},
	} {
		t.Run(r.name, func(t *testing.T) { r.run(t, nil) })
	}
}

// A worktreeRoot whose symlinks lead to the repository is refused on remove after the
// worktree list call. So is a root whose baseRepo is a subdirectory below the git top
// level. The text names the git top level, the prefix of the resolved root that is the
// repository. Before, claustrum answered {"success":true} and deleted the worktree
// with its files, its entry and branch wt1.
func TestWorktreeRemoveRootResolvesIntoRepo(t *testing.T) {
	for _, r := range []r0RemoveRow{
		{name: "Q6s", root: "<B>/L", wp: "<B>/L/cp/w1", leaf: "<T>/cp/w1",
			want: "refusing to remove worktree: <B>/L leads into the repository <T> (at <T>)" + r0Tail},
		{name: "Q17", root: "<B>/L/sub", wp: "<B>/L/sub/cp/w1", leaf: "<T>/sub/cp/w1",
			want: "refusing to remove worktree: <B>/L/sub leads into the repository <T> (at <T>)" + r0Tail},
		{name: "Q18", root: "<B>/M", wp: "<B>/M/cp/w1", leaf: "<T>/sub/cp/w1",
			want: "refusing to remove worktree: <B>/M leads into the repository <T> (at <T>)" + r0Tail},
		{name: "Q19", base: "<T>/sub", root: "<T>/.claude", wp: "<T>/.claude/cp/w1", leaf: "<T>/.claude/cp/w1",
			want: "refusing to remove worktree: <T>/.claude leads into the repository <T>/sub (at <T>)" + r0Tail},
	} {
		t.Run(r.name, func(t *testing.T) {
			r.run(t, []string{"rev-parse --show-toplevel", "rev-parse --absolute-git-dir", "worktree list --porcelain -z"})
		})
	}
}

// A root that holds the repository is not refused (row Q15s). A root in a checkout
// of another repository gets the ownership refusal when <T> has a linked worktree
// (row Q20b). Both as on f6010b97 and 89cb6289.
func TestWorktreeRemoveRootOutsideRepo(t *testing.T) {
	t.Run("Q15s", func(t *testing.T) {
		f := newR0Fixture(t)
		leaf := filepath.Join(f.B, "cp", "w1")
		dirtyWorktree(t, f.T, leaf, "wt1")
		got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": f.T, "worktreePath": leaf, "branchName": "wt1", "worktreeRoot": f.B}))
		if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
			t.Errorf("remove = %s, want %s", got, want)
		}
		if _, err := os.Lstat(leaf); err == nil || branchExists(t, f.T, "wt1") {
			t.Errorf("worktree %s or branch wt1 is still there, but all three remove them", leaf)
		}
	})
	t.Run("Q20b", func(t *testing.T) {
		f := newR0Fixture(t)
		runGit(t, f.T, "worktree", "add", "-q", "-b", "w0", filepath.Join(f.X, "o", "w0"))
		r0Repo(t, f.T2)
		leaf := filepath.Join(f.T2, ".claude", "cp", "w1")
		dirtyWorktree(t, f.T2, leaf, "wt1")
		got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": f.T, "worktreePath": leaf, "branchName": "wt1",
			"worktreeRoot": filepath.Join(f.T2, ".claude")}))
		want := refusalFrame(t, f.expand("refusing to remove worktree: <T2>/.claude/cp/w1 is not a worktree of <T> "+
			"(<T2>/.claude/cp/w1 carries a .git file that does not name this repository's own worktree admin directory), "+
			"so it is left in place; remove it by hand if it is a leftover"))
		if got != want {
			t.Errorf("remove =\n  %s\nwant\n  %s", got, want)
		}
		keptDirty(t, f.T2, leaf, "wt1")
	})
}

// A worktreeRoot in the repository is refused on create with errorCode unsafe_path,
// before the root-chain step, and nothing is created. Before, claustrum sent its
// "inside a git checkout" text for rows K1 to K4, K8 and K9. Row K10 keeps that
// text, and rows K6 and K7 create.
func TestWorktreeCreateRootInRepo(t *testing.T) {
	const s7 = " is inside a git checkout (<T2> has a .git entry); a worktree location must be outside " +
		"every checkout, so that no session working in one can reach it — choose a directory that is not part of any repository"
	for _, r := range []struct {
		name, base, root, wp, want string
		created                    bool
	}{
		{name: "K1", root: "<T>/.claude", wp: "<T>/.claude/cp/c1",
			want: "refusing to create worktree: <T>/.claude is the repository <T> or inside it" + r0Tail},
		{name: "K2", root: "<T>", wp: "<T>/cp/c1",
			want: "refusing to create worktree: <T> is the repository <T> or inside it" + r0Tail},
		{name: "K3", root: "<T>/sub", wp: "<T>/sub/cp/c1",
			want: "refusing to create worktree: <T>/sub is the repository <T> or inside it" + r0Tail},
		{name: "K4", root: "<B>/L", wp: "<B>/L/cp/c1",
			want: "refusing to create worktree: <B>/L leads into the repository <T> (at <T>)" + r0Tail},
		{name: "K8", root: "<B>/L/sub", wp: "<B>/L/sub/cp/c1",
			want: "refusing to create worktree: <B>/L/sub leads into the repository <T> (at <T>)" + r0Tail},
		{name: "K9", base: "<T>/sub", root: "<T>/.claude", wp: "<T>/.claude/cp/c1",
			want: "refusing to create worktree: <T>/.claude leads into the repository <T>/sub (at <T>)" + r0Tail},
		{name: "K10", root: "<T2>/.claude", wp: "<T2>/.claude/cp/c1",
			want: "refusing to create worktree: <T2>/.claude" + s7},
		{name: "K6", root: "<B>", wp: "<B>/cp/c1", created: true},
		{name: "K7", root: "<B>/Tx", wp: "<B>/Tx/cp/c1", created: true},
	} {
		t.Run(r.name, func(t *testing.T) {
			f := newR0Fixture(t)
			dirtyWorktree(t, f.T, filepath.Join(f.T, ".claude", "worktrees", "w1"), "wt1")
			r0Repo(t, f.T2)
			for _, d := range []string{filepath.Join(f.T2, ".claude"), filepath.Join(f.B, "Tx")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			base := "<T>"
			if r.base != "" {
				base = r.base
			}
			wp := f.expand(r.wp)
			got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", map[string]any{
				"baseRepo": f.expand(base), "worktreePath": wp, "branchName": "c1",
				"worktreeRoot": f.expand(r.root)}))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` +
				jsonEscape(t, f.expand(r.want)) + `","errorCode":"unsafe_path"}}`
			if r.created {
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":"` + jsonEscape(t, wp) +
					`","sourceBranch":"main","branch":"c1"}}`
			}
			if got != want {
				t.Errorf("create =\n  %s\nwant\n  %s", got, want)
			}
			_, err := os.Lstat(filepath.Dir(wp))
			if made := err == nil || branchExists(t, f.T, "c1"); made != r.created {
				t.Errorf("created %s or branch c1 = %v, want %v", filepath.Dir(wp), made, r.created)
			}
		})
	}
}
