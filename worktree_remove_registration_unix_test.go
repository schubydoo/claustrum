//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Which registration git.worktree_remove deletes, and its git calls, as on 89cb6289.
// The rows are those of the remove probe on Linux and macOS VMs. This file is unix
// only, because it compares working directories as text.

// removeCallLog puts the logging git stand-in on PATH and returns a function that
// sends one request and gives its frame and its git calls. Each call is one line:
//
//	<cwd>|<H, R, L or ->|<the argv without the -c pairs>[ D=<GIT_DIR>][ C=<GIT_COMMON_DIR>]
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
			line := cwd + "|" + env + "|" + strings.Join(argv, " ")
			for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR"} {
				for _, kv := range c.all {
					if v, ok := strings.CutPrefix(kv, k+"="); ok {
						line += " " + k[4:5] + "=" + v
					}
				}
			}
			lines = append(lines, strings.ReplaceAll(line, root, "<fx>"))
		}
		return raw, lines
	}
}

// callExcludes is the read of the user excludes with no daemon environment.
const callExcludes = "<temp>|-|config --includes --path core.excludesFile D=/dev/null"

// The GIT_DIR and GIT_COMMON_DIR part of a call line: the pin of T, the daemon
// environment of rows 1 to 4, and the value in a folder with no repository.
const (
	pinT      = " C=<fx>/T/.git"
	pinX      = " D=<fx>/X/.git C=<fx>/X/.git"
	pinNoRepo = " D=/dev/null"
)

// callsCheck is the heavy listing and `rev-parse --absolute-git-dir` in cwd.
func callsCheck(cwd, pin string) []string {
	return []string{cwd + "|H|config -z --list" + pin, cwd + "|H|rev-parse --absolute-git-dir" + pin}
}

// callsPair is the pair of registrationProbe.
func callsPair(gitDir, workTree, pin string) []string {
	return []string{
		gitDir + "|L|--git-dir=" + gitDir + " config -z --list" + pin,
		gitDir + "|L|--git-dir=" + gitDir + " --work-tree=" + workTree + " rev-parse --show-toplevel" + pin,
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

// E1, probe row 1 (B-pre) on Linux, macOS and Windows VMs, and battery row E13 on
// Linux and Windows VMs: the daemon has GIT_DIR and GIT_COMMON_DIR of X. The folder
// goes. The entry s0 of T and the branch s0 of T stay. Row 1 makes 9 calls.
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
	if !hasBranch(t, f.T, "s0") {
		t.Error("the branch s0 of T is gone")
	}
	wantRemoveCalls(t, "row 1", calls, 9, []string{callExcludes + " C=<fx>/X/.git"}, callsCheck("<fx>/T", pinX), callsCheck("<fx>/T", pinX))
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
	if !exists(f.entry(f.X, "w1")) || !hasBranch(t, f.X, "w1") {
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
	if hasBranch(t, f.X, "w1") {
		t.Error("the branch w1 of X stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone")
	}
	wantRemoveCalls(t, "row 2", calls, 8, callsCheck("<fx>/T", pinX))
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
	wantRemoveCalls(t, "row 3", calls, 11, []string{callExcludes + " C=<fx>/X/.git"}, callsCheck("<fx>/T", pinX), callsCheck("<fx>/T", pinX),
		callsPair("<fx>/X/.git", "<fx>/T", pinX))
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
	if hasBranch(t, f.X, "s0") {
		t.Error("the branch s0 of X stays")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row 4)")
	}
	if !hasBranch(t, f.T, "s0") {
		t.Error("the branch s0 of T is gone")
	}
	wantRemoveCalls(t, "row 4", calls, 11, []string{callExcludes + " C=<fx>/X/.git"}, callsCheck("<fx>/T", pinX), callsCheck("<fx>/T", pinX))
}

// E5, probe row 5 (B-cd-only): the daemon has GIT_COMMON_DIR of X only. The entry s0
// of T goes. In this fixture X does not hold the commit of T. The branch then stays,
// and the request makes 7 calls. claustrum answered so before this rule too.
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
	wantRemoveCalls(t, "row 5", calls, 7, []string{callExcludes + " C=<fx>/X/.git"}, callsCheck("<fx>/T", " C=<fx>/X/.git"))
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
	wantRemoveCalls(t, "row 6", calls, 9, []string{callExcludes}, callsCheck("<fx>/T", pinX), callsCheck("<fx>/T", pinX))
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

// Row p6d (Linux and macOS VMs): the baseRepo of row 7c with S locked in T. claustrum refuses,
// and 89cb6289 deletes S. That is divergence D22.
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
	check, pair := callsCheck("<fx>/T", pinT), callsPair("<fx>/T/.git", "<fx>/T", pinT)
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
		}, removeKept, 5, [][]string{callsCheck("<fx>/plain", pinNoRepo)}, []string{"s0"}},
		// Battery rows W01 to W07-b, W14, W15 and W16 (Windows VM): the folder and its
		// entry are gone, and the worktrees directory is empty. The pair runs twice.
		{"W01 gone, no entry", func(t *testing.T, f regFixture) map[string]any {
			for _, p := range []string{f.S, f.entry(f.T, "s0")} {
				if err := os.RemoveAll(p); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		}, removeOK, 13, [][]string{check, pair, pair}, nil},
		{"17 A5", func(t *testing.T, f regFixture) map[string]any {
			return map[string]any{"baseRepo": f.T + "/..", "worktreePath": f.S}
		}, "(its registrations could not be examined); retry", 5, [][]string{callsCheck("<fx>", pinNoRepo), callsCheck("<fx>", pinNoRepo)}, []string{"s0"}},
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
		"<fx>/T|L|config -z --list" + pinT, "<fx>/T|L|rev-parse --show-toplevel" + pinT}, callsCheck("<fx>/T", pinT),
		[]string{"<fx>/T|L|config -z --list" + pinT, "<fx>/T|L|worktree list --porcelain -z" + pinT})
}

