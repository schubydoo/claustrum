//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFilesReadRealDotDotAfterLink checks that a ".." after a link follows the
// link target, as files.realpath does, for an absolute and a relative path.
func TestFilesReadRealDotDotAfterLink(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "d", "f"), "x", 0o644)
	writeFile(t, filepath.Join(root, "d", "real", "g"), "y", 0o644)
	if err := os.Symlink(filepath.Join("d", "real"), filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	// Raw text: filepath.Join would clean the ".." by text.
	for _, p := range []string{root + "/l/../f", "l/../f"} {
		want, err := resolveRealpath(root+"/l/../f", false)
		if err != nil || want.Real == "" {
			t.Fatalf("resolveRealpath: %+v %v", want, err)
		}
		if got := readRealName(p); got != want.Real {
			t.Errorf("readRealName(%q) = %q, want %q", p, got, want.Real)
		}
	}
}
