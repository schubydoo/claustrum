//go:build !linux && !darwin

package sysproc

import "os"

// AwaitExit returns at once here and reports the exit as seen. A LeaderGate
// then closes right after the start, so a stop sends no group kill. On Windows
// the group kill is a no-op (ReapProcessGroup). A Unix that is not Linux or
// macOS is no build target.
func AwaitExit(int) bool { return true }

// SurelyRuns has no test here and reports false.
func SurelyRuns(*os.Process) bool { return false }
