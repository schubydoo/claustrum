//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Which registration git.worktree_remove deletes, and its git calls, as on 89cb6289.
// The rows are those of the remove probe on Linux and macOS VMs. This file is unix
// only, because it compares working directories as text.

// regFixture is the base fixture of the probe: a repository T with a worktree S at
// T/.claude/worktrees/s0 on branch s0, made by plain git, and a second repository X.
type regFixture struct {
	root, T, S, X string
}

func newRegFixture(t *testing.T) regFixture {
	t.Helper()
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	f := regFixture{root: root, T: filepath.Join(root, "T"), X: filepath.Join(root, "X")}
	f.S = filepath.Join(f.T, ".claude", "worktrees", "s0")
	for _, r := range []struct{ dir, branch, msg string }{{f.T, "main", "t0"}, {f.X, "xonly", "x0"}} {
		runGit(t, root, "init", "-q", "-b", r.branch, r.dir)
		writeFile(t, filepath.Join(r.dir, r.msg+".txt"), r.msg+"\n", 0o644)
		runGit(t, r.dir, "add", ".")
		runGit(t, r.dir, "commit", "-q", "-m", r.msg)
	}
	runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", f.S)
	return f
}

func (f regFixture) entry(repo, name string) string {
	return filepath.Join(repo, ".git", "worktrees", name)
}

// gitDirOf is <repo>/.git.
func gitDirOf(repo string) string { return filepath.Join(repo, ".git") }

// hasBranch reports whether repo has the branch. It reads the ref file, so the
// daemon environment of the test does not change the answer.
func hasBranch(repo, branch string) bool {
	return exists(filepath.Join(repo, ".git", "refs", "heads", branch))
}

