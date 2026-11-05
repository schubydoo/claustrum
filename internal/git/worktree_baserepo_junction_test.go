package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The walk of a baseRepo spelled through a junction followed by "..", as b3081b51
// answers it on Windows (measured on Windows). A Windows test with real junctions is in
// worktree_baserepo_junction_windows_test.go.

// dotDotJunction is the fixture of these tests. A is the repository <out>/T, the meaning of
// <out>/J/../T by text. B is the repository <out>/DEEP/T. J links to <out>/DEEP/x, so
// the file system takes <out>/J/../T to B.
type dotDotJunction struct {
	A, B    wtFixture
	J       string
	spelled string
}

// newDotDotJunction builds the fixture. link makes J, a junction on Windows. live adds
// the worktree w1 to A and to B.
func newDotDotJunction(t *testing.T, link func(t *testing.T, link, target string), live bool) dotDotJunction {
	t.Helper()
	a := newWTFixture(t, false)
	out := filepath.Dir(a.top)
	bTop := filepath.Join(out, "DEEP", "T")
	initTrustMain(t, bTop)
	b := wtFixture{realGit: a.realGit, top: bTop, base: bTop, regDir: filepath.Join(bTop, ".git", "worktrees")}
	if err := os.MkdirAll(filepath.Join(out, "DEEP", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := dotDotJunction{A: a, B: b, J: filepath.Join(out, "J")}
	link(t, f.J, filepath.Join(out, "DEEP", "x"))
	sep := string(filepath.Separator)
	f.spelled = f.J + sep + ".." + sep + "T"
	if live {
		runGit(t, a.top, "worktree", "add", "-q", "-b", "w1", a.leaf())
		runGit(t, b.top, "worktree", "add", "-q", "-b", "w1", b.leaf())
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	return f
}

// wantW1 checks that the worktree w1 of f, its registration and its branch exist as want
// says.
func (f wtFixture) wantW1(t *testing.T, name string, want bool) {
	t.Helper()
	leaf, reg, ref := exists(f.leaf()), exists(filepath.Join(f.regDir, "w1")), f.hasRef(t, "w1")
	if leaf != want || reg != want || ref != want {
		t.Errorf("%s: leaf %v, registration %v, branch %v, want %v", name, leaf, reg, ref, want)
	}
}

// containmentFrames are the remove and create frames of a worktreePath in B, which is
// not inside baseRepo as sent.
func (f dotDotJunction) containmentFrames(t *testing.T) (remove, create string) {
	t.Helper()
	const guidance = "; session worktrees are only created and removed under <repository>/.claude/worktrees"
	return spellRemoveFrame(t, "refusing to remove worktree: "+tickPath(f.B.leaf())+" is not inside the repository "+f.spelled+guidance),
		spellCreateFrame(t, "refusing to create worktree: "+tickPath(f.B.leaf())+" is not inside the repository "+f.spelled+guidance, "unsafe_path")
}

// spelledWalkFails walks the names as sent. A link before ".." passes, and ".." drops it
// by text. Without foldFirst a missing name or a file before ".." fails, and so does a
// dangling link. With foldFirst ".." drops each of them before it is tested, as b3081b51
// judges a baseRepo <T>\missing\.. and <T>\t.txt\.. on Windows (measured on Windows).
// baseRepoWalkFails takes foldFirst. Mutations: drop the !fi.IsDir() test (t.txt/.. turns
// it red). Ignore the stat error (missing/.. turns it red). Drop the fold arm (the
// foldFirst rows turn red).
func TestSpelledWalkFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the link of this test stands for a junction off Windows")
	}
	out := t.TempDir()
	for _, d := range []string{filepath.Join(out, "repo", "sub"), filepath.Join(out, "DEEP", "x")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(out, "repo", "t.txt"), "t\n", 0o644)
	if err := os.Symlink(filepath.Join(out, "DEEP", "x"), filepath.Join(out, "J")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "gone"), filepath.Join(out, "dl")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(out)
	for _, c := range []struct {
		p          string
		want, fold bool // fold is the answer with foldFirst
	}{
		{"J/../repo", false, false},
		{filepath.Join(out, "J") + "/../repo", false, false},
		{filepath.Join(out, "repo"), false, false},
		{filepath.Join(out, "repo", "t.txt"), false, false},
		{filepath.Join(out, "repo") + "/./sub/..", false, false},
		{".", false, false},
		{"../" + filepath.Base(out) + "/repo", false, false},
		{filepath.Join(out, "repo") + "/missing/..", true, false},
		{filepath.Join(out, "repo") + "/t.txt/..", true, false},
		{filepath.Join(out, "repo") + "/missing/x/../..", true, false},
		{filepath.Join(out, "dl") + "/..", true, false},
		{filepath.Join(out, "missing"), true, true},
		{filepath.Join(out, "repo") + "/missing/../gone", true, true},
	} {
		if got := spelledWalkFails(c.p, os.Stat, false); got != c.want {
			t.Errorf("spelledWalkFails(%q, false) = %v, want %v", c.p, got, c.want)
		}
		if got := spelledWalkFails(c.p, os.Stat, true); got != c.fold {
			t.Errorf("spelledWalkFails(%q, true) = %v, want %v", c.p, got, c.fold)
		}
	}
	// baseRepoWalkFails folds first, and the walk without foldFirst fails.
	old := baseRepoWalksAsSpelled
	t.Cleanup(func() { baseRepoWalksAsSpelled = old })
	baseRepoWalksAsSpelled = true
	p := filepath.Join(out, "repo") + "/t.txt/.."
	if baseRepoWalkFails(p) || !linkWalkFails(p, false) {
		t.Errorf("baseRepoWalkFails(%q) = %v, linkWalkFails(%q, false) = %v, want false and true",
			p, baseRepoWalkFails(p), p, linkWalkFails(p, false))
	}
}

// junctionWalkLikeWindows makes linkWalkFails walk as on Windows, and makes the
// evalSymlinks seam fail like Windows for a path with one of links before its last name.
func junctionWalkLikeWindows(t *testing.T, links ...string) {
	t.Helper()
	oldEval, oldSpelled := evalSymlinks, baseRepoWalksAsSpelled
	t.Cleanup(func() { evalSymlinks, baseRepoWalksAsSpelled = oldEval, oldSpelled })
	baseRepoWalksAsSpelled = true
	evalSymlinks = func(p string) (string, error) {
		for _, l := range links {
			if strings.HasPrefix(p, l+string(filepath.Separator)) {
				return "", &os.PathError{Op: "lstat", Path: l, Err: syscall.ENOTDIR}
			}
		}
		return oldEval(p)
	}
}

// With the walk of Windows and a link in the place of the junction, a baseRepo
// <out>/J/../T passes the trust-root test. The worktreePath in B then gets the
// containment refusal, which names baseRepo as sent, and nothing is deleted or made. A
// path with a junction before its last name gets its branches. The remove and create
// rows in A are in the Windows test, because off Windows the kernel takes the ".." after
// the link. Mutation: in linkWalkFails, drop the arm of baseRepoWalksAsSpelled (each row
// gets the trust-root refusal or isRepo false).
func TestBaseRepoJunctionWalkSimulated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TestBaseRepoJunctionDotDotLexical runs these rows on real junctions")
	}
	symlink := func(t *testing.T, link, target string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("remove-outside-repo", func(t *testing.T) {
		f := newDotDotJunction(t, symlink, true)
		junctionWalkLikeWindows(t, f.J)
		want, _ := f.containmentFrames(t)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.spelled, "worktreePath": f.B.leaf()}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.A.wantW1(t, "A", true)
		f.B.wantW1(t, "B", true)
	})
	t.Run("create-outside-repo", func(t *testing.T) {
		f := newDotDotJunction(t, symlink, false)
		junctionWalkLikeWindows(t, f.J)
		_, want := f.containmentFrames(t)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": f.spelled, "worktreePath": f.B.leaf(), "branchName": "w1"}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.A.wantW1(t, "A", false)
		f.B.wantW1(t, "B", false)
	})
	t.Run("list-branches-junction-at-parent", func(t *testing.T) {
		requireGit(t)
		base := t.TempDir()
		initTrustMain(t, filepath.Join(base, "real", "repo"))
		jp := filepath.Join(base, "JP")
		symlink(t, jp, filepath.Join(base, "real"))
		resetUserExcludesCache(t)
		clearDaemonConfigEnv(t)
		junctionWalkLikeWindows(t, jp)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.list_branches", map[string]any{"path": filepath.Join(jp, "repo")}))
		if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":["main"]}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	// With the walk of Windows, ".." drops a missing name or a file before it untested,
	// as b3081b51 lists <T>\missing\.. and <T>\t.txt\.. (measured on Windows). Off
	// Windows the kernel walks git's path, so a refused count shows that the walk passed.
	// Mutation: pass false to linkWalkFails in gitListBranches (each row gets isRepo false).
	t.Run("list-branches-fold-dotdot", func(t *testing.T) {
		requireGit(t)
		repo := filepath.Join(t.TempDir(), "T")
		initTrustMain(t, repo)
		writeFile(t, filepath.Join(repo, "t.txt"), "t\n", 0o644)
		resetUserExcludesCache(t)
		clearDaemonConfigEnv(t)
		plantEnv(t, "GIT_CONFIG_COUNT=x")
		junctionWalkLikeWindows(t)
		want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` +
			jsonString(t, `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`) + `}}`
		for _, p := range []string{filepath.Join(repo, "missing") + "/..", filepath.Join(repo, "t.txt") + "/.."} {
			if got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.list_branches", map[string]any{"path": p})); got != want {
				t.Errorf("%s\n got %s\nwant %s", p, got, want)
			}
		}
	})
}
