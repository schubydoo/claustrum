//go:build unix

package main

import (
	"encoding/json"
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

// These tests pin the steps of git.worktree_create from the stale entry step to
// the start of the checkout, as 5fd08069 answers them in round tier0-10 (Linux
// VM). In each cell a hook changes the disk right after `git worktree add`. Here
// the git stub does that, with its "steps" action after the real add.

// afterAddSteps makes the stub run steps after the real `worktree add`.
func afterAddSteps(t *testing.T, steps ...[]string) {
	t.Helper()
	stubSteps(t, steps...)
	slowGit(t, "worktree,add", "post", 0, "", "")
}

// realRoot is the folder that holds the repository of f, with its symlinks
// resolved, as git records a path.
func (f wtFixture) realRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(filepath.Dir(f.top))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// wantKept checks the disk after a refusal that removes nothing: the leaf holds
// leafWant, the registrations folder holds regsWant, and each branch stays.
func (f wtFixture) wantKept(t *testing.T, leafWant, regsWant []string, branches ...string) {
	t.Helper()
	if got := f.leafEntries(t); !slices.Equal(got, leafWant) {
		t.Errorf("leaf entries = %q, want %q", got, leafWant)
	}
	if got := f.regs(t); !slices.Equal(got, regsWant) {
		t.Errorf("registrations = %q, want %q", got, regsWant)
	}
	for _, b := range branches {
		if !f.hasRef(t, b) {
			t.Errorf("refs/heads/%s is gone, want it kept", b)
		}
	}
}

// TestWorktreeCreateWorktreesIsLink pins cell V1 of round tier0-10 and cell E1
// of round tier0-11: `<git dir>/worktrees` is a symbolic link to a folder, in
// the state shadow. The create is refused before the add. No `worktree add`
// runs, no leaf and no new branch stay, and the target of the link gets no new
// entry. In cell E1 the request has an existingBranch that exists, and the
// frame has the text one time.
func TestWorktreeCreateWorktreesIsLink(t *testing.T) {
	for _, tc := range []struct{ name, existing string }{{"V1", ""}, {"E1", "eb"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			runGit(t, f.repo, "branch", "eb")
			// The fixture has one worktree, so the folder is there, as in the cell.
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "wt")
			link := filepath.Join(f.gitDir, "worktrees")
			if err := os.Rename(link, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			before := entryNames(t, target)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
			if tc.existing != "" {
				params["existingBranch"] = tc.existing
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			wantError(t, raw, `git worktree add failed: cannot prepare the repository's worktrees directory: "`+link+`" is not a directory`, "worktree_add_failed")
			for _, c := range calls {
				if slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") {
					t.Errorf("a worktree add ran: %q", c.argv)
				}
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf after the refusal: %v, want none", err)
			}
			if _, err := os.Stat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
				t.Errorf("refs/heads/w1 after the refusal: %v, want none", err)
			}
			if _, err := os.Stat(filepath.Join(f.gitDir, "refs", "heads", "eb")); err != nil {
				t.Errorf("refs/heads/eb after the refusal: %v, want it kept", err)
			}
			if got := entryNames(t, target); !slices.Equal(got, before) {
				t.Errorf("the target of the link holds %q, want %q", got, before)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link worktrees after the refusal: %v, want the link", err)
			}
		})
	}
}

// TestWorktreeCreateNoPairAtALinkedWorktrees pins cells C6, C6n and C6b of
// round task105-06 (Linux VM): `<git dir>/worktrees` is a symbolic link to an
// empty real folder. The create is refused with the text of cell V1, and no
// call of the pair before the add runs. A broken link and a file at that name
// give no pair either (claustrum's own, not measured). The control is a real
// folder with the entry of another worktree: the pair runs (cell R-Q2b).
func TestWorktreeCreateNoPairAtALinkedWorktrees(t *testing.T) {
	for _, row := range []struct {
		name  string
		stage func(t *testing.T, at string)
		pair  bool
	}{
		{"C6 link to an empty folder", func(t *testing.T, at string) {
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "wtreal")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			linkAt(t, at, target)
		}, false},
		{"broken link", func(t *testing.T, at string) {
			linkAt(t, at, filepath.Join(resolveTestRoot(t, t.TempDir()), "gone"))
		}, false},
		{"file", func(t *testing.T, at string) { mustWrite(t, at, "") }, false},
		{"R-Q2b real folder", nil, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			at := filepath.Join(f.gitDir, worktreesSubdir)
			if row.stage != nil {
				if err := os.RemoveAll(at); err != nil {
					t.Fatal(err)
				}
				row.stage(t, at)
			}
			raw, calls := f.create(t, nil)
			if row.pair {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
			} else {
				wantError(t, raw, `git worktree add failed: cannot prepare the repository's worktrees directory: "`+at+`" is not a directory`, "worktree_add_failed")
			}
			if got := ranPair(calls, f.repo); got != row.pair {
				t.Errorf("the pair ran = %v, want %v", got, row.pair)
			}
		})
	}
}

