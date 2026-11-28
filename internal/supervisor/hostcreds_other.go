//go:build !linux

package supervisor

import (
	"os"
	"os/exec"
)

// openHostCreds makes no pipe on macOS and Windows, where process.spawn refuses each
// hostCreds before the spawn (13047a51, measured).
func openHostCreds(map[string]string) (r, w *os.File, err error) { return nil, nil, nil }

func hostCredsEnv(env []string, _ *os.File) []string { return env }

func placeHostCreds(*exec.Cmd, *os.File) {}

func sendHostCreds(*os.File, map[string]string, int) {}
