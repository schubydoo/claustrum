package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Which registration git.worktree_remove deletes or keeps, as on 89cb6289, and the
// lock refusals of divergence D22. These tests compare frames and end states only, so
// they run on every system. The call logs are in the unix file.

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

// hasBranch reports whether repo has the branch. It asks git, without the GIT_DIR and
// GIT_COMMON_DIR that a test sets for the daemon.
func hasBranch(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") && !strings.HasPrefix(kv, "GIT_COMMON_DIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd.Run() == nil
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

// lockedText is the refusal of a locked worktree whose folder is present.
const lockedText = "is locked (git worktree lock); unlock it to remove it"

// Probe row 1 (Linux, macOS and Windows VMs): the daemon has GIT_DIR and
// GIT_COMMON_DIR of X. The folder goes. The entry s0 of T and the branch s0 of T stay.
func TestWorktreeRemoveDaemonGitDirEndState(t *testing.T) {
	f := newRegFixture(t)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	if raw := removeFrame(t, f.removeParams()); raw != removeOK {
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
}

// Probe row 5 (Linux, macOS and Windows VMs): the daemon has GIT_COMMON_DIR of X
// alone. The folder and the entry s0 of T go. X does not hold the commit of T here, so
// the branch stays.
func TestWorktreeRemoveDaemonCommonDirOnlyEndState(t *testing.T) {
	f := newRegFixture(t)
	daemonGitEnv(t, "", gitDirOf(f.X))
	if raw := removeFrame(t, f.removeParams()); raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	if exists(f.S) || exists(f.entry(f.T, "s0")) {
		t.Error("the folder or the entry s0 of T stays, want both gone (row 5)")
	}
}

// Row p2b (Linux and macOS VMs): the daemon has GIT_COMMON_DIR of X alone, and
// baseRepo is a subfolder of T. The folder and the entry of T go. X does not hold the
// commit of T in this fixture, so the branch stays here. On the macOS VM the fixture
// let 89cb6289 delete the branch.
func TestWorktreeRemoveDaemonCommonDirOnlySubfolderBase(t *testing.T) {
	f := newRegFixture(t)
	sub := filepath.Join(f.T, "sub")
	wt := filepath.Join(sub, ".claude", "worktrees", "p2b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.T, "worktree", "add", "-q", "-b", "p2b", wt)
	daemonGitEnv(t, "", gitDirOf(f.X))
	raw := removeFrame(t, map[string]any{"baseRepo": sub, "worktreePath": wt, "branchName": "p2b"})
	if raw != removeKept {
		t.Fatalf("frame = %s\nwant %s", raw, removeKept)
	}
	if exists(wt) || exists(f.entry(f.T, "p2b")) {
		t.Error("the folder or the entry p2b of T stays, want both gone (row p2b)")
	}
}

// Row p3b (Linux, macOS and Windows VMs): the daemon has GIT_DIR of X and GIT_COMMON_DIR of a third
// repository Y. X holds an entry zz whose record names S. The folder and the entry zz
// of X go, and the entry s0 of T stays.
func TestWorktreeRemoveDaemonGitDirAndOtherCommonDir(t *testing.T) {
	f := newRegFixture(t)
	y := filepath.Join(f.root, "Y")
	runGit(t, f.root, "init", "-q", "-b", "yonly", y)
	runGit(t, y, "commit", "-q", "--allow-empty", "-m", "y0")
	zz := f.entry(f.X, "zz")
	writeFile(t, filepath.Join(zz, "gitdir"), filepath.Join(f.S, ".git")+"\n", 0o644)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(y))
	if raw := removeFrame(t, f.removeParams()); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("frame = %s\nwant success", raw)
	}
	if exists(f.S) || exists(zz) {
		t.Error("the folder or the entry zz of X stays, want both gone (row p3b)")
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the entry s0 of T is gone, want it kept (row p3b)")
	}
}

// Row p6 (Linux, macOS and Windows VMs), divergence D22: the daemon has GIT_DIR and GIT_COMMON_DIR of X,
// and S is locked in T. claustrum refuses and deletes nothing. 89cb6289 answers
// success and deletes S.
func TestWorktreeRemoveDaemonGitDirLockedInBaseRepoIsRefused(t *testing.T) {
	f := newRegFixture(t)
	runGit(t, f.T, "worktree", "lock", f.S)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw := removeFrame(t, f.removeParams())
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, lockedText) {
		t.Errorf("frame = %s\nwant the locked refusal", raw)
	}
	if !exists(f.S) || !exists(f.entry(f.T, "s0")) {
		t.Error("the locked worktree or its entry is gone")
	}
}

// Row p6f (Linux, macOS and Windows VMs), D22 for a folder that is gone: the same
// environment, S locked in T and its folder removed by hand. claustrum refuses.
// 89cb6289 answers success and deletes nothing.
func TestWorktreeRemoveDaemonGitDirGoneLockedInBaseRepoIsRefused(t *testing.T) {
	f := newRegFixture(t)
	runGit(t, f.T, "worktree", "lock", f.S)
	if err := os.RemoveAll(f.S); err != nil {
		t.Fatal(err)
	}
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw := removeFrame(t, f.removeParams())
	if !strings.Contains(raw, "is gone but its registration is locked (git worktree lock)") {
		t.Errorf("frame = %s\nwant the gone-and-locked refusal", raw)
	}
	if !exists(f.entry(f.T, "s0")) {
		t.Error("the locked entry is gone")
	}
}

// Row p6b (Linux, macOS and Windows VMs): the same environment, and the lock is on an entry of X whose
// record names S. Both 89cb6289 and claustrum refuse and delete nothing.
func TestWorktreeRemoveDaemonGitDirLockedInOtherRepoIsRefused(t *testing.T) {
	f := newRegFixture(t)
	zz := f.entry(f.X, "zz")
	writeFile(t, filepath.Join(zz, "gitdir"), filepath.Join(f.S, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(zz, "locked"), "", 0o644)
	daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	raw := removeFrame(t, f.removeParams())
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, lockedText) {
		t.Errorf("frame = %s\nwant the locked refusal", raw)
	}
	if !exists(f.S) || !exists(zz) || !exists(f.entry(f.T, "s0")) {
		t.Error("the worktree or an entry is gone")
	}
}

// D22 with a worktreeRoot: the daemon has GIT_DIR of X alone, and the worktree
// beneath the root is locked in T. claustrum answers the locked refusal and deletes
// nothing. The reference is not measured there.
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

// Row p6e (Linux, macOS and Windows VMs), divergence D22: the daemon has GIT_DIR of X
// alone, and S is locked in T. claustrum refuses and deletes nothing. 89cb6289
// answers success and deletes S.
func TestWorktreeRemoveDaemonGitDirOnlyLockedInBaseRepoIsRefused(t *testing.T) {
	f := newRegFixture(t)
	runGit(t, f.T, "worktree", "lock", f.S)
	daemonGitEnv(t, gitDirOf(f.X), "")
	raw := removeFrame(t, f.removeParams())
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, lockedText) {
		t.Errorf("frame = %s\nwant the locked refusal", raw)
	}
	if !exists(f.S) || !exists(f.entry(f.T, "s0")) {
		t.Error("the locked worktree or its entry is gone")
	}
}
