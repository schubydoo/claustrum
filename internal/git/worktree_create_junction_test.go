package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// The create of a worktree whose path goes through a junction, as b3081b51 makes it
// on Windows (measured on Windows). A Windows test with real junctions is in
// worktree_create_junction_windows_test.go.

// junctionLeaf is the fixture of these tests. repo is <out>/real/repo. With the link
// at the repo, <out>/J leads to repo, and baseRepo is J. With the link at the parent,
// <out>/JP leads to <out>/real, and baseRepo is JP/repo. leaf is the worktreePath as sent.
type junctionLeaf struct {
	link, repo, baseRepo, leaf string
}

// newJunctionLeaf builds the fixture with the link at the repo (toParent false) or at
// its parent (toParent true). link makes the link, a junction on Windows.
func newJunctionLeaf(t *testing.T, link func(t *testing.T, link, target string), toParent bool) junctionLeaf {
	t.Helper()
	requireGit(t)
	out := resolveTestRoot(t, t.TempDir())
	f := junctionLeaf{repo: filepath.Join(out, "real", "repo")}
	initTrustMain(t, f.repo)
	if toParent {
		f.link = filepath.Join(out, "JP")
		link(t, f.link, filepath.Join(out, "real"))
		f.baseRepo = filepath.Join(f.link, "repo")
	} else {
		f.link = filepath.Join(out, "J")
		link(t, f.link, f.repo)
		f.baseRepo = f.link
	}
	f.leaf = filepath.Join(f.baseRepo, ".claude", "worktrees", "w1")
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	return f
}

func (f junctionLeaf) create(t *testing.T) string {
	t.Helper()
	return gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": f.baseRepo, "branchName": "w1", "worktreePath": f.leaf}))
}

func (f junctionLeaf) remove(t *testing.T) string {
	t.Helper()
	return gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": f.baseRepo, "worktreePath": f.leaf, "branchName": "w1"}))
}

// createdFrame is the success frame of the create: the path as sent.
func (f junctionLeaf) createdFrame(t *testing.T) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, f.leaf) +
		`,"sourceBranch":"main","branch":"w1"}}`
}

// wantDisk checks the disk of the real repository after the create (made true) or
// after the remove (made false), as on b3081b51. The new worktree, its
// registration and the branch are in repo. The gitdir record and the .git file of
// the leaf name the paths with the link resolved. After the remove the folders
// .claude/worktrees and .git/worktrees stay, and the link stays.
func (f junctionLeaf) wantDisk(t *testing.T, made bool) {
	t.Helper()
	realLeaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	reg := filepath.Join(f.repo, ".git", "worktrees", "w1")
	if leaf, r, b := exists(realLeaf), exists(reg), branchExists(t, f.repo, "w1"); leaf != made || r != made || b != made {
		t.Errorf("leaf %v, registration %v, branch %v, want each %v", leaf, r, b, made)
	}
	if !exists(f.link) || !exists(filepath.Join(f.repo, ".claude", "worktrees")) || !exists(filepath.Join(f.repo, ".git", "worktrees")) {
		t.Errorf("the link, .claude/worktrees or .git/worktrees of %s is gone", f.repo)
	}
	if !made {
		return
	}
	if got, want := mustRead(t, filepath.Join(reg, "gitdir")), filepath.ToSlash(realLeaf)+"/.git\n"; got != want {
		t.Errorf("gitdir record = %q, want %q", got, want)
	}
	if got, want := mustRead(t, filepath.Join(realLeaf, ".git")), "gitdir: "+filepath.ToSlash(reg)+"\n"; got != want {
		t.Errorf(".git file of the leaf = %q, want %q", got, want)
	}
}

// leafLikeWindows makes the checkpoint resolve as on Windows: evalSymlinks fails for
// a path below link (junctionWalkLikeWindows), and the path of a handle is the path
// with the link resolved.
func leafLikeWindows(t *testing.T, link string) {
	t.Helper()
	oldFlag, oldFinal := leafResolvesByHandle, finalLeafPath
	t.Cleanup(func() { leafResolvesByHandle, finalLeafPath = oldFlag, oldFinal })
	leafResolvesByHandle, finalLeafPath = true, filepath.EvalSymlinks
	junctionWalkLikeWindows(t, link)
}

// With the resolve of Windows and a link in the place of the junction, the create
// and the remove succeed, and the disk is as on b3081b51.
// Mutation: in leafResolve, answer the error of evalSymlinks with no finalLeafPath
// (the create answers "the create holds no checkpoint of the new worktree").
func TestWorktreeCreateThroughJunctionSimulated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TestWorktreeCreateThroughJunction runs these rows on real junctions")
	}
	for _, row := range []struct {
		name     string
		toParent bool
	}{{"junction-at-repo", false}, {"junction-at-parent", true}} {
		t.Run(row.name, func(t *testing.T) {
			f := newJunctionLeaf(t, linkInPlaceOfJunction, row.toParent)
			leafLikeWindows(t, f.link)
			if got, want := f.create(t), f.createdFrame(t); got != want {
				t.Fatalf("create\ngot  %s\nwant %s", got, want)
			}
			f.wantDisk(t, true)
			if got := f.remove(t); got != spellRemoveOK {
				t.Errorf("remove\ngot  %s\nwant %s", got, spellRemoveOK)
			}
			f.wantDisk(t, false)
		})
	}
}

