//go:build !windows

package git

// externalWorktreeUnsupportedRefusal is a no-op off Windows: custom worktree locations
// (worktreeRoot) are supported on unix, matching the reference. Only the Windows build
// (externalunsupported_windows.go) gates the capability off.
var externalWorktreeUnsupportedRefusal = func(worktreeRoot, verb string) string { return "" }
