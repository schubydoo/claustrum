//go:build unix

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStagedAddInAFolderWithSearchOnly: the folder of the daemon has mode 0100
// after the first request. A create answers privateCopyText with the error of
// the mkdir of `.s<n>`, as with mode 0500. 4c111e86 answers that text in both
// states (measured on Linux).
func TestStagedAddInAFolderWithSearchOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0100")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	start := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + strings.Trim(jsonQuoted(t, privateCopyText), `"`) + `mkdir ` +
		strings.Trim(jsonQuoted(t, filepath.Join(dir, addCopyPrefix)), `"`)
	rest, ok := strings.CutPrefix(raw, start)
	n, tail, _ := strings.Cut(rest, ":")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" || tail != ` permission denied","errorCode":"worktree_add_failed"}}` {
		t.Errorf("git.worktree_create = %s\nwant it to start %s<n>: permission denied", raw, start)
	}
}

// umaskModes are the modes that a new file of mode 0666 and a new folder of
// mode 0777 get in dir under the umask of the test.
func umaskModes(t *testing.T, dir string) (file, folder os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, "probe-file")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Mkdir(filepath.Join(dir, "probe-folder"), 0o777); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Join(dir, "probe-folder"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), di.Mode().Perm()
}

// TestStagedCopyModes: in `.s<n>`, HEAD, config and info/exclude are made
// with mode 0666 and info, objects and refs with 0777, less the umask. The
// folder, worktrees and each entry keep 0700, and the entry files keep 0600.
// Under umask 022 4c111e86 gives 0644 and 0755 (measured on Linux).
func TestStagedCopyModes(t *testing.T) {
	file, folder := umaskModes(t, t.TempDir())
	if file == 0o600 && folder == 0o700 {
		t.Skip("the umask of the test gives the old modes")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	want := map[string]os.FileMode{
		".": 0o700, "HEAD": file, "config": file, "info": folder, "info/exclude": file, "objects": folder, "refs": folder,
		"worktrees": 0o700, "worktrees/wr": 0o700, "worktrees/wr/HEAD": 0o600, "worktrees/wr/commondir": 0o600, "worktrees/wr/gitdir": 0o600,
	}
	for name, mode := range want {
		fi, err := os.Lstat(filepath.Join(st.dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if fi.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v", name, fi.Mode().Perm(), mode)
		}
	}
}

// TestStagedCopyOfAnUnreadableGitdir: a `gitdir` of a registration with mode
// 000 gets an empty copy in `.s<n>`. 4c111e86 writes a `gitdir` there
// (measured on Linux). The empty text is claustrum's own.
func TestStagedCopyOfAnUnreadableGitdir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	gitdir := filepath.Join(f.gitDir, "worktrees", "wr", "gitdir")
	if err := os.Chmod(gitdir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitdir, 0o644) })
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	got, err := os.ReadFile(filepath.Join(st.dir, "worktrees", "wr", "gitdir"))
	if err != nil || len(got) != 0 {
		t.Errorf("the copy of gitdir = %q, %v, want an empty file", got, err)
	}
}

// TestCreateWritesTheHeadCopyAgain: after the first request the copy of HEAD
// of the repository has mode 000, no bytes, or is an empty folder. A create
// writes it again and succeeds, as 4c111e86 does (measured on Linux). A
// folder with a file stays with that file, and the create fails (not
// measured).
func TestCreateWritesTheHeadCopyAgain(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	for _, tc := range []struct {
		name string
		mut  func(t *testing.T, head string)
		ok   bool
	}{
		{"mode 000", func(t *testing.T, head string) {
			if err := os.Chmod(head, 0); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"no bytes", func(t *testing.T, head string) { mustWrite(t, head, "") }, true},
		{"an empty folder", func(t *testing.T, head string) {
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(head, 0o700); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a folder with a file", func(t *testing.T, head string) {
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(head, "kept"), "kept\n")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			head := filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "HEAD")
			want := mustRead(t, head)
			tc.mut(t, head)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if got := strings.Contains(raw, `"success":true`); got != tc.ok {
				t.Errorf("git.worktree_create = %s, want success %v", raw, tc.ok)
			}
			fi, err := os.Lstat(head)
			switch {
			case err != nil:
				t.Errorf("the copy of HEAD: %v", err)
			case tc.ok && (!fi.Mode().IsRegular() || mustRead(t, head) != want):
				t.Errorf("the copy of HEAD is %v, want a file with %q", fi.Mode(), want)
			case !tc.ok && !fi.IsDir():
				t.Errorf("the copy of HEAD is %v, want the folder kept", fi.Mode())
			case !tc.ok && mustRead(t, filepath.Join(head, "kept")) != "kept\n":
				t.Errorf("the file in the folder at the copy of HEAD changed")
			}
		})
	}
}

// TestSearchOnlyFolderWithALinkedCopy: the folder of the repository copy is a
// link, and the folder of the daemon has mode 0100. The create gets the open
// error of the folder of the daemon, and nothing is written through the link
// (claustrum's own, not measured).
func TestSearchOnlyFolderWithALinkedCopy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user opens a folder of mode 0100")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	copyDir := filepath.Join(dir, shadowRepoName(f.gitDir))
	target := t.TempDir()
	if err := os.Rename(copyDir, filepath.Join(target, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "moved"), copyDir); err != nil {
		t.Fatal(err)
	}
	before := entryStamps(t, target)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: open ` +
		strings.Trim(jsonQuoted(t, dir), `"`) + `: permission denied","errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if after := entryStamps(t, target); !slices.Equal(after, before) {
		t.Errorf("the target of the link changed:\n %q\nwant\n %q", after, before)
	}
}

// TestWriteSearchFolderModes: only a folder of the daemon whose owner bits
// allow search and no write gets the mkdir text of `.s<n>`. Mode 0300 keeps
// the open error (not measured).
func TestWriteSearchFolderModes(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user opens a folder of any mode")
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		mode os.FileMode
		want bool
	}{{0o100, true}, {0o110, true}, {0o300, false}, {0o700, false}, {0o000, false}} {
		if err := os.Chmod(dir, tc.mode); err != nil {
			t.Fatal(err)
		}
		if got := searchOnlyFolder(dir); got != tc.want {
			t.Errorf("mode %o: %v, want %v", tc.mode, got, tc.want)
		}
	}
	_ = os.Chmod(dir, 0o700)
}

// entryStamps are the path, mode, size and write time of each entry below dir,
// as Lstat sees them.
func entryStamps(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for p, fi := range entryTreeOf(t, dir) {
		out = append(out, fmt.Sprintf("%s %v %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano()))
	}
	slices.Sort(out)
	return out
}

// TestStagedCopyBoundOfAnEntryHead: a `HEAD` of a registration with 4096
// bytes gets its copy in `.s<n>`, and one of 4097 bytes gets none. Its other
// files get their copies. 4c111e86 does so (measured on Linux).
func TestStagedCopyBoundOfAnEntryHead(t *testing.T) {
	for _, tc := range []struct {
		size   int
		copied bool
	}{{4096, true}, {4097, false}} {
		f := newRepoFolderFixture(t)
		clearFolderEnv(t)
		u := f.useOf(t, f.repo)
		head := "ref: refs/heads/" + strings.Repeat("x", tc.size-len("ref: refs/heads/")-1) + "\n"
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "wr", "HEAD"), head)
		st, err := openAddStage(heldUse(t, f.gitDir, u))
		if err != nil {
			t.Fatal(err)
		}
		_, err = os.Lstat(filepath.Join(st.dir, "worktrees", "wr", "HEAD"))
		if copied := err == nil; copied != tc.copied {
			t.Errorf("a HEAD of %d bytes: copied %v, want %v", tc.size, copied, tc.copied)
		}
		if _, err := os.Lstat(filepath.Join(st.dir, "worktrees", "wr", "commondir")); err != nil {
			t.Errorf("a HEAD of %d bytes: commondir: %v", tc.size, err)
		}
		st.close()
	}
}

// TestReadHeadCopyDoesNotBlockOnAFIFO: a FIFO put at the copy of HEAD after
// its Lstat found a regular file does not hang the read under repoMu. The
// read fails, and the request writes the copy again (claustrum's own).
func TestReadHeadCopyDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	mustWrite(t, head, "ref: refs/heads/main\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	seen, err := root.Lstat("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	mustMkfifo(t, head)
	done := make(chan bool, 1)
	go func() {
		_, ok := readHeadCopy(root, seen)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("readHeadCopy of a FIFO = ok, want a failed read")
		}
	case <-time.After(3 * time.Second):
		// A blocked open of the read side ends when a writer opens the FIFO.
		if w, err := os.OpenFile(head, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("readHeadCopy blocked on a FIFO at the copy of HEAD")
	}
}

// TestReadHeadCopyNeedsTheFileOfItsLstat: the read gives the text of the
// regular file that the Lstat saw. Another file put there, and a file over
// shadowHeadMaxBytes, give no text (claustrum's own).
func TestReadHeadCopyNeedsTheFileOfItsLstat(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	mustWrite(t, head, "ref: refs/heads/main\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	seen, err := root.Lstat("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if data, ok := readHeadCopy(root, seen); !ok || string(data) != "ref: refs/heads/main\n" {
		t.Errorf("readHeadCopy = %q, %v, want the text of HEAD", data, ok)
	}
	mustWrite(t, filepath.Join(dir, "other"), "ref: refs/heads/other\n")
	if err := os.Rename(filepath.Join(dir, "other"), head); err != nil {
		t.Fatal(err)
	}
	if data, ok := readHeadCopy(root, seen); ok {
		t.Errorf("readHeadCopy of another file = %q, ok, want no text", data)
	}
	mustWrite(t, head, strings.Repeat("x", shadowHeadMaxBytes+1))
	big, err := root.Lstat("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readHeadCopy(root, big); ok {
		t.Error("readHeadCopy of a file over shadowHeadMaxBytes = ok, want no text")
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	small, err := root.Lstat("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// The same file grows past shadowHeadMaxBytes after its Lstat.
	if err := os.WriteFile(head, []byte(strings.Repeat("y", shadowHeadMaxBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHeadCopy(root, small); ok {
		t.Error("readHeadCopy of a file that grew past shadowHeadMaxBytes = ok, want no text")
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHeadCopy(root, seen); ok {
		t.Error("readHeadCopy of a gone file = ok, want no text")
	}
}

// TestCannotOpenRegular: only a regular file that the daemon may not open
// gets the empty gitdir copy. A readable file, a folder and a gone path do
// not.
func TestCannotOpenRegular(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user opens a file of mode 000")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "readable"), "x")
	mustWrite(t, filepath.Join(dir, "closed"), "x")
	if err := os.Chmod(filepath.Join(dir, "closed"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for name, want := range map[string]bool{"readable": false, "closed": true, "folder": false, "gone": false} {
		if got := cannotOpenRegular(root, name); got != want {
			t.Errorf("cannotOpenRegular(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestRepoFolderBySearch: the search-only open of a repository folder needs a
// permission error and a real folder that opens. Any other case gives nil.
func TestRepoFolderBySearch(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user opens a folder of mode 000")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "file"), "x")
	if r := repoFolderBySearch(repo, os.ErrPermission); r == nil {
		t.Error("repoFolderBySearch of a real folder = nil, want its root")
	} else {
		_ = r.Close()
	}
	if r := repoFolderBySearch(repo, os.ErrNotExist); r != nil {
		_ = r.Close()
		t.Error("repoFolderBySearch with no permission error = a root, want nil")
	}
	for _, p := range []string{filepath.Join(dir, "gone"), filepath.Join(dir, "file")} {
		if r := repoFolderBySearch(p, os.ErrPermission); r != nil {
			_ = r.Close()
			t.Errorf("repoFolderBySearch(%s) = a root, want nil", p)
		}
	}
	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	if r := repoFolderBySearch(closed, os.ErrPermission); r != nil {
		_ = r.Close()
		t.Error("repoFolderBySearch of a folder of mode 000 = a root, want nil")
	}
}

// TestStagedReadsDoNotBlockOnAFIFO: a FIFO put at a gitdir entry of a
// registration after its Lstat does not hang the reads of the staged add
// under the add lock. readRootFile gives no text, and cannotOpenRegular
// answers false (claustrum's own).
func TestStagedReadsDoNotBlockOnAFIFO(t *testing.T) {
	for name, read := range map[string]func(*os.Root) bool{
		"readRootFile": func(r *os.Root) bool {
			_, ok := readRootFile(r, "gitdir", 1024)
			return ok
		},
		"cannotOpenRegular": func(r *os.Root) bool { return cannotOpenRegular(r, "gitdir") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			entry := filepath.Join(dir, "gitdir")
			mustWrite(t, entry, "/repo/wt/.git\n")
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			prev := regularLstatDone
			regularLstatDone = func(string) {
				if err := os.Remove(entry); err != nil {
					t.Error(err)
				}
				mustMkfifo(t, entry)
			}
			t.Cleanup(func() { regularLstatDone = prev })
			done := make(chan bool, 1)
			go func() { done <- read(root) }()
			select {
			case got := <-done:
				if got {
					t.Errorf("%s of a FIFO put there after the Lstat = true, want false", name)
				}
			case <-time.After(3 * time.Second):
				if w, err := os.OpenFile(entry, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = w.Close()
				}
				t.Fatalf("%s blocked on a FIFO at the gitdir entry", name)
			}
		})
	}
}
