//go:build windows

package main

import (
	"context"
	"os"
)

// loginPATHFromShell is false on Windows: no login shell runs, and a read logs no
// line (from the code).
const loginPATHFromShell = false

// extractLoginPATH returns the daemon's own PATH for spawned children. Windows has no
// login shell, so the daemon's value takes its place. os.Getenv ignores the case of
// the name here, so a daemon entry named Path gives the value too. buildEnv then sets
// the entry with the exact name PATH. The os/exec package keeps the last of the
// entries whose names differ only in case, so a Path entry of the daemon goes.
// An empty value gives no step.
//
// Measured on a Windows VM against 89cb6289. A daemon with Path=SYS gives the child
// PATH=SYS and no Path entry (R1). There a caller Path value is lost (R4). A daemon with
// PATH=SYS gives PATH=SYS (R6b). A daemon Path with an empty value stays, with no
// PATH entry (R6c). A second spawn gets the same entry as the first (R8). The CLI
// child of -install keeps Path (R2), and it does not go through buildEnv. The read
// of 52d82f0a on Windows is not measured.
func extractLoginPATH(context.Context) (string, error) { return os.Getenv("PATH"), nil }