// Battery rows Q20, Q20b and Y8w (Linux VM): a worktree beneath a worktreeRoot whose
// `.git` file names a git dir that is not a verified entry of baseRepo. Nothing is
// deleted. The calls after the first 7 are those of 89cb6289. Rows Q20b and Y8w make
// the pair, and row Q20 does not.
func TestWorktreeRemoveExternalUnverifiedCalls(t *testing.T) {
	check := callsCheck("<fx>/T", pinT)
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
			func(leaf string) [][]string { return [][]string{check, check, callsPair("<fx>/T/.git", leaf, pinT)} }},
		// Y8w: the `.git` file names an entry of T whose record is of another worktree.
		{"Y8w", func(t *testing.T, f regFixture, leaf string) {
			writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+f.entry(f.T, "s0")+"\n", 0o644)
		}, "carries a .git file naming an admin directory whose own record is of a different worktree", 15,
			func(leaf string) [][]string {
				pair := callsPair("<fx>/T/.git", leaf, pinT)
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
			head := [][]string{{callExcludes, "<fx>/T|L|config -z --list" + pinT, "<fx>/T|L|rev-parse --show-toplevel" + pinT}, check,
				{"<fx>/T|L|config -z --list" + pinT, "<fx>/T|L|worktree list --porcelain -z" + pinT}}
			wantRemoveCalls(t, c.name, calls, c.total,
				slices.Concat(head, c.tail(strings.ReplaceAll(leaf, f.root, "<fx>")))...)
		})
	}
}

