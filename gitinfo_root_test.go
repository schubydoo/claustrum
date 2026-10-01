package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The root, the repo name and the default branch of git.info. Each test names the
// mutation that turns it red.

// infoRoot is the root git.info answers for the repository folder dir: the walk root on
// Linux and macOS, and git's own spelling on Windows.
func infoRootOf(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.ToSlash(canonicalPath(dir))
	}
	return dir
}

// infoFields decodes a git.info result into its members.
func infoFields(t *testing.T, r rpcReply) map[string]any {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("info = %+v, want a result", r.Error)
	}
	var m map[string]any
	if err := json.Unmarshal(r.Result, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// On Linux and macOS git.info runs no `rev-parse --show-toplevel`. On a
// plain repository it makes 9 calls: the excludes read, and four listings each
// before rev-parse --git-dir, remote get-url, symbolic-ref and branch --show-current
// (row L01). Mutation: keep the show-toplevel call.
func TestInfoRunsNoShowToplevel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows git confirms the root (TestInfoRootWindows)")
	}
	f := newListingFixture(t)
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	// The root has its symlinks resolved. The temporary directory of macOS is behind
	// one.
	if root := canonicalPath(f.top); !strings.Contains(raw, `"root":"`+jsonEscape(t, root)+`"`) {
		t.Errorf("info = %s, want root %s", raw, root)
	}
	var argv [][]string
	for _, c := range calls {
		argv = append(argv, c.argv)
		if slices.Contains(c.argv, "--show-toplevel") {
			t.Errorf("call %q asks git for the top level", c.argv)
		}
	}
	if len(calls) != 9 {
		t.Errorf("calls = %q, want 9", argv)
	}
}

// With the daemon's GIT_DIR naming a repository and a plain folder as
// path, `rev-parse --git-dir` succeeds, but the walk from the folder finds nothing, so
// the answer is the bare shape (row I04a). Mutation: take the root from git's
// environment.
func TestInfoRootIgnoresDaemonGitDir(t *testing.T) {
	f := newListingFixture(t)
	plain := t.TempDir()
	requireTempOutsideCheckout(t, plain)
	t.Setenv("GIT_DIR", filepath.Join(f.top, ".git"))
	raw, calls := f.call(t, "git.info", map[string]any{"path": plain})
	if got := replyPart(t, raw); got != notRepoInfo {
		t.Errorf("info = %s, want %s", got, notRepoInfo)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--git-dir") }) {
		t.Errorf("no rev-parse --git-dir ran")
	}
}

// core.worktree in the repository does not move the root (row I02). With
// the daemon's GIT_DIR and GIT_WORK_TREE set and a plain folder as path, the answer is
// the bare shape, where git itself names GIT_WORK_TREE (row I04b). Mutation: take the
// root from git's --show-toplevel.
func TestInfoRootIgnoresCoreWorktree(t *testing.T) {
	r := newTrustRepo(t)
	w := filepath.Join(r.base, "W")
	if err := os.Mkdir(w, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, r.T, "config", "core.worktree", w)
	m := infoFields(t, info(t, r.T))
	if m["root"] != infoRootOf(r.T) || m["repo"] != "T" {
		t.Errorf("info = %v, want root %s and repo T", m, infoRootOf(r.T))
	}
	runGit(t, r.T, "config", "--unset", "core.worktree")
	plain := filepath.Join(r.base, "P")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(r.T, ".git"))
	t.Setenv("GIT_WORK_TREE", w)
	wantResult(t, "info(P)", info(t, plain), notRepoInfo)
}

// The branch origin/HEAD names is verified as a commit. A dangling one and
// one that names a blob give "", and so does a name that starts with "-", even when
// it resolves (rows I10a to I10c). A valid one is kept. Mutations: skip the verify, or
// verify the "-" name.
func TestInfoDefaultBranchIsVerified(t *testing.T) {
	r := newTrustRepo(t)
	gitDir := filepath.Join(r.T, ".git")
	head := strings.TrimSpace(gitOut(t, r.T, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(r.T, "blob.txt"), "blob\n", 0o644)
	blob := strings.TrimSpace(gitOut(t, r.T, "hash-object", "-w", "blob.txt"))
	writeFile(t, filepath.Join(gitDir, "refs", "remotes", "origin", "main"), head+"\n", 0o644)
	writeFile(t, filepath.Join(gitDir, "refs", "remotes", "origin", "-x"), head+"\n", 0o644)
	writeFile(t, filepath.Join(gitDir, "refs", "remotes", "origin", "x"), blob+"\n", 0o644)
	originHead := filepath.Join(gitDir, "refs", "remotes", "origin", "HEAD")
	for _, tc := range []struct{ target, want string }{
		{"main", "main"},
		{"gone", ""},
		{"x", ""},
		{"-x", ""},
	} {
		t.Run(tc.target, func(t *testing.T) {
			writeFile(t, originHead, "ref: refs/remotes/origin/"+tc.target+"\n", 0o644)
			if got := infoFields(t, info(t, r.T))["defaultBranch"]; got != tc.want {
				t.Errorf("defaultBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

// A root whose name starts with "-" or "+" leaves the repo member out, and
// the other members keep their order (rows I11a and I11b). Mutation: always emit the
// member.
func TestInfoRepoMemberLeftOutForDashAndPlus(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	for _, name := range []string{"-repo", "+repo", "R"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(base, name)
			initTrustMain(t, dir)
			r := info(t, dir)
			want := `{"isRepo":true,"repo":"` + name + `","branch":"main","root":"` + jsonEscape(t, infoRootOf(dir)) + `"`
			if name != "R" {
				want = `{"isRepo":true,"branch":"main","root":"` + jsonEscape(t, infoRootOf(dir)) + `"`
			}
			wantResultPrefix(t, "info", r, want)
		})
	}
}