// removeCallLog puts the logging git stand-in on PATH and returns a function that
// sends one request and gives its frame and its git calls. Each call is one line:
//
//	<cwd>|<H, R, L or ->|<the argv without the -c pairs>
//
// H is the heavy environment, R that of the branch step, L the light one. <fx> is
// root. The daemon environment of a row is set with t.Setenv after this call, so the
// test restores it.
func removeCallLog(t *testing.T, root string) func(method string, params map[string]any) (string, []string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	empty := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	for _, k := range daemonGitKeys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	ctxlog := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
	s := newTestServer(t)
	return func(method string, params map[string]any) (string, []string) {
		t.Helper()
		if err := os.WriteFile(ctxlog, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		raw := dispatchRaw(t, s, rpcLine(t, method, params))
		if fi, err := os.Stat(ctxlog); err != nil || fi.Size() == 0 {
			return raw, nil
		}
		var lines []string
		for _, c := range readShapeCalls(t, ctxlog) {
			var argv []string
			for i := 0; i < len(c.argv); i++ {
				if c.argv[i] == "-c" {
					i++
					continue
				}
				argv = append(argv, c.argv[i])
			}
			env := "-"
			switch {
			case slices.Contains(c.env, "GIT_ALLOW_PROTOCOL=https:ssh"):
				env = "L"
			case slices.Contains(c.env, "GIT_GRAFT_FILE="+os.DevNull):
				env = "R"
			case slices.Contains(c.env, "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh"):
				env = "H"
			}
			cwd := c.cwd
			if slices.Contains(c.argv, "core.excludesFile") {
				cwd = "<temp>"
			}
			lines = append(lines, strings.ReplaceAll(cwd+"|"+env+"|"+strings.Join(argv, " "), root, "<fx>"))
		}
		return raw, lines
	}
}

const callExcludes = "<temp>|-|config --includes --path core.excludesFile"

// callsCheck is the heavy listing and `rev-parse --absolute-git-dir` in cwd.
func callsCheck(cwd string) []string {
	return []string{cwd + "|H|config -z --list", cwd + "|H|rev-parse --absolute-git-dir"}
}

// callsPair is the pair of registrationProbe.
func callsPair(gitDir, workTree string) []string {
	return []string{
		gitDir + "|L|--git-dir=" + gitDir + " config -z --list",
		gitDir + "|L|--git-dir=" + gitDir + " --work-tree=" + workTree + " rev-parse --show-toplevel",
	}
}

// wantRemoveCalls compares the calls before the branch step with head, and the count
// of all calls with total. The branch step starts at the listing before for-each-ref.
func wantRemoveCalls(t *testing.T, what string, got []string, total int, head ...[]string) {
	t.Helper()
	want := slices.Concat(head...)
	cut := len(got)
	for i, c := range got {
		if strings.Contains(c, "|for-each-ref ") {
			cut = i - 1
			break
		}
	}
	if len(got) != total || cut < 0 || !slices.Equal(got[:cut], want) {
		t.Errorf("%s: %d calls, want %d, and before the branch step\ngot:\n  %s\nwant:\n  %s", what, len(got), total,
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func (f regFixture) removeParams() map[string]any {
	return map[string]any{"baseRepo": f.T, "worktreePath": f.S, "branchName": "s0"}
}

// daemonGitEnv sets GIT_DIR and GIT_COMMON_DIR of the daemon for this test. An empty
// value leaves the variable unset.
func daemonGitEnv(t *testing.T, gitDir, commonDir string) {
	t.Helper()
	if gitDir != "" {
		t.Setenv("GIT_DIR", gitDir)
	}
	if commonDir != "" {
		t.Setenv("GIT_COMMON_DIR", commonDir)
	}
}

// E1, probe row 1 (B-pre) and battery row E13: the daemon has GIT_DIR and
// GIT_COMMON_DIR of X. The folder goes. The entry s0 of T and the branch s0 of T
// stay. 9 calls.
func TestWorktreeRemoveDaemonGitDirKeepsEntryOfBaseRepo(t *testing.T) {
	f := newRegFixture(t)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw, calls := run("git.worktree_remove", f.removeParams())
	if raw != removeOK {
		t.Fatalf("frame = %s\nwant %s", raw, removeOK)
	}
	if exists(f.S) {
		t.Error("the worktree folder stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row 1)")
	}
	if !hasBranch(f.T, "s0") {
		t.Error("the branch s0 of T is gone")
	}
	wantRemoveCalls(t, "row 1", calls, 9, []string{callExcludes}, callsCheck("<fx>/T"), callsCheck("<fx>/T"))
}

// E2, probe row 2 (C-own): the same environment. The daemon creates w1, which git
// registers in X, and then removes it. The entry w1 of X and the branch w1 of X go.
// 8 calls.
func TestWorktreeRemoveDaemonGitDirDropsOwnEntryInOtherRepo(t *testing.T) {
	f := newRegFixture(t)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	w1 := filepath.Join(f.T, ".claude", "worktrees", "w1")
	params := map[string]any{"baseRepo": f.T, "worktreePath": w1, "branchName": "w1"}
	if raw, _ := run("git.worktree_create", params); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s", raw)
	}
	if !exists(f.entry(f.X, "w1")) || !hasBranch(f.X, "w1") {
		t.Fatal("the create did not register w1 in X")
	}
	raw, calls := run("git.worktree_remove", params)
	if raw != removeOK {
		t.Fatalf("frame = %s\nwant %s", raw, removeOK)
	}
	if exists(w1) {
		t.Error("the worktree folder stays")
	}
	if exists(f.entry(f.X, "w1")) {
		t.Error("the entry w1 of X stays, want it gone (row 2)")
	}
	if hasBranch(f.X, "w1") {
		t.Error("the branch w1 of X stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone")
	}
	wantRemoveCalls(t, "row 2", calls, 8, callsCheck("<fx>/T"))
}

// E3, probe row 3 (B-xrec): the same environment. X holds an entry zz whose record
// names S. The entry zz of X goes and the entry s0 of T stays. 11 calls.
func TestWorktreeRemoveDaemonGitDirDropsEntryByPathInOtherRepo(t *testing.T) {
	f := newRegFixture(t)
	zz := f.entry(f.X, "zz")
	writeFile(t, filepath.Join(zz, "gitdir"), filepath.Join(f.S, ".git")+"\n", 0o644)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw, calls := run("git.worktree_remove", f.removeParams())
	if raw != removeOK {
		t.Fatalf("frame = %s\nwant %s", raw, removeOK)
	}
	if exists(zz) {
		t.Error("the entry zz of X stays, want it gone (row 3)")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row 3)")
	}
	wantRemoveCalls(t, "row 3", calls, 11, []string{callExcludes}, callsCheck("<fx>/T"), callsCheck("<fx>/T"),
		callsPair("<fx>/X/.git", "<fx>/T"))
}

// E4, probe row 4 (B-xbranch): the same environment. X holds a merged branch s0. The
// branch s0 of X goes. The entry s0 of T and the branch s0 of T stay. 11 calls.
func TestWorktreeRemoveDaemonGitDirBranchOfOtherRepo(t *testing.T) {
	f := newRegFixture(t)
	runGit(t, f.X, "branch", "s0")
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw, calls := run("git.worktree_remove", f.removeParams())
	if raw != removeOK {
		t.Fatalf("frame = %s\nwant %s", raw, removeOK)
	}
	if hasBranch(f.X, "s0") {
		t.Error("the branch s0 of X stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row 4)")
	}
	if !hasBranch(f.T, "s0") {
		t.Error("the branch s0 of T is gone")
	}
	wantRemoveCalls(t, "row 4", calls, 11, []string{callExcludes}, callsCheck("<fx>/T"), callsCheck("<fx>/T"))
}

// E5, probe row 5 (B-cd-only): the daemon has GIT_COMMON_DIR of X only. The entry s0
// of T goes. The branch stays, because its commit is not in X. 7 calls. claustrum
// answered so before this rule too.
func TestWorktreeRemoveDaemonCommonDirOnlyDropsEntryOfBaseRepo(t *testing.T) {
	f := newRegFixture(t)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, "", gitDirOf(f.X))
	raw, calls := run("git.worktree_remove", f.removeParams())
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	if exists(f.S) || exists(f.entry(f.T, "s0")) {
		t.Error("the folder or the entry s0 of T stays, want both gone (row 5)")
	}
	wantRemoveCalls(t, "row 5", calls, 7, []string{callExcludes}, callsCheck("<fx>/T"))
}

// E6, probe row 6 (B-gd-only): the daemon has GIT_DIR of X only. The entry s0 of T
// stays. 9 calls: the listing and the rev-parse run twice.
func TestWorktreeRemoveDaemonGitDirOnlyCalls(t *testing.T) {
	f := newRegFixture(t)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, gitDirOf(f.X), "")
	raw, calls := run("git.worktree_remove", f.removeParams())
	if raw != removeOK {
		t.Fatalf("frame = %s\nwant %s", raw, removeOK)
	}
	if exists(f.S) || !exists(f.entry(f.T, "s0")) {
		t.Error("want the folder gone and the entry s0 of T kept (row 6)")
	}
	wantRemoveCalls(t, "row 6", calls, 9, []string{callExcludes}, callsCheck("<fx>/T"), callsCheck("<fx>/T"))
}

