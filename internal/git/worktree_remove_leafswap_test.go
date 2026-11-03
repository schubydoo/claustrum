package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A leaf that something swaps for a new directory after its open and before the
// identity check is refused. Neither the new directory nor the moved leaf loses an
// entry.
func TestDeleteWorktreeDirRefusesALeafSwappedAfterItsOpen(t *testing.T) {
	dir := t.TempDir()
	wt := filepath.Join(dir, "wt")
	moved := filepath.Join(dir, "wt-moved")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "file.txt"), "file\n", 0o644)
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /nowhere\n", 0o644)
	parent, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()

	calls := 0
	var setupErr error
	leafOpenedHook = func() {
		calls++
		if err := os.Rename(wt, moved); err != nil {
			setupErr = errors.Join(setupErr, err)
			return
		}
		if err := os.Mkdir(wt, 0o755); err != nil {
			setupErr = errors.Join(setupErr, err)
			return
		}
		if err := os.WriteFile(filepath.Join(wt, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
			setupErr = errors.Join(setupErr, err)
		}
	}
	t.Cleanup(func() { leafOpenedHook = nil })

	err = deleteWorktreeDir(parent, "wt", wt)
	if calls != 1 || setupErr != nil {
		t.Fatalf("hook calls = %d, setup error = %v, want one call and no error", calls, setupErr)
	}
	var r *worktreeRefusal
	if !errors.As(err, &r) || !strings.Contains(r.text, "changed while the removal was checking it") {
		t.Errorf("deleteWorktreeDir = %v, want the replaced refusal", err)
	}
	if b, err := os.ReadFile(filepath.Join(wt, "keep.txt")); err != nil || string(b) != "keep\n" {
		t.Errorf("new leaf keep.txt = (%q, %v), want it kept", b, err)
	}
	for _, name := range []string{"file.txt", ".git"} {
		if _, err := os.Lstat(filepath.Join(moved, name)); err != nil {
			t.Errorf("moved leaf %s: %v, want it kept", name, err)
		}
	}
}
