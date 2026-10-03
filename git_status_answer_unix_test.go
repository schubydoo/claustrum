//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The submodule rows that need a symbolic link or a file mode.
func TestGitStatusSubmoduleLinkRows(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, f statusFixture)
		want []string
	}{
		// Row o5: the gitlink path is a symlink to a folder outside the work tree.
		{"o5 symlink out of the work tree", func(t *testing.T, f statusFixture) {
			out := filepath.Join(f.root, "OUT")
			writeFile(t, filepath.Join(out, ".git"), "x\n", 0o644)
			mustSymlink(t, out, filepath.Join(f.W, "gl"))
		}, []string{subUnread}},
		// Row o5b: a symlink to a folder inside the work tree counts as present.
		{"o5b symlink inside the work tree", func(t *testing.T, f statusFixture) {
			writeFile(t, filepath.Join(f.W, "inner", ".git"), "x\n", 0o644)
			mustSymlink(t, "inner", filepath.Join(f.W, "gl"))
		}, []string{subPresent}},
		// Row o6c: a dangling symlink at .git counts as present.
		{"o6c dangling .git symlink", func(t *testing.T, f statusFixture) {
			if err := os.Mkdir(filepath.Join(f.W, "gl"), 0o755); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, "nowhere", filepath.Join(f.W, "gl", ".git"))
		}, []string{subPresent}},
		// Row o5c: a folder of mode 000.
		{"o5c folder of mode 000", func(t *testing.T, f statusFixture) {
			skipIfRoot(t)
			gl := filepath.Join(f.W, "gl")
			if err := os.Mkdir(gl, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(gl, 0o755) })
		}, []string{subUnread}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusFixture(t)
			f.commitGitlink(t)
			c.edit(t, f)
			if got := submoduleEntries(t, f.status(t)); !slices.Equal(got, c.want) {
				t.Errorf("submodule entries = %q\nwant %q", got, c.want)
			}
		})
	}
}
