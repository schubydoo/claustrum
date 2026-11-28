//go:build windows

package supervisor

// cwdUnusable is false on Windows. The second cwd test is not measured there
// (claustrum's own).
func cwdUnusable(string) bool { return false }
