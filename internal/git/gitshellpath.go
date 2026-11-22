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
// 87375cc7 tests each git on the PATH of the login shell in order. A git that is
// not laid out as an install (<prefix>/bin/git with a folder
// <prefix>/libexec/git-core or <prefix>/lib/git-core) runs no call, one log line
// names it, and the next git is tested (shellPathGitSkipLine, measured on macOS
// with two gits, and on Linux with one). The first git laid out so gets the call
// `<git> --exec-path`. git.info, git.list_branches, git.worktree_create and the
// pane's git of a "gitRead":true spawn then run it (measured on Linux).
// Claustrum's own, not measured: the walk past the first git on Linux, the rest
// of macOS, Windows has no login shell here, the call has the limit of
// shellPathGitExecPathBound, a git in a session folder runs no call and the walk
// goes on, a failed call gives the line with its error and ends the walk, the
// line comes once for each git, and the answer is kept for one value of the
// login-shell PATH.

// shellPathGitSkipLine is the line of a git of the login-shell PATH that the daemon
// does not use. The first %q is the git, then the reason, the state of the
// `--exec-path` call and its output.
const shellPathGitSkipLine = "[sessiondirs] not using %q from the login shell's PATH as Claude's git: %s: %s (exec path %q). If this host has no other git, sessions here get no worktree. Claude uses a git on the system's own PATH, or one installed as <prefix>/bin/git with <prefix>/libexec/git-core or <prefix>/lib/git-core"

// shellPathGitExecPathBound is the limit of the `--exec-path` call (claustrum's own,
// not measured: no row shows the bound of 87375cc7 or the kind of its stop).
var shellPathGitExecPathBound = 10 * time.Second

// shellPathGitKill ends the `--exec-path` call at its bound, through the handle of
// that one process. A test replaces it.
var shellPathGitKill = func(p *os.Process) { _ = p.Kill() }

// shellPathGitWaited runs right after the wait of the `--exec-path` call. A test
// sets it to run a stop late, as the watch of the context of Go can.
var shellPathGitWaited = func(*exec.Cmd) {}

// shellPathValue is the PATH of the login shell that a read already gave, after
// a wait for a read that runs (shellenv.AwaitRunningLoginPATH). A git call never
// starts a read. With no result there is no login git (claustrum's own, not
// measured). A test replaces it.
var shellPathValue = func() string {
	shellenv.AwaitRunningLoginPATH()
	return shellenv.CurrentLoginPATH()
}

var (
	shellPathGitMu     sync.Mutex
	shellPathGitDone   bool
	shellPathGitFor    string          // the login-shell PATH of the answer
	shellPathGitPath   string          // the git that the answer accepts, or ""
	shellPathGitAt     time.Time       // the end of the walk of the answer
	shellPathGitLogged map[string]bool // the gits that the skip line named
)

// ShellPathGit is the git of the login-shell PATH that the git methods run, or ""
// when the PATH of the daemon holds a git, or when the login-shell PATH holds
// none that is laid out as an install.
func ShellPathGit() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if _, err := exec.LookPath("git"); !errors.Is(err, exec.ErrNotFound) {
		return ""
	}
	path := shellPathValue()
	shellPathGitMu.Lock()
	defer shellPathGitMu.Unlock()
	// A git that the answer accepts holds for one value of the login-shell PATH,
	// also its session-folder test. An answer with no git holds for
	// shadowFailureKeep after its walk, as a failed start test does. A change of
	// the off switch or of the session folders shows only after that. Each git
	// call still runs sessiondirs.PickGit at its start (gitCommand). Claustrum's
	// own, not measured.
	if shellPathGitDone && shellPathGitFor == path && (shellPathGitPath != "" || shadowClock().Sub(shellPathGitAt) < shadowFailureKeep) {
		return shellPathGitPath
	}
	shellPathGitDone, shellPathGitFor = true, path
	shellPathGitPath = walkShellPath(path)
	shellPathGitAt = shadowClock()
	return shellPathGitPath
}

// walkShellPath tests each git on path in order and returns the first one that
// the daemon uses, or "". The caller holds shellPathGitMu.
func walkShellPath(path string) string {
	for _, git := range gitsOn(path) {
		if !laidOutAsInstall(git) {
			logShellPathGitSkip(git, "not laid out as an install", "not started", "")
			continue
		}
		if inSessionFolder(git) {
			continue
		}
		reason, state, execPath := shellPathGitTest(git)
		if reason == "" {
			return git
		}
		logShellPathGitSkip(git, reason, state, execPath)
		return ""
	}
	return ""
}

// logShellPathGitSkip logs shellPathGitSkipLine for git, once for each git.
// The caller holds shellPathGitMu.
func logShellPathGitSkip(git, reason, state, execPath string) {
	if shellPathGitLogged == nil {
		shellPathGitLogged = map[string]bool{}
	}
	if !shellPathGitLogged[git] {
		shellPathGitLogged[git] = true
		logging.Infof(shellPathGitSkipLine, git, reason, state, execPath)
	}
}

// gitsOn lists the executable gits in the absolute folders of path, in order.
func gitsOn(path string) []string {
	var gits []string
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, "git")); err == nil {
			gits = append(gits, p)
		}
	}
	return gits
}

// laidOutAsInstall reports whether git is <prefix>/bin/git with a folder
// <prefix>/libexec/git-core or <prefix>/lib/git-core. It runs no git.
func laidOutAsInstall(git string) bool {
	bin := filepath.Dir(git)
	prefix := filepath.Dir(bin)
	return filepath.Base(bin) == "bin" && (isFolder(filepath.Join(prefix, "libexec", "git-core")) || isFolder(filepath.Join(prefix, "lib", "git-core")))
}

// inSessionFolder reports whether the check of session folders passes over git,
// as at the start of every git call (sessiondirs.PickGit). Such a git runs no
// call, and the walk goes on (claustrum's own, not measured).
func inSessionFolder(git string) bool {
	cmd := exec.Command(git)
	sessiondirs.PickGit(cmd)
	return cmd.Err != nil || cmd.Path != git
}

// shellPathGitTest runs the `--exec-path` call of git, a git of the login-shell
// PATH laid out as an install. reason is "" for a git that the daemon uses. Else
// it is the reason, state is the state of the call, and execPath is its output.
func shellPathGitTest(git string) (reason, state, execPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), shellPathGitExecPathBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "--exec-path")
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
// the call gets a kill of that process only, and only while it is not reaped
// (sysproc.LeaderGate). The gate closes after its exit and before the wait reaps
// it, so a late stop from the watch of the context of Go sends nothing (claustrum's
// own, from the code).
//
// The wait for stdout ends shadowCallDrain after the exit or the bound, so a child
// of git that holds stdout does not hold the call. A call that exited 0 then
// counts as a success, with the output read so far. The kill of Go at the end of
// that wait goes through the handle of the process, which sends nothing after the
// reap (from the code of Go).
func runExecPath(cmd *exec.Cmd) error {
	gate := &sysproc.LeaderGate{}
	cmd.Cancel = func() error {
		gate.Do(func() { shellPathGitKill(cmd.Process) })
		return nil
	}
	cmd.WaitDelay = shadowCallDrain
	if err := cmd.Start(); err != nil {
		return err
	}
	gate.CloseAfterExit(cmd.Process, sysproc.AwaitExit(cmd.Process.Pid))
	err := cmd.Wait()
	shellPathGitWaited(cmd)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}

// isFolder reports whether p is a folder, a link to one included.
func isFolder(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