// wantAllCalls compares every call of a request.
func wantAllCalls(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %d calls, want %d\ngot:\n  %s\nwant:\n  %s", what, len(got), len(want),
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

const callForEachRef = "|R|for-each-ref --count=10001 --format=%(objectname)%00%(refname)%00%(symref) refs/heads/"

// Probe row 16 and battery rows R18, R18b and N00 (Linux and macOS VMs): baseRepo is a
// plain folder and the worktree folder is gone. Each call carries GIT_DIR=/dev/null,
// the two calls of the branch step too.
func TestWorktreeRemoveNoRepositoryCallsCarryNullGitDir(t *testing.T) {
	f := newRegFixture(t)
	plain := filepath.Join(f.root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": plain,
		"worktreePath": filepath.Join(plain, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	wantAllCalls(t, "N00", calls, callExcludes,
		"<fx>/plain|H|config -z --list"+pinNoRepo,
		"<fx>/plain|H|rev-parse --absolute-git-dir"+pinNoRepo,
		"<fx>/plain|L|config -z --list"+pinNoRepo,
		"<fx>/plain"+callForEachRef+pinNoRepo)
}

// Battery row N04 (Linux and macOS VMs): the `.git` file of baseRepo names a git dir
// that is gone. The two listings of the removal fail and carry
// GIT_COMMON_DIR=<that git dir>. With worktreeRoot the one listing carries it too.
func TestWorktreeRemoveGoneGitFileTargetCalls(t *testing.T) {
	f := newRegFixture(t)
	r := filepath.Join(f.root, "R")
	gone := filepath.Join(f.root, "nonexistent")
	writeFile(t, filepath.Join(r, ".git"), "gitdir: "+gone+"\n", 0o644)
	leaf := filepath.Join(f.root, "ext", "cp", "w1")
	writeFile(t, filepath.Join(leaf, "keep.txt"), "k\n", 0o644)
	pin := " C=<fx>/nonexistent"
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": r,
		"worktreePath": filepath.Join(r, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	wantAllCalls(t, "N04 remove", calls, callExcludes, "<fx>/R|H|config -z --list"+pin, "<fx>/R|L|config -z --list"+pin)
	raw, calls = run("git.worktree_remove", map[string]any{"baseRepo": r, "worktreePath": leaf,
		"branchName": "w1", "worktreeRoot": filepath.Join(f.root, "ext")})
	// The text after "fatal: " is git's own. git 2.47 names the git dir there, and
	// git 2.55 prints "(null)".
	want := "cannot determine the repository's work tree: git finds no repository here: fatal: not a git repository"
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, want) {
		t.Fatalf("frame = %s\nwant %s", raw, want)
	}
	wantAllCalls(t, "N04 remove with worktreeRoot", calls, "<fx>/R|L|config -z --list"+pin)
}

// Battery row N05a (Linux and macOS VMs): the daemon GIT_DIR names the `.git` file of
// a linked worktree. The folder is gone. The reply carries branchKept, and the request
// makes no git call. In the row the read of the user excludes came with an earlier
// request. Here the removal is the first request, and claustrum makes that read.
func TestWorktreeRemoveDaemonGitDirFileMakesNoCall(t *testing.T) {
	f := newRegFixture(t)
	twt := filepath.Join(f.root, "T_wt")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "twt", twt)
	run := removeCallLog(t, f.root)
	daemonGitEnv(t, filepath.Join(twt, ".git"), "")
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": f.T,
		"worktreePath": filepath.Join(f.T, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	wantAllCalls(t, "N05a", calls, callExcludes)
}

// Battery rows L13z-g and DG2h-g (Linux VM): with worktreeRoot and a baseRepo that
// holds no repository, the request makes a light listing and `rev-parse
// --show-toplevel`, both with GIT_DIR=/dev/null.
func TestWorktreeRemoveExternalNoRepositoryCalls(t *testing.T) {
	f := newRegFixture(t)
	n := filepath.Join(f.root, "N")
	if err := os.MkdirAll(n, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(f.root, "ext", "cp", "w1")
	writeFile(t, filepath.Join(leaf, "keep.txt"), "k\n", 0o644)
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": n, "worktreePath": leaf,
		"branchName": "w1", "worktreeRoot": filepath.Join(f.root, "ext")})
	want := `"error":"failed to remove worktree: cannot determine the repository's work tree: exit status 128"`
	if !strings.Contains(raw, want) {
		t.Fatalf("frame = %s\nwant %s", raw, want)
	}
	wantAllCalls(t, "L13z-g", calls, callExcludes,
		"<fx>/N|L|config -z --list"+pinNoRepo, "<fx>/N|L|rev-parse --show-toplevel"+pinNoRepo)
}

// Battery rows LNKb-g and LNKr-g (Linux VM, LNKb-g on a macOS VM): baseRepo is the
// head of a chain of 45 symlinks. The one git call is the read of the user excludes.
func TestWorktreeRemoveExternalSymlinkChainCalls(t *testing.T) {
	f := newRegFixture(t)
	prev := f.T
	for i := 1; i <= 45; i++ {
		link := filepath.Join(f.root, "l"+strconv.Itoa(i))
		if err := os.Symlink(filepath.Base(prev), link); err != nil {
			t.Fatal(err)
		}
		prev = link
	}
	leaf := filepath.Join(f.root, "ext", "cp", "w1")
	writeFile(t, filepath.Join(leaf, "keep.txt"), "k\n", 0o644)
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": prev, "worktreePath": leaf,
		"branchName": "w1", "worktreeRoot": filepath.Join(f.root, "ext")})
	want := "listing the configuration in force: chdir " + prev + ": too many levels of symbolic links"
	if !strings.Contains(raw, want) {
		t.Fatalf("frame = %s\nwant %s", raw, want)
	}
	wantAllCalls(t, "LNKr-g", calls, callExcludes)
}