// TestWorktreeCreateLinkedWorktreesUnderAHold pins cell C6b of rounds
// task105-04 and task105-06 (Linux VM). `<git dir>/worktrees` is a symbolic
// link to an empty real folder, and a hold starts while `rev-parse
// --absolute-git-dir` of the create runs. The add is refused with the text of
// offWorktreesRefusal. Its listing in the repository, in the form of the git
// directory, is the last call, with no pair and no add. Mutation: in
// worktreeAdd, test offWorktreesRefusal before hardenedGitCmd. The listing then
// does not run.
func TestWorktreeCreateLinkedWorktreesUnderAHold(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	at := filepath.Join(h.gitDir, worktreesSubdir)
	if err := os.RemoveAll(at); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(resolveTestRoot(t, t.TempDir()), "wtreal")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	linkAt(t, at, target)
	holdA(t, "rev-parse,--absolute-git-dir")
	params, _ := h.createParams()
	want := `{"success":false,"error":"git worktree add failed: cannot list the repository's worktree entries: \"worktrees\" is a link or a file where Claude needs a real directory; put the directory itself in its place","errorCode":"worktree_add_failed"}`
	if got := h.startsInA(t, "git.worktree_create", params); got != want {
		t.Fatalf("C6b: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "rev-parse,--absolute-git-dir", true)
	var after []envCall
	for _, c := range calls[i+1:] {
		if !sameDir(c.cwd, h.repoB) {
			after = append(after, c)
		}
	}
	if len(after) != 1 || !after[0].listing() || !sameDir(after[0].cwd, h.repo) {
		t.Fatalf("C6b: the calls after absolute-git-dir are %v, want one listing in %s", after, h.repo)
	}
	if v := callCommonDir(after[0]); !sameDir(v, h.gitDir) {
		t.Errorf("C6b: the listing has GIT_COMMON_DIR=%q, want the git directory %s", v, h.gitDir)
	}
}

// TestWorktreeCreateTwoNewEntries pins cell V2 of round tier0-10: a
// second add puts one more entry into the registrations folder before the first
// add ends. The create is refused. Both entries and both branches stay, and the
// leaf holds its .git file only.
func TestWorktreeCreateTwoNewEntries(t *testing.T) {
	f, s := postAddFixture(t)
	second := filepath.Join(f.realRoot(t), "T", ".claude", "worktrees", "w1b")
	afterAddSteps(t, []string{"git", "worktree", "add", "--no-track", "--no-checkout", "-b", "w1b", second})
	raw, _ := f.create(t, s, "w1", "", 0)
	wantError(t, raw, "refusing to create worktree: "+namedLeaf(f.leaf())+
		" carries a .git file naming an admin entry other than the one just created for it", "unsafe_path")
	f.wantKept(t, []string{".git"}, []string{"w1", "w1b"}, "w1", "w1b")
	if _, err := os.Lstat(filepath.Join(f.regDir, "w1", "index")); !os.IsNotExist(err) {
		t.Errorf("the registration holds an index (Lstat err %v), want none", err)
	}
}

// TestWorktreeCreateLeafGitFile pins cells V3a, V3b and V3c of round tier0-10. After the add the leaf has no .git file, a folder at .git,
// or a .git file with no blank after `gitdir:`. Each create is refused, with no
// checkout. The registration and the branch stay. The empty leaf of V3a goes.
func TestWorktreeCreateLeafGitFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps func(gitFile, reg string) [][]string
		text  func(leaf string) string
		leaf  []string
	}{
		{"V3a", func(g, _ string) [][]string { return [][]string{{"rm", g}} },
			func(l string) string { return l + " has no .git file" }, nil},
		{"V3b", func(g, _ string) [][]string { return [][]string{{"rm", g}, {"mkdir", g}} },
			func(l string) string { return l + "/.git is not a regular file" }, []string{".git"}},
		{"V3c", func(g, reg string) [][]string { return [][]string{{"write", g, "gitdir:" + reg + "\n"}} },
			func(l string) string { return l + "/.git does not name a git dir" }, []string{".git"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			reg := filepath.Join(f.realRoot(t), "T", ".git", "worktrees", "w1")
			afterAddSteps(t, tc.steps(filepath.Join(f.leaf(), ".git"), reg)...)
			raw, _ := f.create(t, s, "w1", "", 0)
			wantError(t, raw, "refusing to create worktree: "+tc.text(namedLeaf(f.leaf())), "unsafe_path")
			f.wantKept(t, tc.leaf, []string{"w1"}, "w1")
			if _, err := os.Stat(filepath.Join(f.regDir, "w1", "HEAD")); err != nil {
				t.Errorf("the registration lost its HEAD: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(f.regDir, "w1", "index")); !os.IsNotExist(err) {
				t.Errorf("the registration holds an index (Lstat err %v), want none", err)
			}
		})
	}
}

// TestWorktreeCreateRecordWithSlash pins cell V4 of round tier0-10 and cells R1,
// R2 and R3 of round tier0-11: after the add the gitdir record of the new entry
// is <leaf>/.git/, <leaf>/.git with a blank at its end, <leaf>/./.git or
// <leaf>//.git. The create succeeds with the files, and the record keeps its
// text.
func TestWorktreeCreateRecordWithSlash(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"V4", "/.git/\n"}, {"R1", "/.git \n"}, {"R2", "/./.git\n"}, {"R3", "//.git\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			reg := filepath.Join(f.regDir, "w1")
			record := filepath.Join(f.realRoot(t), "T", ".claude", "worktrees", "w1") + tc.tail
			afterAddSteps(t, []string{"write", filepath.Join(reg, "gitdir"), record})
			raw, _ := f.create(t, s, "w1", "", 0)
			b, err := os.ReadFile(filepath.Join(f.leaf(), ".git"))
			if err != nil {
				t.Fatal(err)
			}
			f.wantCreated(t, raw, string(b), reg)
			if got, _ := os.ReadFile(filepath.Join(reg, "gitdir")); string(got) != record {
				t.Errorf("the record = %q, want %q", got, record)
			}
		})
	}
}

