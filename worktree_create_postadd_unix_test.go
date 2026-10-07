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

// postAddFixture makes the fixture, then starts the git stub, and returns the
// fixture and the server. The fixture comes first, so its realGit is the git of the
// host and not the stub. D5 is off.
func postAddFixture(t *testing.T) (wtFixture, *server) {
	t.Helper()
	requireGit(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	return f, newTestServer(t)
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

// linkRegistrations makes target, a new empty folder, and makes <git dir>/worktrees
// of the fixture a symlink whose value is link. The caller gives a link that leads
// to target: target itself, or a relative path such as ../../WTREG (row D-5).
func (f wtFixture) linkRegistrations(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, f.regDir); err != nil {
		t.Fatal(err)
	}
}

// namedLeaf is the leaf as five refusals of git.worktree_create name it: with its
// symlinks resolved, or as sent when it does not resolve. Call it after the
// request, when the leaf exists.
func namedLeaf(leaf string) string {
	if resolved, err := filepath.EvalSymlinks(leaf); err == nil {
		return resolved
	}
	return leaf
}

func notOursText(leaf string) string {
	return "refusing to create worktree: " + namedLeaf(leaf) +
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
//
// The third case is not a measured cell. It is the state of row B-E1, and T holds a
// second worktree, so T has a worktrees folder with an entry of another name and no
// entry w1. claustrum answers the "does not name" text there. That is claustrum's
// rule from cell P-p, where 89cb6289 answers that text with a worktrees folder that
// holds no entry of the name.
func TestWorktreeCreateRegistrationInOtherRepository(t *testing.T) {
	for _, tc := range []struct {
		name       string
		xBranch    string
		otherEntry bool
	}{
		{"B-E1", "xonly", false},
		{"B-E3", "main", false},
		{"an entry of another name in T (claustrum's rule from cell P-p, not measured)", "xonly", true},
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
			leaf := filepath.Join(T, ".claude", "worktrees", "w1")
			if tc.otherEntry {
				runGit(t, T, "worktree", "add", "-q", "-b", "other", filepath.Join(root, "other"))
			}
			s := newTestServer(t)
			t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": T, "branchName": "w1", "worktreePath": leaf}))
			wantText := "refusing to create worktree: " + namedLeaf(leaf) + " was not populated by git worktree add"
			if tc.otherEntry {
				wantText = notOursText(leaf)
			}
			wantError(t, raw, wantText, "unsafe_path")
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
			tEnts, err := os.ReadDir(filepath.Join(T, ".git", "worktrees"))
			if !tc.otherEntry {
				if !os.IsNotExist(err) {
					t.Errorf("T has a registrations directory (ReadDir err %v), want none", err)
				}
				return
			}
			if err != nil || len(tEnts) != 1 || tEnts[0].Name() != "other" {
				t.Errorf("the registrations of T = %v (err %v), want only other", tEnts, err)
			}
			if _, err := os.Lstat(filepath.Join(T, ".git", "worktrees", "other", "index")); err != nil {
				t.Errorf("the registration other of T lost its index: %v", err)
			}
		})
	}
}

// TestWorktreeCreateGitDirNotAnswered pins a create whose `rev-parse
// --absolute-git-dir` gives no answer. baseRepo is a subfolder of the repository,
// so the git directory that claustrum then assumes, <baseRepo>/.git, does not
// exist. The registration tests need the answer of git and do not run. The
// read-tree runs with that git directory and fails, and the create answers the
// failed checkout and rolls back. That is claustrum's choice (not measured). The
// frame and the disk are those of claustrum before it had the registration tests.
// The text of git after "(checkout): " is not pinned.
func TestWorktreeCreateGitDirNotAnswered(t *testing.T) {
	f, s := postAddFixture(t)
	f.base = filepath.Join(f.top, "sub")
	if err := os.Mkdir(f.base, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	stubRule2(t, "rev-parse,--absolute-git-dir", "", 0, "1", "", "")

	raw, _ := f.create(t, s, "w1", "", 0)
	const head = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): `
	if !strings.HasPrefix(raw, head) || !strings.HasSuffix(raw, `","errorCode":"worktree_add_failed"}}`) {
		t.Errorf("reply = %s\nwant the failed checkout frame", raw)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"rev-parse\x1f--absolute-git-dir", "worktree\x1fadd", "read-tree"} {
		if !strings.Contains(string(calls), call) {
			t.Errorf("the call log holds no %q\n%q", call, calls)
		}
	}
	// The rollback removes the leaf and the branch. It finds no registration under
	// <baseRepo>/.git, so the registration that git made stays.
	f.assertRolledBack(t, []string{"w1"}, false)
}

