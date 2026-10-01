//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests cover a worktreeRoot that leads into a checkout of the repository: the
// main checkout, the checkout of baseRepo or a linked worktree. Each row name is a row
// of the side-by-side runs against f6010b97 and 89cb6289 on Linux and macOS VMs. The
// fixture of r0Fixture is extended here. <B>/W and <B>/X/o/w0 are linked worktrees
// of <T>, made by plain git. worktreeRoot is unix-only.

const (
	r0cListing    = "config -z --list"
	r0cLinkedTail = "; a worktree location must be outside the repository's checkouts"
)

// r0cRemoveCalls are the git calls of a remove that is refused after `worktree list`,
// and r0cCreateCalls those of a refused create. The once-cached excludes read is left
// out. With it, that is 7 calls and 9 calls, as on f6010b97 and 89cb6289.
var (
	r0cRemoveCalls = []string{r0cListing, "rev-parse --show-toplevel", r0cListing,
		"rev-parse --absolute-git-dir", r0cListing, "worktree list --porcelain -z"}
	r0cCreateCalls = []string{r0cListing, "rev-parse --is-inside-work-tree", r0cListing,
		"rev-parse --show-toplevel", r0cListing, "rev-parse --absolute-git-dir", r0cListing,
		"worktree list --porcelain -z"}
)

