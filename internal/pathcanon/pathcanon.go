package pathcanon

import (
	"os"
	"path/filepath"
)

// OfGone is Canonical for a path that possibly no longer exists. It resolves
// the nearest existing ancestor and joins the missing rest back on. After a delete
// the worktree is gone, but its parent still resolves. A remove named through a
// symlinked root (macOS /tmp -> /private/tmp) then still matches the path in the
// entry, where git records the resolved path. Measured against f6010b97 on macOS, where the
// reference deletes the entry.
func OfGone(p string) string {
	p = filepath.Clean(p)
	var missing []string
	for cur := p; ; {
		if _, err := os.Lstat(cur); err == nil {
			return filepath.Join(append([]string{Canonical(cur)}, missing...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
}
