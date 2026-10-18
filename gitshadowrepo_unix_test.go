//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// syncInTime is the answer of a syncRepo call, or an error when the call still
// runs after 10 s.
func syncInTime(done chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("the call still waits")
	}
}

// TestShadowRepoModes: on Linux and macOS every folder of the folder of a
// repository has the mode 0700 and every file 0600 (s3-linux cell A3, Linux VM.
// s3-macos cell A3 and cell 24 of s3p2b-edges, macOS VM).
func TestShadowRepoModes(t *testing.T) {
	f := newRepoFolderFixture(t)
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	mustWrite(t, filepath.Join(f.gitDir, "shallow"), strings.Repeat("a", 40)+"\n")
	mustWrite(t, filepath.Join(f.gitDir, "info", "attributes"), "*.txt text\n")
	mustWrite(t, filepath.Join(f.gitDir, "rebase-merge", "head-name"), "refs/heads/side\n")
	mustWrite(t, filepath.Join(f.gitDir, "BISECT_START"), "main\n")
	mustWrite(t, filepath.Join(f.gitDir, "BISECT_LOG"), "x\n")
	dir := f.sync(t, f.repo)
	tree := entryTreeOf(t, dir)
	if len(tree) != 14 {
		t.Fatalf("entries = %v", treeNames(tree))
	}
	for name, fi := range tree {
		want := fs.FileMode(0o600)
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			continue
		case fi.IsDir():
			want = 0o700
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has the mode %o, want %o", name, got, want)
		}
	}
}

// TestShadowRepoLinks: the link `worktrees` is made also where its target does
// not exist (s3p2-macos cell B3a, macOS VM), and for a bare repository it
// points into the repository (s3p2-linux cell B3c). A repository with a
// `reftable` folder gets a link to it (cell 25 of s3p2b-edges, macOS VM).
func TestShadowRepoLinks(t *testing.T) {
	f := newRepoFolderFixture(t)
	base := filepath.Dir(f.repo)
	bare := filepath.Join(base, "bare.git")
	runGit(t, base, "init", "-q", "--bare", "-b", "main", bare)
	if _, err := os.Lstat(filepath.Join(bare, "worktrees")); err == nil {
		t.Fatal("the bare fixture has a worktrees folder")
	}
	dir := f.sync(t, bare)
	if got, want := mustReadlink(t, filepath.Join(dir, "worktrees")), filepath.Join(bare, "worktrees"); got != want {
		t.Errorf("worktrees of a bare repository points to %s, want %s", got, want)
	}
	if _, err := os.Lstat(filepath.Join(bare, "worktrees")); err == nil {
		t.Error("the daemon made the target of the link")
	}
	if _, err := os.Lstat(filepath.Join(dir, "reftable")); err == nil {
		t.Error("a repository with no reftable folder got the reftable entry")
	}

	if err := os.Mkdir(filepath.Join(bare, "reftable"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(t, bare)
	if got, want := mustReadlink(t, filepath.Join(dir, "reftable")), filepath.Join(bare, "reftable"); got != want {
		t.Errorf("reftable points to %s, want %s", got, want)
	}
}

// TestShadowRepoNameResolvesLinks: on Linux and macOS a request through a
// symbolic link uses the folder of the resolved path (s3p2-macos cell B1,
// macOS VM, and the Linux column of the per system table).
func TestShadowRepoNameResolvesLinks(t *testing.T) {
	f := newRepoFolderFixture(t)
	link := filepath.Join(filepath.Dir(f.repo), "link")
	if err := os.Symlink(f.repo, link); err != nil {
		t.Fatal(err)
	}
	want := f.sync(t, f.repo)
	if got := f.sync(t, link); got != want {
		t.Errorf("through a link: folder %s, want %s", got, want)
	}
	if names := entryNames(t, f.h.dir); len(names) != 2 {
		t.Errorf("the folder of the daemon holds %v, want one repository folder and the lock file", names)
	}
}

// TestShadowRepoSourceKinds: a source file that is not a regular file gets no
// copy, and the read does not wait on a FIFO. The config gate refuses a FIFO
// as info/exclude before this code runs. A file that changes its kind after
// the gate is claustrum's own case.
func TestShadowRepoSourceKinds(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	if _, err := os.Lstat(filepath.Join(dir, "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	trust := gitDirTrustFor(f.repo)
	read := repoReadVerdict(trust, nil)

	exclude := filepath.Join(f.gitDir, "info", "exclude")
	mustRemove(t, exclude)
	if err := syscall.Mkfifo(exclude, 0o644); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(f.gitDir, "rebase-merge", "head-name")
	if err := os.MkdirAll(filepath.Dir(head), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.gitDir, "HEAD"), head); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.h.syncRepo(trust.pinCommonDir, read)
		done <- err
	}()
	if err := syncInTime(done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "info", "exclude")); err == nil {
		t.Error("a FIFO as info/exclude left the copy in place")
	}
	if _, err := os.Lstat(filepath.Join(dir, "rebase-merge")); err == nil {
		t.Error("a symbolic link as rebase-merge/head-name got a copy")
	}
}

// TestShadowFolderRemovalLeavesLinkTargets: the removal of the folder of the
// daemon, and of a copy in it, follows no link. The folder that `worktrees`
// points to keeps its entries.
func TestShadowFolderRemovalLeavesLinkTargets(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	entry := filepath.Join(f.gitDir, "worktrees", "wr", "HEAD")
	if _, err := os.Stat(filepath.Join(dir, "worktrees", "wr", "HEAD")); err != nil {
		t.Fatalf("the link does not reach the entry of the fixture: %v", err)
	}
	f.h.close()
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("the folder of the repository is still there after the shutdown")
	}
	if _, err := os.Stat(entry); err != nil {
		t.Errorf("the shutdown removed an entry behind the link: %v", err)
	}
}
