//go:build !linux

package git

// helperKillAtDeadline is false: no row measures the kill of
// worktreecontentkill.go on macOS or Windows.
const helperKillAtDeadline = false

// leafLinkErr is nil on macOS and Windows: there a link in place of a committed
// file keeps the codes of the check before this rule (claustrum's own, not
// measured).
func leafLinkErr(string, string) error { return nil }
