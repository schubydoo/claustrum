package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// The process.spawn checks of the launcher param. The rules of the managed launcher are
// at the top of methods_launcher.go.

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
