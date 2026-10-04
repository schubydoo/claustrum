//go:build windows

package main

import "os"

// extractLoginPATH records the daemon's own PATH for spawned children. Windows has no
// login shell, so the daemon's value takes its place. os.Getenv ignores the case of
// the name here, so a daemon entry named Path gives the value too. buildEnv then sets
// the entry with the exact name PATH. The os/exec package keeps the last of the
// entries whose names differ only in case, so a Path entry of the daemon goes.
// An empty value gives no step.
//
// Measured on a Windows VM against 89cb6289. A daemon with Path=SYS gives the child
// PATH=SYS and no Path entry (R1). A caller Path value is lost (R4). A daemon with
// PATH=SYS gives PATH=SYS (R6b). A daemon Path with an empty value stays, with no
// PATH entry (R6c). A second spawn gets the same entry as the first (R8). The CLI
// child of -install keeps Path (R2), and it does not go through buildEnv.
func extractLoginPATH() { setLoginPATH(os.Getenv("PATH")) }
