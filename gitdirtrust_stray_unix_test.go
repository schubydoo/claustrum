//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The stray-commondir rows that need a FIFO, a symlink or a mode bit. They run on
// Linux and macOS only.

// A FIFO, an absolute symlink that leaves the git directory, and a file
// the daemon cannot read are refused with their reasons and the stray tail (rows T09,
// T07a and T08). Mutation: reuse the tail of the entry text. A dangling symlink that is
// absolute, or that leaves the git directory, is refused the same way (rows T07d and
// T07e on a Linux VM). Mutation: count every dangling symlink as no commondir.
func TestStrayCommondirNotAPlainFileUnix(t *testing.T) {
	t.Run("FIFO", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		mkfifo(t, cd)
		rep := dispatchWithin(t, cd, "git.info", map[string]any{"path": r.T})
		wantRPCError(t, "info(T)", rep, wantTP(cd, "commondir is not a regular file"))
	})
	t.Run("absolute symlink out", func(t *testing.T) {
		r := newTrustRepo(t)
		out := filepath.Join(r.base, "out.txt")
		writeFile(t, out, ".", 0o644)
		cd := filepath.Join(r.T, ".git", "commondir")
		symlink(t, out, cd)
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "path escapes from parent"))
	})
	for _, tc := range []struct{ name, target string }{
		{"T07d absolute dangling symlink", "/nonexistent-claustrum-t07d"},
		{"T07e relative dangling symlink out", "../nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.T, ".git", "commondir")
			symlink(t, tc.target, cd)
			want := wantTP(cd, "path escapes from parent")
			wantRPCError(t, "info(T)", info(t, r.T), want)
			wantCreateRefused(t, r.T, r.T, want)
		})
	}
	t.Run("mode 000", func(t *testing.T) {
		skipIfRoot(t)
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		writeFile(t, cd, ".", 0o644)
		if err := os.Chmod(cd, 0); err != nil {
			t.Fatal(err)
		}
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "permission denied"))
	})
}

