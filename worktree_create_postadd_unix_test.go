//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests pin the tests of git.worktree_create between `git worktree add` and
// the checkout. The rows are those of 89cb6289 on a Linux VM with git 2.43 and a
// macOS VM with git 2.50. With a registrations directory that is a symlink, git
// 2.43 writes the path through the link into the .git file of the new worktree,
// and git 2.50 writes the resolved path. The git stub writes that file after the
// real add, so both shapes are staged on any git of the host.

// postAddFixture starts the git stub and returns the fixture and the server. D5 is
// off.
func postAddFixture(t *testing.T) (wtFixture, *server) {
	t.Helper()
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	return newWTFixture(t, false), newTestServer(t)
}

// afterAdd makes the stub write content to file after the real add, and then
// sleep d.
func afterAdd(t *testing.T, file, content string, d time.Duration) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "setfile")
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF", file)
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", content)
	slowGit(t, "worktree,add", "post", d, "", "")
}

// linkRegistrations makes <git dir>/worktrees of the fixture a symlink to target,
// a new empty folder.
func (f wtFixture) linkRegistrations(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, f.regDir); err != nil {
		t.Fatal(err)
	}
}

func notOursText(leaf string) string {
	return "refusing to create worktree: " + leaf +
		" carries a .git file that does not name this repository's own worktree admin directory"
}

// wantNothingRolledBack checks the disk after a refusal: the leaf holds its .git
// file only, with the bytes gitFile, the registration reg stays with no index, and
// branch w1 stays.
func (f wtFixture) wantNothingRolledBack(t *testing.T, gitFile, reg string) {
	t.Helper()
	if got := f.leafEntries(t); !slices.Equal(got, []string{".git"}) {
		t.Errorf("leaf entries = %q, want only .git", got)
	}
	if b, _ := os.ReadFile(filepath.Join(f.leaf(), ".git")); string(b) != gitFile {
		t.Errorf("leaf .git = %q, want %q", b, gitFile)
	}
	if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
		t.Errorf("the registration %s is gone: %v", reg, err)
	}
	if _, err := os.Lstat(filepath.Join(reg, "index")); !os.IsNotExist(err) {
		t.Errorf("the registration holds an index (Lstat err %v), want none", err)
	}
	if !f.hasRef(t, "w1") {
		t.Error("refs/heads/w1 is gone, want it kept")
	}
}

// wantCreated checks the disk after a success: the leaf holds the checkout, its
// .git file has the bytes gitFile, and the registration reg holds the index.
func (f wtFixture) wantCreated(t *testing.T, raw, gitFile, reg string) {
	t.Helper()
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, f.leaf()) + `,"sourceBranch":"main","branch":"w1"}}`
	if raw != want {
		t.Fatalf("reply = %s\nwant %s", raw, want)
	}
	if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
		t.Errorf("the leaf holds no checkout: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.leaf(), ".git")); string(b) != gitFile {
		t.Errorf("leaf .git = %q, want %q", b, gitFile)
	}
	if fi, err := os.Lstat(filepath.Join(reg, "index")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the registration %s holds no index file: %v", reg, err)
	}
}

