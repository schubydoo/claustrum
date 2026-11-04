package git

import (
	"context"
	"os/exec"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// gitCommand is exec.Command of git by its bare name, with a git in a session
// folder passed over (sessiondirs.PickGit). Every git call of the git methods
// starts through it or through gitCommandContext.
func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	sessiondirs.PickGit(cmd)
	return cmd
}

// gitCommandContext is gitCommand with ctx, as exec.CommandContext.
func gitCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	sessiondirs.PickGit(cmd)
	return cmd
}
