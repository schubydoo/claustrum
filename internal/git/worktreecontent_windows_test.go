package git

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestContentCheckSharingViolationPasses: a file of the content check that
// another handle holds open with share mode 0 passes the compare (the
// reference answers success, measured on Windows).
func TestContentCheckSharingViolationPasses(t *testing.T) {
	leaf := t.TempDir()
	p := filepath.Join(leaf, "CLAUDE.md")
	writeFile(t, p, "c\n", 0o644)
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
	c := &contentCheck{leaf: leaf, commit: strings.Repeat("0", 40)}
	if r := c.compare(contentFile{path: "CLAUDE.md", mode: "100644", id: strings.Repeat("0", 40)}); r != nil {
		t.Errorf("compare = %+v, want a pass", r)
	}
}
