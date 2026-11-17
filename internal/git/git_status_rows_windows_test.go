//go:build windows

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows forms of TestGitStatusDotDotRows and TestGitStatusFailingCalls. The
// Windows row on 4c111e86 gives the frames of Linux for each of these rows, with a
// git that takes the excludes file of the daemon. The suite gives the daemon a user
// excludes file (windowsUserExcludes), so no NUL reaches git.

// Measured on Windows: a `..` component in a path inside baseRepo answers
// isRepo:false, with back slashes and with forward slashes. Outside baseRepo it
// passes, and so does a `.`.
func TestGitStatusWindowsDotDotRows(t *testing.T) {
	f := newStatusFixture(t)
	wt := filepath.Join(f.T, ".claude", "worktrees")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", filepath.Join(wt, "s0"))
	for _, d := range []string{filepath.Join(wt, "q"), filepath.Join(f.W, "sub")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ name, path, want string }{
		{"outside, folder there", f.W + `\sub\..`, statusClean},
		{"inside, no folder", wt + `\s0\missing\..`, statusNotRepo},
		{"inside, no folder before the leaf", wt + `\none\..\s0`, statusNotRepo},
		{"inside, folder there", wt + `\q\..\s0`, statusNotRepo},
		{"dot", wt + `\s0\.`, statusClean},
	} {
		for _, path := range []string{c.path, filepath.ToSlash(c.path)} {
			if got := statusFrame(t, path, f.T); got != c.want {
				t.Errorf("%s (%s): git.status = %s\nwant %s", c.name, path, got, c.want)
			}
		}
	}
}

// statusCallCount puts the logging git stand-in on PATH and returns a function that
// runs one git.status and gives its frame and the number of its git calls. As in
// statusCallLog, each request counts as a first request on its repository.
func statusCallCount(t *testing.T) func(path, base string) (string, int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range daemonGitKeys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	h := newTestGitDeps()
	return func(path, base string) (string, int) {
		t.Helper()
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		forgetRepoVerdicts()
		raw := gitDispatch(t, h, rpcLine(t, "git.status", map[string]any{"path": path, "baseRepo": base}))
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return raw, strings.Count(string(b), "\n")
	}
}

// Measured on Windows: one failing git call each gives the frame of Linux. The
// counts are those of the Linux form. The Windows row counts the same calls on
// 4c111e86 and on claustrum.
func TestGitStatusWindowsFailingCalls(t *testing.T) {
	f := newStatusFixture(t)
	run := statusCallCount(t)
	if raw, _ := run(f.W, f.T); raw != statusClean {
		t.Fatalf("control = %s", raw)
	}
	exit1 := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"exit status 1"}}`
	for _, c := range []struct {
		name, match, want string
		calls             int
	}{
		{"absolute-git-dir", "rev-parse,--absolute-git-dir", statusNotRepo, 3},
		{"show-toplevel", "rev-parse,--show-toplevel", statusNotRepo, 5},
		{"status", "status,--porcelain", exit1, 9},
		{"ls-files", "ls-files,-s", exit1, 13},
		{"diff-index", "diff-index,--cached", exit1, 21},
		{"hash-object", "hash-object", statusClean, 21},
		// The listing with --git-dir=<common>, which has back slashes on Windows.
		{"listing in the common directory", "--git-dir=" + filepath.Join(f.T, ".git") + ",--list",
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 1: boom"}}`, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			stderr := ""
			if c.name == "listing in the common directory" {
				stderr = "boom"
			}
			slowGit(t, c.match, "fail", 0, stderr, "")
			raw, calls := run(f.W, f.T)
			if raw != c.want {
				t.Errorf("frame = %s\nwant %s", raw, c.want)
			}
			if calls != c.calls {
				t.Errorf("%d calls, want %d", calls, c.calls)
			}
		})
	}
}
