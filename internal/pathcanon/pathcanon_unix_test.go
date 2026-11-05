//go:build unix

package pathcanon

import (
	"os"
	"path/filepath"
	"testing"
)

// FinalPath resolves a symbolic link on Linux and macOS, and answers the error of a
// path that does not exist. Mutation: answer p and nil (both rows fail).
func TestFinalPathResolvesLinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "J")); err != nil {
		t.Fatal(err)
	}
	if got, err := FinalPath(filepath.Join(dir, "J")); err != nil || got != real {
		t.Errorf("FinalPath = %q, %v, want %q", got, err, real)
	}
	if _, err := FinalPath(filepath.Join(dir, "missing")); err == nil {
		t.Error("FinalPath of a missing path answered no error")
	}
}
