//go:build windows

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// git.worktree_create refuses a junctioned .claude or .claude\worktrees at the parent
// step and creates nothing: no directory in the junction target, no entry and no
// branch. Measured against f6010b97 on Windows.
func TestWorktreeCreateRefusesJunctionParent(t *testing.T) {
	for _, c := range []struct{ name, junction string }{
		{"claude", ".claude"},
		{"worktrees", filepath.Join(".claude", "worktrees")},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireGit(t)
			base := resolveTestRoot(t, t.TempDir())
			repo := filepath.Join(base, "repo")
			copyFixtureTemplate(t, "worktree-remove", repo, func(t *testing.T, dir string) {
				buildGit(t, dir, "init", "-q", "-b", "main")
				buildGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
			})
			if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if c.name == "claude" {
				if err := os.Remove(filepath.Join(repo, ".claude")); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(base, "outside")
			makeJunction(t, filepath.Join(repo, c.junction), outside)
			wp := filepath.Join(repo, ".claude", "worktrees", "wt1")
			raw := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "wt1", "worktreePath": wp}))
			want := "failed to create parent directory: " + filepath.Join(repo, c.junction) + " is not a directory"
			if got := removeErrorField(t, raw); got != want || !strings.Contains(raw, `"errorCode":"mkdir_failed"`) {
				t.Errorf("reply = %s\nwant error %q with errorCode mkdir_failed", raw, want)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Errorf("the junction target holds %v (err=%v), want it empty", entries, err)
			}
			mustBeGone(t, filepath.Join(repo, ".git", "worktrees"))
			if gitExitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/wt1") {
				t.Error("branch wt1 was created")
			}
		})
	}
}

// shortPathOf returns the 8.3 spelling of the existing path p, or skips the test when
// the volume keeps no short names.
func shortPathOf(t *testing.T, p string) string {
	t.Helper()
	pUTF16, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(pUTF16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("GetShortPathName(%s): n=%d err=%v", p, n, err)
	}
	s := windows.UTF16ToString(buf[:n])
	if filepath.Base(s) == filepath.Base(p) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	return s
}

// A gone worktree whose parent directory is gone too, with a locked registration, is
// refused when baseRepo is sent in its 8.3 short form. The branch stays. Measured
// against f6010b97 on Windows.
func TestWorktreeRemoveGoneLockedShortBase(t *testing.T) {
	requireGit(t)
	repo := filepath.Join(resolveTestRoot(t, t.TempDir()), "longrepository1")
	copyFixtureTemplate(t, "worktree-remove", repo, func(t *testing.T, dir string) {
		buildGit(t, dir, "init", "-q", "-b", "main")
		buildGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	})
	holder := filepath.Join(repo, ".claude", "worktrees")
	runGit(t, repo, "worktree", "add", "-q", "-b", "wt1", filepath.Join(holder, "wt1"))
	runGit(t, repo, "worktree", "lock", filepath.Join(holder, "wt1"))
	if err := os.RemoveAll(holder); err != nil {
		t.Fatal(err)
	}
	short := shortPathOf(t, repo)
	wp := filepath.Join(short, ".claude", "worktrees", "wt1")
	raw := removeFrame(t, map[string]any{"baseRepo": short, "worktreePath": wp, "branchName": "wt1"})
	wantRemoveError(t, raw, "refusing to remove worktree: `"+wp+"` is gone but its registration is "+
		"locked (git worktree lock); unlock it to remove the registration and branch")
	mustExist(t, filepath.Join(repo, ".git", "worktrees", "wt1", "locked"))
	if !gitExitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/wt1") {
		t.Error("branch wt1 was deleted")
	}
}

// A registered leaf sent through its 8.3 short name is removed with its branch, but
// its entry stays, because the short name does not match the long path git recorded.
// Measured against f6010b97 on Windows.
func TestWorktreeRemoveShortNameKeepsEntry(t *testing.T) {
	f := newRmFixture(t)
	leaf := filepath.Join(filepath.Dir(f.wt), "longworktree1")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "longworktree1", leaf)
	leafUTF16, err := windows.UTF16PtrFromString(leaf)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(leafUTF16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("GetShortPathName(%s): n=%d err=%v", leaf, n, err)
	}
	shortLeaf := filepath.Base(windows.UTF16ToString(buf[:n]))
	if shortLeaf == "longworktree1" {
		t.Skip("8.3 short names are disabled on this volume")
	}
	wp := filepath.Join(filepath.Dir(leaf), shortLeaf)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": wp, "branchName": "longworktree1"})
	if raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, leaf)
	mustExist(t, filepath.Join(f.repo, ".git", "worktrees", "longworktree1"))
	if f.hasBranch("longworktree1") {
		t.Error("branch longworktree1 is still there")
	}
	// The same leaf by its long name drops the entry
	// (measured).
	if raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"}); raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	if _, err := os.Lstat(f.entry()); !os.IsNotExist(err) {
		t.Errorf("entry %s is still there (err=%v)", f.entry(), err)
	}
}

// Measured (`89cb6289`, on Windows): baseRepo and worktreePath go through a
// junction to the repository. The folder and its entry are both gone after the
// removal.
func TestWorktreeRemoveThroughJunctionDropsEntry(t *testing.T) {
	f := newRmFixture(t)
	jt := filepath.Join(filepath.Dir(f.repo), "JT")
	makeJunction(t, jt, f.repo)
	wp := filepath.Join(jt, ".claude", "worktrees", filepath.Base(f.wt))
	raw := removeFrame(t, map[string]any{"baseRepo": jt, "worktreePath": wp})
	if raw != removeOK {
		t.Fatalf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, f.wt)
	if _, err := os.Lstat(f.entry()); !os.IsNotExist(err) {
		t.Errorf("entry %s is still there (err=%v), want it gone", f.entry(), err)
	}
}

