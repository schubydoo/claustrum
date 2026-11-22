//go:build !windows

package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
	"github.com/schubydoo/claustrum/internal/shellenv"
)

// loginGitFixture makes the PATH of the daemon hold no git, and the login-shell
// PATH the folders of dirs. It forgets the answer of LoginGit before and after.
func loginGitFixture(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	old := loginShellPATH
	loginShellPATH = func() string { return strings.Join(dirs, string(os.PathListSeparator)) }
	resetLoginGitForTest()
	t.Cleanup(func() {
		loginShellPATH = old
		resetLoginGitForTest()
	})
}

// fakeLoginGit writes an executable git script at path. It logs each call to
// path.log, prints execPath for --exec-path with exit code code, and runs the
// real git for any other call.
func fakeLoginGit(t *testing.T, path, execPath string, code int, realGit string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"call: $*\" >> \"" + path + ".log\"\n" +
		"if [ \"$1\" = --exec-path ]; then echo \"" + execPath + "\"; exit " + strconv.Itoa(code) + "; fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestLoginGitUsesAnInstall: with no git on the PATH of the daemon, a git of the
// login-shell PATH laid out as <prefix>/bin/git with <prefix>/libexec/git-core
// or <prefix>/lib/git-core is the git of the git calls, after its --exec-path
// call. gitLookupError then gives nil (87375cc7, measured on Linux with
// libexec). Mutation: keep the bare name when PATH finds no git (the command
// does not start).
func TestLoginGitUsesAnInstall(t *testing.T) {
	realGit := requireRealGit(t)
	for _, lib := range []string{"libexec", "lib"} {
		t.Run(lib, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "pfx")
			if err := os.MkdirAll(filepath.Join(prefix, lib, "git-core"), 0o755); err != nil {
				t.Fatal(err)
			}
			git := filepath.Join(prefix, "bin", "git")
			fakeLoginGit(t, git, filepath.Join(prefix, lib, "git-core"), 0, realGit)
			loginGitFixture(t, filepath.Join(t.TempDir(), "none"), filepath.Dir(git))
			if got := LoginGit(); got != git {
				t.Fatalf("LoginGit = %q, want %q", got, git)
			}
			if err := gitLookupError(); err != nil {
				t.Errorf("gitLookupError = %v, want nil", err)
			}
			cmd := gitCommand("version")
			if cmd.Err != nil || cmd.Path != git {
				t.Fatalf("gitCommand: path %q, error %v, want %q", cmd.Path, cmd.Err, git)
			}
			if out, err := cmd.Output(); err != nil || !strings.HasPrefix(string(out), "git version") {
				t.Errorf("git version through the login git = %q, %v", out, err)
			}
			if log, _ := os.ReadFile(git + ".log"); !strings.Contains(string(log), "call: --exec-path\n") {
				t.Errorf("calls of the login git = %q, want --exec-path", log)
			}
		})
	}
}

// TestLoginGitSkipsANonInstall: a git of the login-shell PATH that is not laid
// out as an install runs no call, the daemon uses none, and one line names it
// (87375cc7, measured on Linux). A create then answers git_not_found. With no git
// on either PATH there is no line. Mutations: use that git (LoginGit names it), or
// log no line.
func TestLoginGitSkipsANonInstall(t *testing.T) {
	realGit := requireRealGit(t)
	git := filepath.Join(t.TempDir(), "lb", "git")
	fakeLoginGit(t, git, "/x", 0, realGit)
	loginGitFixture(t, filepath.Dir(git))
	logs := captureLogBuf(t)
	for range 2 {
		if got := LoginGit(); got != "" {
			t.Fatalf("LoginGit = %q, want none", got)
		}
	}
	if err := gitLookupError(); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("gitLookupError = %v, want the error of a git that PATH does not find", err)
	}
	line := fmtLoginGitSkipLine(git, "not laid out as an install", "not started", "")
	if n := strings.Count(logs.String(), line+"\n"); n != 1 {
		t.Errorf("log:\n%s\nwant the line %q once, got %d", logs, line, n)
	}
	if log, err := os.ReadFile(git + ".log"); err == nil {
		t.Errorf("calls of the login git = %q, want none", log)
	}

	loginGitFixture(t, t.TempDir())
	logs.Reset()
	if got := LoginGit(); got != "" || logs.String() != "" {
		t.Errorf("no git: LoginGit = %q, log %q, want none", got, logs)
	}
}