// TestWorktreeCreateRegistrationName pins the name test of the .git file of the new
// worktree. <git dir>/worktrees is a symlink, and the stub writes the .git file in
// the shape of git 2.50 (the resolved path) or of git 2.43 (the path through the
// link).
//
//   - Rows D-1, D-4 and D-5 (macOS VM) and D-11 (Linux VM): the resolved path lies
//     in a folder named WTREG or WORKTREES. The create is refused, and nothing is
//     rolled back.
//   - Row D-3 (macOS VM): the resolved path lies in a folder named worktrees. The
//     create succeeds. D-3 against D-4 straddles the name.
//   - Rows D-1, D-3, D-4 and D-5 (Linux VM): the path through the link. The create
//     succeeds, and the index is in the link target.
func TestWorktreeCreateRegistrationName(t *testing.T) {
	for _, tc := range []struct {
		name, target, link string
		resolved, refused  bool
	}{
		{"D-1 resolved", "WTREG", "", true, true},
		{"D-1 through the link", "WTREG", "", false, false},
		{"D-3 resolved", "alt/worktrees", "", true, false},
		{"D-3 through the link", "alt/worktrees", "", false, false},
		{"D-4 resolved", "alt/WORKTREES", "", true, true},
		{"D-4 through the link", "alt/WORKTREES", "", false, false},
		{"D-5 resolved", "WTREG", "../../WTREG", true, true},
		{"D-5 through the link", "WTREG", "../../WTREG", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			target := filepath.Join(filepath.Dir(f.top), filepath.FromSlash(tc.target))
			link := tc.link
			if link == "" {
				link = target
			}
			f.linkRegistrations(t, target, link)
			value := filepath.Join(f.regDir, "w1")
			if tc.resolved {
				value = filepath.Join(target, "w1")
			}
			gitFile := "gitdir: " + value + "\n"
			afterAdd(t, filepath.Join(f.leaf(), ".git"), gitFile, 0)

			raw, _ := f.create(t, s, "w1", "", 0)
			reg := filepath.Join(target, "w1")
			if !tc.refused {
				f.wantCreated(t, raw, gitFile, reg)
				return
			}
			wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
			f.wantNothingRolledBack(t, gitFile, reg)
		})
	}
}

// TestWorktreeCreateRegistrationCommonDir pins rows D-12 and D-13 (Linux and macOS
// VMs), which straddle the second part of the test.
//
//   - Row D-13: the commondir file of the registration holds "../../x". The create
//     is refused, and nothing is rolled back.
//   - Row D-12: the .git file of the new worktree names /elsewhere/worktrees/w1,
//     which does not exist. The create succeeds, the .git file stays as it is, and
//     the registration that git made holds the index.
func TestWorktreeCreateRegistrationCommonDir(t *testing.T) {
	t.Run("D-13", func(t *testing.T) {
		f, s := postAddFixture(t)
		reg := filepath.Join(f.regDir, "w1")
		afterAdd(t, filepath.Join(reg, "commondir"), "../../x\n", 0)
		raw, _ := f.create(t, s, "w1", "", 0)
		wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
		b, err := os.ReadFile(filepath.Join(f.leaf(), ".git"))
		if err != nil {
			t.Fatal(err)
		}
		f.wantNothingRolledBack(t, string(b), reg)
	})
	t.Run("D-12", func(t *testing.T) {
		f, s := postAddFixture(t)
		const gitFile = "gitdir: /elsewhere/worktrees/w1\n"
		afterAdd(t, filepath.Join(f.leaf(), ".git"), gitFile, 0)
		raw, _ := f.create(t, s, "w1", "", 0)
		f.wantCreated(t, raw, gitFile, filepath.Join(f.regDir, "w1"))
		if !f.hasRef(t, "w1") {
			t.Error("refs/heads/w1 is gone, want it kept")
		}
	})
}

// TestWorktreeCreateRegistrationTestOrder pins the place of the test, in the state
// of row D-1 with the resolved path (macOS VM).
//
//   - Row D-9: timeoutMs expired during the add. The answer is the refusal, not the
//     timeout frame, and nothing is rolled back.
//   - Row D-2: a read-tree that fails. The answer is the refusal, and no read-tree
//     runs.
func TestWorktreeCreateRegistrationTestOrder(t *testing.T) {
	stage := func(t *testing.T, d time.Duration) (wtFixture, *server, string, string) {
		f, s := postAddFixture(t)
		target := filepath.Join(filepath.Dir(f.top), "WTREG")
		f.linkRegistrations(t, target, target)
		gitFile := "gitdir: " + filepath.Join(target, "w1") + "\n"
		afterAdd(t, filepath.Join(f.leaf(), ".git"), gitFile, d)
		return f, s, gitFile, filepath.Join(target, "w1")
	}
	t.Run("D-9", func(t *testing.T) {
		f, s, gitFile, reg := stage(t, 300*time.Millisecond)
		raw, _ := f.create(t, s, "w1", "", 1)
		wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
		f.wantNothingRolledBack(t, gitFile, reg)
	})
	t.Run("D-2", func(t *testing.T) {
		f, s, gitFile, reg := stage(t, 0)
		log := filepath.Join(t.TempDir(), "calls.log")
		t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
		stubRule2(t, "read-tree", "", 0, "1", "", "")
		raw, _ := f.create(t, s, "w1", "", 0)
		wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
		f.wantNothingRolledBack(t, gitFile, reg)
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "worktree\x1fadd") {
			t.Fatalf("the call log holds no add: the stub did not run\n%q", b)
		}
		if strings.Contains(string(b), "read-tree") {
			t.Errorf("a read-tree ran after the refusal\n%q", b)
		}
	})
}

