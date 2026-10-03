//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
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
		// The goroutine uses t. Let it end before the test fails, so that it cannot
		// call t after the test is over.
		select {
		case <-done:
		case <-time.After(20 * time.Second):
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

// Rows y2 to y5 (Linux and macOS VMs) and y10 (Linux VM): a `..` component in a path
// inside baseRepo answers isRepo:false. Outside baseRepo it passes, and so does a `.`.
func TestGitStatusDotDotRows(t *testing.T) {
	f := newStatusFixture(t)
	wt := filepath.Join(f.T, ".claude", "worktrees")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", filepath.Join(wt, "s0"))
	for _, d := range []string{filepath.Join(wt, "q"), filepath.Join(f.W, "sub")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ name, path, want string }{
		{"y2 outside, folder there", f.W + "/sub/..", statusClean},
		{"y3 inside, no folder", wt + "/s0/missing/..", statusNotRepo},
		{"y4 inside, no folder before the leaf", wt + "/none/../s0", statusNotRepo},
		{"y5 inside, folder there", wt + "/q/../s0", statusNotRepo},
		{"y10 dot", wt + "/s0/.", statusClean},
	} {
		if got := statusFrame(t, c.path, f.T); got != c.want {
			t.Errorf("%s: git.status = %s\nwant %s", c.name, got, c.want)
		}
	}
}

// Rows r2 and r3 (macOS VM): the reftable rows that need a FIFO or a symlink.
func TestGitStatusReftableLinkRows(t *testing.T) {
	// Row r3: a table that is a symlink answers isRepo:false.
	t.Run("r3 table is a symlink", func(t *testing.T) {
		f := newStatusFixtureInit(t, "status-reftable", "--ref-format=reftable")
		dir := filepath.Join(f.entry, "reftable")
		names, err := os.ReadFile(filepath.Join(dir, "tables.list"))
		if err != nil {
			t.Fatal(err)
		}
		table := filepath.Join(dir, strings.TrimSpace(string(names)))
		if err := os.Rename(table, filepath.Join(dir, "real.copy")); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, "real.copy", table)
		if got := f.status(t); got != statusNotRepo {
			t.Errorf("git.status = %s\nwant %s", got, statusNotRepo)
		}
	})
	// Row r2: a FIFO as tables.list is left out with no wait. The temporary folder
	// then gets the table and no list. git decides the frame: git 2.50 on the macOS
	// VM read every tracked file as added, and another git can exit non-zero. The
	// test asserts only that the gate passed and that no read waited.
	t.Run("r2 tables.list is a FIFO", func(t *testing.T) {
		f := newStatusFixtureInit(t, "status-reftable", "--ref-format=reftable")
		list := filepath.Join(f.entry, "reftable", "tables.list")
		if err := os.Remove(list); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(list, 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		files, ok := statusReftable(statusEntryAt(t, f.entry).root)
		if !ok || len(files) != 1 || files[0] == "tables.list" {
			t.Errorf("statusReftable = %q %v, want the one table, no list, and true", files, ok)
		}
		got := statusWithin(t, f, list)
		if !strings.Contains(got, `"isRepo":true`) && !strings.Contains(got, `"message":"exit status `) {
			t.Errorf("git.status = %s\nwant a status or the exit status of git", got)
		}
	})
}

// Rows w1 and w2 (Linux and macOS VMs): in a plain repository an entry `reftable`
// that is a symlink out of the entry, or a regular file, counts as no folder. The
// answer is the clean status.
func TestGitStatusReftableNotAFolder(t *testing.T) {
	t.Run("w1 symlink out of the entry", func(t *testing.T) {
		f := newStatusFixture(t)
		out := filepath.Join(f.root, "out")
		writeFile(t, filepath.Join(out, "a.ref"), "table", 0o644)
		mustSymlink(t, out, filepath.Join(f.entry, "reftable"))
		if got := f.status(t); got != statusClean {
			t.Errorf("git.status = %s\nwant %s", got, statusClean)
		}
	})
	t.Run("w2 regular file", func(t *testing.T) {
		f := newStatusFixture(t)
		f.write(t, "reftable", "x\n")
		if got := f.status(t); got != statusClean {
			t.Errorf("git.status = %s\nwant %s", got, statusClean)
		}
	})
	// Rows w1 and w1b (macOS VM): in a reftable repository the folder is moved out
	// and a symlink takes its place. Nothing of it is copied and the gate passes.
	// git decides the frame: on the VM it showed every tracked file as added.
	for name, keepList := range map[string]bool{"w1 moved out": true, "w1b moved out, no list": false} {
		t.Run(name, func(t *testing.T) {
			f := newStatusFixtureInit(t, "status-reftable", "--ref-format=reftable")
			out := filepath.Join(f.root, "out")
			if err := os.Rename(filepath.Join(f.entry, "reftable"), out); err != nil {
				t.Fatal(err)
			}
			if !keepList {
				if err := os.Remove(filepath.Join(out, "tables.list")); err != nil {
					t.Fatal(err)
				}
			}
			mustSymlink(t, out, filepath.Join(f.entry, "reftable"))
			e := statusEntryAt(t, f.entry)
			if files, ok := statusReftable(e.root); !ok || files != nil {
				t.Errorf("statusReftable = %q %v, want no file and true", files, ok)
			}
			got := f.status(t)
			if !strings.Contains(got, `"isRepo":true`) && !strings.Contains(got, `"message":"exit status `) {
				t.Errorf("git.status = %s\nwant a status or the exit status of git", got)
			}
		})
	}
}
