package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The git-directory trust check (gitdirtrust.go). Every rule was measured side by side
// against f6010b97 on a Linux VM, and on macOS and Windows VMs where the case can
// exist. Each test below drives one rule through the RPC methods over real git
// fixtures.

// The texts, written out here independently of the implementation's constants.
const (
	wantTrustPrefix = "the repository's git directory could not be trusted; git not run: commondir not as git writes it: "
	wantM1Tail      = " exists where git itself never writes one (git keeps that file only in a linked worktree's entry under .git/worktrees/); remove it if you did not create it, and treat its appearance as tampering"
	wantM5          = "config-defined hooks could not be pinned off; git not run: the worktree entry this folder's .git names no longer exists"
)

func wantM1(cd string) string { return wantTrustPrefix + fmt.Sprintf("%q", cd) + wantM1Tail }

func wantM2(cd, repo string) string {
	return wantTrustPrefix + fmt.Sprintf("%q", cd) + " does not name the entry's own repository, " +
		fmt.Sprintf("%q", repo) + "; restore the file to read ../.. or remove that worktree entry and add the worktree again"
}

func wantM3(entry string) string {
	return wantTrustPrefix + "worktree entry " + fmt.Sprintf("%q", entry) +
		" has none (git always writes one there, containing ../..); the entry is damaged or was not made by git — recreate the worktree with git worktree add, or remove the entry"
}

func wantM4(cd, reason string) string {
	return wantTrustPrefix + fmt.Sprintf("%q", cd) + " is not a small plain file (" + reason +
		"); remove it, or remove that worktree entry and add the worktree again so git rewrites it"
}

func wantLockCheck(wt string) string {
	return "failed to remove worktree: could not check whether " + wt +
		" is locked (its registrations could not be examined); retry"
}

// trustRepo is the fixture of the measurement. T is a main repository on branch main.
// TW is its linked worktree (branch lw). SW and D are session worktrees of T. SWW is a
// session worktree made from TW. Paths are resolved, the way the check reports them.
type trustRepo struct {
	base, T, TW, SW, D, SWW string
}

// realTempDir is t.TempDir with symlinks resolved (macOS puts it under /var, a link).
func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newTrustRepo(t *testing.T) trustRepo {
	t.Helper()
	requireGit(t)
	base := realTempDir(t)
	r := trustRepo{
		base: base,
		T:    filepath.Join(base, "T"),
		TW:   filepath.Join(base, "T_wt"),
	}
	r.SW = filepath.Join(r.T, ".claude", "worktrees", "s0")
	r.D = filepath.Join(r.T, ".claude", "worktrees", "d0")
	r.SWW = filepath.Join(r.TW, ".claude", "worktrees", "s1")
	initTrustMain(t, r.T)
	runGit(t, r.T, "worktree", "add", "-q", "-b", "lw", r.TW)
	runGit(t, r.T, "worktree", "add", "-q", "-b", "s0", r.SW)
	runGit(t, r.T, "worktree", "add", "-q", "-b", "d0", r.D)
	runGit(t, r.TW, "worktree", "add", "-q", "-b", "s1", r.SWW)
	return r
}

// initTrustMain makes dir a repository on branch main with one commit.
func initTrustMain(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, ".gitignore"), ".claude/worktrees/\n", 0o644)
	runGit(t, dir, "add", ".gitignore")
	runGit(t, dir, "commit", "-q", "-m", "init")
}

// entry is the linked-worktree entry of TW.
func (r trustRepo) entry() string { return filepath.Join(r.T, ".git", "worktrees", "T_wt") }

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func trustCall(t *testing.T, method string, params map[string]any) rpcReply {
	t.Helper()
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, method, params))
	var r rpcReply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("%s: undecodable reply %s: %v", method, raw, err)
	}
	return r
}

// wantRPCError asserts a -32603 error carrying exactly msg.
func wantRPCError(t *testing.T, what string, r rpcReply, msg string) {
	t.Helper()
	if r.Error == nil || r.Error.Code != -32603 || r.Error.Message != msg {
		t.Errorf("%s = %+v %s\nwant -32603 %q", what, r.Error, r.Result, msg)
	}
}

// wantResult asserts a result whose JSON is exactly want.
func wantResult(t *testing.T, what string, r rpcReply, want string) {
	t.Helper()
	if r.Error != nil || string(r.Result) != want {
		t.Errorf("%s = %+v %s\nwant result %s", what, r.Error, r.Result, want)
	}
}

// wantResultPrefix asserts a result whose JSON starts with prefix.
func wantResultPrefix(t *testing.T, what string, r rpcReply, prefix string) {
	t.Helper()
	if r.Error != nil || !strings.HasPrefix(string(r.Result), prefix) {
		t.Errorf("%s = %+v %s\nwant a result starting %s", what, r.Error, r.Result, prefix)
	}
}

func info(t *testing.T, path string) rpcReply {
	return trustCall(t, "git.info", map[string]any{"path": path})
}

func listBranches(t *testing.T, path string) rpcReply {
	return trustCall(t, "git.list_branches", map[string]any{"path": path})
}

func status(t *testing.T, path, base string) rpcReply {
	return trustCall(t, "git.status", map[string]any{"path": path, "baseRepo": base})
}