// TestWorktreeCreateRegistrationInOtherRepository pins rows B-E1 and B-E3 (Linux
// and macOS VMs, 10 of 10 runs each). The daemon has GIT_COMMON_DIR of another
// repository X in its environment, and T and X hold a commit with the same id. git
// then makes the registration in X, and T has none. The create is refused with the
// "was not populated" text, and nothing is rolled back. In B-E3, X also has a
// branch main.
func TestWorktreeCreateRegistrationInOtherRepository(t *testing.T) {
	for _, tc := range []struct {
		name    string
		xBranch string
	}{
		{"B-E1", "xonly"},
		{"B-E3", "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireGit(t)
			oldTimeout := gitTimeout
			t.Cleanup(func() { gitTimeout = oldTimeout })
			gitTimeout = 0
			// Equal dates give equal commit ids in T and X.
			t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z")
			t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
			root := t.TempDir()
			T, X := filepath.Join(root, "T"), filepath.Join(root, "X")
			for _, r := range []string{T, X} {
				if err := os.MkdirAll(r, 0o755); err != nil {
					t.Fatal(err)
				}
				runGit(t, r, "init", "-q", "-b", "main")
				writeFile(t, filepath.Join(r, "t.txt"), "t\n", 0o644)
				runGit(t, r, "add", ".")
				runGit(t, r, "commit", "-q", "-m", "c0")
			}
			if tc.xBranch != "main" {
				runGit(t, X, "branch", "-m", "main", tc.xBranch)
			}
			idOf := func(repo, ref string) string {
				cmd := exec.Command("git", "-C", repo, "rev-parse", ref)
				cmd.Env = append(os.Environ(), gitNoAutoMaintenance...)
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(out))
			}
			if a, b := idOf(T, "refs/heads/main"), idOf(X, "refs/heads/"+tc.xBranch); a != b {
				t.Fatalf("commit ids differ (%s, %s): this fixture does not stage the row", a, b)
			}
			s := newTestServer(t)
			t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			leaf := filepath.Join(T, ".claude", "worktrees", "w1")
			raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": T, "branchName": "w1", "worktreePath": leaf}))
			wantError(t, raw, "refusing to create worktree: "+leaf+" was not populated by git worktree add", "unsafe_path")
			ents, err := os.ReadDir(leaf)
			if err != nil || len(ents) != 1 || ents[0].Name() != ".git" {
				t.Errorf("leaf entries = %v (err %v), want only .git", ents, err)
			}
			reg := filepath.Join(X, ".git", "worktrees", "w1")
			if b, _ := os.ReadFile(filepath.Join(leaf, ".git")); !strings.HasSuffix(strings.TrimSpace(string(b)), filepath.Join("X", ".git", "worktrees", "w1")) {
				t.Errorf("leaf .git = %q, want the registration in X", b)
			}
			if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
				t.Errorf("the registration in X is gone: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(reg, "index")); !os.IsNotExist(err) {
				t.Errorf("the registration in X holds an index (Lstat err %v), want none", err)
			}
			if _, err := os.Lstat(filepath.Join(T, ".git", "worktrees")); !os.IsNotExist(err) {
				t.Errorf("T has a registrations directory (Lstat err %v), want none", err)
			}
		})
	}
}

