//go:build !linux && !darwin

package git

// awaitLeaderExit returns at once here and reports the exit as seen. Windows
// makes no private LFS store (storeInCheckout), so no watch runs, and its
// group kill is a no-op. On another system the watch then ends before the
// exit of git and sends no kill. On a Unix that is not Linux or macOS the
// gate of the deadline stop (leaderGate) closes at once too, so the deadline
// sends no group kill there. That Unix is no build target.
func awaitLeaderExit(int) bool { return true }
