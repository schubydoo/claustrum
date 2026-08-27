//go:build unix

package main

import "path/filepath"

// canonicalPath resolves p to the spelling git reports — symlinks resolved, e.g.
// macOS /tmp -> /private/tmp. A no-op for a symlink-free Linux path (so the frame
// battery stays byte-identical). Falls back to p when it cannot be resolved (e.g.
// the path does not exist).
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
