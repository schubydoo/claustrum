//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The arms of the helpers of gitdirfolders_unix_test.go that the cells do not
// reach: a create that holds no git directory, a folder that is no
// repository, and a nil held folder.

// A create that holds no git directory opens the registrations folder by the
// path. A git directory or a registrations folder that does not exist gives
// no refusal. A file at the name of the registrations folder gives the
// refusal with the text of the open.
func TestWorktreesOpenRefusalWithoutAHeldFolder(t *testing.T) {
	g := t.TempDir()
	if got := (gitUse{gitDir: filepath.Join(g, "missing")}).worktreesOpenRefusal(); got != "" {
		t.Fatalf("missing git directory: got %q, want no refusal", got)
	}
	if got := (gitUse{gitDir: g}).worktreesOpenRefusal(); got != "" {
		t.Fatalf("missing registrations folder: got %q, want no refusal", got)
	}
	if err := os.WriteFile(filepath.Join(g, worktreesSubdir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := (gitUse{gitDir: g}).worktreesOpenRefusal()
	if !strings.HasPrefix(got, prepareWorktreesPrefix) || got == prepareWorktreesPrefix {
		t.Fatalf("file at the registrations folder: got %q, want %q and the text of the open", got, prepareWorktreesPrefix)
	}
}

// A folder that is no repository gives no refusal of a create with
// "shadowDirOff":true, and it is no linked entry.
func TestFolderOutsideARepositoryIsNoLinkedEntry(t *testing.T) {
	dir := t.TempDir()
	if got := offWorktreesRefusal(dir); got != "" {
		t.Fatalf("offWorktreesRefusal: got %q, want no refusal", got)
	}
	if entryIsLink(dir) {
		t.Fatal("entryIsLink: a folder with no repository reads as a linked entry")
	}
	if entryIsLink(filepath.Join(dir, "missing")) {
		t.Fatal("entryIsLink: a folder that does not exist reads as a linked entry")
	}
}

// With a daemon GIT_DIR the create keeps the earlier answer: entryIsLink is
// false, also for a linked entry.
func TestEntryIsLinkWithADaemonGitDir(t *testing.T) {
	f := newCellFixture(t)
	t.Setenv("GIT_DIR", filepath.Join(f.repo, ".git"))
	if entryIsLink(f.repo) {
		t.Fatal("entryIsLink is true with a daemon GIT_DIR")
	}
}

// A leaf that already exists gets no parent folders from
// makeRefusedLeafParents, and the folder at the path stays as it is.
func TestMakeRefusedLeafParentsLeavesAnExistingLeaf(t *testing.T) {
	f := newCellFixture(t)
	leaf := f.leaf()
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(leaf, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeRefusedLeafParents(&gitParams{BaseRepo: f.repo, WorktreePath: leaf}, f.repo)
	if b, err := os.ReadFile(marker); err != nil || string(b) != "x" {
		t.Fatalf("the leaf changed: %q, %v", b, err)
	}
}

// A create that holds no git directory has no folder to take over.
func TestReplacedAtPathOfNoHeldFolder(t *testing.T) {
	var a *acceptedGitDir
	if a.replacedAtPath(t.TempDir()) != nil {
		t.Fatal("a nil held folder gave a new held folder")
	}
}
