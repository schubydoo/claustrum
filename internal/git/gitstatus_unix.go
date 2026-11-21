//go:build unix

package git

// statusProbeDir is the --work-tree value of the `rev-parse --show-toplevel`
// call for a `path` outside baseRepo: `path` with its symlinks resolved
// (measured).
func statusProbeDir(clean string) string {
	return resolvedDir(clean)
}

// statusPathOpenFails is the Windows open test of `path`. On Linux and macOS
// it never fails, so the request goes on as before.
func statusPathOpenFails(string) bool { return false }