func create(t *testing.T, base string) (rpcReply, string) {
	wt := filepath.Join(base, ".claude", "worktrees", "w1")
	return trustCall(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "worktreePath": wt, "branchName": "w1"}), wt
}

func remove(t *testing.T, base, wt, branch string) rpcReply {
	return trustCall(t, "git.worktree_remove", map[string]any{
		"baseRepo": base, "worktreePath": wt, "branchName": branch})
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// branchExists reports whether the main repository at repo holds refs/heads/name. It
// reads the files, so a damaged repository or a GIT_DIR in the environment does not
// change the answer.
func branchExists(t *testing.T, repo, name string) bool {
	t.Helper()
	gitDir := filepath.Join(repo, ".git")
	if exists(filepath.Join(gitDir, "refs", "heads", name)) {
		return true
	}
	packed, _ := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	return strings.Contains(string(packed), " refs/heads/"+name+"\n")
}

// wantCreateRefused asserts create answered worktree_add_failed with msg and created
// nothing: no worktree directory, no entry, no branch.
func wantCreateRefused(t *testing.T, base, repoForBranch string, msg string) {
	t.Helper()
	r, wt := create(t, base)
	want, _ := json.Marshal(worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	wantResult(t, "create", r, string(want))
	if exists(wt) {
		t.Errorf("create refused but left %s behind", wt)
	}
	if branchExists(t, repoForBranch, "w1") {
		t.Errorf("create refused but made branch w1")
	}
}

// wantRemoveRefused asserts remove answered the lock-check refusal and deleted nothing.
func wantRemoveRefused(t *testing.T, base, wt, branch, repoForBranch string) {
	t.Helper()
	checkRemoveRefused(t, remove(t, base, wt, branch), wt, branch, repoForBranch)
}

// checkRemoveRefused asserts that the remove reply r is the lock-check refusal and that
// nothing was deleted.
func checkRemoveRefused(t *testing.T, r rpcReply, wt, branch, repoForBranch string) {
	t.Helper()
	want, _ := json.Marshal(worktreeRemoveResult{Success: false, Error: wantLockCheck(wt)})
	wantResult(t, "remove", r, string(want))
	if !exists(wt) {
		t.Errorf("remove refused but deleted %s", wt)
	}
	if !branchExists(t, repoForBranch, branch) {
		t.Errorf("remove refused but deleted branch %s", branch)
	}
}

const (
	notRepoInfo   = `{"isRepo":false,"repoSlug":"","defaultBranch":""}`
	notRepoList   = `{"isRepo":false,"branches":[]}`
	notRepoStatus = `{"isRepo":false,"clean":false}`
	notRepoCreate = `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}`
)

// The refusal shape of each method: a commondir in a main
// repository's git directory is refused with M1 on every method that runs in that
// repository. For git.status the check is on baseRepo, not on path. A linked worktree
// of the same repository still works for info and list_branches, because its own git
// directory is an honest entry. Remove refuses and deletes nothing. The honest control
// shows the fixture passes when the stray file is absent.
func TestGitDirTrustStrayCommondirInMainRepo(t *testing.T) {
	r := newTrustRepo(t)
	wantResultPrefix(t, "control info(T)", info(t, r.T), `{"isRepo":true`)

	cd := filepath.Join(r.T, ".git", "commondir")
	writeFile(t, cd, ".\n", 0o644)
	m1 := wantM1(cd)

	wantRPCError(t, "info(T)", info(t, r.T), m1)
	wantRPCError(t, "list_branches(T)", listBranches(t, r.T), m1)
	wantRPCError(t, "status(SW,T)", status(t, r.SW, r.T), m1)
	wantRPCError(t, "status(TW,T)", status(t, r.TW, r.T), m1)
	wantCreateRefused(t, r.T, r.T, m1)
	wantRemoveRefused(t, r.T, r.SW, "s0", r.T)

	// TW's git directory is its entry, which is honest: info and list_branches there
	// still answer.
	wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true`)
	wantResultPrefix(t, "list_branches(TW)", listBranches(t, r.TW), `{"isRepo":true`)
}

// A commondir of ANY type is refused, and so is one that names the git
// directory itself.
func TestGitDirTrustStrayCommondirAnyType(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, cd, gitDir string)
	}{
		{"directory", func(t *testing.T, cd, _ string) {
			if err := os.Mkdir(cd, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"names itself", func(t *testing.T, cd, gitDir string) { writeFile(t, cd, gitDir+"\n", 0o644) }},
		{"empty", func(t *testing.T, cd, _ string) { writeFile(t, cd, "", 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			gitDir := filepath.Join(r.T, ".git")
			cd := filepath.Join(gitDir, "commondir")
			tc.make(t, cd, gitDir)
			wantRPCError(t, "info(T)", info(t, r.T), wantM1(cd))
		})
	}
}

// An entry's commondir must name its own repository once trimmed, taken
// relative to the entry and cleaned. Every accepted spelling answers. Every other value
// is refused with M2 on the methods that run in the worktree, and remove from that
// worktree deletes nothing.
func TestGitDirTrustEntryCommondirMustNameItsRepository(t *testing.T) {
	pass := map[string]func(r trustRepo) string{
		"../.. with newline":   func(trustRepo) string { return "../..\n" },
		"trailing slash":       func(trustRepo) string { return "../../\n" },
		"CRLF":                 func(trustRepo) string { return "../..\r\n" },
		"no newline":           func(trustRepo) string { return "../.." },
		"absolute own":         func(r trustRepo) string { return filepath.Join(r.T, ".git") + "\n" },
		"absolute own dotted":  func(r trustRepo) string { return r.T + "/.git/worktrees/..\n" },
		"trailing white space": func(trustRepo) string { return "../..  \t\n" },
	}
	for name, content := range pass {
		t.Run("accepts "+name, func(t *testing.T) {
			r := newTrustRepo(t)
			writeFile(t, filepath.Join(r.entry(), "commondir"), content(r), 0o644)
			wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true`)
		})
	}

	refuse := map[string]func(t *testing.T, r trustRepo) string{
		"another repository, absolute": func(t *testing.T, r trustRepo) string {
			x := filepath.Join(r.base, "X")
			initTrustMain(t, x)
			return filepath.Join(x, ".git") + "\n"
		},
		"another repository, relative": func(t *testing.T, r trustRepo) string {
			initTrustMain(t, filepath.Join(r.base, "X"))
			return "../../../../X/.git\n"
		},
		"a missing path":    func(*testing.T, trustRepo) string { return "/nonexistent/.git\n" },
		"empty":             func(*testing.T, trustRepo) string { return "" },
		"a NUL byte":        func(*testing.T, trustRepo) string { return "../..\x00junk\n" },
		"missing, relative": func(*testing.T, trustRepo) string { return "../../nope\n" },
	}
	for name, content := range refuse {
		t.Run("refuses "+name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.entry(), "commondir")
			writeFile(t, cd, content(t, r), 0o644)
			m2 := wantM2(cd, filepath.Join(r.T, ".git"))
			wantRPCError(t, "info(TW)", info(t, r.TW), m2)
			wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), m2)
			wantCreateRefused(t, r.TW, r.T, m2)
			wantRemoveRefused(t, r.TW, r.SWW, "s1", r.T)
		})
	}
}