// A remove through the link at the parent of a live worktree that git made in the real
// repository removes the leaf, the registration and the branch, as b3081b51 does
// (measured on Windows). Mutation: at locateInRepoWorktree, call
// evalSymlinks in place of leafResolve (the remove takes the gone-worktree branch:
// the registration and the branch go, and the leaf stays).
func TestWorktreeRemoveLiveThroughJunctionSimulated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TestWorktreeCreateThroughJunction runs this row on a real junction")
	}
	f := newJunctionLeaf(t, linkInPlaceOfJunction, true)
	f.addLive(t)
	leafLikeWindows(t, f.link)
	if got := f.remove(t); got != spellRemoveOK {
		t.Errorf("remove\ngot  %s\nwant %s", got, spellRemoveOK)
	}
	f.wantDisk(t, false)
}

// A remove through the link at the parent, whose handle path of the leaf is home, gets
// the home refusal with that path, and the leaf, the registration and the branch stay.
// The path as sent passes the home guard of the caller. Not measured: claustrum's own
// guard (D2). Mutation: in locateInRepoWorktree, drop the home test of the handle branch
// (the remove succeeds and deletes the leaf that stands for home).
func TestWorktreeRemoveHandlePathHomeGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the link of this test stands for a junction off Windows")
	}
	f := newJunctionLeaf(t, linkInPlaceOfJunction, true)
	f.addLive(t)
	realLeaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	leafLikeWindows(t, f.link)
	t.Setenv("HOME", realLeaf)
	want := spellRemoveFrame(t, "worktreePath must not be or contain the home directory: "+strconv.Quote(realLeaf))
	if got := f.remove(t); got != want {
		t.Errorf("remove\ngot  %s\nwant %s", got, want)
	}
	f.wantDisk(t, true)
}

// addLive makes the worktree w1 with git in the real repository, at the real leaf.
func (f junctionLeaf) addLive(t *testing.T) {
	t.Helper()
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(f.repo, ".claude", "worktrees", "w1"))
	resetUserExcludesCache(t)
}

// A checkout that fails through the link at the repo rolls the create back, as
// b3081b51 does through a junction (measured on Windows). The leaf goes, and so do the
// moved registration (by its identity, with no read of its record) and the branch. The
// link and the real repository stay.
// Mutation: in leafResolve, answer the error of evalSymlinks with no finalLeafPath
// (the create answers the checkpoint text before the checkout).
func TestWorktreeCreateThroughJunctionRollbackSimulated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TestWorktreeCreateThroughJunctionRollback runs this on a real junction")
	}
	f := newJunctionLeaf(t, linkInPlaceOfJunction, false)
	realGit := mustLookGit(t)
	installGitSlowStub(t, realGit)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
	leafLikeWindows(t, f.link)
	wantError(t, f.create(t), "git worktree add failed (checkout): fatal: synthetic checkout failure", "worktree_add_failed")
	f.wantRolledBack(t)
}

// wantRolledBack checks the disk after a rollback of a create through the link: no
// leaf, no registration, no branch, and the link and the folders above stay.
func (f junctionLeaf) wantRolledBack(t *testing.T) {
	t.Helper()
	realLeaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	if exists(realLeaf) || exists(filepath.Join(f.repo, ".git", "worktrees", "w1")) || branchExists(t, f.repo, "w1") {
		t.Errorf("leaf %v, registration %v, branch %v, want all gone", exists(realLeaf),
			exists(filepath.Join(f.repo, ".git", "worktrees", "w1")), branchExists(t, f.repo, "w1"))
	}
	if !exists(f.link) || !exists(filepath.Join(f.repo, ".claude", "worktrees")) || !exists(filepath.Join(f.repo, ".git", "HEAD")) {
		t.Errorf("the link, .claude/worktrees or the repository %s is gone", f.repo)
	}
}

