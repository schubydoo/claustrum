package supervisor

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
// at the top of methods_launcher.go in package main.

// The process.spawn texts of the launcher param.
const (
	ManagedSpawnRelative  = "command must be an absolute path when a launcher is given"
	ManagedSpawnCannotUse = "the managed launcher cannot be used: "
	ManagedNoInterpreter  = "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)"
)

// SpawnRefusal is a spawn failure that carries its own JSON-RPC code.
type SpawnRefusal struct {
	Code int
	Msg  string
}

func (e *SpawnRefusal) Error() string { return e.Msg }

// RefuseManagedLauncher runs the process.spawn checks of a launcher, before the cwd check
// and before the env is built. The measured order: on Windows any launcher is refused,
// before the cwd check. A relative command is -32602, before any launcher check. Then the
// launcher checks give -32004. An empty launcher gets the no-element text.
//
// claustrum's choice (not measured): the Windows refusal comes before the relative
// command check.
func RefuseManagedLauncher(launcher []string, command string) error {
	if r := install.HostRefusal(); r != "" {
		return &SpawnRefusal{Code: rpc.CodeLauncherUnusable, Msg: ManagedSpawnCannotUse + r}
	}
	if !filepath.IsAbs(command) {
		return &SpawnRefusal{Code: rpc.CodeInvalidParam, Msg: ManagedSpawnRelative}
	}
	if r := install.Check(launcher, command); r != "" {
		return &SpawnRefusal{Code: rpc.CodeLauncherUnusable, Msg: ManagedSpawnCannotUse + r}
	}
	return nil
}

// LaunchedCommandError checks the command of a launched spawn before the launcher
// starts. A command that is missing, a folder or not executable gives the same
// -32603 "fork/exec <command>: <reason>" frame as a spawn without a launcher. The
// launcher does not run (measured for a missing file, a folder and a 0644 file).
//
// claustrum's choice (not measured): this check runs before the launcher starts, so
// a bad command wins over a launcher that does not start.
func LaunchedCommandError(command string) error {
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

// ManagedLauncherStartError turns the start failure of a launcher into the -32005
// refusal. err comes from exec.Cmd.Start, or it is the "fork/exec <path>: <reason>"
// text of the exec-child trampoline. A missing file at exec (the #! interpreter or
// the ELF loader) is the interpreter text. Any other reason is its errno text, as
// "exec format error" is (measured).
//
// claustrum's choice (not measured): the reasons other than those two are the
// errno text as it stands.
func ManagedLauncherStartError(a0 string, err error) error {
	reason := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		reason = pe.Err.Error()
	} else {
		reason = strings.TrimPrefix(reason, "fork/exec "+a0+": ")
	}
	if reason == syscall.ENOENT.Error() {
		reason = ManagedNoInterpreter
	}
	return &SpawnRefusal{Code: rpc.CodeLauncherStart, Msg: "the managed launcher " + a0 + " could not be started: " + reason}
}
