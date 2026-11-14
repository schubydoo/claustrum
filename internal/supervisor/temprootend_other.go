//go:build !linux && !darwin

package supervisor

// exitSeenUnreaped has no wait that leaves a child unreaped here, so the group
// SIGKILL of killGroupOnTempRootExit is not sent (claustrum's own).
func exitSeenUnreaped(int) bool { return false }

func groupMadeAtSpawn(int) bool { return false }
