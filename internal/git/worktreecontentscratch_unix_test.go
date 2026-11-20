//go:build unix

package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The arms of the scratch folder of the content check that end in a separate
// `hash-object --stdin -w` (claustrum's own, not measured). Each folder or
// path below is one that the system or a user can give on Linux and macOS.

// TestHashOnLineEndInPath: a scratch folder whose path holds a line end takes
// no file, as `--stdin-paths` reads one path per line. The caller sends the
// object to a separate run, and the git that ran stays for the next object.
func TestHashOnLineEndInPath(t *testing.T) {
	id := strings.Repeat("a", 40)
	for _, end := range []string{"\n", "\r"} {
		dir := filepath.Join(t.TempDir(), "a"+end+"b")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		c := &contentCheck{scratch: root}
		h := fakeBatch(t, id+"\n")
		if got, ran, err := c.hashOn(h, "blob", id, []byte("x")); got != "" || ran != h || !errors.Is(err, errNoHashHelper) {
			t.Errorf("%q: hashOn = %q, %p, %v; want no answer, the same git %p and %v", end, got, ran, err, h, errNoHashHelper)
		}
		if names, err := os.ReadDir(dir); err != nil || len(names) != 0 {
			t.Errorf("%q: the scratch folder holds %v, %v; want no file", end, names, err)
		}
	}
}

// scratchCreate runs a create of f whose commit and top tree go through the
// long-running hash-object calls. It fails the test unless the create
// matches with no stop line of a long-running git, the two long-running
// hash-object calls ran, and separate `hash-object --stdin -w` calls hashed
// separate objects.
func scratchCreate(t *testing.T, f checkoutRuleFixture, separate int) {
	t.Helper()
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if strings.Contains(logs.String(), "stopped using the long-running git") {
		t.Errorf("log:\n%s\nwant no stop line", logs)
	}
	if want := checkedLine("1", "file", 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if l, s := hashObjectWrites(calls); l != 2 || s != separate {
		t.Errorf("%d long-running and %d separate hash-object -w calls, want 2 and %d", l, s, separate)
	}
}

// scratchOpens replaces the open of each new scratch folder with before and
// then the real open. It returns the paths of the folders, in their order.
func scratchOpens(t *testing.T, before func(dir string) error) func() []string {
	t.Helper()
	old := openScratchRoot
	t.Cleanup(func() { openScratchRoot = old })
	var mu sync.Mutex
	var dirs []string
	openScratchRoot = func(dir string) (*os.Root, error) {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		if err := before(dir); err != nil {
			t.Errorf("before the open of %s: %v", dir, err)
		}
		return old(dir)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(dirs)
	}
}

// TestContentCheckScratchMkdirFails: a folder of the daemon that takes no new
// entry when the commit comes to the long-running hash-object. The scratch
// folder is not made, the commit goes to a separate `hash-object --stdin -w`,
// and the create matches. The batch call makes the folder read-only before it
// answers, and the separate call makes it writable again, so the tree gets
// its file.
func TestContentCheckScratchMkdirFails(t *testing.T) {
	skipIfRoot(t)
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	// A git.info makes the folder of the daemon.
	if raw, _ := f.info(t, f.repo, nil); raw == "" {
		t.Fatal("git.info gave no reply")
	}
	own := filepath.Join(f.run, shadowRootName, ownFolderName(f.shadowFixture))
	fi, err := os.Stat(own)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the folder of the daemon %s: %v, %v", own, fi, err)
	}
	mode := fi.Mode().Perm()
	t.Cleanup(func() { _ = os.Chmod(own, mode) })
	slowGit(t, "cat-file,--batch", "pre", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "chmod\x1f"+own+"\x1f500")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "hash-object,--stdin,-w")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION2", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE2", "chmod\x1f"+own+"\x1f"+fmt.Sprintf("%o", mode))
	var wrote []string
	oldWrite := writeObjectBody
	t.Cleanup(func() { writeObjectBody = oldWrite })
	writeObjectBody = func(root *os.Root, name string, body []byte) error {
		wrote = append(wrote, name)
		return oldWrite(root, name, body)
	}
	scratchCreate(t, f, 1)
	if commit := gitRevParse(t, f.repo, "HEAD"); len(wrote) != 1 || wrote[0] == commit {
		t.Errorf("files written %q, want only the one of the tree, not of commit %s", wrote, commit)
	}
	noReadbackFolder(t, f)
}

// TestContentCheckScratchOpenFails: a scratch folder that does not open after
// its mkdir goes at once. Each object then goes to a separate `hash-object
// --stdin -w`, and the create matches.
func TestContentCheckScratchOpenFails(t *testing.T) {
	skipIfRoot(t)
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	dirs := scratchOpens(t, func(dir string) error { return os.Chmod(dir, 0) })
	t.Cleanup(func() {
		for _, d := range dirs() {
			_ = os.Chmod(d, 0o700)
		}
	})
	scratchCreate(t, f, 2)
	if got := dirs(); len(got) != 2 {
		t.Errorf("scratch folders %q, want one for each of 2 objects", got)
	}
	for _, d := range dirs() {
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s after its failed open: %v, want it gone", d, err)
		}
	}
	noReadbackFolder(t, f)
}

// TestContentCheckScratchSwapped: a link to another folder put at the path of
// the scratch folder between its mkdir and its open stays, and the folder that
// it names gets no file. Each object then goes to a separate `hash-object
// --stdin -w`, and the create matches.
func TestContentCheckScratchSwapped(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	target := t.TempDir()
	dirs := scratchOpens(t, func(dir string) error {
		if err := os.Remove(dir); err != nil {
			return err
		}
		return os.Symlink(target, dir)
	})
	scratchCreate(t, f, 2)
	if got := dirs(); len(got) != 2 {
		t.Errorf("scratch folders %q, want one for each of 2 objects", got)
	}
	for _, d := range dirs() {
		if fi, err := os.Lstat(d); err != nil || fi.Mode().Type() != os.ModeSymlink {
			t.Errorf("%s after the swap: %v, %v; want the link kept", d, fi, err)
		}
	}
	if names, err := os.ReadDir(target); err != nil || len(names) != 0 {
		t.Errorf("the folder that the link names holds %v, %v; want no file", names, err)
	}
}