// E7, probe row 7c and battery rows A1, A3 and G3: baseRepo is <T>/missing/.. with
// no folder `missing`. The folder goes, the entry s0 stays, and the one git call is
// the read of the user excludes.
func TestWorktreeRemoveMissingBaseRepoKeepsEntry(t *testing.T) {
	f := newRegFixture(t)
	run := removeCallLog(t, f.root)
	params := f.removeParams()
	params["baseRepo"] = f.T + "/missing/.."
	raw, calls := run("git.worktree_remove", params)
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	if exists(f.S) {
		t.Error("the worktree folder stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row 7c)")
	}
	wantRemoveCalls(t, "row 7c", calls, 1, []string{callExcludes})
}

// claustrum still refuses a locked entry for the baseRepo of row 7c. Not measured.
func TestWorktreeRemoveMissingBaseRepoLockedStillRefused(t *testing.T) {
	f := newRegFixture(t)
	runGit(t, f.T, "worktree", "lock", f.S)
	s := newTestServer(t)
	params := f.removeParams()
	params["baseRepo"] = f.T + "/missing/.."
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", params))
	if !strings.Contains(raw, "is locked (git worktree lock); unlock it to remove it") {
		t.Errorf("frame = %s\nwant the locked refusal", raw)
	}
	if !exists(f.S) || !exists(f.entry(f.T, "s0")) {
		t.Error("the locked worktree or its entry is gone")
	}
}

