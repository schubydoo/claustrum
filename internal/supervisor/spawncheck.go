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
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The process.spawn checks of the launcher param. The rules of the managed launcher are
// at the top of methods_launcher.go in package main.

// The process.spawn texts of the launcher param.
const (
	ManagedSpawnRelative  = "command must be an absolute path when a launcher is given"
	ManagedSpawnCannotUse = "the managed launcher cannot be used: "
	ManagedNoInterpreter  = "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)"
	// SuppliesCannotUse is the prefix of a launcher refusal of a launcherSuppliesCli
	// spawn (c9125eb8, measured on Linux, macOS and Windows).
	SuppliesCannotUse = "the launcher cannot be used: "
)

// SpawnRefusal is a spawn failure that carries its own JSON-RPC code. Fault is the
// launcherFault of a launcher refusal, or "".
type SpawnRefusal struct {
	Code  int
	Msg   string
	Fault string
}

func (e *SpawnRefusal) Error() string { return e.Msg }

// LauncherOpts are the launcherCheck, launcherSuppliesCli and envOnlyIfSent members
// of a process.spawn with a launcher (c9125eb8). The shape test of package main ran
// before. Check is nil with no launcherCheck. Supplies is a launcherSuppliesCli spawn:
// the launcher gets the args with no command. EnvOnlyIfSent are the names that the
// child gets only from the env of the request (claustrum's own, not measured).
type LauncherOpts struct {
	Check         *install.LauncherCheck
	Supplies      bool
	EnvOnlyIfSent []string
}

// RefuseManagedLauncher runs the process.spawn checks of a launcher, before the cwd check
// and before the env is built. The measured order: on Windows any launcher is refused,
// before the cwd check. A relative command is -32602, before any launcher check. Then the
// launcher checks give -32004. An empty launcher gets the no-element text.
//
// c9125eb8 adds the windows_host fault to the Windows refusal (measured on Windows).
// With a launcherCheck, macOS refuses with macos_host (measured), and Linux runs the
// file check (install.CheckLauncherFile). A supplies spawn takes the prefix
// SuppliesCannotUse and has no command to test. lo is nil for a spawn with neither
// member.
//
// With no launcherCheck the refusal carries the fault of install.CheckFault.
//
// claustrum's own, not measured: the Windows refusal comes before the relative
// command check, and so does the macOS one. With a launcherCheck, the own-path test
// keeps its text and has no fault, and an empty launcher with a command gets the
// not_absolute fault as a supplies spawn does.
func RefuseManagedLauncher(launcher []string, command string, lo *LauncherOpts) error {
	supplies := lo != nil && lo.Supplies
	prefix := ManagedSpawnCannotUse
	if supplies {
		prefix = SuppliesCannotUse
	}
	refuse := func(fault, reason string) error {
		return &SpawnRefusal{Code: rpc.CodeLauncherUnusable, Msg: prefix + reason, Fault: fault}
	}
	if r := install.HostRefusal(); r != "" {
		return refuse(install.FaultWindowsHost, r)
	}
	checked := lo != nil && lo.Check != nil
	if checked {
		if f, r := install.LauncherCheckHostRefusal(); f != "" {
			return refuse(f, r)
		}
	}
	if !supplies && !filepath.IsAbs(command) {
		return &SpawnRefusal{Code: rpc.CodeInvalidParam, Msg: ManagedSpawnRelative}
	}
	if !checked {
		if f, r := install.CheckFault(launcher, command); r != "" {
			return refuse(f, r)
		}
		return nil
	}
	if len(launcher) == 0 {
		return refuse(install.FaultNotAbsolute, install.NoLauncherText)
	}
	if !supplies && launcher[0] == command {
		return refuse("", install.Check(launcher, command))
	}
	if f, r := install.CheckLauncherFile(launcher[0], *lo.Check); f != "" {
		return refuse(f, r)
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

// dropUnsentEnv removes each name of names from env unless the env param of the
// request sets it. So the child gets such a name only from the request, not from the
// daemon or the SSH agent hand-off (claustrum's own, not measured: a row shows only
// the shape of envOnlyIfSent and one success).
func dropUnsentEnv(env []string, names []string, sent map[string]string) []string {
	for _, n := range names {
		if _, ok := sent[n]; !ok {
			env = sysproc.RemoveEnvKey(env, n)
		}
	}
	return env
}
