//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSocketFilesRealpathWindowsCleansDotDot pins the Windows rule (claustrum's
// own, not measured): Win32 removes a ".." by text before it opens a path, so a
// ".." after a link removes the link name and not a folder of its target. The
// rule holds for the request path and for the relative target of a dangling link.
func TestSocketFilesRealpathWindowsCleansDotDot(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	for _, d := range []string{"a", filepath.Join("real", "dir")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "real", "dir"), filepath.Join(root, "a", "L")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if err := os.Symlink(`L\..\gone`, filepath.Join(root, "a", "d")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	got := realpathCalls(t, root, []map[string]any{
		{"path": root + `\a\L\..\new`, "missingLeaf": true},
		{"path": filepath.Join(root, "a", "d"), "missingLeaf": true},
	})
	for i, w := range []string{`"wouldBe":"<DIR>/a/new"`, `"wouldBe":"<DIR>/a/gone","dangling":true`} {
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}