// TestWorktreeCreateSpellingCallFails pins cells V5 and V5c of round
// tier0-10. After the add the record of the new entry names another path. The
// repository also holds a live sibling worktree w9 on branch w9.
//
//   - V5: git fails the `rev-parse --show-toplevel` call. The create answers the
//     retry frame, and the leaf, the registration w1 and branch w1 go. The
//     registration w9, the sibling worktree and branch w9 stay.
//   - V5c: the call works. The create answers the "different worktree" text, and
//     nothing is removed.
func TestWorktreeCreateSpellingCallFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails bool
	}{{"V5", true}, {"V5c", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			root := f.realRoot(t)
			live := filepath.Join(root, "live", "w9")
			runGit(t, f.top, "worktree", "add", "-q", "-b", "w9", live)
			reg, w9 := filepath.Join(f.regDir, "w1"), filepath.Join(f.regDir, "w9")
			afterAddSteps(t, []string{"write", filepath.Join(reg, "gitdir"), filepath.Join(root, "elsewhere", "w", ".git") + "\n"})
			if tc.fails {
				stubRule2(t, "rev-parse,--show-toplevel", "", 0, "128", "", "")
			}
			raw, _ := f.create(t, s, "w1", "", 0)
			if tc.fails {
				want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (verification): ` +
					`could not verify the worktree's registration: git did not report its spelling of the path (entry w1); retry",` +
					`"errorCode":"worktree_add_failed"}}`
				if raw != want {
					t.Errorf("reply = %s\nwant %s", raw, want)
				}
				f.assertRolledBack(t, []string{"w9"}, false)
			} else {
				wantError(t, raw, differentWorktreeText(f.leaf()), "unsafe_path")
				f.wantKept(t, []string{".git"}, []string{"w1", "w9"}, "w1")
				if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
					t.Errorf("the registration w1 lost its HEAD: %v", err)
				}
			}
			for _, name := range []string{"HEAD", "commondir", "gitdir"} {
				if _, err := os.Stat(filepath.Join(w9, name)); err != nil {
					t.Errorf("the registration of the sibling lost %s: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(live, "t.txt")); err != nil {
				t.Errorf("the sibling worktree lost its checkout: %v", err)
			}
			if !f.hasRef(t, "w9") {
				t.Error("refs/heads/w9 is gone, want it kept")
			}
		})
	}
}

