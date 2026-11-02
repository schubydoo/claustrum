package files

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFilesReadRealRelative checks that a relative path with real gives the
// absolute real name (claustrum's own, not measured).
func TestFilesReadRealRelative(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "f"), "x", 0o644)
	t.Chdir(root)
	want := filepath.ToSlash(filepath.Join(root, "f"))
	if got := readRealName("f"); got != want {
		t.Errorf("readRealName(f) = %q, want %q", got, want)
	}
}

// TestFilesReadRealLoop checks that a name that cannot be resolved gives no
// real (claustrum's own, not measured).
func TestFilesReadRealLoop(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if got := readRealName(a); got != "" {
		t.Errorf("readRealName(loop) = %q, want empty", got)
	}
}
