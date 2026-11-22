package git

import (
	"context"
	"errors"
	"os/exec"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// gitCommand is exec.Command of git by its bare name, with a git in a session
// folder passed over (sessiondirs.PickGit). Every git call of the git methods
// starts through it or through gitCommandContext. With no git on the PATH of
// the daemon it runs the git of the login-shell PATH (ShellPathGit).
func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	useShellPathGit(cmd)
	sessiondirs.PickGit(cmd)
	return cmd
}

// gitCommandContext is gitCommand with ctx, as exec.CommandContext.
func gitCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	useShellPathGit(cmd)
	sessiondirs.PickGit(cmd)
	return cmd
}

// useShellPathGit makes cmd, a command of git by its bare name that the PATH of the
// daemon did not find, run the git of the login-shell PATH, when there is one.
func useShellPathGit(cmd *exec.Cmd) {
	if !errors.Is(cmd.Err, exec.ErrNotFound) {
		return
	}
	if p := ShellPathGit(); p != "" {
		cmd.Path, cmd.Err = p, nil
	}
}

// noGitStarts reports that a git call cannot start now: PATH finds no git that
// the daemon runs, or each git on it lies in a session folder
// (sessiondirs.NoOtherGitError). It starts no process but the `--exec-path`
// call of ShellPathGit.
func noGitStarts() bool {
	err := gitCommand("version").Err
	return errors.Is(err, exec.ErrNotFound) || errors.As(err, new(sessiondirs.NoOtherGitError))
}