// TestWorktreeCreateSpellingCallFailsOnOldEntry pins claustrum's own guard of
// the rollback after a failed spelling call (not measured). The state is that of cell P-c: the daemon has
// GIT_COMMON_DIR of repository X, git makes the registration in X, and T holds an
// old entry w1 whose record names another worktree. git fails the `rev-parse
// --show-toplevel` call. The registrations folder of T has no new name, so the
// create keeps the "different worktree" refusal and removes nothing.
func TestWorktreeCreateSpellingCallFailsOnOldEntry(t *testing.T) {
	T, X, oldReg := stageOldRegistrationOfSameName(t)
	leaf := filepath.Join(T, ".claude", "worktrees", "w1")
	before := regFiles(t, oldReg)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	stubRule2(t, "rev-parse,--show-toplevel", "", 0, "128", "", "")
	t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
	raw := frameWithin(t, "git.worktree_create", createParams(T, leaf, "w1"))
	wantError(t, raw, differentWorktreeText(leaf), "unsafe_path")
	if got := regFiles(t, oldReg); !slices.Equal(got, before) {
		t.Errorf("the old registration = %q\nwant %q", got, before)
	}
	if ents, err := os.ReadDir(leaf); err != nil || len(ents) != 1 || ents[0].Name() != ".git" {
		t.Errorf("leaf entries = %v (err %v), want only .git", ents, err)
	}
	for _, p := range []string{filepath.Join(X, ".git", "worktrees", "w1", "HEAD"), filepath.Join(T, ".git", "refs", "heads", "w1"),
		filepath.Join(T, ".git", "refs", "heads", "old1")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
}

// TestWorktreeCreateCommondirThroughLink pins cell V6 of round
// tier0-10: after the add the commondir file of the new entry holds an absolute
// path that reaches the git directory through a symbolic link. The create is
// refused, and nothing is removed. The link and the git directory stay.
func TestWorktreeCreateCommondirThroughLink(t *testing.T) {
	f, s := postAddFixture(t)
	root := f.realRoot(t)
	link, reg := filepath.Join(root, "gl"), filepath.Join(f.regDir, "w1")
	afterAddSteps(t, []string{"symlink", filepath.Join(root, "T", ".git"), link},
		[]string{"write", filepath.Join(reg, "commondir"), link + "\n"})
	raw, _ := f.create(t, s, "w1", "", 0)
	wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
	b, err := os.ReadFile(filepath.Join(f.leaf(), ".git"))
	if err != nil {
		t.Fatal(err)
	}
	f.wantNothingRolledBack(t, string(b), reg)
	if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
		t.Errorf("registrations = %q, want w1", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link after the refusal: %v, want the link", err)
	}
	if _, err := os.Stat(filepath.Join(link, "HEAD")); err != nil {
		t.Errorf("the git directory behind the link lost its HEAD: %v", err)
	}
}

// TestWorktreeCreateEntryIsLink pins cell V7 of round tier0-10: after
// the add the new entry is moved away, and a symbolic link to it is put at its
// name. The create is refused before the checkout, and nothing is removed: the
// leaf, the link, the folder behind the link and the branch stay.
func TestWorktreeCreateEntryIsLink(t *testing.T) {
	f, s := postAddFixture(t)
	reg, moved := filepath.Join(f.regDir, "w1"), filepath.Join(f.realRoot(t), "moved-w1")
	afterAddSteps(t, []string{"rename", reg, moved}, []string{"symlink", moved, reg})
	raw, _ := f.create(t, s, "w1", "", 0)
	wantError(t, raw, notOursText(f.leaf()), "unsafe_path")
	f.wantKept(t, []string{".git"}, []string{"w1"}, "w1")
	if fi, err := os.Lstat(reg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the entry after the refusal: %v, want the link", err)
	}
	got := pathsBelow(t, moved)
	for _, name := range []string{"HEAD", "commondir", "gitdir"} {
		if !slices.Contains(got, name) {
			t.Errorf("the folder behind the link holds %q, want %s kept", got, name)
		}
	}
	if slices.Contains(got, "index") {
		t.Errorf("the folder behind the link holds an index: %q", got)
	}
}

// TestWorktreeCreateLeafReplaced pins cell V8 of round tier0-10: after
// the add the leaf is renamed away, and a new folder with a copy of the .git file
// is put at its path. The create is refused before the checkout. The new folder
// holds .git only, and nothing is removed.
func TestWorktreeCreateLeafReplaced(t *testing.T) {
	f, s := postAddFixture(t)
	old := f.leaf() + ".old"
	gitFile := "gitdir: " + filepath.Join(f.realRoot(t), "T", ".git", "worktrees", "w1") + "\n"
	afterAddSteps(t, []string{"rename", f.leaf(), old}, []string{"mkdir", f.leaf()},
		[]string{"write", filepath.Join(f.leaf(), ".git"), gitFile})
	raw, _ := f.create(t, s, "w1", "", 0)
	wantError(t, raw, "refusing to create worktree: "+namedLeaf(f.leaf())+
		" is no longer the directory that was created for the worktree", "unsafe_path")
	f.wantNothingRolledBack(t, gitFile, filepath.Join(f.regDir, "w1"))
	if b, err := os.ReadFile(filepath.Join(old, ".git")); err != nil || string(b) != gitFile {
		t.Errorf("the .git file of the folder that was renamed away = %q (err %v), want %q: this fixture does not stage the cell", b, err, gitFile)
	}
	if ents, err := os.ReadDir(old); err != nil || len(ents) != 1 {
		t.Errorf("the folder that was renamed away holds %v (err %v), want .git only", ents, err)
	}
}

// TestWorktreeCreateStaleEntryOfMainRepository pins cells V9 and V9c of
// round tier0-10. The main repository holds a stale entry `old` whose record
// names the new leaf.
//
//   - V9: baseRepo is a linked worktree of that repository. The entry goes, and
//     the create succeeds.
//   - V9c: baseRepo is the main repository. The same.
//   - The entry holds a `locked` file (the rule that stays, not measured in this
//     round): it stays with every file, and the add fails with the text of git.
//
// Branch oldb stays in every case.
func TestWorktreeCreateStaleEntryOfMainRepository(t *testing.T) {
	for _, tc := range []struct {
		name           string
		linked, locked bool
	}{{"V9", true, false}, {"V9c", false, false}, {"V9 locked", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			requireGit(t)
			f := newWTFixture(t, tc.linked)
			root := f.realRoot(t)
			gone := filepath.Join(root, "gone", "old")
			runGit(t, f.top, "worktree", "add", "-q", "-b", "oldb", gone)
			if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
				t.Fatal(err)
			}
			realBase, err := filepath.EvalSymlinks(f.base)
			if err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(f.regDir, "old")
			writeFile(t, filepath.Join(old, "gitdir"), filepath.Join(realBase, ".claude", "worktrees", "w1", ".git")+"\n", 0o644)
			if tc.locked {
				writeFile(t, filepath.Join(old, "locked"), "", 0o644)
			}
			before := regFiles(t, old)
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			wantRegs := []string{"w1"}
			if tc.linked {
				wantRegs = []string{"LW", "w1"}
			}
			if tc.locked {
				if !strings.Contains(raw, `"error":"git worktree add failed: `) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
					t.Errorf("reply = %s\nwant the failed add of git", raw)
				}
				if got := regFiles(t, old); !slices.Equal(got, before) {
					t.Errorf("the locked entry = %q\nwant %q", got, before)
				}
				if got := f.regs(t); !slices.Equal(got, []string{"LW", "old"}) {
					t.Errorf("registrations = %q, want LW and old", got)
				}
			} else {
				if !strings.Contains(raw, `"success":true`) {
					t.Errorf("reply = %s\nwant success", raw)
				}
				if got := f.regs(t); !slices.Equal(got, wantRegs) {
					t.Errorf("registrations = %q, want %q", got, wantRegs)
				}
				if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
					t.Errorf("the leaf holds no checkout: %v", err)
				}
			}
			if !f.hasRef(t, "oldb") {
				t.Error("refs/heads/oldb is gone, want it kept")
			}
		})
	}
}

