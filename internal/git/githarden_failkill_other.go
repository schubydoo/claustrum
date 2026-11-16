//go:build !linux

package git

// failedCheckoutKillsGroup is false outside Linux. On macOS the wait for the
// exit has no status after an ESRCH, so a failure is not known before the reap
// (claustrum's own, not measured). Windows has no process group.
const failedCheckoutKillsGroup = false