// Battery row DG2i-g (Linux VM): the HEAD of baseRepo reads "garbage" and its
// commondir is a dangling symlink. The worktree folder is present. The one git call
// is the read of the user excludes.
func TestWorktreeRemoveRefusedGitDirReadsExcludes(t *testing.T) {
	f := newRegFixture(t)
	writeFile(t, filepath.Join(f.T, ".git", "HEAD"), "garbage\n", 0o644)
	if err := os.Symlink("nothing-here", filepath.Join(f.T, ".git", "commondir")); err != nil {
		t.Fatal(err)
	}
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", f.removeParams())
	want := "could not check whether " + f.S + " is locked (its registrations could not be examined); retry"
	if !strings.Contains(raw, want) {
		t.Fatalf("frame = %s\nwant %s", raw, want)
	}
	if !exists(f.S) {
		t.Error("the worktree folder was deleted")
	}
	wantAllCalls(t, "DG2i-g", calls, callExcludes)
}

// Battery row DG1c-g (Linux VM): with worktreeRoot, baseRepo does not exist and the
// `.git` file of the leaf names an entry of another missing repository. The frame is
// that of 89cb6289, and the one git call is the read of the user excludes.
func TestWorktreeRemoveExternalMissingBaseRepoFrame(t *testing.T) {
	f := newRegFixture(t)
	gone := filepath.Join(f.root, "gone")
	leaf := filepath.Join(f.root, "ext", "cp", "w1")
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.Join(f.root, "other", ".git", "worktrees", "x")+"\n", 0o644)
	writeFile(t, filepath.Join(leaf, "a.txt"), "a\n", 0o644)
	run := removeCallLog(t, f.root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": gone, "worktreePath": leaf,
		"branchName": "w1", "worktreeRoot": filepath.Join(f.root, "ext")})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not verify that ` + leaf +
		` is a worktree of ` + gone + ` (config-defined hooks could not be pinned off; git not run: listing the configuration in force: chdir ` +
		gone + `: no such file or directory); retry"}}`
	if raw != want {
		t.Fatalf("frame = %s\nwant  %s", raw, want)
	}
	if !exists(filepath.Join(leaf, "a.txt")) {
		t.Error("the leaf was deleted")
	}
	wantAllCalls(t, "DG1c-g", calls, callExcludes)
}

// Rows p1 and p1b (Linux and macOS VMs): no git on PATH, and a baseRepo that does not
// exist as sent. The answer is the lock-check refusal, nothing is deleted, and no git
// call starts.
func TestWorktreeRemoveMissingBaseRepoWithoutGitRefuses(t *testing.T) {
	for _, c := range []struct{ name, base string }{
		{"p1 missing", "missing"},
		{"p1b dangling link", "dl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRegFixture(t)
			if err := os.Symlink("nowhere", filepath.Join(f.T, "dl")); err != nil {
				t.Fatal(err)
			}
			s := newTestServer(t)
			t.Setenv("PATH", t.TempDir())
			params := f.removeParams()
			params["baseRepo"] = f.T + "/" + c.base + "/.."
			raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", params))
			want := `"error":"failed to remove worktree: could not check whether ` + f.S +
				` is locked (its registrations could not be examined); retry"`
			if !strings.Contains(raw, want) {
				t.Errorf("frame = %s\nwant %s", raw, want)
			}
			if !exists(f.S) || !exists(f.entry(f.T, "s0")) {
				t.Error("the worktree folder or its entry is gone")
			}
		})
	}
}

