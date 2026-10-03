//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// statusWithin runs git.status and fails the test when no frame comes within the
// limit. If the daemon waits on fifo, a writer is opened so that the wait ends and
// no goroutine is left behind.
func statusWithin(t *testing.T, f statusFixture, fifo string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- statusFrame(t, f.W, f.T) }()
	select {
	case got := <-done:
		return got
	case <-time.After(20 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatalf("git.status gave no frame in 20 s with a FIFO at %s", fifo)
		return ""
	}
}

// Rows n05a, C20a, C20b, n14b and o15b: a FIFO in place of an entry file answers
// isRepo:false, and no read waits for a writer.
func TestGitStatusEntryFIFOAnswersAtOnce(t *testing.T) {
	for _, name := range []string{"gitdir", "commondir", "config.worktree", "index", "HEAD"} {
		t.Run(name, func(t *testing.T) {
			f := newStatusFixture(t)
			p := filepath.Join(f.entry, name)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0o644); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			if got := statusWithin(t, f, p); got != statusNotRepo {
				t.Errorf("git.status = %s\nwant %s", got, statusNotRepo)
			}
		})
	}
}

// The rows that need a symbolic link.
func TestGitStatusLinkRows(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, f statusFixture) (path string)
		want string
	}{
		// Row n06: path is a symlink to the worktree.
		{"n06 path through a symlink", func(t *testing.T, f statusFixture) string {
			l := filepath.Join(f.root, "L")
			mustSymlink(t, f.W, l)
			return l
		}, statusClean},
		// Row n02: the entry is a symlink to a folder elsewhere.
		{"n02 symlinked entry", func(t *testing.T, f statusFixture) string {
			aside := filepath.Join(f.T, ".git", "aside")
			if err := os.Mkdir(aside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(f.entry, filepath.Join(aside, "W")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, "../aside/W", f.entry)
			return f.W
		}, statusNotRepo},
		// Row n02b: a real entry folder that a symlink also points at counts once.
		{"n02b real entry beside a symlink to it", func(t *testing.T, f statusFixture) string {
			if err := os.Rename(f.entry, f.entry+".real"); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, "W.real", f.entry)
			writeFile(t, filepath.Join(f.W, ".git"), "gitdir: "+f.entry+".real\n", 0o644)
			return f.W
		}, statusClean},
		// Row n19e: commondir is a relative symlink. Row n19f: an absolute one.
		{"n19e commondir relative symlink", func(t *testing.T, f statusFixture) string {
			if err := os.Rename(filepath.Join(f.entry, "commondir"), filepath.Join(f.entry, "cd.real")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, "cd.real", filepath.Join(f.entry, "commondir"))
			return f.W
		}, statusClean},
		{"n19f commondir absolute symlink", func(t *testing.T, f statusFixture) string {
			if err := os.Rename(filepath.Join(f.entry, "commondir"), filepath.Join(f.entry, "cd.real")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, filepath.Join(f.entry, "cd.real"), filepath.Join(f.entry, "commondir"))
			return f.W
		}, statusNotRepo},
		// Row o15a: index is a symlink to a regular file in the entry.
		{"o15a index symlink", func(t *testing.T, f statusFixture) string {
			if err := os.Rename(filepath.Join(f.entry, "index"), filepath.Join(f.entry, "index.real")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, "index.real", filepath.Join(f.entry, "index"))
			return f.W
		}, statusClean},
		// Rows n07 and n07L: path lies inside baseRepo and .claude is a symlink.
		{"n07 symlinked .claude inside baseRepo", func(t *testing.T, f statusFixture) string {
			elsewhere := filepath.Join(f.root, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o755); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, elsewhere, filepath.Join(f.T, ".claude"))
			s0 := filepath.Join(f.T, ".claude", "worktrees", "s0")
			runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", s0)
			// n07L: the gitdir file names the path through the link.
			writeFile(t, filepath.Join(f.T, ".git", "worktrees", "s0", "gitdir"), filepath.Join(s0, ".git")+"\n", 0o644)
			return s0
		}, statusNotRepo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusFixture(t)
			path := c.edit(t, f)
			if got := statusFrame(t, path, f.T); got != c.want {
				t.Errorf("git.status = %s\nwant %s", got, c.want)
			}
		})
	}
}

// Row n11b: baseRepo is a symlink to T. Row n11: a dangling symlink.
func TestGitStatusBaseRepoSymlink(t *testing.T) {
	f := newStatusFixture(t)
	bl, dl := filepath.Join(f.root, "BL"), filepath.Join(f.root, "DL")
	mustSymlink(t, f.T, bl)
	mustSymlink(t, filepath.Join(f.root, "nothing"), dl)
	if got := statusFrame(t, f.W, bl); got != statusClean {
		t.Errorf("n11b git.status = %s\nwant %s", got, statusClean)
	}
	if got := statusFrame(t, f.W, dl); got != statusNotRepo {
		t.Errorf("n11 git.status = %s\nwant %s", got, statusNotRepo)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// A path inside a baseRepo that lies below a symlink. No row measured it. claustrum
// matches the entry through the resolved baseRepo and the part below it as sent.
func TestGitStatusInsidePathBelowSymlinkedParent(t *testing.T) {
	f := newStatusFixture(t)
	s0 := filepath.Join(f.T, ".claude", "worktrees", "s0")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", s0)
	lnk := filepath.Join(f.root, "lnk")
	mustSymlink(t, f.root, lnk)
	base := filepath.Join(lnk, "T")
	if got := statusFrame(t, filepath.Join(base, ".claude", "worktrees", "s0"), base); got != statusClean {
		t.Errorf("git.status = %s\nwant %s", got, statusClean)
	}
}