// Remove checks baseRepo only. Damage to the entry of the worktree being
// removed does not stop the removal, which deletes the worktree and its entry.
func TestGitDirTrustRemoveIgnoresTheRemovedWorktreesEntry(t *testing.T) {
	r := newTrustRepo(t)
	dEntry := filepath.Join(r.T, ".git", "worktrees", "d0")
	writeFile(t, filepath.Join(dEntry, "commondir"), "/nonexistent\n", 0o644)
	wantResult(t, "remove(T,D)", remove(t, r.T, r.D, "d0"), `{"success":true}`)
	if exists(r.D) || exists(dEntry) {
		t.Errorf("remove(T,D) left the worktree (%v) or its entry (%v)", exists(r.D), exists(dEntry))
	}
}

// An entry without commondir is refused with M3.
func TestGitDirTrustEntryWithoutCommondir(t *testing.T) {
	r := newTrustRepo(t)
	if err := os.Remove(filepath.Join(r.entry(), "commondir")); err != nil {
		t.Fatal(err)
	}
	m3 := wantM3(r.entry())
	wantRPCError(t, "info(TW)", info(t, r.TW), m3)
	wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), m3)
	wantCreateRefused(t, r.TW, r.T, m3)
	wantRemoveRefused(t, r.TW, r.SWW, "s1", r.T)
}

// An entry directory that is gone, found through the worktree's `.git` file,
// means no repository on every method. Git itself fails there too, so the old answer
// was the hooks refusal carrying git's error.
func TestGitDirTrustEntryGoneMeansNoRepository(t *testing.T) {
	r := newTrustRepo(t)
	if err := os.RemoveAll(r.entry()); err != nil {
		t.Fatal(err)
	}
	wantResult(t, "info(TW)", info(t, r.TW), notRepoInfo)
	wantResult(t, "list_branches(TW)", listBranches(t, r.TW), notRepoList)
	r1, wt := create(t, r.TW)
	wantResult(t, "create(TW)", r1, notRepoCreate)
	if exists(wt) {
		t.Errorf("create(TW) left %s", wt)
	}
	wantRemoveRefused(t, r.TW, r.SWW, "s1", r.T)
}

