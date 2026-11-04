//go:build unix

package git

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCopyEntryRefsFaultsOnUnix pins three faults of the refs of a
// registration: a FIFO as tables.list and a named table that is a link, with
// the texts of b3081b51 (measured on Linux), and a named table that is a FIFO
// (claustrum's own, not measured). No fault opens a FIFO for a read that
// waits, and nothing outside the git directory is copied.
func TestCopyEntryRefsFaultsOnUnix(t *testing.T) {
	for _, row := range []struct {
		name  string
		stage func(t *testing.T, git, reftable string)
		want  string
	}{
		{"a FIFO as tables.list", func(t *testing.T, _, reftable string) {
			list := filepath.Join(reftable, "tables.list")
			if err := os.Remove(list); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(list, 0o644); err != nil {
				t.Fatal(err)
			}
		}, `"worktrees/wr/reftable": "worktrees/wr/reftable/tables.list" is not a regular file`},
		{"a named table that is a link", func(t *testing.T, _, reftable string) {
			table := filepath.Join(reftable, "0x1-0x3-aa.ref")
			out := filepath.Join(t.TempDir(), "copy")
			mustWrite(t, out, "table\n")
			if err := os.Remove(table); err != nil {
				t.Fatal(err)
			}
			linkAt(t, table, out)
		}, `"worktrees/wr/reftable": "0x1-0x3-aa.ref": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to`},
		{"a named table that is a FIFO", func(t *testing.T, _, reftable string) {
			table := filepath.Join(reftable, "0x1-0x3-aa.ref")
			if err := os.Remove(table); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(table, 0o644); err != nil {
				t.Fatal(err)
			}
		}, `"worktrees/wr/reftable": "0x1-0x3-aa.ref" is not a regular file`},
	} {
		t.Run(row.name, func(t *testing.T) {
			git, reftable := refsFixture(t, "0x1-0x3-aa.ref")
			row.stage(t, git, reftable)
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wr"), 0o700); err != nil {
				t.Fatal(err)
			}
			// A copy that opens the FIFO for a read waits for ever, so it runs
			// with a short limit.
			done := make(chan error, 1)
			gitRoot, dirRoot := openTestRoot(t, git), openTestRoot(t, dir)
			go func() { done <- copyEntryRefs(gitRoot, dirRoot, "wr") }()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the copy did not return: it waits on a table that is no regular file")
			}
			var refsErr *refsCopyError
			if !errors.As(err, &refsErr) || refsErr.text != row.want {
				t.Errorf("the copy = %v\nwant %s", err, row.want)
			}
			if _, err := os.Lstat(filepath.Join(dir, "worktrees", "wr", "reftable", "0x1-0x3-aa.ref")); !os.IsNotExist(err) {
				t.Errorf("the table in the copy: %v, want none", err)
			}
		})
	}
}
