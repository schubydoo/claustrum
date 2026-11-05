package childrec

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// fakeProc points procinfo.Root at a temp tree and returns a helper to populate a pid's files.
// The tree has an empty locks file: a lock table that can be read and names no holder.
func fakeProc(t *testing.T) (root string, mk func(pid int, name, content string), link func(pid int, name, target string)) {
	t.Helper()
	root = t.TempDir()
	old := procinfo.Root()
	t.Cleanup(func() { procinfo.SetRootForTest(old) })
	procinfo.SetRootForTest(root)
	if err := os.WriteFile(filepath.Join(root, "locks"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mk = func(pid int, name, content string) {
		d := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link = func(pid int, name, target string) {
		p := filepath.Join(root, strconv.Itoa(pid), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	return root, mk, link
}
