//go:build !linux && !darwin

package git

import "os"

// gitStillRuns has no test here. On Windows a Kill of a git that exited
// fails, so stopGitProcess tells the two apart. The replies are equal
// on both builds (measured on Windows).
func gitStillRuns(*os.Process) bool { return true }