// commondir must be a regular file of at most 1048576 bytes. Exactly 1 MiB
// passes, one byte more is refused with M4. A directory is refused with M4.
func TestGitDirTrustCommondirMustBeASmallPlainFile(t *testing.T) {
	t.Run("exactly 1 MiB passes", func(t *testing.T) {
		r := newTrustRepo(t)
		body := "../.." + strings.Repeat(" ", 1<<20-len("../..")-1) + "\n"
		writeFile(t, filepath.Join(r.entry(), "commondir"), body, 0o644)
		wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true`)
	})
	t.Run("one byte more is refused", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.entry(), "commondir")
		body := "../.." + strings.Repeat(" ", 1<<20-len("../..")) + "\n"
		writeFile(t, cd, body, 0o644)
		m4 := wantM4(cd, "commondir is larger than 1048576 bytes")
		wantRPCError(t, "info(TW)", info(t, r.TW), m4)
		wantCreateRefused(t, r.TW, r.T, m4)
		wantRemoveRefused(t, r.TW, r.SWW, "s1", r.T)
	})
	t.Run("a directory is refused", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.entry(), "commondir")
		if err := os.Remove(cd); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(cd, 0o755); err != nil {
			t.Fatal(err)
		}
		wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), wantM4(cd, "commondir is not a regular file"))
	})
}

// A `.git` directory that fails the git-directory test ends the walk.
// With no commondir inside, that is no repository, where git itself walks on to the
// outer repository. With a commondir inside, it is refused with M1.
func TestGitDirTrustBrokenNestedDotGitStopsTheWalk(t *testing.T) {
	t.Run("empty .git directory", func(t *testing.T) {
		r := newTrustRepo(t)
		a := filepath.Join(r.T, "a")
		if err := os.MkdirAll(filepath.Join(a, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		sa := filepath.Join(a, ".claude", "worktrees", "sa")
		runGit(t, r.T, "worktree", "add", "-q", "-b", "sa", sa)
		wantResult(t, "info(T/a)", info(t, a), notRepoInfo)
		wantResult(t, "list_branches(T/a)", listBranches(t, a), notRepoList)
		wantResult(t, "status(SA,T/a)", status(t, sa, a), notRepoStatus)
		r1, wt := create(t, a)
		wantResult(t, "create(T/a)", r1, notRepoCreate)
		if exists(wt) {
			t.Errorf("create(T/a) left %s", wt)
		}
		wantRemoveRefused(t, a, sa, "sa", r.T)
	})
	t.Run(".git directory holding only commondir", func(t *testing.T) {
		r := newTrustRepo(t)
		a := filepath.Join(r.T, "a")
		cd := filepath.Join(a, ".git", "commondir")
		writeFile(t, cd, "../../.git\n", 0o644)
		wantRPCError(t, "info(T/a)", info(t, a), wantM1(cd))
		wantRPCError(t, "status(SW,T/a)", status(t, r.SW, a), wantM1(cd))
	})
}

// The HEAD test, seen through a nested `.git` directory: a nested `.git` directory holding objects and refs is
// judged on its HEAD. A passing HEAD makes it the repository (it has no branches). A
// failing HEAD means no repository. Git itself walks on to T, which has branches.
func TestGitDirTrustHeadTest(t *testing.T) {
	for _, tc := range []struct {
		name string
		head string
		pass bool
	}{
		{"ref with space", "ref: refs/heads/main\n", true},
		{"ref with tabs", "ref:\t\trefs/heads/main\n", true},
		{"ref with no space", "ref:refs/heads/main\n", true},
		{"ref, newline, refs", "ref:\nrefs/heads/main\n", true},
		{"ref CRLF", "ref: refs/heads/main\r\n", true},
		{"246 spaces", "ref:" + strings.Repeat(" ", 246) + "refs/heads/main\n", true},
		{"247 spaces", "ref:" + strings.Repeat(" ", 247) + "refs/heads/main\n", false},
		{"250 spaces", "ref:" + strings.Repeat(" ", 250) + "refs/heads/main\n", false},
		{"ref then junk", "ref: refs/heads/main" + strings.Repeat("x", 300) + "\n", true},
		{"40 hex lower", strings.Repeat("a", 40) + "\n", true},
		{"40 hex upper", strings.Repeat("A", 40) + "\n", true},
		{"40 hex then junk", strings.Repeat("a", 40) + strings.Repeat("z", 300), true},
		{"39 hex", strings.Repeat("a", 39) + "\n", false},
		{"non-hex 40th", strings.Repeat("a", 39) + "g\n", false},
		{"ref to something else", "ref: something-else\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			g := filepath.Join(r.T, "a", ".git")
			for _, d := range []string{"objects", "refs"} {
				if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(g, "HEAD"), tc.head, 0o644)
			want := notRepoList
			if tc.pass {
				want = `{"isRepo":true,"branches":[]}`
			}
			wantResult(t, "list_branches(T/a)", listBranches(t, filepath.Join(r.T, "a")), want)
		})
	}
	t.Run("objects and refs must exist, of any type", func(t *testing.T) {
		for _, tc := range []struct {
			name, missing string
			asFile        bool
			pass          bool
		}{
			{"objects missing", "objects", false, false},
			{"refs missing", "refs", false, false},
			{"objects a file", "objects", true, true},
			{"refs a file", "refs", true, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := newTrustRepo(t)
				g := filepath.Join(r.T, "a", ".git")
				for _, d := range []string{"objects", "refs"} {
					if d == tc.missing {
						if tc.asFile {
							writeFile(t, filepath.Join(g, d), "x", 0o644)
						}
						continue
					}
					if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				writeFile(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n", 0o644)
				rep := listBranches(t, filepath.Join(r.T, "a"))
				if tc.pass {
					// Passing the test lets git run, pinned to a/.git, and the answer is
					// git's own. Git on Linux and macOS rejects a `.git` whose objects or
					// refs is a file, and the pin keeps it from walking on to T (NH18).
					// Git for Windows accepts that layout, so there the answer is the
					// nested repository, which has no branches. On Windows this differs
					// from the "no repository" a type check gives. On Linux and macOS the
					// D06 case in TestGitDirTrustEntryNeedsAValidRepository tells them apart.
					want := notRepoList
					if runtime.GOOS == "windows" {
						want = `{"isRepo":true,"branches":[]}`
					}
					wantResultPrefix(t, "list_branches(T/a)", rep, want)
					return
				}
				wantResult(t, "list_branches(T/a)", rep, notRepoList)
			})
		}
	})
	t.Run("HEAD a directory", func(t *testing.T) {
		r := newTrustRepo(t)
		g := filepath.Join(r.T, "a", ".git")
		for _, d := range []string{"objects", "refs", "HEAD"} {
			if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		wantResult(t, "list_branches(T/a)", listBranches(t, filepath.Join(r.T, "a")), notRepoList)
	})
}

// A linked-worktree entry counts as an entry only
// when its grandparent passes the git-directory test and its own name is not ".git".
// Otherwise its honest commondir counts as stray: M1. A regular file named objects
// still passes the test, so the entry is judged as an entry and git answers for itself.
func TestGitDirTrustEntryNeedsAValidRepository(t *testing.T) {
	t.Run("main objects missing", func(t *testing.T) {
		r := newTrustRepo(t)
		obj := filepath.Join(r.T, ".git", "objects")
		if err := os.Rename(obj, obj+".gone"); err != nil {
			t.Fatal(err)
		}
		m1 := wantM1(filepath.Join(r.entry(), "commondir"))
		wantRPCError(t, "info(TW)", info(t, r.TW), m1)
		wantCreateRefused(t, r.TW, r.T, m1)
		// T itself now fails the test: no repository, so remove from T is refused.
		wantRemoveRefused(t, r.T, r.D, "d0", r.T)
	})
	t.Run("main objects a regular file", func(t *testing.T) {
		r := newTrustRepo(t)
		obj := filepath.Join(r.T, ".git", "objects")
		if err := os.Rename(obj, obj+".gone"); err != nil {
			t.Fatal(err)
		}
		writeFile(t, obj, "x", 0o644)
		rep := info(t, r.TW)
		if rep.Error != nil && strings.HasPrefix(rep.Error.Message, wantTrustPrefix) {
			t.Errorf("info(TW) = %s; a file named objects passes the test, so the entry must not be refused", rep.Error.Message)
		}
	})
	t.Run("entry named .git", func(t *testing.T) {
		r := newTrustRepo(t)
		dotEntry := filepath.Join(r.T, ".git", "worktrees", ".git")
		if err := os.Rename(r.entry(), dotEntry); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(r.TW, ".git"), "gitdir: "+dotEntry+"\n", 0o644)
		wantRPCError(t, "info(TW)", info(t, r.TW), wantM1(filepath.Join(dotEntry, "commondir")))
	})
}

// A `.git` file must start with the exact bytes "gitdir:" and be at most 1 MiB.
// Otherwise there is no repository, where git passed its own error through before. A
// relative target is taken relative to the directory that holds the `.git` file.
func TestGitDirTrustGitFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(r trustRepo) string
		pass    bool
	}{
		{"no prefix", func(r trustRepo) string { return r.entry() + "\n" }, false},
		{"upper-case prefix", func(r trustRepo) string { return "GITDIR: " + r.entry() + "\n" }, false},
		{"over 1 MiB", func(r trustRepo) string {
			return "gitdir: " + r.entry() + strings.Repeat(" ", 1<<20) + "\n"
		}, false},
		{"relative", func(trustRepo) string { return "gitdir: ../T/.git/worktrees/T_wt\n" }, true},
		{"points at the main git directory", func(r trustRepo) string {
			return "gitdir: " + filepath.Join(r.T, ".git") + "\n"
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			writeFile(t, filepath.Join(r.TW, ".git"), tc.content(r), 0o644)
			rep := info(t, r.TW)
			if tc.pass {
				wantResultPrefix(t, "info(TW)", rep, `{"isRepo":true`)
				return
			}
			wantResult(t, "info(TW)", rep, notRepoInfo)
			wantResult(t, "list_branches(TW)", listBranches(t, r.TW), notRepoList)
			r1, _ := create(t, r.TW)
			wantResult(t, "create(TW)", r1, notRepoCreate)
		})
	}
}

// A bare repository: with no `.git`, the directory itself is
// the git directory. A stray commondir there is refused.
func TestGitDirTrustBareRepository(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	b := filepath.Join(base, "B.git")
	runGit(t, base, "init", "-q", "--bare", "-b", "main", b)
	wantResult(t, "control list_branches(B)", listBranches(t, b), `{"isRepo":true,"branches":[]}`)
	cd := filepath.Join(b, "commondir")
	writeFile(t, cd, ".\n", 0o644)
	wantRPCError(t, "list_branches(B)", listBranches(t, b), wantM1(cd))
	wantRPCError(t, "info(B)", info(t, b), wantM1(cd))
}

// A submodule-style git directory: a `.git` file names a git directory
// under .git/modules/. With no commondir it is trusted. With one it is refused.
func TestGitDirTrustSubmoduleGitDir(t *testing.T) {
	r := newTrustRepo(t)
	sub := filepath.Join(r.T, "sub")
	modGit := filepath.Join(r.T, ".git", "modules", "sub")
	for _, d := range []string{sub, filepath.Dir(modGit)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, r.base, "init", "-q", "-b", "main", "--separate-git-dir", modGit, sub)
	wantResultPrefix(t, "control info(sub)", info(t, sub), `{"isRepo":true`)
	cd := filepath.Join(modGit, "commondir")
	writeFile(t, cd, ".\n", 0o644)
	wantRPCError(t, "info(sub)", info(t, sub), wantM1(cd))
	wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true`)
}

// A trusted entry runs git with the common directory pinned to the repository.
// The check accepts trailing and leading white space in commondir, although git itself
// rejects the file. With the pin, git reads the repository, and remove from that
// worktree succeeds. Without the pin, git fails and the method answers with the hooks
// refusal.
func TestGitDirTrustPinsTheCommonDirectory(t *testing.T) {
	for _, content := range []string{"../..  \t\n", "  \t../..\n"} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			r := newTrustRepo(t)
			writeFile(t, filepath.Join(r.entry(), "commondir"), content, 0o644)
			rep := info(t, r.TW)
			wantResultPrefix(t, "info(TW)", rep, `{"isRepo":true,"repo":"T_wt"`)
			// What git answers past the pin depends on its version. Whatever it is, a
			// failing git never becomes the branch name, and git's tab never reaches
			// the create error (both references write a space there).
			if strings.Contains(string(rep.Result), `"branch":"fatal`) {
				t.Errorf("info(TW) = %s, carries git's error as the branch", rep.Result)
			}
			if c, _ := create(t, r.TW); strings.ContainsAny(string(c.Result), "\t") || strings.Contains(string(c.Result), `\t`) {
				t.Errorf("create(TW) = %s, keeps a tab in the error text", c.Result)
			}
			wantResult(t, "remove(TW,SWW)", remove(t, r.TW, r.SWW, "s1"), `{"success":true}`)
			if exists(r.SWW) {
				t.Errorf("remove(TW,SWW) left the worktree")
			}
			// The entry lives under the main repository's git directory, not under
			// TW's `.git` (a file). The prune finds it there.
			if e := filepath.Join(r.T, ".git", "worktrees", "s1"); exists(e) {
				t.Errorf("remove(TW,SWW) left the entry %s", e)
			}
		})
	}
}

