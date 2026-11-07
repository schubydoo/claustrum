package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// On Windows, with no user excludes file, the status call of git.status passes the null
// device NUL as core.excludesFile. Git for Windows 2.55 answers "fatal: cannot use NUL as
// an exclude file" with exit 128. c9125eb8 then answers -32603 "exit status 128"
// (measured on Windows). The stub plays that git, so the test needs no git 2.55. It
// refuses a status call that passes the null device of this OS. On Linux and macOS
// that device is /dev/null, so there the row only shows how the refusal passes through.
func TestGitStatusNullDeviceExcludesStopsTheStatus(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f := newStatusFixture(t)
	installGitSlowStub(t, realGit)
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	empty := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	t.Setenv("USERPROFILE", empty)
	resetUserExcludesCache(t)
	stageGit(t, stagedCall{Match: []string{"status", "--porcelain", "core.excludesFile=" + os.DevNull}, Exit: 128,
		Stderr: "fatal: cannot use " + os.DevNull + " as an exclude file\n"})

	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"exit status 128"}}`
	if got := f.status(t); got != want {
		t.Fatalf("git.status = %s\nwant %s", got, want)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var status int
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		argv := strings.Split(line, "\x1f")
		switch {
		case slices.Contains(argv, "status"):
			status++
			if !slices.Contains(argv, "core.excludesFile="+os.DevNull) {
				t.Errorf("status call %q, want core.excludesFile=%s", argv, os.DevNull)
			}
		case status > 0 && (slices.Contains(argv, "ls-files") || slices.Contains(argv, "diff-index")):
			t.Errorf("call %q after the failed status call, want none", argv)
		}
	}
	if status != 1 {
		t.Errorf("%d status calls, want 1:\n%s", status, b)
	}
}
