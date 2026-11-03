package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// The managed launcher (reference build 89cb6289). A host administrator names a
// launcher in the managed settings. The launcher is a program that runs the Claude
// Code CLI on this host: the daemon puts its argv in front of the command.
// Three surfaces use it:
//
//   - launcher.resolve reads the managed settings and answers none, usable,
//     unusable or unreadable (handleLauncher below).
//   - process.spawn takes a `launcher` param and runs the child through it, or
//     refuses the spawn (refuseManagedLauncher, spawnVia in process.go).
//   - -install and -probe-cli run the CLI through the launcher, but only when
//     CLAUDE_SSH_MANAGED_LAUNCHER=1 (internal/install/managedlauncher_run.go).
//
// Every rule below is measured against 89cb6289 unless its comment says "claustrum's
// choice (not measured)". Every text is copied byte for byte from a measured capture.
//
// The word "launcher" alone already names the -serve parent process in server.go
// and dahandoff.go, so the identifiers here say "managed launcher".

// managedLauncherLog writes the [LauncherHandler] line of a launcher.resolve
// answer. An unusable or unreadable answer writes one line, a usable or none answer
// writes none. A repeat of the last answer writes no line, and a usable answer in
// between makes the next equal refusal log again (measured).
//
// claustrum's choices (not measured) follow. The key is the whole line, so the same
// reason from another file logs again. A none answer resets it like a usable one.
// The key lives for one daemon. The lines log at WARN.
type managedLauncherLog struct {
	mu   sync.Mutex
	last string
	// ignoredVarLogged is set once the ignored-variable line was written.
	ignoredVarLogged bool
}

// firstIgnoredVar reports true the first time only, so the ignored-variable line
// logs once per daemon.
func (l *managedLauncherLog) firstIgnoredVar() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := !l.ignoredVarLogged
	l.ignoredVarLogged = true
	return first
}

func (l *managedLauncherLog) note(res rpc.ManagedLauncherResult) {
	line := ""
	switch res.Status {
	case install.StatusUnusable:
		line = "[LauncherHandler] managed launcher from " + res.Source + " refused: " + res.Reason
	case install.StatusUnreadable:
		line = "[LauncherHandler] managed settings unreadable: " + res.Path + ": " + res.Reason
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if line == l.last {
		return
	}
	l.last = line
	if line != "" {
		logging.Warnf("%s", line)
	}
}

type managedLauncherParams struct {
	CliPath string `json:"cliPath"`
}

// ExpandPaths is a no-op. claustrum's choice (not measured): cliPath is not
// `~`-expanded, so the own-path check compares the caller's text.
func (p *managedLauncherParams) ExpandPaths() {}

// handleLauncher serves the launcher namespace. Its one method is launcher.resolve.
// No params member, a non-object params or a non-string cliPath is -32602 "Invalid
// params". {}, null params, a null or empty cliPath and a wrong key are -32602
// "cliPath is required". Any other string is a cliPath, a relative one too.
func (s *server) handleLauncher(req *request) rpc.Response {
	if req.Method != "launcher.resolve" {
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	var p managedLauncherParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.CliPath == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "cliPath is required")
	}
	res, note := install.Resolve(p.CliPath)
	if note != "" && s.managedLog.firstIgnoredVar() {
		logging.Infof("%s", note)
	}
	s.managedLog.note(res)
	return rpc.OKResult(req.ID, res)
}

// The process.spawn texts of the launcher param.
const (
	managedSpawnRelative  = "command must be an absolute path when a launcher is given"
	managedSpawnCannotUse = "the managed launcher cannot be used: "
	managedNoInterpreter  = "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)"
)

// spawnRefusal is a spawn failure that carries its own JSON-RPC code.
type spawnRefusal struct {
	code int
	msg  string
}

func (e *spawnRefusal) Error() string { return e.msg }

// refuseManagedLauncher runs the process.spawn checks of a launcher, before the cwd check
// and before the env is built. The measured order: on Windows any launcher is refused,
// before the cwd check. A relative command is -32602, before any launcher check. Then the
// launcher checks give -32004. An empty launcher gets the no-element text.
//
// claustrum's choice (not measured): the Windows refusal comes before the relative
// command check.
func refuseManagedLauncher(launcher []string, command string) error {
	if r := install.HostRefusal(); r != "" {
		return &spawnRefusal{code: rpc.CodeLauncherUnusable, msg: managedSpawnCannotUse + r}
	}
	if !filepath.IsAbs(command) {
		return &spawnRefusal{code: rpc.CodeInvalidParam, msg: managedSpawnRelative}
	}
	if r := install.Check(launcher, command); r != "" {
		return &spawnRefusal{code: rpc.CodeLauncherUnusable, msg: managedSpawnCannotUse + r}
	}
	return nil
}

// launchedCommandError checks the command of a launched spawn before the launcher
// starts. A command that is missing, a folder or not executable gives the same
// -32603 "fork/exec <command>: <reason>" frame as a spawn without a launcher. The
// launcher does not run (measured for a missing file, a folder and a 0644 file).
//
// claustrum's choice (not measured): this check runs before the launcher starts, so
// a bad command wins over a launcher that does not start.
func launchedCommandError(command string) error {
	if _, err := os.Stat(command); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return &fs.PathError{Op: "fork/exec", Path: command, Err: pe.Err}
		}
		return err
	}
	if !install.IsExecutableRegular(command) {
		return &fs.PathError{Op: "fork/exec", Path: command, Err: syscall.EACCES}
	}
	return nil
}

// managedLauncherStartError turns the start failure of a launcher into the -32005
// refusal. err comes from exec.Cmd.Start, or it is the "fork/exec <path>: <reason>"
// text of the exec-child trampoline. A missing file at exec (the #! interpreter or
// the ELF loader) is the interpreter text. Any other reason is its errno text, as
// "exec format error" is (measured).
//
// claustrum's choice (not measured): the reasons other than those two are the
// errno text as it stands.
func managedLauncherStartError(a0 string, err error) error {
	reason := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		reason = pe.Err.Error()
	} else {
		reason = strings.TrimPrefix(reason, "fork/exec "+a0+": ")
	}
	if reason == syscall.ENOENT.Error() {
		reason = managedNoInterpreter
	}
	return &spawnRefusal{code: rpc.CodeLauncherStart, msg: "the managed launcher " + a0 + " could not be started: " + reason}
}
