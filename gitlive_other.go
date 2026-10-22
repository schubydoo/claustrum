//go:build !linux && !darwin

package main

import "os"

// gitStillRuns has no test here. On Windows a Kill of a git that exited
// fails, so stopGitProcess tells the two apart. Cells A4, A4h35 and A4x of
// round task111-08 are equal on both builds (Windows VM).
func gitStillRuns(*os.Process) bool { return true }
