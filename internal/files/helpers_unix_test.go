//go:build unix

package files

import (
	"os"
	"path/filepath"
	"testing"
)

// skipIfRoot: chmod-based denial is a no-op for uid 0.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denial tests are meaningless as root")
	}
}

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
