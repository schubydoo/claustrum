//go:build unix

package main

import (
	"path/filepath"
	"testing"
)

// resolveTestRoot canonicalizes a t.TempDir() root to the spelling the daemon
// will report for paths under it: symlink-resolved, which matters on macOS
// where the temp dir lives under /var -> /private/var. No-op on Linux.
func resolveTestRoot(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
