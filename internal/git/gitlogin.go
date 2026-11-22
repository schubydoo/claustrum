package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/sessiondirs"
	"github.com/schubydoo/claustrum/internal/shellenv"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The git of the login-shell PATH. When the PATH of the daemon holds no git,
// 87375cc7 runs the first git on the PATH of the login shell, if it is laid out
// as an install: <prefix>/bin/git with a folder <prefix>/libexec/git-core or
// <prefix>/lib/git-core. Before it uses that git it runs `<git> --exec-path`.
// git.info, git.list_branches, git.worktree_create and the pane's git of a
// "gitRead":true spawn then run it (measured on Linux). A git that is not laid
// out so runs no call, and one log line names it (loginGitSkipLine, measured on
// Linux and macOS). Claustrum's own, not measured: the rest of macOS follows
// Linux, Windows has no login shell here, the call has the limit of
// loginGitExecPathBound, a git in a session folder runs no call, a call that
// fails gives the line with its error, the line comes once for each git, and
// the answer is kept for one value of the login-shell PATH.

// loginGitSkipLine is the line of a git of the login-shell PATH that the daemon
// does not use. The first %q is the git, then the reason, the state of the
// `--exec-path` call and its output.
const loginGitSkipLine = "[sessiondirs] not using %q from the login shell's PATH as Claude's git: %s: %s (exec path %q). If this host has no other git, sessions here get no worktree. Claude uses a git on the system's own PATH, or one installed as <prefix>/bin/git with <prefix>/libexec/git-core or <prefix>/lib/git-core"

// loginGitExecPathBound is the limit of the `--exec-path` call (claustrum's own,
// not measured: no row shows the bound of 87375cc7 or the kind of its stop).
var loginGitExecPathBound = 10 * time.Second

// loginGitKill ends the `--exec-path` call at its bound, through the handle of
// that one process. A test replaces it.
var loginGitKill = func(p *os.Process) { _ = p.Kill() }

// loginGitWaited runs right after the wait of the `--exec-path` call. A test
// sets it to run a stop late, as the watch of the context of Go can.
var loginGitWaited = func(*exec.Cmd) {}

// loginShellPATH is the PATH of the login shell that a read already gave, after
// a wait for a read that runs (shellenv.AwaitRunningLoginPATH). A git call never
// starts a read. With no result there is no login git (claustrum's own, not
// measured). A test replaces it.
var loginShellPATH = func() string {
	shellenv.AwaitRunningLoginPATH()
	return shellenv.CurrentLoginPATH()
}

var (
	loginGitMu     sync.Mutex
	loginGitDone   bool
	loginGitFor    string          // the login-shell PATH of the answer
	loginGitPath   string          // the git that the answer accepts, or ""
	loginGitLogged map[string]bool // the gits that the skip line named
)

// LoginGit is the git of the login-shell PATH that the git methods run, or ""
// when the PATH of the daemon holds a git, or when the login-shell PATH holds
// none that is laid out as an install.
func LoginGit() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if _, err := exec.LookPath("git"); !errors.Is(err, exec.ErrNotFound) {
		return ""
	}
	path := loginShellPATH()
	loginGitMu.Lock()
	defer loginGitMu.Unlock()
	if loginGitDone && loginGitFor == path {
		return loginGitPath
	}
	loginGitDone, loginGitFor, loginGitPath = true, path, ""
	git := firstGitOn(path)
	if git == "" {
		return ""
	}
	reason, state, execPath := loginGitTest(git)
	if reason == "" {
		loginGitPath = git
		return git
	}
	if loginGitLogged == nil {
		loginGitLogged = map[string]bool{}
	}
	if !loginGitLogged[git] {
		loginGitLogged[git] = true
		logging.Infof(loginGitSkipLine, git, reason, state, execPath)
	}
	return ""
}

// firstGitOn is the first executable git in the absolute folders of path, or "".
func firstGitOn(path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, "git")); err == nil {
			return p
		}
	}
	return ""
}

// loginGitTest tests git, a git of the login-shell PATH. reason is "" for a git
// that the daemon uses. Else it is the reason, state is the state of the
// `--exec-path` call, and execPath is its output. A git that is not laid out as
// an install runs no call: "not started" with no output (measured on Linux).
func loginGitTest(git string) (reason, state, execPath string) {
	bin := filepath.Dir(git)
	prefix := filepath.Dir(bin)
	laidOut := filepath.Base(bin) == "bin" && (isFolder(filepath.Join(prefix, "libexec", "git-core")) || isFolder(filepath.Join(prefix, "lib", "git-core")))
	if !laidOut {
		return "not laid out as an install", "not started", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginGitExecPathBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "--exec-path")
	// A git in a session folder is passed over, as at the start of every git
	// call (gitCommand). With no other git the call does not start (claustrum's
	// own, not measured).
	sessiondirs.PickGit(cmd)
	cmd.Dir = gitVersionDir
	cmd.Env = gitChildEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	err := runExecPath(cmd)
	execPath = strings.TrimSpace(out.String())
	if err != nil {
		return "its --exec-path call failed", err.Error(), execPath
	}
	return "", "", execPath
}

// runExecPath starts cmd, the `--exec-path` call, and waits for it. At its bound
// the call gets one kill of that process only, and only while it is not reaped
// (sysproc.LeaderGate). The gate closes after its exit and before the wait reaps
// it, so a late stop from the watch of the context of Go sends nothing (claustrum's
// own, from the code).
func runExecPath(cmd *exec.Cmd) error {
	gate := &sysproc.LeaderGate{}
	cmd.Cancel = func() error {
		gate.Do(func() { loginGitKill(cmd.Process) })
		return nil
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	gate.CloseAfterExit(cmd.Process, sysproc.AwaitExit(cmd.Process.Pid))
	err := cmd.Wait()
	loginGitWaited(cmd)
	return err
}

// isFolder reports whether p is a folder, a link to one included.
func isFolder(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
