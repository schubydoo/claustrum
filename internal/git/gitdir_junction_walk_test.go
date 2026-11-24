package git

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// A daemon GIT_DIR through a junction is judged and pinned at its real path, as
// 4c111e86 runs it (measured on Windows). Mutation: in gitDirTrustFor, drop the
// realParentPath line.
func TestDaemonGitDirThroughJunction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symbolic link stands for the junction off Windows")
	}
	f := newJunctionRepos(t, junctionsLikeWindows(t))
	t.Setenv("GIT_DIR", filepath.Join(f.KP, ".git"))
	// Off Windows the judge resolves a link by itself, so the test also asks that the
	// step took the handle path of the folder above the git directory.
	asked := false
	oldFinal := finalLeafPath
	t.Cleanup(func() { finalLeafPath = oldFinal })
	finalLeafPath = func(p string) (string, error) {
		asked = asked || p == f.KP
		return oldFinal(p)
	}
	if got, want := commonDirPinEnv(f.base), []string{"GIT_COMMON_DIR=" + dotGitOf(f.P)}; !slices.Equal(got, want) {
		t.Errorf("pin = %q, want %q", got, want)
	}
	if !asked {
		t.Error("the daemon GIT_DIR was not taken to its real path")
	}
}

// realParentPath resolves the folder above the last name and keeps that name, so a
// link at the name stays a link for the tests that refuse it. Mutation: resolve p
// itself in place of filepath.Dir(p).
func TestRealParentPathKeepsTheLastName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symbolic link stands for the junction off Windows")
	}
	f := newJunctionRepos(t, junctionsLikeWindows(t))
	if got, want := realParentPath(filepath.Join(f.KP, "J")), f.J; got != want {
		t.Errorf("through a junction = %q, want %q", got, want)
	}
	if got, want := realParentPath(filepath.Join(f.P, "t.txt")), filepath.Join(f.P, "t.txt"); got != want {
		t.Errorf("no junction = %q, want %q", got, want)
	}
}

// With no junction in the path, and off Windows, the walk takes no handle path.
// Mutation: drop the walkStartsAsSpelled test of junctionFreePath.
func TestJunctionFreePathOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows takes the path of a handle")
	}
	dir := realTempDir(t)
	link := filepath.Join(dir, "l")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	old := pathHoldsJunction
	t.Cleanup(func() { pathHoldsJunction = old })
	pathHoldsJunction = func(string) bool { return true }
	if got, ok := junctionFreePath(link); ok {
		t.Errorf("junctionFreePath = %q, want none", got)
	}
}

// junctionOpenRefused makes the open of the leaf's folder fail as os.Root fails it at
// a Windows junction.
func junctionOpenRefused(t *testing.T) {
	t.Helper()
	oldOpen, oldBelow := removeParentOpener, junctionBelowRepo
	t.Cleanup(func() { removeParentOpener, junctionBelowRepo = oldOpen, oldBelow })
	removeParentOpener = func(string, string, string) (removeTarget, error) {
		return removeTarget{}, errors.New(`openat .claude\worktrees: path escapes from parent`)
	}
	junctionBelowRepo = func(string, string) bool { return true }
}

// A remove with a junction between baseRepo and the leaf, whose record names the leaf
// as the caller spelled it: success, the branch and the registration go, and the
// folder behind the junction stays with its `.git` file (4c111e86, measured on
// Windows). Mutation: in removeGoneWorktree, skip the delete when behindJunction is
// set (the registration stays). In locateInRepoWorktree, drop the arm of
// behindJunction (the reply is the refusal).
func TestRemoveBehindASecondJunction(t *testing.T) {
	f := newWTFixture(t, false)
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	junctionOpenRefused(t)
	// The record must name the leaf as the request spells it. On Windows git records the
	// long name of a temporary folder that the test spells in 8.3 form, so the request
	// takes the long name there too.
	top, leaf := f.top, f.leaf()
	if runtime.GOOS == "windows" {
		top = pathcanon.FinalDir(f.top)
		leaf = filepath.Join(top, ".claude", "worktrees", "w1")
	}
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": top, "worktreePath": leaf, "branchName": "w1"}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.leaf(), ".git")) {
		t.Error("the folder behind the junction lost its .git file")
	}
	if exists(filepath.Join(f.regDir, "w1")) {
		record, _ := os.ReadFile(filepath.Join(f.regDir, "w1", "gitdir"))
		t.Errorf("the registration of the spelled leaf stayed: record %q, leaf %q", record, leaf)
	}
	if f.hasRef(t, "w1") {
		t.Error("the branch stayed")
	}
}