// TestStaleRegistryDir pins the folder of the stale entry step: `worktrees` of
// the common git directory of baseRepo. A linked worktree gets the folder of the
// main repository (cell V9 of round tier0-10). A separate git directory gets its
// own folder (cell S1 of round tier3-02, Linux, macOS and Windows VMs). The
// last two rows are not measured: they follow the commondir rule of git.
func TestStaleRegistryDir(t *testing.T) {
	for _, tc := range []struct {
		name      string
		admin     string // the folder that the .git file of baseRepo names, below the root
		commondir string // the text of its commondir file, or "" for none
		registry  string // the folder that holds the stale entry, below the root
	}{
		{"a linked worktree (cell V9)", "main/.git/worktrees/B", "../..\n", "main/.git/worktrees"},
		{"a separate git directory with no commondir (cell S1)", "sep", "", "sep/worktrees"},
		{"a git directory with a commondir whose parent is not worktrees", "mods/sub", ".\n", "mods/sub/worktrees"},
		{"a folder below worktrees with no commondir", "x/worktrees/sub", "", "x/worktrees/sub/worktrees"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			repo := filepath.Join(root, "B")
			admin := filepath.Join(root, filepath.FromSlash(tc.admin))
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			mkdirForTest(t, admin)
			writeFile(t, filepath.Join(repo, ".git"), "gitdir: "+admin+"\n", 0o644)
			if tc.commondir != "" {
				writeFile(t, filepath.Join(admin, "commondir"), tc.commondir, 0o644)
			}
			entry := filepath.Join(root, filepath.FromSlash(tc.registry), "old")
			writeFile(t, filepath.Join(entry, "gitdir"), leaf+"/.git\n", 0o644)
			dropStaleWorktreeRegistration(repo, leaf, nil)
			if _, err := os.Lstat(entry); !os.IsNotExist(err) {
				t.Errorf("the stale entry stays (Lstat err %v), want it removed", err)
			}
		})
	}
}

// TestCompareAdminRecordSpellings pins which spellings of the gitdir record name
// the leaf after the add. 5fd08069 creates the worktree with a slash at the end
// (cell V4 of round tier0-10, Linux VM), and with a blank at the end, a "/./"
// part and a double slash (cells R1, R2 and R3 of round tier0-11, Linux VM).
// claustrum's clean also accepts a blank at the start and an "x/.." pair (not
// measured). A record of another folder stays one of another worktree. Without
// the clean, each spelling but the plain path is one of another worktree too.
func TestCompareAdminRecordSpellings(t *testing.T) {
	root := realTempDir(t)
	admin, leaf := filepath.Join(root, "worktrees", "w1"), filepath.Join(root, "leaf")
	mkdirForTest(t, leaf)
	for _, tc := range []struct {
		name, record string
		want         adminRecord
	}{
		{"the path", leaf + "/.git\n", recordNamesLeaf},
		{"a slash at the end (cell V4)", leaf + "/.git/\n", recordNamesLeaf},
		{"a blank at the end (cell R1)", leaf + "/.git \n", recordNamesLeaf},
		{"a blank at the start", " " + leaf + "/.git\n", recordNamesLeaf},
		{"a double slash (cell R3)", leaf + "//.git\n", recordNamesLeaf},
		{"a /./ part (cell R2)", leaf + "/./.git\n", recordNamesLeaf},
		{"an x/.. pair", leaf + "/x/../.git\n", recordNamesLeaf},
		{"another folder", leaf + "x/.git\n", recordOfAnother},
		{"another folder with a blank at the end", leaf + "x/.git \n", recordOfAnother},
	} {
		writeFile(t, filepath.Join(admin, "gitdir"), tc.record, 0o644)
		if got := compareAdminRecord(admin, leaf, true); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
		if got, plain := readAdminRecord(admin, leaf), tc.record == leaf+"/.git\n"; (got == recordNamesLeaf) != plain {
			t.Errorf("%s with no clean: %d, want recordNamesLeaf only for the plain path", tc.name, got)
		}
	}
	writeFile(t, filepath.Join(admin, "gitdir"), leaf+"/.git/\n", 0o644)
	if got := readAdminRecord(admin, leaf); got != recordOfAnother {
		t.Errorf("a slash at the end with no clean: %d, want recordOfAnother", got)
	}
	if !adminRecordMismatch(admin, leaf, false) || adminRecordMismatch(admin, leaf, true) {
		t.Error("adminRecordMismatch does not follow its clean switch for a slash at the end")
	}
}

// TestWorktreeCreateListingFails pins claustrum's own answer to a listing of the
// registrations folder that fails with another error than "does not exist" (not
// measured). The create is refused with the "other than the one just created"
// text, with no checkout, and nothing is removed.
func TestWorktreeCreateListingFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails int // the listing that fails: 1 is before the add, 2 is after it
	}{{"before the add", 1}, {"after the add", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			old := readRegistrationsDir
			t.Cleanup(func() { readRegistrationsDir = old })
			calls := 0
			readRegistrationsDir = func(name string) ([]fs.DirEntry, error) {
				if calls++; calls == tc.fails {
					return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("too many open files")}
				}
				return old(name)
			}
			raw, _ := f.create(t, s, "w1", "", 0)
			if calls != 2 {
				t.Fatalf("the registrations folder was listed %d times, want 2: the test did not stage the state", calls)
			}
			wantError(t, raw, "refusing to create worktree: "+namedLeaf(f.leaf())+
				" carries a .git file naming an admin entry other than the one just created for it", "unsafe_path")
			f.wantKept(t, []string{".git"}, []string{"w1"}, "w1")
			if _, err := os.Lstat(filepath.Join(f.regDir, "w1", "index")); !os.IsNotExist(err) {
				t.Errorf("the registration holds an index (Lstat err %v), want none", err)
			}
		})
	}
}

