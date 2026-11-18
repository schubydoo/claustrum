//go:build unix

package git

// statusLFSAfterWorkTree is false on Linux and macOS. The `-c lfs.storage`
// item of the `status` call of git.status comes first there (claustrum's own,
// not measured). It is a variable so that a test can run the place of Windows.
var statusLFSAfterWorkTree = false

// statusProbeDir is the --work-tree value of the `rev-parse --show-toplevel`
// call for a `path` outside baseRepo: `path` with its symlinks resolved
// (measured).
func statusProbeDir(clean string) string {
	return resolvedDir(clean)
}
