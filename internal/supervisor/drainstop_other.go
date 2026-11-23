//go:build !linux && !darwin

package supervisor

import "os"

// leaderKeptOwnGroup is false here: Windows has no group for this kill.
func leaderKeptOwnGroup(int) bool { return false }

// drainGroupRefused refuses every number here.
func drainGroupRefused(int) bool { return true }

// pipesStillHeld is false here.
func pipesStillHeld([2]*os.File) bool { return false }