// Row p4 (Linux and macOS VMs): baseRepo holds an empty `.git` folder and lies in an outer
// repository O. The worktree folder is gone. The branch s0 of O stays, and the request
// makes 5 calls, each with GIT_DIR=/dev/null.
func TestWorktreeRemoveEmptyGitDirKeepsOuterBranch(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	o := filepath.Join(root, "O")
	runGit(t, root, "init", "-q", "-b", "main", o)
	runGit(t, o, "commit", "-q", "--allow-empty", "-m", "o0")
	runGit(t, o, "branch", "s0")
	sub := filepath.Join(o, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := removeCallLog(t, root)
	raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": sub,
		"worktreePath": filepath.Join(sub, ".claude", "worktrees", "s0"), "branchName": "s0"})
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	if !hasBranch(t, o, "s0") {
		t.Error("the branch s0 of the outer repository is gone, want it kept (row p4)")
	}
	wantAllCalls(t, "p4", calls, callExcludes,
		"<fx>/O/sub|H|config -z --list"+pinNoRepo,
		"<fx>/O/sub|H|rev-parse --absolute-git-dir"+pinNoRepo,
		"<fx>/O/sub|L|config -z --list"+pinNoRepo,
		"<fx>/O/sub"+callForEachRef+pinNoRepo)
}

// D22: the daemon has GIT_DIR and GIT_COMMON_DIR of X, and the worktrees folder of T
// has mode 000. claustrum cannot read the lock there, so it answers the lock-check
// refusal and deletes nothing. Not measured on the reference.
func TestWorktreeRemoveUnreadableBaseRepoEntriesRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 000")
	}
	for _, c := range []struct {
		name string
		gone bool
	}{{"folder present", false}, {"folder gone", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := newRegFixture(t)
			if c.gone {
				if err := os.RemoveAll(f.S); err != nil {
					t.Fatal(err)
				}
			}
			chmodFor(t, filepath.Join(f.T, ".git", "worktrees"), 0o000)
			daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
			raw := removeFrame(t, f.removeParams())
			want := `"error":"failed to remove worktree: could not check whether ` + f.S +
				` is locked (its registrations could not be examined); retry"`
			if !strings.Contains(raw, want) {
				t.Errorf("frame = %s\nwant %s", raw, want)
			}
			if !c.gone && !exists(f.S) {
				t.Error("the worktree folder was deleted")
			}
		})
	}
}

// Rows q2 and q4 (Linux VM), D22 with a worktreeRoot: the daemon has GIT_DIR of X
// alone, and the worktree beneath the root is locked in T. claustrum answers the
// locked refusal and deletes nothing. 89cb6289 deletes nothing either. Windows
// refuses a worktreeRoot, so this test is in the unix file.
func TestWorktreeRemoveExternalLockedInBaseRepoIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		gone bool
		want string
	}{
		{"folder present", false, lockedText},
		{"folder gone", true, "is gone but its registration is locked (git worktree lock)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRegFixture(t)
			rootDir := filepath.Join(f.root, "ext")
			leaf := filepath.Join(rootDir, "cp", "e0")
			if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, f.T, "worktree", "add", "-q", "-b", "e0", leaf)
			runGit(t, f.T, "worktree", "lock", leaf)
			if c.gone {
				if err := os.RemoveAll(leaf); err != nil {
					t.Fatal(err)
				}
			}
			daemonGitEnv(t, gitDirOf(f.X), "")
			raw := removeFrame(t, map[string]any{"baseRepo": f.T, "worktreePath": leaf,
				"branchName": "e0", "worktreeRoot": rootDir})
			if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, c.want) {
				t.Errorf("frame = %s\nwant %s", raw, c.want)
			}
			if !c.gone && !exists(filepath.Join(leaf, ".git")) {
				t.Error("the locked worktree is gone")
			}
			if !exists(filepath.Join(f.entry(f.T, "e0"), "locked")) {
				t.Error("the locked entry is gone")
			}
		})
	}
}