// TestWorktreeCreateListingsOfTwoFolders pins claustrum's own rule that the two
// listings read one folder (not measured). After the add a commondir file
// appears in the git directory and names another folder X, whose worktrees
// folder holds one entry w1 with a record of the leaf. That entry is the one new
// name against the first listing, which read the folder of the repository. The
// create is refused, and the entry in X keeps its files.
func TestWorktreeCreateListingsOfTwoFolders(t *testing.T) {
	f, s := postAddFixture(t)
	root := f.realRoot(t)
	x := filepath.Join(root, "X")
	other := filepath.Join(x, "worktrees", "w1")
	writeFile(t, filepath.Join(other, "commondir"), "../..\n", 0o644)
	writeFile(t, filepath.Join(other, "gitdir"), filepath.Join(root, "T", ".claude", "worktrees", "w1", ".git")+"\n", 0o644)
	afterAddSteps(t, []string{"write", filepath.Join(root, "T", ".git", "commondir"), x + "\n"})
	raw, _ := f.create(t, s, "w1", "", 0)
	if err := os.Remove(filepath.Join(f.top, ".git", "commondir")); err != nil {
		t.Fatalf("the commondir file of the git directory: %v: the test did not stage the state", err)
	}
	wantError(t, raw, "refusing to create worktree: "+namedLeaf(f.leaf())+
		" carries a .git file naming an admin entry other than the one just created for it", "unsafe_path")
	if got, want := pathsBelow(t, other), []string{"commondir", "gitdir"}; !slices.Equal(got, want) {
		t.Errorf("the entry in X holds %q, want %q", got, want)
	}
	f.wantKept(t, []string{".git"}, []string{"w1"}, "w1")
}

// TestWorktreeCreateNoAnswerKeepsOldTests pins a create whose `rev-parse
// --absolute-git-dir` gives an empty answer (not measured). It keeps two rules
// that it had before round tier0-10.
//
//   - The leaf is replaced after the add: the checkout runs, and the answer is the
//     "was not populated" frame of the test after the checkout.
//   - The record has a slash at its end: the "different worktree" refusal.
func TestWorktreeCreateNoAnswerKeepsOldTests(t *testing.T) {
	t.Run("the leaf is replaced", func(t *testing.T) {
		f, s := postAddFixture(t)
		gitFile := "gitdir: " + filepath.Join(f.realRoot(t), "T", ".git", "worktrees", "w1") + "\n"
		afterAddSteps(t, []string{"rename", f.leaf(), f.leaf() + ".old"}, []string{"mkdir", f.leaf()},
			[]string{"write", filepath.Join(f.leaf(), ".git"), gitFile})
		stubRule2(t, "rev-parse,--absolute-git-dir", "", 0, "0", "", "")
		raw, _ := f.create(t, s, "w1", "", 0)
		wantError(t, raw, f.leaf()+" was not populated by git worktree add", "worktree_add_failed")
		if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
			t.Errorf("the new folder holds no checkout: %v, want the checkout before the test", err)
		}
	})
	t.Run("the record has a slash at its end", func(t *testing.T) {
		f, s := postAddFixture(t)
		reg := filepath.Join(f.regDir, "w1")
		afterAddSteps(t, []string{"write", filepath.Join(reg, "gitdir"),
			filepath.Join(f.realRoot(t), "T", ".claude", "worktrees", "w1", ".git") + "/\n"})
		stubRule2(t, "rev-parse,--absolute-git-dir", "", 0, "0", "", "")
		raw, _ := f.create(t, s, "w1", "", 0)
		wantError(t, raw, differentWorktreeText(f.leaf()), "unsafe_path")
		f.wantKept(t, []string{".git"}, []string{"w1"}, "w1")
	})
}

// TestWorktreeCreateSpellingCallEndedByDeadline pins that a stop does not
// start the rollback after a failed spelling call (claustrum's own, not
// measured). The state is that of cell V5 of round tier0-10, but the time limit
// of a call (shortened to 2 s) stops the `rev-parse --show-toplevel` call. The
// create keeps the "different worktree" refusal, and nothing is removed.
func TestWorktreeCreateSpellingCallEndedByDeadline(t *testing.T) {
	f, s := postAddFixture(t)
	shortGitLimit(t, 2*time.Second)
	reg := filepath.Join(f.regDir, "w1")
	afterAddSteps(t, []string{"write", filepath.Join(reg, "gitdir"), filepath.Join(f.realRoot(t), "elsewhere", "w", ".git") + "\n"})
	stubRule2(t, "rev-parse,--show-toplevel", "", 60*time.Second, "", "", "")
	raw, took := f.create(t, s, "w1", "", 0)
	if took > 30*time.Second {
		t.Fatalf("the create took %v: the deadline did not end the call", took)
	}
	wantError(t, raw, differentWorktreeText(f.leaf()), "unsafe_path")
	f.wantKept(t, []string{".git"}, []string{"w1"}, "w1")
	if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
		t.Errorf("the registration w1 lost its HEAD: %v", err)
	}
}