// differentWorktreeText is the refusal of adminRecordRefusal.
func differentWorktreeText(leaf string) string {
	return "refusing to create worktree: " + namedLeaf(leaf) +
		" carries a .git file naming an admin directory whose own record is of a different worktree"
}

// TestWorktreeCreateOldRegistrationOfSameName pins cells P-c, P-f to P-o and P-q
// (89cb6289, Linux VM with git 2.43 and macOS VM with git 2.50). The state is that
// of row B-E1, and T holds an old registration w1 of another worktree, with its own
// index. git makes the new registration in X. Each cell changes one file of the old
// registration before the request. In every cell the create is refused, nothing is
// rolled back, no checkout runs, and the index of the old registration keeps its
// bytes, its inode and its mtime.
//
//   - P-c: no change. The "different worktree" text.
//   - P-f: no change, timeoutMs is 1, and the add takes longer. The P-c refusal, not
//     the timeout frame. So the record test comes before the deadline test.
//   - P-g, P-i and P-j: the gitdir record is a FIFO, a folder, or missing. The "does
//     not name" text. Against P-k and P-l they straddle "cannot be read" and "read,
//     and names something else".
//   - P-h: the commondir file is a FIFO. The "does not name" text. Against P-c it
//     shows that the commondir test comes before the record test.
//   - P-k: the record is the relative path of the worktree that is gone. P-l: the
//     record is an empty file. P-m: P-c with a `locked` file. The "different
//     worktree" text.
//   - P-n: the commondir file is missing. P-o: the gitdir record has mode 0000.
//     P-q: the folder of the old registration has mode 0000. The "does not name"
//     text. P-o and P-q need a user that is not root.
//
// No test opens a FIFO for writing, so a read that waits fails at fifoAnswerLimit.
func TestWorktreeCreateOldRegistrationOfSameName(t *testing.T) {
	replace := func(name string, with func(t *testing.T, p string)) func(*testing.T, string) {
		return func(t *testing.T, reg string) {
			t.Helper()
			p := filepath.Join(reg, name)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if with != nil {
				with(t, p)
			}
		}
	}
	write := func(name, content string) func(*testing.T, string) {
		return func(t *testing.T, reg string) {
			t.Helper()
			writeFile(t, filepath.Join(reg, name), content, 0o644)
		}
	}
	mkdir := func(t *testing.T, p string) {
		t.Helper()
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		change    func(t *testing.T, reg string)
		text      func(leaf string) string
		fifo      string
		noPerm    string // an entry of the old registration that has mode 0000 during the request
		timeoutMs int
		addTime   time.Duration
	}{
		{name: "P-c", text: differentWorktreeText},
		{name: "P-f", text: differentWorktreeText, timeoutMs: 1, addTime: 300 * time.Millisecond},
		{name: "P-g", change: replace("gitdir", mkfifo), text: notOursText, fifo: "gitdir"},
		{name: "P-h", change: replace("commondir", mkfifo), text: notOursText, fifo: "commondir"},
		{name: "P-i", change: replace("gitdir", mkdir), text: notOursText},
		{name: "P-j", change: replace("gitdir", nil), text: notOursText},
		{name: "P-k", change: write("gitdir", "../../../gone/w1/.git\n"), text: differentWorktreeText},
		{name: "P-l", change: write("gitdir", ""), text: differentWorktreeText},
		{name: "P-m", change: write("locked", "locked by cell P-m\n"), text: differentWorktreeText},
		{name: "P-n", change: replace("commondir", nil), text: notOursText},
		{name: "P-o", text: notOursText, noPerm: "gitdir"},
		{name: "P-q", text: notOursText, noPerm: "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noPerm != "" && os.Geteuid() == 0 {
				t.Skip("root reads a file of mode 0000")
			}
			T, X, oldReg := stageOldRegistrationOfSameName(t)
			index := filepath.Join(oldReg, "index")
			oldIndex, err := os.ReadFile(index)
			if err != nil || len(oldIndex) == 0 {
				t.Fatalf("the old registration holds no index (err %v): this fixture does not stage the cell", err)
			}
			oldInfo, err := os.Stat(index)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(filepath.Join(oldReg, "gitdir")); err != nil || !strings.HasSuffix(string(b), filepath.Join("gone", "w1", ".git")+"\n") {
				t.Fatalf("the old record = %q (err %v), want the worktree that is gone", b, err)
			}
			if tc.change != nil {
				tc.change(t, oldReg)
			}
			regBefore := regFiles(t, oldReg)
			restoreMode := func() {}
			if tc.noPerm != "" {
				p := filepath.Join(oldReg, tc.noPerm)
				fi, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				restoreMode = func() { _ = os.Chmod(p, fi.Mode().Perm()) }
				t.Cleanup(restoreMode)
			}

			// The stub goes on PATH only now, so it slows and logs no add of the fixture.
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			installGitSlowStub(t, realGit)
			log := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
			leaf := filepath.Join(T, ".claude", "worktrees", "w1")
			params := createParams(T, leaf, "w1")
			if tc.timeoutMs > 0 {
				slowGit(t, "worktree,add", "post", tc.addTime, "", "")
				params["timeoutMs"] = tc.timeoutMs
			}
			t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			raw := frameWithin(t, "git.worktree_create", params)
			restoreMode()
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, tc.text(leaf)) + `,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}

			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "worktree\x1fadd") {
				t.Fatalf("the call log holds no add: the stub did not run\n%q", calls)
			}
			if strings.Contains(string(calls), "read-tree") {
				t.Errorf("a read-tree ran after the refusal\n%q", calls)
			}
			if b, err := os.ReadFile(index); err != nil || string(b) != string(oldIndex) {
				t.Errorf("the index of the old registration changed (err %v)", err)
			}
			if fi, err := os.Stat(index); err != nil || !os.SameFile(oldInfo, fi) || !fi.ModTime().Equal(oldInfo.ModTime()) {
				t.Errorf("the index of the old registration is another file or has another mtime now (err %v)", err)
			}
			if got := regFiles(t, oldReg); !slices.Equal(got, regBefore) {
				t.Errorf("the old registration = %q\nwant %q", got, regBefore)
			}
			if tc.fifo != "" {
				wantFifo(t, filepath.Join(oldReg, tc.fifo))
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
		})
	}
}

// regFiles lists the top entries of the registration reg as "name kind content".
// The content is that of the small text files only. It opens no FIFO.
func regFiles(t *testing.T, reg string) []string {
	t.Helper()
	ents, err := os.ReadDir(reg)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		line := e.Name() + " " + e.Type().String()
		switch e.Name() {
		case "gitdir", "commondir", "HEAD", "locked":
			if e.Type().IsRegular() {
				b, _ := os.ReadFile(filepath.Join(reg, e.Name()))
				line += " " + string(b)
			}
		}
		out = append(out, line)
	}
	return out
}

// stageOldRegistrationOfSameName builds the disk state of cell P-c with D5 off: the
// repositories T and X with equal commit ids, and in T the registration w1 of a
// worktree on branch old1 whose folder is gone. It returns T, X and that registration.
func stageOldRegistrationOfSameName(t *testing.T) (string, string, string) {
	t.Helper()
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
	return T, X, filepath.Join(T, ".git", "worktrees", "w1")
}

// TestInstallWorktreeIndexGuard pins the guard of the index placement for a create
// whose registration tests did not run (no tested registration), one case for
// each state of the gitdir record. The index is placed only for a record that can
// be read and names the leaf. In every other state of a registration that is there,
// the guard answers its own error, and the index that is there keeps its bytes and
// its inode. A registration that is gone takes the placement itself, whose error is
// that of cell Z10 (macOS VM). No case opens a FIFO for writing.
func TestInstallWorktreeIndexGuard(t *testing.T) {
	const guardText = "has no gitdir record that names this worktree"
	for _, tc := range []struct {
		name   string
		record func(t *testing.T, root, reg, leaf string)
		want   string // part of the error, or "" for a placed index
	}{
		{"the leaf", func(t *testing.T, _, reg, leaf string) {
			writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
		}, ""},
		{"the leaf as a relative path", func(t *testing.T, _, reg, _ string) {
			writeFile(t, filepath.Join(reg, "gitdir"), "../../../leaf/.git\n", 0o644)
		}, ""},
		{"another path", func(t *testing.T, root, reg, _ string) {
			writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(root, "gone", "w1", ".git")+"\n", 0o644)
		}, guardText},
		{"another path as a relative path", func(t *testing.T, _, reg, _ string) {
			writeFile(t, filepath.Join(reg, "gitdir"), "../../../gone/w1/.git\n", 0o644)
		}, guardText},
		{"an empty file", func(t *testing.T, _, reg, _ string) {
			writeFile(t, filepath.Join(reg, "gitdir"), "", 0o644)
		}, guardText},
		{"missing", func(*testing.T, string, string, string) {}, guardText},
		{"a folder", func(t *testing.T, _, reg, _ string) {
			if err := os.Mkdir(filepath.Join(reg, "gitdir"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, guardText},
		{"a FIFO", func(t *testing.T, _, reg, _ string) {
			mkfifo(t, filepath.Join(reg, "gitdir"))
		}, guardText},
		{"registration gone", func(t *testing.T, _, reg, _ string) {
			if err := os.RemoveAll(reg); err != nil {
				t.Fatal(err)
			}
		}, "openat w1/index: no such file or directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
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
			writeFile(t, index, "old\n", 0o644)
			oldInfo, err := os.Stat(index)
			if err != nil {
				t.Fatal(err)
			}
			tc.record(t, root, reg, leaf)

			var installErr error
			doneWithin(t, "guardedInstallWorktreeIndex", func() { installErr = guardedInstallWorktreeIndex(src, reg, leaf, testedRegistration{}) })
			if tc.want == "" {
				if installErr != nil {
					t.Fatalf("err = %v, want the index placed", installErr)
				}
				wantFileContent(t, index, "new\n")
				return
			}
			if installErr == nil || !strings.Contains(installErr.Error(), tc.want) {
				t.Errorf("err = %v, want %q", installErr, tc.want)
			}
			if _, err := os.Stat(reg); err != nil {
				return // the registration is gone, and its index with it
			}
			wantFileContent(t, index, "old\n")
			if fi, err := os.Stat(index); err != nil || !os.SameFile(oldInfo, fi) {
				t.Errorf("the old index is another file now (err %v)", err)
			}
		})
	}
}

// TestCreatedIndexDir pins the folder of the index, the first answer of
// createdRegistrationRefusal. For a .git value in a folder named worktrees, it is
// the registration of the git directory with the last name of the value, also when
// the value names a folder that does not exist (row D-12). A value in a folder of
// another name is refused, and no folder is answered.
func TestCreatedIndexDir(t *testing.T) {
	root := t.TempDir()
	gitDir, leaf := filepath.Join(root, ".git"), filepath.Join(root, "leaf")
	reg := filepath.Join(gitDir, "worktrees", "w1")
	writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(reg, "commondir"), "../..\n", 0o644)
	if got, refusal := createdRegistrationRefusal(gitDir, leaf, "/elsewhere/worktrees/w1"); got != reg || refusal != "" {
		t.Errorf("index folder = %q, refusal %q, want %s and no refusal", got, refusal, reg)
	}
	if got, refusal := createdRegistrationRefusal(gitDir, leaf, "/elsewhere/WTREG/w1"); got != "" || refusal != notOursText(leaf) {
		t.Errorf("index folder = %q, refusal %q, want no folder and the text", got, refusal)
	}
}

// TestCreatedRegistrationRefusalStates pins the tests of createdRegistrationRefusal.
// A leaf with no .git value is not refused (not measured). With no registrations
// directory the answer is the "was not populated" text (rows B-E1, B-E3 and D-8,
// and cell D8e). With a registrations directory and no entry of the name it is the
// "does not name" text. 89cb6289 answers that text in cell P-p. The state with an
// entry of another name is not measured, and it gets the same text from the rule.
//
// The other states have a registration that is there. With no commondir file it is
// refused (cell P-n). With a commondir file and no gitdir record, it is refused
// (cell P-j). A commondir file that holds the absolute git directory passes, with a
// record that can be read.
//
// Three more states need a user that is not root. A record of mode 0000 is refused
// (cell P-o). So is a registration folder of mode 0000 (cell P-q): its stat
// answers, and the commondir read fails. In a registrations directory of mode 0000
// the stat of the entry fails with "permission denied". That state is not measured.
// It gets the "does not name" text, not the "was not populated" text.
func TestCreatedRegistrationRefusalStates(t *testing.T) {
	root := t.TempDir()
	gitDir, leaf := filepath.Join(root, ".git"), filepath.Join(root, "leaf")
	registry := filepath.Join(gitDir, "worktrees")
	reg := filepath.Join(registry, "w1")
	for _, d := range []string{gitDir, leaf} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	refusalOf := func(adminDir string) string {
		t.Helper()
		got, refusal := createdRegistrationRefusal(gitDir, leaf, adminDir)
		if refusal != "" && got != "" {
			t.Errorf("refusal %q with the index folder %s, want no folder", refusal, got)
		}
		if refusal == "" && adminDir != "" && got != reg {
			t.Errorf("index folder = %q, want %s", got, reg)
		}
		return refusal
	}
	record := filepath.Join(reg, "gitdir")
	if got := refusalOf(""); got != "" {
		t.Errorf("no .git value: refusal %q, want none", got)
	}
	if got, want := refusalOf(reg), "refusing to create worktree: "+namedLeaf(leaf)+" was not populated by git worktree add"; got != want {
		t.Errorf("no registrations directory (rows B-E1, B-E3 and D-8, cell D8e): refusal %q, want %q", got, want)
	}
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := refusalOf(reg); got != notOursText(leaf) {
		t.Errorf("an empty registrations directory (89cb6289 in cell P-p): refusal %q, want the text", got)
	}
	if err := os.Mkdir(filepath.Join(registry, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := refusalOf(reg); got != notOursText(leaf) {
		t.Errorf("an entry of another name only (not measured): refusal %q, want the text", got)
	}
	if err := os.Mkdir(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, record, filepath.Join(leaf, ".git")+"\n", 0o644)
	if got := refusalOf(reg); got != notOursText(leaf) {
		t.Errorf("no commondir file (cell P-n): refusal %q, want the text", got)
	}
	writeFile(t, filepath.Join(reg, "commondir"), gitDir+"\n", 0o644)
	if got := refusalOf(reg); got != "" {
		t.Errorf("absolute commondir: refusal %q, want none", got)
	}
	if os.Geteuid() != 0 {
		for _, tc := range []struct{ name, path string }{
			{"a record of mode 0000 (cell P-o)", record},
			{"a registration folder of mode 0000 (cell P-q)", reg},
			{"a registrations directory of mode 0000 (not measured)", registry},
		} {
			fi, err := os.Stat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(tc.path, 0); err != nil {
				t.Fatal(err)
			}
			got := refusalOf(reg)
			if err := os.Chmod(tc.path, fi.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if got != notOursText(leaf) {
				t.Errorf("%s: refusal %q, want the text", tc.name, got)
			}
		}
		if got := refusalOf(reg); got != "" {
			t.Errorf("after the modes are back: refusal %q, want none", got)
		}
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if got := refusalOf(reg); got != notOursText(leaf) {
		t.Errorf("no gitdir record: refusal %q, want the text", got)
	}
}

// TestCreatedRegistrationTakesOneValue pins that the tests and the index folder use
// the .git value that the caller read, and that the .git file of the leaf is not
// read again. The file on disk names another folder than the value in each case, as
// after a change between two reads. One call answers both the refusal and the index
// folder, so the folder is the one that the tests saw.
func TestCreatedRegistrationTakesOneValue(t *testing.T) {
	root := t.TempDir()
	gitDir, leaf := filepath.Join(root, ".git"), filepath.Join(root, "leaf")
	reg := filepath.Join(gitDir, "worktrees", "w1")
	writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(reg, "commondir"), "../..\n", 0o644)
	const other = "/elsewhere/WTREG/w2"

	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+other+"\n", 0o644)
	got, refusal := createdRegistrationRefusal(gitDir, leaf, reg)
	if refusal != "" {
		t.Errorf("value %s, file names %s: refusal %q, want none", reg, other, refusal)
	}
	if got != reg {
		t.Errorf("value %s, file names %s: index folder = %q, want %s", reg, other, got, reg)
	}

	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+reg+"\n", 0o644)
	got, refusal = createdRegistrationRefusal(gitDir, leaf, other)
	if refusal != notOursText(leaf) {
		t.Errorf("value %s, file names %s: refusal %q, want the text", other, reg, refusal)
	}
	if got != "" {
		t.Errorf("value %s, file names %s: index folder = %q, want none", other, reg, got)
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

// TestPostAddReadsDoNotWaitOnFifo calls the reads after the add with a FIFO at the
// path. Each answers at once, with the answer of a file that cannot be read. A FIFO
// as the commondir file of a git directory makes git itself wait (git 2.47, Linux
// host), so no request reaches gitDirRegistryDir in that state. No code here opens
// a FIFO for writing.
func TestPostAddReadsDoNotWaitOnFifo(t *testing.T) {
	root := realTempDir(t)
	gitDir, leaf := filepath.Join(root, ".git"), filepath.Join(root, "leaf")
	reg := filepath.Join(gitDir, "worktrees", "w1")
	mkfifo(t, filepath.Join(gitDir, "commondir"))
	mkfifo(t, filepath.Join(reg, "commondir"))
	mkfifo(t, filepath.Join(reg, "gitdir"))
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+reg+"\n", 0o644)

	var registry, refusal string
	var record adminRecord
	var mismatch bool
	var installErr error
	src := filepath.Join(root, "new-index")
	writeFile(t, src, "new\n", 0o644)
	doneWithin(t, "the reads after the add", func() {
		registry = gitDirRegistryDir(gitDir)
		_, refusal = createdRegistrationRefusal(gitDir, leaf, worktreeAdminDir(leaf))
		record = readAdminRecord(reg, leaf)
		mismatch = adminRecordMismatch(reg, leaf)
		installErr = guardedInstallWorktreeIndex(src, reg, leaf, testedRegistration{})
	})
	if want := filepath.Join(gitDir, "worktrees"); registry != want {
		t.Errorf("gitDirRegistryDir(FIFO commondir) = %s, want %s", registry, want)
	}
	if refusal != notOursText(leaf) {
		t.Errorf("createdRegistrationRefusal(FIFO commondir) = %q, want the text of cell P-h", refusal)
	}
	if record != recordUnreadable {
		t.Errorf("readAdminRecord(FIFO record) = %d, want recordUnreadable", record)
	}
	if mismatch {
		t.Error("adminRecordMismatch(FIFO record) = true, want false: cell P-g gets the other text")
	}
	if installErr == nil {
		t.Error("guardedInstallWorktreeIndex(FIFO record) placed the index, want the guard")
	}
	if _, err := os.Lstat(filepath.Join(reg, "index")); !os.IsNotExist(err) {
		t.Errorf("the registration holds an index (Lstat err %v), want none", err)
	}
}