// A GIT_DIR in the daemon's environment is the git directory judged for every
// request path, even a plain directory. A gone entry named there is refused with M5.
// A relative GIT_DIR is taken per request directory, so a linked worktree (whose `.git`
// is a file) answers no repository. A GIT_DIR naming nothing else is left to git.
func TestGitDirTrustDaemonGitDir(t *testing.T) {
	t.Run("stray commondir in the named repository", func(t *testing.T) {
		r := newTrustRepo(t)
		x := filepath.Join(r.base, "X")
		initTrustMain(t, x)
		cd := filepath.Join(x, ".git", "commondir")
		writeFile(t, cd, ".\n", 0o644)
		plain := filepath.Join(r.base, "N")
		if err := os.Mkdir(plain, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", filepath.Join(x, ".git"))
		m1 := wantM1(cd)
		wantRPCError(t, "info(N)", info(t, plain), m1)
		wantRPCError(t, "list_branches(T)", listBranches(t, r.T), m1)
		wantRPCError(t, "status(SW,T)", status(t, r.SW, r.T), m1)
		wantCreateRefused(t, r.T, r.T, m1)
		wantRemoveRefused(t, r.T, r.SW, "s0", r.T)
	})
	t.Run("gone entry", func(t *testing.T) {
		r := newTrustRepo(t)
		t.Setenv("GIT_DIR", filepath.Join(r.T, ".git", "worktrees", "ghost"))
		wantRPCError(t, "info(T)", info(t, r.T), wantM5)
		wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), wantM5)
		wantRPCError(t, "status(SW,T)", status(t, r.SW, r.T), wantM5)
		wantCreateRefused(t, r.T, r.T, wantM5)
		wantRemoveRefused(t, r.T, r.SW, "s0", r.T)
	})
	t.Run("gone, not an entry", func(t *testing.T) {
		r := newTrustRepo(t)
		nope := filepath.Join(r.base, "nope.git")
		t.Setenv("GIT_DIR", nope)
		wantResult(t, "info(T)", info(t, r.T), notRepoInfo)
		wantResult(t, "list_branches(T)", listBranches(t, r.T), notRepoList)
		// git still runs with GIT_COMMON_DIR pinned to that GIT_DIR, as f6010b97 pins
		// it on Linux and Windows VMs (row E05).
		if got, want := commonDirPinEnv(r.T), []string{"GIT_COMMON_DIR=" + nope}; !slices.Equal(got, want) {
			t.Errorf("pin = %q, want %q", got, want)
		}
	})
	t.Run("relative, names nothing", func(t *testing.T) {
		// A relative GIT_DIR that names nothing in the request directory is pinned
		// joined to that directory, as f6010b97 pins it on Linux and Windows VMs
		// (row E02, directory N).
		r := newTrustRepo(t)
		plain := filepath.Join(r.base, "N")
		if err := os.Mkdir(plain, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", ".git")
		wantResult(t, "info(N)", info(t, plain), notRepoInfo)
		got := commonDirPinEnv(plain)
		want := "GIT_COMMON_DIR=" + filepath.Join(canonicalPath(plain), ".git")
		if len(got) != 1 || canonicalPath(strings.TrimPrefix(got[0], "GIT_COMMON_DIR=")) != canonicalPath(strings.TrimPrefix(want, "GIT_COMMON_DIR=")) {
			t.Errorf("pin = %q, want %q", got, want)
		}
	})
	t.Run("relative .git", func(t *testing.T) {
		r := newTrustRepo(t)
		t.Setenv("GIT_DIR", ".git")
		wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true`)
		if nonDirGitDirIsNoRepo {
			wantResult(t, "info(TW)", info(t, r.TW), notRepoInfo)
			wantResult(t, "list_branches(TW)", listBranches(t, r.TW), notRepoList)
		}
	})
}

// A GIT_COMMON_DIR in the daemon's environment turns the check off for
// git.info and git.list_branches only. git.status and git.worktree_remove still refuse,
// and git.worktree_create refuses with the text wrapped as a failed add.
func TestGitDirTrustDaemonCommonDir(t *testing.T) {
	r := newTrustRepo(t)
	gitDir := filepath.Join(r.T, ".git")
	cd := filepath.Join(gitDir, "commondir")
	writeFile(t, cd, ".\n", 0o644)
	t.Setenv("GIT_COMMON_DIR", gitDir)
	m1 := wantM1(cd)
	wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true`)
	wantResultPrefix(t, "list_branches(T)", listBranches(t, r.T), `{"isRepo":true`)
	wantRPCError(t, "status(SW,T)", status(t, r.SW, r.T), m1)
	wantCreateRefused(t, r.T, r.T, "git worktree add failed: cannot locate the repository's git directory: "+m1)
	wantRemoveRefused(t, r.T, r.SW, "s0", r.T)
}

// GIT_CONFIG_PARAMETERS and GIT_CEILING_DIRECTORIES in the daemon's environment
// do not move the check. Parity controls: every build answers these the same way.
func TestGitDirTrustOtherGitEnvironment(t *testing.T) {
	r := newTrustRepo(t)
	t.Run("GIT_CONFIG_PARAMETERS core.bare", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_PARAMETERS", "'core.bare'='true'")
		wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true`)
	})
	t.Run("GIT_CEILING_DIRECTORIES", func(t *testing.T) {
		sub := filepath.Join(r.T, "a", "b")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", r.T)
		wantResult(t, "info(T/a/b)", info(t, sub), notRepoInfo)
		wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true`)
	})
}

