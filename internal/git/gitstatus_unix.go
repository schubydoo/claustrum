//go:build unix

package git

// statusProbeDir is the --work-tree value of the `rev-parse --show-toplevel`
// call for a `path` outside baseRepo: `path` with its symlinks resolved
// (measured).
func statusProbeDir(clean string) string {
	return resolvedDir(clean)
}
