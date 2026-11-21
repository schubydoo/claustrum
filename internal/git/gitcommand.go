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
// the daemon it runs the git of the login-shell PATH (LoginGit).
func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	useLoginGit(cmd)
	sessiondirs.PickGit(cmd)
	return cmd
}

// gitCommandContext is gitCommand with ctx, as exec.CommandContext.
func gitCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	useLoginGit(cmd)
	sessiondirs.PickGit(cmd)
	return cmd
}

// useLoginGit makes cmd, a command of git by its bare name that the PATH of the
// daemon did not find, run the git of the login-shell PATH, when there is one.
func useLoginGit(cmd *exec.Cmd) {
	if !errors.Is(cmd.Err, exec.ErrNotFound) {
		return
	}
	if p := LoginGit(); p != "" {
		cmd.Path, cmd.Err = p, nil
	}
}