// TestWorktreeCreateOldRegistrationOfSameName pins cell P-c (89cb6289, Linux VM
// with git 2.43 and macOS VM with git 2.50). The state is that of row B-E1, and T holds an old registration w1
// of another worktree, with its own index. git makes the new registration in X. The
// create is refused with the "different worktree" text. Nothing is rolled back, no
// checkout runs, and the index of the old registration keeps its bytes.
func TestWorktreeCreateOldRegistrationOfSameName(t *testing.T) {
	requireGit(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	// Equal dates give equal commit ids in T and X.
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	root := t.TempDir()
	T, X := filepath.Join(root, "T"), filepath.Join(root, "X")
	for _, r := range []string{T, X} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, r, "init", "-q", "-b", "main")
		writeFile(t, filepath.Join(r, "t.txt"), "t\n", 0o644)
		runGit(t, r, "add", ".")
		runGit(t, r, "commit", "-q", "-m", "c0")
	}
	runGit(t, X, "branch", "-m", "main", "xonly")
	// The old registration: a worktree on branch old1 whose folder is gone.
	gone := filepath.Join(root, "gone")
	runGit(t, T, "worktree", "add", "-q", "-b", "old1", filepath.Join(gone, "w1"))
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	oldReg := filepath.Join(T, ".git", "worktrees", "w1")
	oldIndex, err := os.ReadFile(filepath.Join(oldReg, "index"))
	if err != nil || len(oldIndex) == 0 {
		t.Fatalf("the old registration holds no index (err %v): this fixture does not stage the cell", err)
	}
	oldInfo, err := os.Stat(filepath.Join(oldReg, "index"))
	if err != nil {
		t.Fatal(err)
	}
	oldRecord, err := os.ReadFile(filepath.Join(oldReg, "gitdir"))
	if err != nil || !strings.HasSuffix(string(oldRecord), filepath.Join("gone", "w1", ".git")+"\n") {
		t.Fatalf("the old record = %q (err %v), want the worktree that is gone", oldRecord, err)
	}

	s := newTestServer(t)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
	leaf := filepath.Join(T, ".claude", "worktrees", "w1")
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": T, "branchName": "w1", "worktreePath": leaf}))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, "refusing to create worktree: "+leaf+
		" carries a .git file naming an admin directory whose own record is of a different worktree") + `,"errorCode":"unsafe_path"}}`
	if raw != want {
		t.Errorf("reply = %s\nwant %s", raw, want)
	}
	if b, err := os.ReadFile(filepath.Join(oldReg, "index")); err != nil || string(b) != string(oldIndex) {
		t.Errorf("the index of the old registration changed (err %v)", err)
	}
	if fi, err := os.Stat(filepath.Join(oldReg, "index")); err != nil || !os.SameFile(oldInfo, fi) {
		t.Errorf("the index of the old registration is another file now (err %v)", err)
	}
	if b, _ := os.ReadFile(filepath.Join(oldReg, "gitdir")); string(b) != string(oldRecord) {
		t.Errorf("the old record = %q, want %q", b, oldRecord)
	}
	ents, err := os.ReadDir(leaf)
	if err != nil || len(ents) != 1 || ents[0].Name() != ".git" {
		t.Errorf("leaf entries = %v (err %v), want only .git", ents, err)
	}
	newReg := filepath.Join(X, ".git", "worktrees", "w1")
	if b, _ := os.ReadFile(filepath.Join(leaf, ".git")); !strings.HasSuffix(strings.TrimSpace(string(b)), filepath.Join("X", ".git", "worktrees", "w1")) {
		t.Errorf("leaf .git = %q, want the registration in X", b)
	}
	if _, err := os.Stat(filepath.Join(newReg, "HEAD")); err != nil {
		t.Errorf("the registration in X is gone: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(newReg, "index")); !os.IsNotExist(err) {
		t.Errorf("the registration in X holds an index (Lstat err %v), want none", err)
	}
	if _, err := os.Stat(filepath.Join(T, ".git", "refs", "heads", "w1")); err != nil {
		t.Errorf("refs/heads/w1 of T is gone: %v", err)
	}
}