// r0cCalls returns the logged git calls without the excludes read, each call as one
// string.
func r0cCalls(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		if len(c) > 1 && c[0] == "config" && c[1] == "--includes" {
			continue
		}
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// linked adds a clean worktree of <T> at path on a new branch.
func (f *r0Fixture) linked(t *testing.T, path, branch string) {
	t.Helper()
	path = f.expand(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.T, "worktree", "add", "-q", "-b", branch, path)
}

// withW adds the linked worktree <B>/W on branch lw, with an untracked folder sub.
func withW(t *testing.T, f *r0Fixture) {
	f.linked(t, "<B>/W", "lw")
	if err := os.MkdirAll(filepath.Join(f.B, "W", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// withW0 adds the linked worktree <B>/X/o/w0 on branch o0.
func withW0(t *testing.T, f *r0Fixture) {
	f.linked(t, "<B>/X/o/w0", "o0")
}

// r0cState lists the entries under <T>/.git/worktrees and the branches of <T>.
func r0cState(t *testing.T, f *r0Fixture) string {
	t.Helper()
	var names []string
	for _, d := range []string{"worktrees", filepath.Join("refs", "heads")} {
		entries, err := os.ReadDir(filepath.Join(f.T, ".git", d))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		for _, e := range entries {
			names = append(names, d+"/"+e.Name())
		}
	}
	return strings.Join(names, " ")
}

// sameFileOrSkip skips the test unless a and b are the same file, as on a
// case-insensitive macOS file system.
func sameFileOrSkip(t *testing.T, a, b string) {
	t.Helper()
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA != nil || errB != nil || !os.SameFile(fa, fb) {
		t.Skipf("%s and %s are not the same file: the file system is case-sensitive", a, b)
	}
}

type r0cRow struct {
	name, base, root, wp string
	// leaf is where a dirty worktree of <T> is made, on branch (wt1 when empty).
	leaf, branch string
	// setup runs before the leaf is made, and after runs after it. check runs at the
	// end of a remove that succeeds.
	setup, after, check func(t *testing.T, f *r0Fixture)
	// want is the refusal text, or "" for a remove that succeeds.
	want string
	// anyCalls skips the check of the git calls. calls replaces r0cRemoveCalls when it
	// is set.
	anyCalls bool
	calls    []string
}

func (r r0cRow) fixture(t *testing.T) (f *r0Fixture, leaf, branch string) {
	f = newR0Fixture(t)
	if r.setup != nil {
		r.setup(t, f)
	}
	branch = r.branch
	if branch == "" {
		branch = "wt1"
	}
	if r.leaf != "" {
		leaf = f.expand(r.leaf)
		dirtyWorktree(t, f.T, leaf, branch)
	}
	if r.after != nil {
		r.after(t, f)
	}
	return f, leaf, branch
}

func (r r0cRow) params(f *r0Fixture, branch string) map[string]any {
	base := "<T>"
	if r.base != "" {
		base = r.base
	}
	return map[string]any{"baseRepo": f.expand(base), "worktreePath": f.expand(r.wp),
		"branchName": branch, "worktreeRoot": f.expand(r.root)}
}

// remove sends the row to git.worktree_remove. A refusal must delete nothing: the
// leaf keeps its modified and its untracked file, and every entry and branch of <T>
// stays. A success must delete the leaf, its entry and its branch.
func (r r0cRow) remove(t *testing.T) {
	f, leaf, branch := r.fixture(t)
	_, leafErr := os.Stat(leaf)
	before := r0cState(t, f)
	calls := logGitArgv(t)
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", r.params(f, branch)))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`
	if r.want != "" {
		want = refusalFrame(t, f.expand(r.want))
	}
	if got != want {
		t.Errorf("remove =\n  %s\nwant\n  %s", got, want)
	}
	if r.want == "" {
		if _, err := os.Lstat(leaf); err == nil || branchExists(t, f.T, branch) {
			t.Errorf("worktree %s or branch %s is still there, but all three remove them", leaf, branch)
		}
		if r.check != nil {
			r.check(t, f)
		}
		return
	}
	wantCalls := r0cRemoveCalls
	if r.calls != nil {
		wantCalls = r.calls
	}
	if got := r0cCalls(calls()); !r.anyCalls && strings.Join(got, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("git calls = %q, want %q", got, wantCalls)
	}
	if leafErr == nil {
		keptDirty(t, f.T, leaf, branch)
	}
	if after := r0cState(t, f); after != before {
		t.Errorf("entries and branches = %s, want %s; a refusal deletes nothing", after, before)
	}
}

// create sends the row to git.worktree_create with branch c1. The refusal must
// create nothing and delete nothing.
func (r r0cRow) create(t *testing.T) {
	f, leaf, branch := r.fixture(t)
	before := r0cState(t, f)
	calls := logGitArgv(t)
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", r.params(f, "c1")))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` +
		jsonEscape(t, f.expand(r.want)) + `","errorCode":"unsafe_path"}}`
	if got != want {
		t.Errorf("create =\n  %s\nwant\n  %s", got, want)
	}
	if got := r0cCalls(calls()); strings.Join(got, "\n") != strings.Join(r0cCreateCalls, "\n") {
		t.Errorf("git calls = %q, want %q", got, r0cCreateCalls)
	}
	if _, err := os.Lstat(f.expand(r.wp)); err == nil || branchExists(t, f.T, "c1") {
		t.Errorf("create made %s or branch c1; a refusal creates nothing", f.expand(r.wp))
	}
	if leaf != "" {
		keptDirty(t, f.T, leaf, branch)
	}
	if after := r0cState(t, f); after != before {
		t.Errorf("entries and branches = %s, want %s; a refusal changes nothing", after, before)
	}
}

// A root that leads into the main checkout, or into the checkout of baseRepo, is
// refused on remove with the main form. The walk goes from the resolved root up, and
// "(at ...)" names the outermost path that is the same file as one of the two. Before,
// claustrum answered {"success":true} to rows E1, W1 and Y6. It deleted the dirty
// leaf, its entry and its branch, or with E1 the branch of a live worktree.
func TestWorktreeRemoveRootInMainCheckout(t *testing.T) {
	requireWorktreeListZ(t)
	const w1 = "<T>/.claude/worktrees/w1"
	for _, r := range []r0cRow{
		// A root that does not exist, under a symlink to <T>. Its longest existing
		// prefix is resolved. The live worktree w1 holds wt1.
		{name: "E1", root: "<B>/L/missing", wp: "<B>/L/missing/cp/w1", leaf: w1,
			want: "refusing to remove worktree: <B>/L/missing leads into the repository <T> (at <T>)" + r0Tail},
		// baseRepo is the linked worktree W, and the root is in the main checkout.
		{name: "W1", base: "<B>/W", root: "<T>/.claude", wp: "<T>/.claude/cp/w1", leaf: "<T>/.claude/cp/w1",
			setup: withW,
			want:  "refusing to remove worktree: <T>/.claude leads into the repository <B>/W (at <T>)" + r0Tail},
		// The root is in W, the checkout that holds baseRepo W/sub.
		{name: "Y4", base: "<B>/W/sub", root: "<B>/W/.claude", wp: "<B>/W/.claude/cp/w1", leaf: "<B>/W/.claude/cp/w1",
			setup: withW,
			want:  "refusing to remove worktree: <B>/W/.claude leads into the repository <B>/W/sub (at <B>/W)" + r0Tail},
		// The main checkout wins over the linked worktree w1 nearer to the root.
		{name: "Y6", base: "<B>/W", root: w1 + "/r", wp: w1 + "/r/cp/w2", leaf: w1 + "/r/cp/w2", branch: "wt2",
			setup: func(t *testing.T, f *r0Fixture) { withW(t, f); f.linked(t, w1, "wt1") },
			want:  "refusing to remove worktree: <T>/.claude/worktrees/w1/r leads into the repository <B>/W (at <T>)" + r0Tail},
		// The linked worktree W2 inside the main checkout holds baseRepo W2/sub. So the
		// checkout of baseRepo and the main checkout both hold the root. The main
		// checkout names the path. It is also the outer one here.
		{name: "T2b", base: "<T>/in/W2/sub", root: "<T>/in/W2/.claude",
			wp: "<T>/in/W2/.claude/cp/w1", leaf: "<T>/in/W2/.claude/cp/w1",
			setup: func(t *testing.T, f *r0Fixture) {
				f.linked(t, "<T>/in/W2", "lw2")
				if err := os.MkdirAll(filepath.Join(f.T, "in", "W2", "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "refusing to remove worktree: <T>/in/W2/.claude leads into the repository <T>/in/W2/sub (at <T>)" + r0Tail},
	} {
		t.Run(r.name, r.remove)
	}
}

// A root that leads into a linked worktree of the repository is refused on remove with
// the linked form. The listed path is compared as a string with the resolved root.
// Before, claustrum answered {"success":true} to each row. It deleted the dirty leaf,
// its entry and its branch, or with Y10 the branch of a live worktree.
func TestWorktreeRemoveRootInLinkedWorktree(t *testing.T) {
	requireWorktreeListZ(t)
	const w0 = "<B>/X/o/w0"
	for _, r := range []r0cRow{
		// W2 is also row Y2c: the gitdir file of w0 as git wrote it.
		{name: "W2", root: w0, wp: w0 + "/cp/w1", leaf: w0 + "/cp/w1", setup: withW0,
			want: "refusing to remove worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <T> (at <B>/X/o/w0)" + r0cLinkedTail},
		{name: "Y1", base: "<T>/sub", root: w0, wp: w0 + "/cp/w1", leaf: w0 + "/cp/w1", setup: withW0,
			want: "refusing to remove worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <T>/sub (at <B>/X/o/w0)" + r0cLinkedTail},
		{name: "Y5", base: "<B>/W", root: w0, wp: w0 + "/cp/w1", leaf: w0 + "/cp/w1",
			setup: func(t *testing.T, f *r0Fixture) { withW(t, f); withW0(t, f) },
			want:  "refusing to remove worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <B>/W (at <B>/X/o/w0)" + r0cLinkedTail},
		// Of two nested linked worktrees, the outer w0 is listed first and named.
		{name: "Y6n", root: w0 + "/in/w3/r", wp: w0 + "/in/w3/r/cp/w1", leaf: w0 + "/in/w3/r/cp/w1",
			setup: func(t *testing.T, f *r0Fixture) { withW0(t, f); f.linked(t, w0+"/in/w3", "o3") },
			want:  "refusing to remove worktree: <B>/X/o/w0/in/w3/r leads into <B>/X/o/w0, a worktree of the repository <T> (at <B>/X/o/w0)" + r0cLinkedTail},
		// A root that does not exist, under a symlink to w0. The live worktree w1
		// holds wt1.
		{name: "Y10", root: "<B>/L2/missing", wp: "<B>/L2/missing/cp/w1", leaf: "<T>/.claude/worktrees/w1",
			setup: func(t *testing.T, f *r0Fixture) {
				withW0(t, f)
				if err := os.Symlink(f.expand(w0), filepath.Join(f.B, "L2")); err != nil {
					t.Fatal(err)
				}
			},
			want: "refusing to remove worktree: <B>/L2/missing leads into <B>/X/o/w0, a worktree of the repository <T> (at <B>/X/o/w0)" + r0cLinkedTail},
	} {
		t.Run(r.name, r.remove)
	}
}

// A root that does not exist or does not resolve, and that leads into no checkout, is
// refused on remove after `worktree list`. In row Y9 the linked worktree w0 was
// deleted with the leaf inside it. In row T3 w0 is locked and was deleted. In row T4
// two levels of the root are missing. In row T5 the root is a symlink to a path that
// does not exist. Before, claustrum answered {"success":true} to rows Y9 and T3 to
// T5. It deleted branch wt1, and in row Y9 also the entry of the leaf. In rows T3 to
// T5 the live worktree w1 holds wt1.
func TestWorktreeRemoveRootMissing(t *testing.T) {
	const w0, w1 = "<B>/X/o/w0", "<T>/.claude/worktrees/w1"
	gone := func(t *testing.T, f *r0Fixture) {
		if err := os.RemoveAll(f.expand(w0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []r0cRow{
		{name: "Y9", root: w0, wp: w0 + "/cp/w1", leaf: w0 + "/cp/w1", setup: withW0, after: gone,
			want: "failed to remove worktree: the worktree location <B>/X/o/w0 is not reachable (<B>/X/o/w0 does not " +
				"exist); nothing was removed — retry once it is available, or remove the worktree by hand"},
		{name: "T3", root: w0, wp: w0 + "/cp/w1", leaf: w1, setup: withW0,
			after: func(t *testing.T, f *r0Fixture) {
				runGit(t, f.T, "worktree", "lock", f.expand(w0))
				gone(t, f)
			},
			want: "failed to remove worktree: the worktree location <B>/X/o/w0 is not reachable (<B>/X/o/w0 does not " +
				"exist); nothing was removed — retry once it is available, or remove the worktree by hand"},
		{name: "T4", root: "<B>/gone/r", wp: "<B>/gone/r/cp/w1", leaf: w1,
			want: "failed to remove worktree: the worktree location <B>/gone/r is not reachable (<B>/gone/r does not " +
				"exist); nothing was removed — retry once it is available, or remove the worktree by hand"},
		{name: "T5", root: "<B>/D", wp: "<B>/D/cp/w1", leaf: w1,
			setup: func(t *testing.T, f *r0Fixture) {
				if err := os.Symlink(filepath.Join(f.B, "nothere"), filepath.Join(f.B, "D")); err != nil {
					t.Fatal(err)
				}
			},
			want: "failed to remove worktree: the worktree location <B>/D is not reachable (lstat <B>/nothere: no such " +
				"file or directory); nothing was removed — retry once it is available, or remove the worktree by hand"},
	} {
		t.Run(r.name, r.remove)
	}
}

// stubGitCall puts a `git` first on PATH that runs the git found before it, except
// for a call whose arguments hold the words of call. That call runs cmd instead. It
// also sets the D5 deadline gitTimeout to d, and 0 means no deadline.
func stubGitCall(t *testing.T, call, cmd string, d time.Duration) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *\" " + call + " \"*) " + cmd + " ;; esac\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := gitTimeout
	gitTimeout = d
	t.Cleanup(func() { gitTimeout = old })
}

// The row has the shape of W2: the root is the linked worktree w0, and the dirty leaf
// is inside it. If the D5 deadline stops the first `worktree list` call, the remove
// answers the work-tree refusal with the exec error. No git call runs after it, and
// nothing is deleted. The reference has no such deadline, so this is claustrum's
// choice (not measured).
func TestWorktreeRemoveListKilledRefuses(t *testing.T) {
	r := r0cRow{root: "<B>/X/o/w0", wp: "<B>/X/o/w0/cp/w1", leaf: "<B>/X/o/w0/cp/w1", setup: withW0,
		after: func(t *testing.T, f *r0Fixture) { stubGitCall(t, "worktree list", "exec sleep 30", 2*time.Second) },
		want:  "failed to remove worktree: cannot determine the repository's work tree: signal: killed"}
	r.remove(t)
}

// baseRepo is W/sub, and git lists the linked worktree W as <B>/S/W, through the
// symlink <B>/S to <B>. The root is W/.claude, with the dirty leaf inside it. If the
// D5 deadline stops `rev-parse --show-toplevel`, the remove answers the work-tree
// refusal with the exec error, and no git call runs after it. Nothing is deleted.
// Without that refusal, the checkout tests miss W, because git lists it under
// another spelling. The reference has no such deadline, so this is claustrum's
// choice (not measured).
func TestWorktreeRemoveTopLevelKilledRefuses(t *testing.T) {
	r := r0cRow{base: "<B>/W/sub", root: "<B>/W/.claude", wp: "<B>/W/.claude/cp/w1", leaf: "<B>/W/.claude/cp/w1",
		setup: func(t *testing.T, f *r0Fixture) {
			withW(t, f)
			if err := os.Symlink(f.B, filepath.Join(f.B, "S")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.T, ".git", "worktrees", "W", "gitdir"), f.expand("<B>/S/W/.git\n"), 0o644)
		},
		after: func(t *testing.T, f *r0Fixture) {
			stubGitCall(t, "rev-parse --show-toplevel", "exec sleep 30", 2*time.Second)
		},
		calls: []string{r0cListing, "rev-parse --show-toplevel"},
		want:  "failed to remove worktree: cannot determine the repository's work tree: signal: killed"}
	r.remove(t)
}

// After any other failure of `worktree list --porcelain -z`, the daemon runs `worktree
// list --porcelain` with its listing. If that call fails too, the removal is refused
// and nothing is deleted. 89cb6289 answers so with exit status 128 (row DG2s-g on a
// Linux VM). Here both calls exit 129, which is not measured. Mutation: go on after
// the failed calls.
func TestWorktreeRemoveListFailsTwiceRefuses(t *testing.T) {
	r := r0cRow{root: "<B>/X/o/w0", wp: "<B>/X/o/w0/cp/w1", leaf: "<B>/X/o/w0/cp/w1", setup: withW0,
		after: func(t *testing.T, f *r0Fixture) { stubGitCall(t, "worktree list", "exit 129", 0) },
		calls: append(slices.Clone(r0cRemoveCalls), r0cListing, "worktree list --porcelain"),
		want:  "failed to remove worktree: cannot list the repository's worktrees: exit status 129"}
	r.remove(t)
}

// When only the -z form fails, the second call succeeds, and the daemon reads its
// lines. The W2-shaped row then gets its refusal, as with a working -z form. A git
// older than 2.36 has no -z form. This case is claustrum's choice (not measured).
// Mutation: do not read the output of the second call.
func TestWorktreeRemoveListWithoutZIsRead(t *testing.T) {
	r := r0cRow{root: "<B>/X/o/w0", wp: "<B>/X/o/w0/cp/w1", leaf: "<B>/X/o/w0/cp/w1", setup: withW0,
		after: func(t *testing.T, f *r0Fixture) { stubGitCall(t, "worktree list --porcelain -z", "exit 129", 0) },
		calls: append(slices.Clone(r0cRemoveCalls), r0cListing, "worktree list --porcelain"),
		want:  "refusing to remove worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <T> (at <B>/X/o/w0)" + r0cLinkedTail}
	r.remove(t)
}

// If the first `worktree list` call fails and the D5 deadline stops the second one,
// the answer is the worktree-list refusal with "signal: killed". Nothing is deleted.
// The reference has no such deadline, so this text is claustrum's own (not measured).
func TestWorktreeRemoveListSecondCallKilledRefuses(t *testing.T) {
	r := r0cRow{root: "<B>/X/o/w0", wp: "<B>/X/o/w0/cp/w1", leaf: "<B>/X/o/w0/cp/w1", setup: withW0,
		after: func(t *testing.T, f *r0Fixture) {
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := "#!/bin/sh\ncase \" $* \" in\n" +
				"*\" worktree list --porcelain -z \"*) exit 129 ;;\n" +
				"*\" worktree list --porcelain \"*) exec sleep 30 ;;\n" +
				"esac\nexec '" + real + "' \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			old := gitTimeout
			gitTimeout = 2 * time.Second
			t.Cleanup(func() { gitTimeout = old })
		},
		anyCalls: true,
		want:     "failed to remove worktree: cannot list the repository's worktrees: signal: killed"}
	r.remove(t)
}

// A root that no checkout test matches goes on, and the remove deletes the worktree, as
// on f6010b97 and 89cb6289. In row Y2 git lists w0 through the symlink <B>/S to
// <B>/X, and the listed spelling is not resolved. In row Y7 the root holds w0.
func TestWorktreeRemoveRootBesideLinkedWorktree(t *testing.T) {
	const w0 = "<B>/X/o/w0"
	keptW0 := func(t *testing.T, f *r0Fixture) {
		if _, err := os.Stat(filepath.Join(f.X, "o", "w0", "a.txt")); err != nil || !branchExists(t, f.T, "o0") {
			t.Errorf("linked worktree w0 or branch o0 is gone (%v); the remove deletes only the leaf", err)
		}
	}
	for _, r := range []r0cRow{
		{name: "Y2", root: w0, wp: w0 + "/cp/w1", leaf: w0 + "/cp/w1", setup: withW0, check: keptW0,
			after: func(t *testing.T, f *r0Fixture) {
				if err := os.Symlink(f.X, filepath.Join(f.B, "S")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(f.T, ".git", "worktrees", "w0", "gitdir"), f.expand("<B>/S/o/w0/.git\n"), 0o644)
				cmd := exec.Command("git", "-C", f.T, "worktree", "list", "--porcelain")
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
				cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
				out, err := cmd.Output()
				if err != nil || !strings.Contains(string(out), f.expand("worktree <B>/S/o/w0\n")) ||
					strings.Contains(string(out), "prunable") {
					t.Fatalf("fixture: git lists\n%s(%v), want <B>/S/o/w0 and nothing prunable", out, err)
				}
			}},
		{name: "Y7", root: "<B>/X/o", wp: "<B>/X/o/cp/w1", leaf: "<B>/X/o/cp/w1", setup: withW0, check: keptW0},
	} {
		t.Run(r.name, r.remove)
	}
}

// On a case-insensitive macOS file system a case variant of <T> is the same file as
// <T>. Row E8: the root <B>/t/.claude matches the main checkout by identity, and
// "(at ...)" keeps the spelling of the request. Row Y3: a case variant of the linked
// worktree w0 does not match, because the linked form compares strings. It goes on to
// the ownership refusal. Both rows are skipped on a case-sensitive file system.
func TestWorktreeRemoveRootCaseVariant(t *testing.T) {
	t.Run("E8", func(t *testing.T) {
		r := r0cRow{root: "<B>/t/.claude", wp: "<B>/t/.claude/cp/w1", leaf: "<T>/.claude/cp/w1",
			setup: func(t *testing.T, f *r0Fixture) { sameFileOrSkip(t, filepath.Join(f.B, "t"), f.T) },
			want:  "refusing to remove worktree: <B>/t/.claude leads into the repository <T> (at <B>/t)" + r0Tail}
		r.remove(t)
	})
	t.Run("Y3", func(t *testing.T) {
		r := r0cRow{root: "<B>/x/o/w0", wp: "<B>/x/o/w0/cp/w1", leaf: "<B>/X/o/w0/cp/w1", anyCalls: true,
			setup: func(t *testing.T, f *r0Fixture) { sameFileOrSkip(t, filepath.Join(f.B, "x"), f.X); withW0(t, f) },
			want: "refusing to remove worktree: <B>/x/o/w0/cp/w1 is not a worktree of <T> (<B>/x/o/w0/cp/w1 carries " +
				"a .git file naming an admin directory whose own record is of a different worktree), so it is left " +
				"in place; remove it by hand if it is a leftover"}
		r.remove(t)
	})
}

// The create twins of the rows above get the same forms with errorCode unsafe_path,
// and nothing is created. f6010b97 makes the same 9 git calls first. 89cb6289 also
// makes 9 calls, in this order. Before, claustrum sent its "inside a git checkout"
// text for rows E2, W3, W4, Y11a and Y11c.
func TestWorktreeCreateRootInCheckout(t *testing.T) {
	requireWorktreeListZ(t)
	const w0 = "<B>/X/o/w0"
	const w1 = "<T>/.claude/worktrees/w1"
	for _, r := range []r0cRow{
		// <T>/.claude does not exist.
		{name: "E2", base: "<T>/sub", root: "<T>/.claude", wp: "<T>/.claude/cp/c1",
			want: "refusing to create worktree: <T>/.claude leads into the repository <T>/sub (at <T>)" + r0Tail},
		{name: "W3", base: "<B>/W", root: "<T>/.claude", wp: "<T>/.claude/cp/c1", leaf: "<T>/.claude/cp/w1",
			setup: withW,
			want:  "refusing to create worktree: <T>/.claude leads into the repository <B>/W (at <T>)" + r0Tail},
		{name: "W4", root: w0, wp: w0 + "/cp/c1", leaf: w0 + "/cp/w1", setup: withW0,
			want: "refusing to create worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <T> (at <B>/X/o/w0)" + r0cLinkedTail},
		{name: "Y11a", base: "<T>/sub", root: w0, wp: w0 + "/cp/c1", leaf: w0 + "/cp/w1", setup: withW0,
			want: "refusing to create worktree: <B>/X/o/w0 leads into <B>/X/o/w0, a worktree of the repository <T>/sub (at <B>/X/o/w0)" + r0cLinkedTail},
		{name: "Y11b", base: "<B>/W/sub", root: "<B>/W/.claude", wp: "<B>/W/.claude/cp/c1", leaf: "<B>/W/.claude/cp/w1",
			setup: withW,
			want:  "refusing to create worktree: <B>/W/.claude leads into the repository <B>/W/sub (at <B>/W)" + r0Tail},
		{name: "Y11c", base: "<B>/W", root: w1 + "/r", wp: w1 + "/r/cp/c1", leaf: w1 + "/r/cp/w2", branch: "wt2",
			setup: func(t *testing.T, f *r0Fixture) { withW(t, f); f.linked(t, w1, "wt1") },
			want:  "refusing to create worktree: <T>/.claude/worktrees/w1/r leads into the repository <B>/W (at <T>)" + r0Tail},
	} {
		t.Run(r.name, r.create)
	}
}

// On create, a D5 kill of `rev-parse --show-toplevel` or `worktree list` does not
// refuse, and the checkout tests run without that answer. The root-chain step still
// refuses the root, because the root or a directory above it holds a .git entry. It sends its own
// "inside a git checkout" text with errorCode unsafe_path, and nothing is created.
// That is claustrum's choice (not measured). The rows:
//   - show-toplevel: the shape of row Y11b, baseRepo W/sub and the root in W, with W
//     listed through the symlink <B>/S. Only the top level of baseRepo matches W.
//   - worktree list: the shape of row W4, the root in the linked worktree w0.
//   - both: the shape of row E2, baseRepo <T>/sub and the root in <T>.
func TestWorktreeCreateCheckoutCallKilled(t *testing.T) {
	const w0 = "<B>/X/o/w0"
	killTop := func(t *testing.T) { stubGitCall(t, "rev-parse --show-toplevel", "exec sleep 30", 2*time.Second) }
	killList := func(t *testing.T) { stubGitCall(t, "worktree list", "exec sleep 30", 2*time.Second) }
	for name, r := range map[string]r0cRow{
		"show-toplevel": {base: "<B>/W/sub", root: "<B>/W/.claude", wp: "<B>/W/.claude/cp/c1",
			setup: func(t *testing.T, f *r0Fixture) {
				withW(t, f)
				if err := os.Symlink(f.B, filepath.Join(f.B, "S")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(f.T, ".git", "worktrees", "W", "gitdir"), f.expand("<B>/S/W/.git\n"), 0o644)
			},
			after: func(t *testing.T, f *r0Fixture) { killTop(t) },
			want:  checkoutRootText("<B>/W/.claude", "<B>/W")},
		"worktree list": {root: w0, wp: w0 + "/cp/c1", setup: withW0,
			after: func(t *testing.T, f *r0Fixture) { killList(t) },
			want:  checkoutRootText(w0, w0)},
		"both": {base: "<T>/sub", root: "<T>/.claude", wp: "<T>/.claude/cp/c1",
			after: func(t *testing.T, f *r0Fixture) { killTop(t); killList(t) },
			want:  checkoutRootText("<T>/.claude", "<T>")},
	} {
		t.Run(name, r.create)
	}
}