// leafResolve takes the path of a handle only with leafResolvesByHandle and only
// after evalSymlinks failed. Mutations: drop `|| !leafResolvesByHandle` (row "off"
// answers the handle path). Call finalLeafPath first (row "resolves" answers it).
// Answer the error of evalSymlinks after a good handle (row "handle" fails).
func TestLeafResolve(t *testing.T) {
	oldEval, oldFlag, oldFinal := evalSymlinks, leafResolvesByHandle, finalLeafPath
	t.Cleanup(func() { evalSymlinks, leafResolvesByHandle, finalLeafPath = oldEval, oldFlag, oldFinal })
	errLink := errors.New("not a directory")
	errHandle := errors.New("no handle")
	for _, c := range []struct {
		name          string
		flag          bool
		evalErr, hErr error
		want          string
		wantErr       error
		wantRecord    string
	}{
		{name: "resolves", flag: true, want: "/eval"},
		{name: "off", flag: false, evalErr: errLink, wantErr: errLink},
		{name: "handle", flag: true, evalErr: errLink, want: "/handle", wantRecord: "/handle"},
		{name: "no handle", flag: true, evalErr: errLink, hErr: errHandle, wantErr: errLink},
	} {
		t.Run(c.name, func(t *testing.T) {
			leafResolvesByHandle = c.flag
			evalSymlinks = func(string) (string, error) {
				if c.evalErr != nil {
					return "", c.evalErr
				}
				return "/eval", nil
			}
			finalLeafPath = func(string) (string, error) {
				if c.hErr != nil {
					return "", c.hErr
				}
				return "/handle", nil
			}
			// leaf does not exist, so resolveAsFarAsExists keeps it.
			leaf := filepath.Join(t.TempDir(), "x", "leaf")
			got, err := leafResolve(leaf)
			if got != c.want || !errors.Is(err, c.wantErr) {
				t.Errorf("leafResolve = %q, %v, want %q, %v", got, err, c.want, c.wantErr)
			}
			want := c.wantRecord
			if want == "" {
				want = leaf
			}
			if got := leafRecordPath(leaf + string(filepath.Separator)); got != want {
				t.Errorf("leafRecordPath = %q, want %q", got, want)
			}
		})
	}
}

// The gitdir record that the staging step writes, and its test after the move, use
// the path of a handle for a leaf that evalSymlinks cannot resolve. b3081b51 writes
// the record with the junction resolved (measured on Windows).
// Mutations: in stagedRecordText or in stagedRecordRefusal, call
// resolveAsFarAsExists in place of leafRecordPath (the record names the leaf as
// sent, or the record of the handle path is refused).
func TestStagedRecordThroughHandle(t *testing.T) {
	f := newStepFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	handle := filepath.Join(f.repo, "real", "w1")
	for _, d := range []string{leaf, handle} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldEval, oldFlag, oldFinal := evalSymlinks, leafResolvesByHandle, finalLeafPath
	t.Cleanup(func() { evalSymlinks, leafResolvesByHandle, finalLeafPath = oldEval, oldFlag, oldFinal })
	leafResolvesByHandle = true
	evalSymlinks = func(p string) (string, error) {
		if p == leaf {
			return "", errors.New("not a directory")
		}
		return oldEval(p)
	}
	finalLeafPath = func(string) (string, error) { return handle, nil }
	text := stagedRecordText(leaf)
	if want := filepath.ToSlash(handle) + "/.git\n"; text != want {
		t.Errorf("stagedRecordText = %q, want %q", text, want)
	}
	f.u.accepted.staged = stagedEntry{name: "w9"}
	mustWrite(t, filepath.Join(f.gitDir, "worktrees", "w9", "gitdir"), text)
	if got := stagedRecordRefusal(f.u.accepted, leaf); got != "" {
		t.Errorf("stagedRecordRefusal = %q, want none", got)
	}
}

// The tests of the checkpoint after the add, and the rmdir of undoFailedAdd, compare
// the path of the same resolve as the checkpoint (leafResolve). A path that now
// leads elsewhere still refuses. Mutations: at the test of verifyCreatedWorktree or
// of undoFailedAdd, call filepath.EvalSymlinks (row "same" gets the "no longer
// leads" text, or the leaf stays).
func TestCheckpointResolvesByHandle(t *testing.T) {
	oldEval, oldFlag, oldFinal := evalSymlinks, leafResolvesByHandle, finalLeafPath
	t.Cleanup(func() { evalSymlinks, leafResolvesByHandle, finalLeafPath = oldEval, oldFlag, oldFinal })
	for _, c := range []struct {
		name      string
		moved     bool
		wantText  bool
		wantStays bool
	}{{"same", false, false, false}, {"moved", true, true, true}} {
		t.Run(c.name, func(t *testing.T) {
			leaf := filepath.Join(t.TempDir(), "w1")
			if err := os.Mkdir(leaf, 0o755); err != nil {
				t.Fatal(err)
			}
			handle := "/handle/w1"
			leafResolvesByHandle = true
			evalSymlinks = func(string) (string, error) { return "", errors.New("not a directory") }
			finalLeafPath = func(string) (string, error) { return handle, nil }
			cp := checkpointCreatedWorktree(leaf)
			defer cp.release()
			if cp.info == nil || cp.resolved != handle {
				t.Fatalf("checkpoint resolved %q, info %v, want %q", cp.resolved, cp.info != nil, handle)
			}
			if c.moved {
				handle = "/handle/elsewhere"
			}
			if got := verifyCreatedWorktree(leaf, cp); (got != "") != c.wantText {
				t.Errorf("verifyCreatedWorktree = %q, want a text %v", got, c.wantText)
			}
			undoFailedAdd(leaf, cp)
			if got := exists(leaf); got != c.wantStays {
				t.Errorf("leaf exists after undoFailedAdd = %v, want %v", got, c.wantStays)
			}
		})
	}
}

// linkInPlaceOfJunction makes link a symbolic link to target, in the place of a junction.
func linkInPlaceOfJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// mustLookGit is the git of PATH, before a stub goes first.
func mustLookGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
