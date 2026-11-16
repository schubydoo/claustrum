//go:build !linux && !darwin

package sysproc

import "os"

// AwaitExitStatus returns at once here and reports the exit as seen, with no
// status. A LeaderGate then closes right after the start, so a stop sends no
// group kill. On Windows the group kill is a no-op (ReapProcessGroup). A Unix
// that is not Linux or macOS is no build target.
func AwaitExitStatus(int) (seen, clean bool) { return true, false }

// SurelyRuns has no test here and reports false.
func SurelyRuns(*os.Process) bool { return false }
