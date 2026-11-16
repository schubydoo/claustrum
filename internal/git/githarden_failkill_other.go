//go:build !linux

package git

// failedCheckoutKillsGroup is false outside Linux. macOS sends none (claustrum's
// own, not measured). Windows has no process group.
const failedCheckoutKillsGroup = false