// A relative symlink that stays in the git directory is followed. Its
// target holds ".", so the repository is served (row T07b). Mutation: refuse every
// symlink.
func TestStrayCommondirRelativeSymlink(t *testing.T) {
	r := newTrustRepo(t)
	gitDir := filepath.Join(r.T, ".git")
	writeFile(t, filepath.Join(gitDir, "cd.txt"), ".", 0o644)
	symlink(t, "cd.txt", filepath.Join(gitDir, "commondir"))
	wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true,"repo":"T"`)
	c, wt := create(t, r.T)
	wantResultPrefix(t, "create(T)", c, `{"success":true`)
	wantResultPrefix(t, "remove(T)", remove(t, r.T, wt, "w1"), `{"success":true`)
}

// A dangling relative symlink that stays in the git directory counts as no commondir.
// The git directory is trusted
// and pinned, and git itself then fails to read the file. Each answer is what the
// existing code makes of git's failure (row T07c): info without branch, list_branches
// "exit status 128", status the bare shape, create the failed add, and remove keeps
// the branch. The text of the failed add is git's own, so the test pins only that it
// names commondir. Mutation: treat the dangling symlink as not a small plain file.
func TestStrayCommondirDanglingSymlink(t *testing.T) {
	r := newTrustRepo(t)
	symlink(t, "nothing-here", filepath.Join(r.T, ".git", "commondir"))
	wantResult(t, "info(T)", info(t, r.T),
		`{"isRepo":true,"repo":"T","root":"`+jsonEscape(t, r.T)+`","repoSlug":"","defaultBranch":""}`)
	wantRPCError(t, "list_branches(T)", listBranches(t, r.T), "exit status 128")
	// The measured request is status with path = baseRepo = T.
	wantResult(t, "status(T,T)", status(t, r.T, r.T), notRepoStatus)
	c, wt := create(t, r.T)
	var res worktreeResult
	if err := json.Unmarshal(c.Result, &res); err != nil || res.Success || res.ErrorCode != "worktree_add_failed" ||
		!strings.HasPrefix(res.Error, "git worktree add failed: ") || !strings.Contains(res.Error, "commondir") {
		t.Errorf("create(T) = %s, want git's failure to read commondir", c.Result)
	}
	wantResult(t, "remove(T)", remove(t, r.T, wt, "w1"), `{"success":true,"branchKept":true}`)
}

// Rows DG3-g and DG3x-g (89cb6289, Linux VM). The request is a remove without
// worktreeRoot. The repository has a good HEAD and a dangling relative commondir
// symlink that stays in `.git`. The answer is success with branchKept. The worktree folder and its entry are
// deleted, and the branch stays, because git cannot list the refs. In row DG3x-g the
// folder is gone before the request, and the entry is deleted. These are pins of rows
// that were equal already. No fix stands behind them. The fixture lives in the test's
// temporary directory only.
func TestStrayCommondirDanglingSymlinkRemoveInRepository(t *testing.T) {
	for _, tc := range []struct {
		name     string
		leafGone bool
	}{
		{"DG3-g_worktree_present", false},
		{"DG3x-g_worktree_gone", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			entry := filepath.Join(r.T, ".git", "worktrees", "s0")
			branch := filepath.Join(r.T, ".git", "refs", "heads", "s0")
			if tc.leafGone {
				if err := os.RemoveAll(r.SW); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{entry, branch} {
				if !exists(p) {
					t.Fatalf("fixture: %s is missing", p)
				}
			}
			symlink(t, "nothing-here", filepath.Join(r.T, ".git", "commondir"))
			wantResult(t, "remove(T)", remove(t, r.T, r.SW, "s0"), `{"success":true,"branchKept":true}`)
			for _, p := range []string{r.SW, entry} {
				if exists(p) {
					t.Errorf("%s is still there", p)
				}
			}
			if !exists(branch) {
				t.Errorf("the branch file %s is gone", branch)
			}
		})
	}
}

// A git directory that the daemon cannot open (mode 0100 keeps stat and the Lstat of
// `commondir` working, but the open of the directory fails) gives S2 without the
// operation and path of the OS error (claustrum's own, not measured). Mutation: read
// the error from openErr instead of osErr in judgeStrayCommondir.
func TestStrayCommondirGitDirNotOpenable(t *testing.T) {
	skipIfRoot(t)
	r := newTrustRepo(t)
	g := filepath.Join(r.T, ".git")
	cd := filepath.Join(g, "commondir")
	writeFile(t, cd, ".", 0o644)
	if err := os.Chmod(g, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(g, 0o755) })
	got := gitDirTrustFor(r.T)
	if got.verdict != gitDirRefused || got.refusal != wantTP(cd, "permission denied") {
		t.Errorf("verdict %v, refusal %q", got.verdict, got.refusal)
	}
}

// unsearchableFixture makes the git stub log every git call, with no GIT_DIR and
// no GIT_COMMON_DIR in the daemon environment. It returns the log.
func unsearchableFixture(t *testing.T) string {
	t.Helper()
	skipIfRoot(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	installGitSlowStub(t, realGit)
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "env.log")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	return log
}

// noSearch takes the search bit from dir until the test ends.
func noSearch(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// wantUnsearchableNoRepo asserts the "no repository" frames of git.info, git.status
// and git.list_branches on path, and that each of their git calls in path carries
// GIT_DIR=<null device>.
func wantUnsearchableNoRepo(t *testing.T, log, path string) {
	t.Helper()
	for _, c := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.info", map[string]any{"path": path},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`},
		{"git.status", map[string]any{"path": path, "baseRepo": path},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`},
		{"git.list_branches", map[string]any{"path": path},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`},
	} {
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := dispatchRaw(t, newTestServer(t), rpcLine(t, c.method, c.params)); got != c.want {
			t.Errorf("%s = %s\nwant %s", c.method, got, c.want)
		}
		for _, call := range readEnvCalls(t, log) {
			if v, _ := envValue(call.env, "GIT_DIR"); call.cwd == path && v != os.DevNull {
				t.Errorf("%s: git %q in %s has GIT_DIR=%q, want %s", c.method, call.argv, path, v, os.DevNull)
			}
		}
	}
}