// TestWorktreeCreateThroughLinkedWorktreeWaits pins cell T1 of round tier0-11.
// One daemon gets two creates: the first through the main repository, the
// second through a linked worktree of it. The stub holds the first `worktree
// add` after git made the registration. The second add must not run inside that
// hold, so both creates succeed. At the start of the second add the stub copies
// the checkout file of the first worktree, which is there only after the first
// add ended. Without the lock by git directory the copy is missing, and the
// first create sees two new entries and is refused.
func TestWorktreeCreateThroughLinkedWorktreeWaits(t *testing.T) {
	f := newWTFixture(t, true)
	installGitSlowStub(t, f.realGit)
	s := newTestServer(t)
	// Only the add of the first create has the word w1.
	slowGit(t, "worktree,add,w1", "post", 4*time.Second, "", "")
	firstLeaf := filepath.Join(f.top, ".claude", "worktrees", "w1")
	snap := filepath.Join(t.TempDir(), "first-checkout")
	duringCallSteps(t, "worktree,add,w2", "", []string{"snap", filepath.Join(firstLeaf, "t.txt"), snap})
	firstLine := rpcLine(t, "git.worktree_create", createParams(f.top, firstLeaf, "w1"))
	secondLeaf := filepath.Join(f.base, ".claude", "worktrees", "w2")
	secondLine := rpcLine(t, "git.worktree_create", createParams(f.base, secondLeaf, "w2"))
	first := make(chan string, 1)
	go func() {
		var b []byte
		if resp := s.dispatch(nil, []byte(firstLine)); resp != nil {
			b, _ = json.Marshal(*resp)
		}
		first <- string(b)
	}()
	// The second create starts when the first add made its registration.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.regDir, "w1", "HEAD")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first create made no registration in 60 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := dispatchRaw(t, s, secondLine)
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("the second add started before the first add ended: %v", err)
	}
	if got := <-first; !strings.Contains(got, `"success":true`) {
		t.Errorf("the create through the main repository = %s\nwant success", got)
	}
	if !strings.Contains(second, `"success":true`) {
		t.Errorf("the create through the linked worktree = %s\nwant success", second)
	}
	for _, leaf := range []string{firstLeaf, secondLeaf} {
		if _, err := os.Stat(filepath.Join(leaf, "t.txt")); err != nil {
			t.Errorf("%s holds no checkout: %v", leaf, err)
		}
	}
	if got, want := f.regs(t), []string{"LW", "w1", "w2"}; !slices.Equal(got, want) {
		t.Errorf("registrations = %q, want %q", got, want)
	}
}

// TestWorktreeCreateRefusalKeepsLeafGitFile pins that the tests after the add
// come before the daemon writes the .git file of the leaf again, in the state
// shadow. After the refusals of cells V5c and V2 of round tier0-10 the file
// still names the copy on 5fd08069 (round tier0-10, Linux VM, 1 run each). The stub writes that text after the add, because git 2.50 writes the
// path of the repository there. The last row is the control: a create with no
// refusal ends with the path of the git directory in the file.
func TestWorktreeCreateRefusalKeepsLeafGitFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps func(regs, other string) [][]string
		text  func(leaf string) string
	}{
		{"V5c", func(regs, other string) [][]string {
			return [][]string{{"write", filepath.Join(regs, "w1", "gitdir"), other + "\n"}}
		}, differentWorktreeText},
		{"V2", func(regs, _ string) [][]string { return [][]string{{"mkdir", filepath.Join(regs, "w1b")}} },
			func(leaf string) string {
				return "refusing to create worktree: " + namedLeaf(leaf) +
					" carries a .git file naming an admin entry other than the one just created for it"
			}},
		{"no refusal", func(_, _ string) [][]string { return nil }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			regs := filepath.Join(f.gitDir, "worktrees")
			named := "gitdir: " + filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "worktrees", "w1") + "\n"
			steps := append([][]string{{"write", filepath.Join(leaf, ".git"), named}},
				tc.steps(regs, filepath.Join(filepath.Dir(f.repo), "elsewhere", "w", ".git"))...)
			afterAddSteps(t, steps...)
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			got := mustRead(t, filepath.Join(leaf, ".git"))
			if tc.text == nil {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("reply = %s\nwant success", raw)
				}
				if want := "gitdir: " + filepath.Join(regs, "w1") + "\n"; got != want {
					t.Errorf("the .git file of the leaf = %q, want %q", got, want)
				}
				return
			}
			wantError(t, raw, tc.text(leaf), "unsafe_path")
			if got != named {
				t.Errorf("the .git file of the leaf = %q\nwant %q, the text before the refusal", got, named)
			}
			if _, err := os.Stat(filepath.Join(regs, "w1", "HEAD")); err != nil {
				t.Errorf("the registration w1 lost its HEAD: %v", err)
			}
		})
	}
}