// The operand rules of every refusal text: each path is cut to 300 runes, then quoted
// with Go %q. The rest of the message is never cut. A non-ASCII path is cut by runes,
// not bytes.
func TestGitDirTrustOperandIsCutTo300Runes(t *testing.T) {
	for _, tc := range []struct{ name, seg string }{
		{"ascii", "a"},
		{"non-ascii", "é"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows cannot start git in a directory over 260 characters")
			}
			requireGit(t)
			base := realTempDir(t)
			deep := filepath.Join(base, strings.Repeat(tc.seg, 100), strings.Repeat(tc.seg, 100), strings.Repeat(tc.seg, 100))
			T := filepath.Join(deep, "T")
			initTrustMain(t, T)
			cd := filepath.Join(T, ".git", "commondir")
			writeFile(t, cd, ".\n", 0o644)
			if utf8.RuneCountInString(cd) <= 300 {
				t.Fatalf("fixture path %d runes, want > 300", utf8.RuneCountInString(cd))
			}
			cut := string([]rune(cd)[:300])
			wantRPCError(t, "info(T)", info(t, T), wantTrustPrefix+fmt.Sprintf("%q", cut)+wantM1Tail)
		})
	}
}

// quoteOperand keeps an invalid UTF-8 byte as a byte (printed \xNN) and counts it as
// one rune.
func TestQuoteOperandInvalidUTF8(t *testing.T) {
	s := "bad\xffname" + strings.Repeat("z", 400)
	got := quoteOperand(s)
	if !strings.HasPrefix(got, `"bad\xffname`) {
		t.Errorf("quoteOperand kept %s, want the byte printed as \\xff", got[:20])
	}
	want := fmt.Sprintf("%q", "bad\xffname"+strings.Repeat("z", 300-len("badxname")))
	if got != want {
		t.Errorf("quoteOperand cut at the wrong place: got %d bytes, want %d", len(got), len(want))
	}
}