// The same remove with a record that names the real folder behind the junction, as
// git writes it for a worktree added there. The registration and the folder stay, and
// only the branch goes (4c111e86 and 87375cc7, measured on Windows).
// Mutation: in dropMatchingEntry, drop the test of sp.has.
func TestRemoveBehindASecondJunctionKeepsARealPathRecord(t *testing.T) {
	f := newWTFixture(t, false)
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
	writeFile(t, filepath.Join(f.regDir, "w1", "gitdir"), filepath.Join(filepath.Dir(f.top), "E", "w1", ".git")+"\n", 0o644)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	junctionOpenRefused(t)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": f.top, "worktreePath": f.leaf(), "branchName": "w1"}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.leaf(), ".git")) || !exists(filepath.Join(f.regDir, "w1")) {
		t.Error("the folder or the registration of the real-path record went")
	}
	if f.hasRef(t, "w1") {
		t.Error("the branch stayed")
	}
}

// Another error of the open keeps the refusal, and a name that is no junction gets no
// success. Mutation: in locateInRepoWorktree, drop the test of the error text.
func TestRemoveKeepsAnotherOpenError(t *testing.T) {
	f := newWTFixture(t, false)
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	oldOpen, oldBelow := removeParentOpener, junctionBelowRepo
	t.Cleanup(func() { removeParentOpener, junctionBelowRepo = oldOpen, oldBelow })
	removeParentOpener = func(string, string, string) (removeTarget, error) {
		return removeTarget{}, errors.New("openat .claude: access denied")
	}
	junctionBelowRepo = func(string, string) bool { return true }
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": f.top, "worktreePath": f.leaf(), "branchName": "w1"}))
	if !strings.Contains(got, `"success":false`) || !f.hasRef(t, "w1") {
		t.Errorf("got %s, branch kept %v, want the refusal and the branch", got, f.hasRef(t, "w1"))
	}
}

// A path whose first hop is a directory symbolic link starts the walk at its real
// path, as 4c111e86 and 87375cc7 run git there (measured on Windows).
// Mutation: in walkStart, call junctionFreePath in place of linkFreePath.
func TestWalkStartTakesTheRealPathOfASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the seams stand for a Windows handle")
	}
	real := realTempDir(t)
	spelled := filepath.Join(real, "spelled")
	if err := os.Mkdir(spelled, 0o755); err != nil {
		t.Fatal(err)
	}
	oldSpelled, oldLink, oldJunction, oldFinal := walkStartsAsSpelled, pathHoldsLink, pathHoldsJunction, finalLeafPath
	t.Cleanup(func() {
		walkStartsAsSpelled, pathHoldsLink, pathHoldsJunction, finalLeafPath = oldSpelled, oldLink, oldJunction, oldFinal
	})
	walkStartsAsSpelled = true
	pathHoldsLink = func(string) bool { return true }
	pathHoldsJunction = func(string) bool { return false }
	finalLeafPath = func(string) (string, error) { return real, nil }
	if got, err := walkStart(spelled); err != nil || got != real {
		t.Errorf("walkStart = %q, %v, want %q", got, err, real)
	}
}

// A `.git` of baseRepo that names the folder of commonDir by another spelling has no
// lock test of its own, so a file at its `worktrees` refuses nothing (4c111e86 and
// 87375cc7, measured on Windows). A `.git` of another folder keeps the
// test. Mutation: in lockedInBaseRepo, drop the sameLockFolder arm.
func TestBaseRepoLockOfOneFolder(t *testing.T) {
	base := realTempDir(t)
	repo, common := filepath.Join(base, "R"), filepath.Join(base, "C")
	for _, d := range []string{filepath.Join(repo, ".git"), common} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(repo, ".git", "worktrees"), "x", 0o644)
	oldSpelled, oldSame := walkStartsAsSpelled, sameLockFolder
	t.Cleanup(func() { walkStartsAsSpelled, sameLockFolder = oldSpelled, oldSame })
	walkStartsAsSpelled = true
	sameLockFolder = func(string, string) bool { return false }
	if _, readable := lockedInBaseRepo(repo, common, "", "", newWorktreePathSet("x")); readable {
		t.Error("another folder: the file at worktrees reads as no lock")
	}
	sameLockFolder = func(string, string) bool { return true }
	if lock, readable := lockedInBaseRepo(repo, common, "", "", newWorktreePathSet("x")); lock != notLocked || !readable {
		t.Errorf("one folder: lock %v, readable %v", lock, readable)
	}
}

// linkWalkSeams makes linkFreePath answer final for each path on any system.
func linkWalkSeams(t *testing.T, final string) {
	t.Helper()
	oldSpelled, oldLink, oldFinal := walkStartsAsSpelled, pathHoldsLink, finalLeafPath
	t.Cleanup(func() { walkStartsAsSpelled, pathHoldsLink, finalLeafPath = oldSpelled, oldLink, oldFinal })
	walkStartsAsSpelled = true
	pathHoldsLink = func(string) bool { return true }
	finalLeafPath = func(string) (string, error) { return final, nil }
}