// TestLoginGitFailedExecPath: a git laid out as an install whose --exec-path
// call fails is not used, and its line gives the error (claustrum's own, not
// measured). Mutation: ignore the exit code of the call.
func TestLoginGitFailedExecPath(t *testing.T) {
	realGit := requireRealGit(t)
	prefix := filepath.Join(t.TempDir(), "pfx")
	if err := os.MkdirAll(filepath.Join(prefix, "libexec", "git-core"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(prefix, "bin", "git")
	fakeLoginGit(t, git, "/x", 1, realGit)
	loginGitFixture(t, filepath.Dir(git))
	logs := captureLogBuf(t)
	if got := LoginGit(); got != "" {
		t.Fatalf("LoginGit = %q, want none", got)
	}
	line := fmtLoginGitSkipLine(git, "its --exec-path call failed", "exit status 1", "/x")
	if !strings.Contains(logs.String(), line+"\n") {
		t.Errorf("log:\n%s\nwant the line %q", logs, line)
	}
}

// installedLoginGit writes script as <prefix>/bin/git with a folder
// <prefix>/libexec/git-core, and makes it the git of the login-shell PATH.
func installedLoginGit(t *testing.T, script string) string {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "pfx")
	if err := os.MkdirAll(filepath.Join(prefix, "libexec", "git-core"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(prefix, "bin", "git")
	if err := os.MkdirAll(filepath.Dir(git), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(git, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loginGitFixture(t, filepath.Dir(git))
	return git
}

// countLoginGitKills replaces loginGitKill with a fake that counts its calls and
// then kills the process of the test through its handle.
func countLoginGitKills(t *testing.T) *atomic.Int32 {
	t.Helper()
	var kills atomic.Int32
	prev := loginGitKill
	t.Cleanup(func() { loginGitKill = prev })
	loginGitKill = func(p *os.Process) {
		kills.Add(1)
		_ = p.Kill()
	}
	return &kills
}

// TestLoginGitExecPathBoundKillsOnce: an --exec-path call that runs past its
// bound gets one kill through loginGitKill, and its git is not used (claustrum's
// own, not measured). Mutation: drop the cmd.Cancel of runExecPath, and the kill
// of Go does not go through loginGitKill.
func TestLoginGitExecPathBoundKillsOnce(t *testing.T) {
	git := installedLoginGit(t, "PATH=/usr/bin:/bin exec sleep 10")
	kills := countLoginGitKills(t)
	prev := loginGitExecPathBound
	t.Cleanup(func() { loginGitExecPathBound = prev })
	loginGitExecPathBound = time.Second
	logs := captureLogBuf(t)
	if got := LoginGit(); got != "" {
		t.Fatalf("LoginGit = %q, want none", got)
	}
	if n := kills.Load(); n != 1 {
		t.Errorf("kills = %d, want 1", n)
	}
	line := fmtLoginGitSkipLine(git, "its --exec-path call failed", "signal: killed", "")
	if !strings.Contains(logs.String(), line+"\n") {
		t.Errorf("log:\n%s\nwant the line %q", logs, line)
	}
}

// TestLoginGitLateStopSendsNoKill: Go runs cmd.Cancel from its watch of the
// context, which can come after the wait reaped the --exec-path call. That call
// sends no kill (claustrum's own, from the code). The test makes the late call
// itself, right after the wait. Mutation: call loginGitKill outside the gate.
func TestLoginGitLateStopSendsNoKill(t *testing.T) {
	installedLoginGit(t, "echo /x")
	kills := countLoginGitKills(t)
	var stops atomic.Int32
	prev := loginGitWaited
	t.Cleanup(func() { loginGitWaited = prev })
	loginGitWaited = func(c *exec.Cmd) {
		if c.Cancel != nil {
			stops.Add(1)
			_ = c.Cancel()
		}
	}
	if got := LoginGit(); got == "" {
		t.Fatal("LoginGit = none, want the login git")
	}
	if stops.Load() != 1 {
		t.Fatalf("late stops = %d, want 1", stops.Load())
	}
	if n := kills.Load(); n != 0 {
		t.Errorf("kills after the reap = %d, want 0", n)
	}
}

// TestLoginGitInASessionFolderRunsNoCall: a git of the login-shell PATH in a
// session folder is passed over before its --exec-path call, as at the start of
// every git call. With no other git it runs no call, and the daemon uses none
// (claustrum's own, not measured). Mutation: drop the PickGit of loginGitTest.
func TestLoginGitInASessionFolderRunsNoCall(t *testing.T) {
	git := installedLoginGit(t, "echo call >> \"$0.log\"\necho /x")
	t.Cleanup(sessiondirs.UseForTest(""))
	if err := sessiondirs.Remember(filepath.Dir(filepath.Dir(git))); err != nil {
		t.Fatal(err)
	}
	logs := captureLogBuf(t)
	if got := LoginGit(); got != "" {
		t.Fatalf("LoginGit = %q, want none", got)
	}
	if log, err := os.ReadFile(git + ".log"); err == nil {
		t.Errorf("calls of the login git = %q, want none", log)
	}
	if !strings.Contains(logs.String(), "[sessiondirs] passing over") {
		t.Errorf("log:\n%s\nwant the passing-over line", logs)
	}
}

// TestLoginGitStartsNoLoginShellRead: with no git on the PATH of the daemon, a git
// call reads the login-shell PATH that a read gave, and starts no read itself
// (claustrum's own, not measured). The control at the end shows that the fake
// counts the read of a spawn. Mutation: call shellenv.AwaitLoginPATH in
// loginShellPATH, and the git call starts a read.
func TestLoginGitStartsNoLoginShellRead(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	shellenv.ResetForTest()
	resetLoginGitForTest()
	t.Cleanup(func() {
		shellenv.ResetForTest()
		resetLoginGitForTest()
	})
	var starts atomic.Int32
	prev := shellenv.LoginPATHExtractor
	t.Cleanup(func() { shellenv.LoginPATHExtractor = prev })
	empty := t.TempDir()
	shellenv.LoginPATHExtractor = func(context.Context) (string, error) {
		starts.Add(1)
		return empty, nil
	}
	shellenv.UseClockForTest(time.Now)
	shellenv.ArmForTest()
	if cmd := gitCommand("version"); cmd.Err == nil {
		t.Fatalf("gitCommand found %q, want no git", cmd.Path)
	}
	if !noGitStarts() {
		t.Error("noGitStarts = false with no git")
	}
	if n := starts.Load(); n != 0 {
		t.Errorf("login-shell reads started by git calls = %d, want 0", n)
	}
	shellenv.AwaitLoginPATH(false)
	if n := starts.Load(); n != 1 {
		t.Errorf("control: reads started by a spawn = %d, want 1", n)
	}
}

// requireRealGit is the path of the git of the host, or a skip.
func requireRealGit(t *testing.T) string {
	t.Helper()
	requireGit(t)
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	return p
}

// resetLoginGitForTest forgets the answer of LoginGit.
func resetLoginGitForTest() {
	loginGitMu.Lock()
	defer loginGitMu.Unlock()
	loginGitDone, loginGitFor, loginGitPath, loginGitLogged = false, "", "", nil
}

// fmtLoginGitSkipLine is loginGitSkipLine with its values.
func fmtLoginGitSkipLine(git, reason, state, execPath string) string {
	return fmt.Sprintf(loginGitSkipLine, git, reason, state, execPath)
}
