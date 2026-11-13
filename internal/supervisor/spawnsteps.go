package supervisor

import (
	"os/exec"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// Two steps of a spawn live in package main: the private temp folder of a
// "cliTempRoot":true spawn (clitemproot_linux.go) and the session folders of a
// bare command name (sessionfolders.go). main sets both once in an init function,
// with SetSpawnSteps. The defaults do nothing. Only the tests of this package run
// a spawn with the defaults.
//
// The temp-folder step returns the env, the member of the reply, the release of the
// folder and its bind. The spawn calls bind once its child runs, with the call that
// ends the child after a loss of the folder, or nil when the spawn did not ask for
// that end.
var (
	privateTempRoot = func(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
		return env, nil, func() {}, func(func()) {}
	}
	bareCommandStart = func(cmd *exec.Cmd, command string, env map[string]string) {}
)

// SetSpawnSteps sets the temp-folder step and the bare-command step of a spawn.
// package main calls it once, in an init function, before any spawn.
func SetSpawnSteps(
	tempRoot func(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())),
	bareStart func(cmd *exec.Cmd, command string, env map[string]string),
) {
	privateTempRoot, bareCommandStart = tempRoot, bareStart
}