// TestPlaceWorktreeIndexGuard pins the guard of the index placement. A
// registration whose gitdir record names another worktree keeps its index. A record
// that names the leaf gets the new index. A registration with no record gets it
// too: that is claustrum's choice (not measured).
func TestPlaceWorktreeIndexGuard(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	reg := filepath.Join(root, ".git", "worktrees", "w1")
	for _, d := range []string{leaf, reg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "new-index")
	writeFile(t, src, "new\n", 0o644)
	index := filepath.Join(reg, "index")
	indexIs := func(want string) {
		t.Helper()
		if b, err := os.ReadFile(index); err != nil || string(b) != want {
			t.Errorf("index = %q (err %v), want %q", b, err, want)
		}
	}

	writeFile(t, index, "old\n", 0o644)
	writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(root, "gone", "w1", ".git")+"\n", 0o644)
	if err := placeWorktreeIndex(src, reg, leaf); err == nil || !strings.Contains(err.Error(), "is of another worktree") {
		t.Errorf("record of another worktree: err = %v, want the guard", err)
	}
	indexIs("old\n")

	resolved, err := filepath.EvalSymlinks(leaf)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(resolved, ".git")+"\n", 0o644)
	if err := placeWorktreeIndex(src, reg, leaf); err != nil {
		t.Errorf("record of the leaf: err = %v, want none", err)
	}
	indexIs("new\n")

	writeFile(t, index, "old\n", 0o644)
	if err := os.Remove(filepath.Join(reg, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if err := placeWorktreeIndex(src, reg, leaf); err != nil {
		t.Errorf("no record: err = %v, want none", err)
	}
	indexIs("new\n")
}

// TestCreatedIndexDir pins the folder of the index. For a .git value in a folder
// named worktrees, it is the registration of the git directory with the last name
// of the value (row D-12). Any other value names the folder itself: only a .git
// file that changed after the test of the create reaches that arm.
func TestCreatedIndexDir(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	if got, want := createdIndexDir(gitDir, "/leaf", "/elsewhere/worktrees/w1"), filepath.Join(gitDir, "worktrees", "w1"); got != want {
		t.Errorf("createdIndexDir = %s, want %s", got, want)
	}
	if got := createdIndexDir(gitDir, "/leaf", "/elsewhere/WTREG/w1"); got != "/elsewhere/WTREG/w1" {
		t.Errorf("createdIndexDir = %s, want the named folder", got)
	}
}

// TestCreatedRegistrationRefusalNotMeasured pins the states that no row measures.
// claustrum does not refuse them: a leaf with no .git file, and a registration with
// no commondir file. A commondir file that holds the absolute git directory passes.
func TestCreatedRegistrationRefusalNotMeasured(t *testing.T) {
	root := t.TempDir()
	gitDir, leaf := filepath.Join(root, ".git"), filepath.Join(root, "leaf")
	reg := filepath.Join(gitDir, "worktrees", "w1")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := createdRegistrationRefusal(gitDir, leaf); got != "" {
		t.Errorf("no .git file: refusal %q, want none", got)
	}
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+reg+"\n", 0o644)
	if got := createdRegistrationRefusal(gitDir, leaf); got != "" {
		t.Errorf("no commondir file: refusal %q, want none", got)
	}
	writeFile(t, filepath.Join(reg, "commondir"), gitDir+"\n", 0o644)
	if got := createdRegistrationRefusal(gitDir, leaf); got != "" {
		t.Errorf("absolute commondir: refusal %q, want none", got)
	}
}

// TestGitDirRegistryDir pins the registrations directory of a git directory: its
// own worktrees folder, or that of the directory that its commondir file names.
func TestGitDirRegistryDir(t *testing.T) {
	root := t.TempDir()
	admin := filepath.Join(root, ".git", "worktrees", "lw")
	if got, want := gitDirRegistryDir(filepath.Join(root, ".git")), filepath.Join(root, ".git", "worktrees"); got != want {
		t.Errorf("plain git directory: %s, want %s", got, want)
	}
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n", 0o644)
	if got, want := gitDirRegistryDir(admin), filepath.Join(root, ".git", "worktrees"); got != want {
		t.Errorf("relative commondir: %s, want %s", got, want)
	}
	writeFile(t, filepath.Join(admin, "commondir"), filepath.Join(root, "other")+"\n", 0o644)
	if got, want := gitDirRegistryDir(admin), filepath.Join(root, "other", "worktrees"); got != want {
		t.Errorf("absolute commondir: %s, want %s", got, want)
	}
}