// E9: the call logs of the rows whose end state was equal before. Each row runs with
// no daemon environment.
func TestWorktreeRemoveRegistrationCallLogs(t *testing.T) {
	check, pair := callsCheck("<fx>/T"), callsPair("<fx>/T/.git", "<fx>/T")
	lockedText := "is locked (git worktree lock); unlock it to remove it"
	goneLockedText := "is gone but its registration is locked (git worktree lock)"
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f regFixture) map[string]any
		frame string // the frame, or a part of its error text
		total int
		head  [][]string
		// entries are the names under T/.git/worktrees after the request.
		entries []string
	}{
		{"K0 plain", nil, removeOK, 9, [][]string{check}, nil},
		{"K1 no worktree", func(t *testing.T, f regFixture) map[string]any {
			nope := filepath.Join(f.T, ".claude", "worktrees", "nope")
			writeFile(t, filepath.Join(nope, "n.txt"), "n\n", 0o644)
			return map[string]any{"baseRepo": f.T, "worktreePath": nope, "branchName": "nope"}
		}, removeOK, 11, [][]string{check, pair, pair}, []string{"s0"}},
		{"11 N-dup", func(t *testing.T, f regFixture) map[string]any {
			writeFile(t, filepath.Join(f.entry(f.T, "s0b"), "gitdir"), filepath.Join(f.S, ".git")+"\n", 0o644)
			writeFile(t, filepath.Join(f.S, ".git"), "garbage\n", 0o644)
			return nil
		}, removeOK, 11, [][]string{check, pair}, []string{"s0", "s0b"}},
		{"12 N-plain", func(t *testing.T, f regFixture) map[string]any {
			writeFile(t, filepath.Join(f.S, ".git"), "gitdir: /nowhere/worktrees/zz\n", 0o644)
			return nil
		}, removeOK, 13, [][]string{check, check, pair}, nil},
		{"13 N-foreign", func(t *testing.T, f regFixture) map[string]any {
			runGit(t, f.T, "worktree", "add", "-q", "-b", "d0", filepath.Join(f.T, ".claude", "worktrees", "d0"))
			writeFile(t, filepath.Join(f.S, ".git"), "gitdir: "+f.entry(f.T, "d0")+"\n", 0o644)
			return nil
		}, removeOK, 15, [][]string{check, pair, check, pair}, []string{"d0"}},
		{"14 N-lockpath", func(t *testing.T, f regFixture) map[string]any {
			writeFile(t, filepath.Join(f.S, ".git"), "gitdir: /nowhere/worktrees/zz\n", 0o644)
			writeFile(t, filepath.Join(f.entry(f.T, "s0"), "locked"), "", 0o644)
			return nil
		}, lockedText, 7, [][]string{check, check, pair}, []string{"s0"}},
		{"15 G-gone-locked", func(t *testing.T, f regFixture) map[string]any {
			if err := os.RemoveAll(f.S); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.entry(f.T, "s0"), "locked"), "", 0o644)
			return nil
		}, goneLockedText, 5, [][]string{check, pair}, []string{"s0"}},
		{"15b gone", func(t *testing.T, f regFixture) map[string]any {
			if err := os.RemoveAll(f.S); err != nil {
				t.Fatal(err)
			}
			return nil
		}, removeOK, 11, [][]string{check, pair}, nil},
		{"16 G-norepo", func(t *testing.T, f regFixture) map[string]any {
			plain := filepath.Join(f.root, "plain")
			if err := os.MkdirAll(plain, 0o755); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"baseRepo": plain, "worktreePath": filepath.Join(plain, ".claude", "worktrees", "s0"), "branchName": "s0"}
		}, removeKept, 5, [][]string{callsCheck("<fx>/plain")}, []string{"s0"}},
		{"17 A5", func(t *testing.T, f regFixture) map[string]any {
			return map[string]any{"baseRepo": f.T + "/..", "worktreePath": f.S}
		}, "(its registrations could not be examined); retry", 5, [][]string{callsCheck("<fx>"), callsCheck("<fx>")}, []string{"s0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRegFixture(t)
			params := f.removeParams()
			if c.setup != nil {
				if p := c.setup(t, f); p != nil {
					params = p
				}
			}
			run := removeCallLog(t, f.root)
			raw, calls := run("git.worktree_remove", params)
			if raw != c.frame && (strings.HasPrefix(c.frame, "{") || !strings.Contains(raw, c.frame)) {
				t.Fatalf("frame = %s\nwant %s", raw, c.frame)
			}
			wantRemoveCalls(t, c.name, calls, c.total, slices.Concat([][]string{{callExcludes}}, c.head)...)
			var names []string
			if des, err := os.ReadDir(filepath.Join(f.T, ".git", "worktrees")); err == nil {
				for _, de := range des {
					names = append(names, de.Name())
				}
			}
			if !slices.Equal(names, c.entries) {
				t.Errorf("entries of T = %v, want %v", names, c.entries)
			}
		})
	}
}