// A remove through a link opens the folder at its real path, so the record of git
// matches. Mutation: in locateInRepoWorktree, drop the
// linkFreePath arm.
func TestLocateOpensTheRealPathOfALink(t *testing.T) {
	root := realTempDir(t)
	spelled, final := filepath.Join(root, "K"), filepath.Join(root, "real")
	for _, d := range []string{spelled, final} {
		if err := os.MkdirAll(filepath.Join(d, ".claude", "worktrees", "w1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkWalkSeams(t, final)
	target, err := locateInRepoWorktree(spelled, filepath.Join(spelled, ".claude", "worktrees", "w1"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	if want := filepath.Join(final, ".claude", "worktrees", "w1"); target.path != want {
		t.Errorf("target = %q, want %q", target.path, want)
	}
}

// The --work-tree of git.status after a link is the real path.
// Mutation: in statusPathOf, drop the linkFreePath arm.
func TestStatusProbeTakesTheRealPathOfALink(t *testing.T) {
	root := realTempDir(t)
	spelled, final, base := filepath.Join(root, "P"), filepath.Join(root, "real"), filepath.Join(root, "base")
	for _, d := range []string{spelled, final, base} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkWalkSeams(t, final)
	sp, ok := statusPathOf(spelled, base)
	if !ok || sp.probeTree != final {
		t.Errorf("probe tree = %q, %v, want %q", sp.probeTree, ok, final)
	}
}

// A status of a linked worktree whose `.git` file names its entry through a junction
// goes on in the target of the junction. An entry with no junction goes
// on in its pin, and another answer stays. Mutation: in statusCommonOfEntry, return
// t.pinCommonDir in place of t.holdFolderFor.
func TestStatusCommonOfAnEntryThroughAJunction(t *testing.T) {
	root := realTempDir(t)
	entry, other := filepath.Join(root, "entry"), filepath.Join(root, "other")
	for _, d := range []string{entry, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	through := gitDirTrust{pinCommonDir: "J", worktreeEntry: entry, configDir: "R"}
	if got := statusCommonOfEntry(through, entry); got != "R" {
		t.Errorf("through a junction = %q, want R", got)
	}
	if got := statusCommonOfEntry(gitDirTrust{pinCommonDir: "P", worktreeEntry: entry}, entry); got != "P" {
		t.Errorf("no junction = %q, want P", got)
	}
	if got := statusCommonOfEntry(through, other); got != other {
		t.Errorf("another answer = %q, want %q", got, other)
	}
}

// A daemon GIT_COMMON_DIR chooses the registrations of a baseRepo that did not move,
// so a linked worktree through a junction is no false move. A folder that moved takes
// the pin. Mutation: in pinnedCommonDir, drop the daemonCommonDirSet test.
func TestPinnedCommonDirWithADaemonCommonDir(t *testing.T) {
	root := realTempDir(t)
	base, other := filepath.Join(root, "base"), filepath.Join(root, "other")
	for _, d := range []string{base, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	req := &gitCall{removePin: "pin", removePinned: true, removeBase: baseFolderIdentity(base)}
	t.Setenv("GIT_COMMON_DIR", filepath.Join(root, "daemon"))
	if dir, moved := req.pinnedCommonDir(base, "answered"); dir != "answered" || moved {
		t.Errorf("no move = %q, %v", dir, moved)
	}
	if dir, moved := req.pinnedCommonDir(other, "answered"); dir != "pin" || !moved {
		t.Errorf("a move = %q, %v", dir, moved)
	}
}

// A link at baseRepo whose real path holds home gets no text refusal at the locate, so
// the home refusal of the leaf names the path as sent. No row names another spelling.
// Mutation: in locateInRepoWorktree, set byHandle after linkFreePath.
func TestLocateKeepsTheSpelledPathOfTheHomeRefusal(t *testing.T) {
	root := realTempDir(t)
	spelled, final := filepath.Join(root, "L"), filepath.Join(root, "real")
	for _, d := range []string{spelled, final} {
		if err := os.MkdirAll(filepath.Join(d, ".claude", "worktrees", "w1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkWalkSeams(t, final)
	t.Setenv(homeEnvVar(), filepath.Join(final, ".claude", "worktrees", "w1"))
	target, err := locateInRepoWorktree(spelled, filepath.Join(spelled, ".claude", "worktrees", "w1"))
	if err != nil {
		t.Fatalf("locate = %v, want no refusal that names the real path", err)
	}
	target.close()
}
