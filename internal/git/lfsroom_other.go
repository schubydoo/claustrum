//go:build !linux && !darwin

package git

// awaitLeaderExit returns at once here. Windows makes no private LFS store
// (storeInCheckout), so no watch runs. On another system the watch then ends
// before the exit of git and sends no kill.
func awaitLeaderExit(int) {}