// worktree_create runs git with replace objects and grafts off, so its
// checkout carries the real blob. git.status still honours replace objects.
func TestGitDirTrustCreateIgnoresReplaceObjects(t *testing.T) {
	r := newTrustRepo(t)
	writeFile(t, filepath.Join(r.T, "v.txt"), "real\n", 0o644)
	runGit(t, r.T, "add", "v.txt")
	runGit(t, r.T, "commit", "-q", "-m", "v")
	real := gitOut(t, r.T, "rev-parse", "HEAD:v.txt")
	fakeFile := filepath.Join(r.base, "fake.txt")
	writeFile(t, fakeFile, "fake\n", 0o644)
	fake := gitOut(t, r.T, "hash-object", "-w", fakeFile)
	runGit(t, r.T, "replace", real, fake)

	rep, wt := create(t, r.T)
	wantResultPrefix(t, "create(T)", rep, `{"success":true`)
	b, err := os.ReadFile(filepath.Join(wt, "v.txt"))
	// Git for Windows checks out with CRLF (core.autocrlf), so compare line endings
	// normalised: the content, real or fake, is what the rule is about.
	if err != nil || strings.ReplaceAll(string(b), "\r\n", "\n") != "real\n" {
		t.Errorf("create checked out v.txt = %q (%v), want the real blob", b, err)
	}

	// Status honours the replacement: s0 is at the commit before v.txt existed, so
	// move it to the new commit, which checks out the real content. Then replace that
	// commit with one whose v.txt is fake: the staged blob now differs from HEAD's.
	runGit(t, r.T, "replace", "-d", real)
	runGit(t, r.SW, "reset", "-q", "--hard", "main")
	head := gitOut(t, r.SW, "rev-parse", "HEAD")
	runGit(t, r.T, "replace", head, commitWithFile(t, r, head, "fake\n"))
	wantResult(t, "status(SW,T)", status(t, r.SW, r.T), `{"isRepo":true,"clean":false,"changes":["M  v.txt"]}`)
}