// A git directory that the daemon cannot search is no repository on git.info,
// git.status and git.list_branches, and each git call in the folder carries
// GIT_DIR=<null device>. For a daemon GIT_DIR that names a folder of mode 0600,
// git.info and git.status answer so (measured on Linux). A `.git` file that names
// such a folder, a linked-worktree entry and git.list_branches are claustrum's own,
// not measured. Mutations: return t unchanged from readGitDirTrust, drop the test
// of the GIT_DIR branch of gitDirTrustFor, call requestGitDirTrust in
// gitListBranches, or drop the noRepoPin answer of commonDirPinEnv.
func TestGitDirUnsearchableIsNoRepository(t *testing.T) {
	t.Run("daemon GIT_DIR", func(t *testing.T) {
		log := unsearchableFixture(t)
		base := realTempDir(t)
		g := filepath.Join(base, "G")
		work := filepath.Join(base, "w")
		for _, d := range []string{g, work} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("GIT_DIR", g)
		noSearch(t, g)
		wantUnsearchableNoRepo(t, log, work)
	})
	t.Run("git file", func(t *testing.T) {
		log := unsearchableFixture(t)
		base := realTempDir(t)
		g := filepath.Join(base, "G")
		if err := os.Mkdir(g, 0o755); err != nil {
			t.Fatal(err)
		}
		work := filepath.Join(base, "w")
		writeFile(t, filepath.Join(work, ".git"), "gitdir: "+g+"\n", 0o644)
		noSearch(t, g)
		wantUnsearchableNoRepo(t, log, work)
	})
	t.Run("entry", func(t *testing.T) {
		log := unsearchableFixture(t)
		r := newTrustRepo(t)
		noSearch(t, filepath.Join(r.T, ".git", "worktrees", filepath.Base(r.TW)))
		wantUnsearchableNoRepo(t, log, r.TW)
	})
}

// With a daemon GIT_COMMON_DIR, no git call could carry GIT_DIR=<null device>, so a
// git directory that the daemon cannot search keeps the trust refusal on git.status,
// and no git call runs in the folder (claustrum's own, not measured). Mutation:
// drop the daemonCommonDirSet test of readGitDirTrust.
func TestGitDirUnsearchableWithDaemonCommonDir(t *testing.T) {
	log := unsearchableFixture(t)
	base := realTempDir(t)
	g := filepath.Join(base, "G")
	if err := os.Mkdir(g, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "w")
	writeFile(t, filepath.Join(work, ".git"), "gitdir: "+g+"\n", 0o644)
	t.Setenv("GIT_COMMON_DIR", g)
	noSearch(t, g)
	wantErrorFrame(t, "git.status", map[string]any{"path": work, "baseRepo": work},
		"config-defined hooks could not be pinned off; git not run: this repository's git directory "+
			"could not be examined (permission denied); if .git is a file, check the path written in it")
	for _, call := range readEnvCalls(t, log) {
		if call.cwd == work {
			t.Errorf("git %q ran in %s", call.argv, work)
		}
	}
}

// A failed Lstat of `<G>/commondir` that is no search-permission case gets the
// could-not-be-examined text with the OS error without its path. Only "file name too
// long" is measured (683cc6a9, Linux). "not a directory" is claustrum's own, not
// measured. On Linux and macOS a G that is a file is no repository before this step
// (nonDirGitDirIsNoRepo), so the judge is called directly. Mutation: return false
// from unexaminedGitDir for each error but ENAMETOOLONG.
func TestGitDirUnexaminedNotADirectory(t *testing.T) {
	want := "config-defined hooks could not be pinned off; git not run: this repository's git directory " +
		"could not be examined (not a directory); if .git is a file, check the path written in it"
	g := filepath.Join(t.TempDir(), "G")
	writeFile(t, g, "", 0o644)
	got, refused := judgeStrayCommondir(g, filepath.Join(g, "commondir"))
	if !refused || got.verdict != gitDirRefused || got.refusal != want {
		t.Errorf("refused %v, verdict %v, refusal %q, want %q", refused, got.verdict, got.refusal, want)
	}
}
