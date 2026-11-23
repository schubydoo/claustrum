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

// TestReadRealStartLineBySystem: Windows logs the pass line with no start test
// (4c111e86 and c9125eb8, measured on Windows). Linux logs the line of its start
// test (683cc6a9). macOS logs the pass line (4c111e86 and 87375cc7, measured on macOS).
func TestReadRealStartLineBySystem(t *testing.T) {
	pass := func() bool { return true }
	fail := func() bool { return false }
	for _, tc := range []struct {
		goos      string
		available func() bool
		want      string
	}{
		{"windows", fail, filesRealPassLine},
		{"linux", pass, filesRealPassLine},
		{"linux", fail, filesRealFailLine},
		{"darwin", fail, filesRealPassLine},
	} {
		if got := readRealStartLine(tc.goos, tc.available); got != tc.want {
			t.Errorf("readRealStartLine(%s) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}