// Battery row T6 (Linux VM): with a worktreeRoot that cannot be read, the answer
// comes after 7 calls. The second `rev-parse --absolute-git-dir` does not run.
func TestWorktreeRemoveExternalUnreadableRootCalls(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 000")
	}
	f := newRegFixture(t)
	p := filepath.Join(f.root, "P")
	rootDir := filepath.Join(p, "r")
	if err := os.MkdirAll(filepath.Join(rootDir, "cp"), 0o755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, p, 0o000)
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": f.T,
		"worktreePath": filepath.Join(rootDir, "cp", "w1"), "branchName": "wt1", "worktreeRoot": rootDir})
	want := "failed to remove worktree: lstat " + rootDir + ": permission denied"
	if !strings.Contains(raw, want) {
		t.Fatalf("frame = %s\nwant %s", raw, want)
	}
	wantRemoveCalls(t, "T6", calls, 7, []string{callExcludes,
		"<fx>/T|L|config -z --list", "<fx>/T|L|rev-parse --show-toplevel"}, callsCheck("<fx>/T"),
		[]string{"<fx>/T|L|config -z --list", "<fx>/T|L|worktree list --porcelain -z"})
}

// Battery rows Q20, Q20b and Y8w (Linux VM): a worktree beneath a worktreeRoot whose
// `.git` file names a git dir that is not a verified entry of baseRepo. Nothing is
// deleted. The calls after the first 7 are those of 89cb6289.
func TestWorktreeRemoveExternalUnverifiedCalls(t *testing.T) {
	check := callsCheck("<fx>/T")
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, f regFixture, leaf string)
		reason string
		total  int
		tail   func(leaf string) [][]string
	}{
		// Q20: T has no worktrees directory.
		{"Q20", func(t *testing.T, f regFixture, leaf string) {
			runGit(t, f.T, "worktree", "remove", "--force", f.S)
			if err := os.RemoveAll(filepath.Join(f.T, ".git", "worktrees")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+f.entry(f.X, "w1")+"\n", 0o644)
		}, "/T/.git/worktrees: no such file or directory); retry", 11,
			func(string) [][]string { return [][]string{check, check} }},
		// Q20b: the `.git` file names an entry of another repository.
		{"Q20b", func(t *testing.T, f regFixture, leaf string) {
			writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+f.entry(f.X, "w1")+"\n", 0o644)
		}, "carries a .git file that does not name this repository's own worktree admin directory", 13,
			func(leaf string) [][]string { return [][]string{check, check, callsPair("<fx>/T/.git", leaf)} }},
		// Y8w: the `.git` file names an entry of T whose record is of another worktree.
		{"Y8w", func(t *testing.T, f regFixture, leaf string) {
			writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+f.entry(f.T, "s0")+"\n", 0o644)
		}, "carries a .git file naming an admin directory whose own record is of a different worktree", 15,
			func(leaf string) [][]string {
				pair := callsPair("<fx>/T/.git", leaf)
				return [][]string{check, pair, check, pair}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRegFixture(t)
			rootDir := filepath.Join(f.root, "ext")
			leaf := filepath.Join(rootDir, "cp", "w1")
			writeFile(t, filepath.Join(leaf, "keep.txt"), "k\n", 0o644)
			c.setup(t, f, leaf)
			run := removeCallLog(t, f.root)
			raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": f.T,
				"worktreePath": leaf, "branchName": "wt1", "worktreeRoot": rootDir})
			if !strings.Contains(raw, c.reason) {
				t.Fatalf("frame = %s\nwant %s", raw, c.reason)
			}
			if !exists(filepath.Join(leaf, "keep.txt")) {
				t.Error("the worktree folder was deleted")
			}
			head := [][]string{{callExcludes, "<fx>/T|L|config -z --list", "<fx>/T|L|rev-parse --show-toplevel"}, check,
				{"<fx>/T|L|config -z --list", "<fx>/T|L|worktree list --porcelain -z"}}
			wantRemoveCalls(t, c.name, calls, c.total,
				slices.Concat(head, c.tail(strings.ReplaceAll(leaf, f.root, "<fx>")))...)
		})
	}
}