// A worktree leaf that is a junction to a live worktree is refused with the path in
// backticks. The junction, the worktree behind it, its entry and its branch stay
// (`4c111e86`, measured on Windows).
func TestWorktreeRemoveJunctionLeafIsRefused(t *testing.T) {
	f := newRmFixture(t)
	live := filepath.Join(filepath.Dir(f.repo), "jt5")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "w1", live)
	leaf := filepath.Join(filepath.Dir(f.wt), "w1")
	makeJunction(t, leaf, live)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1"})
	wantRemoveError(t, raw, "refusing to remove worktree: `"+leaf+"` is not a directory")
	mustExist(t, leaf)
	mustExist(t, filepath.Join(live, ".git"))
	mustExist(t, filepath.Join(f.repo, ".git", "worktrees", "jt5"))
	if !f.hasBranch("w1") {
		t.Error("branch w1 was deleted")
	}
}

// Measured (`b3081b51`, on Windows): `.git\worktrees` is moved to TARGET, and a
// junction or a directory symbolic link to TARGET takes its place. A remove of
// the live worktree deletes nothing and answers the lock-check refusal. The
// leaf, the entry behind the link and the branch stay. Every path of the
// fixture is under t.TempDir().
func TestWorktreeRemoveBehindALinkedRegistrationsFolderDeletesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		link func(t *testing.T, link, target string)
	}{
		{"junction", makeJunction},
		{"directory symbolic link", makeDirSymlink},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRmFixture(t)
			dir := filepath.Join(f.repo, ".git", "worktrees")
			target := filepath.Join(filepath.Dir(f.repo), "TARGET")
			if err := os.Rename(dir, target); err != nil {
				t.Fatal(err)
			}
			c.link(t, dir, target)
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonQuoted(t, lockCheckRefusal(f.wt)) + `}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
			mustExist(t, filepath.Join(f.wt, ".git"))
			mustExist(t, filepath.Join(target, "wt1", "gitdir"))
			if !f.hasBranch("wt1") {
				t.Error("branch wt1 is gone, want it kept")
			}
		})
	}
}

// Measured (`b3081b51`, on Windows): the worktree folder is deleted, then
// `.git\worktrees` is moved to TARGET, and a junction or a directory symbolic
// link to TARGET takes its place. A remove answers success and deletes the
// branch. The link and the entry behind it stay. Every path of the fixture is
// under t.TempDir().
func TestWorktreeRemoveGoneLeafKeepsTheEntryBehindALinkedRegistrationsFolder(t *testing.T) {
	for _, c := range []struct {
		name string
		link func(t *testing.T, link, target string)
	}{
		{"junction", makeJunction},
		{"directory symbolic link", makeDirSymlink},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRmFixture(t)
			if err := os.RemoveAll(f.wt); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(f.repo, ".git", "worktrees")
			target := filepath.Join(filepath.Dir(f.repo), "TARGET")
			if err := os.Rename(dir, target); err != nil {
				t.Fatal(err)
			}
			c.link(t, dir, target)
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
			if raw != removeOK {
				t.Errorf("reply = %s, want %s", raw, removeOK)
			}
			if fi, err := os.Lstat(dir); err != nil || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
				t.Errorf("the link %s is not there (err=%v)", dir, err)
			}
			mustExist(t, filepath.Join(target, "wt1", "gitdir"))
			mustExist(t, filepath.Join(target, "wt1", "HEAD"))
			if f.hasBranch("wt1") {
				t.Error("branch wt1 is still there, want it deleted")
			}
		})
	}
}

// A baseRepo through a junction K, with a second junction at .claude\worktrees. The
// record names the real repository path, not the leaf as sent, so it keeps its entry,
// and only the branch goes (4c111e86 and 87375cc7 have this kind of
// record). Mutation: in removeGoneWorktree, give dropWorktreeEntryByPath sp in place of
// dropSet (the real-base spelling of sp matches the record). Only Windows runs it.
func TestWorktreeRemoveBehindJunctionsKeepsRealRecord(t *testing.T) {
	f := newWTFixture(t, false)
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
	out := filepath.Dir(f.top)
	wts := filepath.Dir(f.leaf())
	moved := filepath.Join(out, "E")
	if err := os.Rename(wts, moved); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, wts, moved)
	k := filepath.Join(out, "K")
	makeJunction(t, k, f.top)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": k, "worktreePath": filepath.Join(k, ".claude", "worktrees", "w1"), "branchName": "w1"}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(moved, "w1", ".git")) || !exists(filepath.Join(f.regDir, "w1")) {
		t.Error("the folder or the registration of the real-path record went")
	}
	if f.hasRef(t, "w1") {
		t.Error("the branch stayed")
	}
}

// A create whose leaf exists behind a junction at .claude\worktrees gets the parent
// failure, before the test of a leaf that exists (87375cc7, measured on
// Windows). Mutation: move the junctionParentRefusal test after the os.Lstat of the leaf.
// Only Windows runs it.
func TestWorktreeCreateJunctionComesBeforeTheExistingLeaf(t *testing.T) {
	f := newWTFixture(t, false)
	wts := filepath.Dir(f.leaf())
	if err := os.RemoveAll(wts); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(f.top), "E")
	if err := os.MkdirAll(filepath.Join(moved, "w1"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, wts, moved)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": f.top, "worktreePath": f.leaf(), "branchName": "w1"}))
	want := "failed to create parent directory: " + wts + " is not a directory"
	if !strings.Contains(got, jsonString(t, want)) || !strings.Contains(got, `"errorCode":"mkdir_failed"`) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