// git.info omits `branch` when git cannot name it, instead of sending git's error text.
// Measured side by side against 90fca6e6 and f6010b97 on a Linux VM (H15, H18, NH21,
// NH22). A detached HEAD git can resolve still answers "detached:<sha>".
func TestGitInfoOmitsABranchGitCannotResolve(t *testing.T) {
	junk := func(c string) string { return strings.Repeat(c, 300) }
	for _, tc := range []struct {
		name   string
		nested bool
		head   func(t *testing.T, r trustRepo) string
	}{
		{"ref then junk", false, func(*testing.T, trustRepo) string { return "ref: refs/heads/main\n" + junk("j") }},
		{"sha then junk", false, func(t *testing.T, r trustRepo) string { return gitOut(t, r.T, "rev-parse", "HEAD") + junk("z") }},
		{"nested hex then junk", true, func(*testing.T, trustRepo) string { return strings.Repeat("a", 40) + junk("z") }},
		{"nested ref then junk", true, func(*testing.T, trustRepo) string { return "ref: refs/heads/main\n" + junk("j") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			dir, gitDir, name := r.T, filepath.Join(r.T, ".git"), "T"
			if tc.nested {
				dir, name = filepath.Join(r.T, "a"), "a"
				gitDir = filepath.Join(dir, ".git")
				for _, d := range []string{"objects", "refs"} {
					if err := os.MkdirAll(filepath.Join(gitDir, d), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			writeFile(t, filepath.Join(gitDir, "HEAD"), tc.head(t, r), 0o644)
			// git reports root with forward slashes, also on Windows.
			root, _ := json.Marshal(filepath.ToSlash(dir))
			wantResult(t, "info", info(t, dir),
				`{"isRepo":true,"repo":"`+name+`","root":`+string(root)+`,"repoSlug":"","defaultBranch":""}`)
		})
	}
	t.Run("detached control", func(t *testing.T) {
		r := newTrustRepo(t)
		sha := gitOut(t, r.T, "rev-parse", "HEAD")
		writeFile(t, filepath.Join(r.T, ".git", "HEAD"), sha+"\n", 0o644)
		wantResultPrefix(t, "info", info(t, r.T), `{"isRepo":true,"repo":"T","branch":"detached:`+sha[:7]+`"`)
	})
}

// The light profile carries the replace and graft switches. The heavy one, used only
// by git.status, does not.
func TestHardenedEnvReplaceAndGraftSwitches(t *testing.T) {
	light := hardenedGitEnv(false, nil)
	heavy := hardenedGitEnv(true, nil)
	for _, kv := range []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull} {
		if !slices.Contains(light, kv) {
			t.Errorf("light env lacks %s", kv)
		}
		if slices.Contains(heavy, kv) {
			t.Errorf("heavy env carries %s; git.status must keep honouring replace objects", kv)
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// commitWithFile makes a dangling commit whose tree is base's tree with v.txt set to
// content.
func commitWithFile(t *testing.T, r trustRepo, base, content string) string {
	t.Helper()
	f := filepath.Join(r.base, "c.txt")
	writeFile(t, f, content, 0o644)
	blob := gitOut(t, r.T, "hash-object", "-w", f)
	idx := "GIT_INDEX_FILE=" + filepath.Join(r.base, "tmpindex")
	var tree []byte
	for _, args := range [][]string{
		{"read-tree", base},
		{"update-index", "--add", "--cacheinfo", "100644," + blob + ",v.txt"},
		{"write-tree"},
	} {
		cmd := exec.Command("git", append([]string{"-C", r.T}, args...)...)
		cmd.Env = append(os.Environ(), idx)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		tree = out
	}
	return gitOut(t, r.T, "commit-tree", strings.TrimSpace(string(tree)), "-m", "fake")
}