// TestWorktreeCreateSpellingCallFailsWithOldEntryNamed pins cells P1 and P1c of
// round tier0-11. The repository holds the registration w1 of a live worktree
// on branch ob, at <root>/x/other. git names the new registration w11. After
// the add the .git file of the leaf names the old entry w1.
//
//   - P1: git fails the `rev-parse --show-toplevel` call. The create answers the
//     retry frame with "(entry w1)". The leaf, the entry w11 and branch w1 go.
//     The old entry w1, its worktree and branch ob stay.
//   - P1c: the call works. The create answers the "different worktree" text, and
//     nothing is removed.
func TestWorktreeCreateSpellingCallFailsWithOldEntryNamed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails bool
	}{{"P1", true}, {"P1c", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			root := f.realRoot(t)
			made, other := filepath.Join(root, "x", "w1"), filepath.Join(root, "x", "other")
			runGit(t, f.top, "worktree", "add", "-q", "-b", "ob", made)
			runGit(t, f.top, "worktree", "move", made, other)
			oldReg := filepath.Join(f.regDir, "w1")
			before := regFiles(t, oldReg)
			afterAddSteps(t, []string{"write", filepath.Join(f.leaf(), ".git"), "gitdir: " + filepath.Join(root, "T", ".git", "worktrees", "w1") + "\n"})
			if tc.fails {
				stubRule2(t, "rev-parse,--show-toplevel", "", 0, "128", "", "")
			}
			raw, _ := f.create(t, s, "w1", "", 0)
			if tc.fails {
				want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (verification): ` +
					`could not verify the worktree's registration: git did not report its spelling of the path (entry w1); retry",` +
					`"errorCode":"worktree_add_failed"}}`
				if raw != want {
					t.Errorf("reply = %s\nwant %s", raw, want)
				}
				f.assertRolledBack(t, []string{"w1"}, false)
			} else {
				wantError(t, raw, differentWorktreeText(f.leaf()), "unsafe_path")
				f.wantKept(t, []string{".git"}, []string{"w1", "w11"}, "w1")
			}
			if got := regFiles(t, oldReg); !slices.Equal(got, before) {
				t.Errorf("the old registration w1 = %q\nwant %q", got, before)
			}
			if _, err := os.Stat(filepath.Join(other, "t.txt")); err != nil {
				t.Errorf("the worktree of the old entry lost its checkout: %v", err)
			}
			if !f.hasRef(t, "ob") {
				t.Error("refs/heads/ob is gone, want it kept")
			}
		})
	}
}

// duringCallSteps makes the stub run steps at each call that match picks, before
// git runs. With exit set, the call then ends with that code and runs no git.
func duringCallSteps(t *testing.T, match, exit string, steps ...[]string) {
	t.Helper()
	joined := make([]string, len(steps))
	for i, s := range steps {
		joined[i] = strings.Join(s, "\x1f")
	}
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE2", strings.Join(joined, "\x1e"))
	stubRule2(t, match, "", 0, exit, "steps", "")
}

// TestWorktreeCreateSpellingCallRollbackKeepsSwappedEntry pins claustrum's own
// guard of the rollback in the state of cell P1 of round tier0-11 (not
// measured). During the failed `rev-parse --show-toplevel` call the stub renames
// the new entry w11 to w11x and the registration w9 of a live sibling worktree
// to w11. The rollback takes the identity of w11 from the listing after the add,
// so the folder now at w11 stays with all its files.
func TestWorktreeCreateSpellingCallRollbackKeepsSwappedEntry(t *testing.T) {
	f, s := postAddFixture(t)
	root := f.realRoot(t)
	made, other := filepath.Join(root, "x", "w1"), filepath.Join(root, "x", "other")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "ob", made)
	runGit(t, f.top, "worktree", "move", made, other)
	live := filepath.Join(root, "live", "w9")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w9", live)
	w9, w11 := filepath.Join(f.regDir, "w9"), filepath.Join(f.regDir, "w11")
	before := regFiles(t, w9)
	afterAddSteps(t, []string{"write", filepath.Join(f.leaf(), ".git"), "gitdir: " + filepath.Join(root, "T", ".git", "worktrees", "w1") + "\n"})
	duringCallSteps(t, "rev-parse,--show-toplevel", "128", []string{"rename", w11, w11 + "x"}, []string{"rename", w9, w11})
	raw, _ := f.create(t, s, "w1", "", 0)
	if !strings.Contains(raw, "git did not report its spelling of the path (entry w1); retry") {
		t.Errorf("reply = %s\nwant the retry frame", raw)
	}
	if got := regFiles(t, w11); !slices.Equal(got, before) {
		t.Errorf("the folder at w11 = %q\nwant the files of the sibling %q", got, before)
	}
	if _, err := os.Stat(filepath.Join(live, "t.txt")); err != nil {
		t.Errorf("the sibling worktree lost its checkout: %v", err)
	}
	if !f.hasRef(t, "w9") {
		t.Error("refs/heads/w9 is gone, want it kept")
	}
}

// TestWorktreeCreateRepointsBeforeConfigCalls pins that the daemon writes the
// .git file of the leaf again before the `git config` calls of the
// `config.worktree` step (newWorktreeConfig), in the state shadow. The stub
// copies the .git file of the leaf at the first `git config` call after the add.
func TestWorktreeCreateRepointsBeforeConfigCalls(t *testing.T) {
	f := newSeededShadowFixture(t)
	dropFixtureWorktree(t, f)
	enableWorktreeConfig(t, f, "[core]\n\tbare = false\n")
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	regs := filepath.Join(f.gitDir, "worktrees")
	named := "gitdir: " + filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "worktrees", "w1") + "\n"
	afterAddSteps(t, []string{"write", filepath.Join(leaf, ".git"), named})
	snap := filepath.Join(t.TempDir(), "git-file")
	duringCallSteps(t, "config", "", []string{"snap", filepath.Join(leaf, ".git"), snap})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("reply = %s\nwant success", raw)
	}
	want := "gitdir: " + filepath.Join(regs, "w1") + "\n"
	if got := mustRead(t, snap); got != want {
		t.Errorf("the .git file of the leaf at the first git config call after the add = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(regs, "w1", worktreeConfigName)); err != nil {
		t.Errorf("the config.worktree step made no file: %v", err)
	}
}
