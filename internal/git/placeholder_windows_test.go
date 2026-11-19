//go:build windows

package git

import (
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The Windows SDK names of the two tags of the rows. golang.org/x/sys
// does not define them.
const (
	ioReparseTagProjFS = 0x9000001C // IO_REPARSE_TAG_PROJFS, public Windows SDK value
	ioReparseTagWIM    = 0x80000008 // IO_REPARSE_TAG_WIM, public Windows SDK value
)

// reparseTagIoctl sends ioctl with the reparse header of tag and no data to the
// folder dir, as the folders of the rows were made.
func reparseTagIoctl(dir string, ioctl, tag uint32) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	head := binary.LittleEndian.AppendUint32(nil, tag)
	head = append(head, 0, 0, 0, 0)
	var n uint32
	return windows.DeviceIoControl(h, ioctl, &head[0], uint32(len(head)), nil, 0, &n, nil)
}

// tagFolder makes an empty folder at dir with the reparse tag tag. The tag goes
// again at the end of the test, so the temp folder can be removed. Where the
// system does not set the tag or does not read it as a folder that is irregular,
// the test fails when CI is set, as in makeJunction, and is skipped elsewhere.
func tagFolder(t *testing.T, dir string, tag uint32) {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reparseTagIoctl(dir, windows.FSCTL_SET_REPARSE_POINT, tag); err != nil {
		skipOutsideCI(t, "set the tag %#x: %v", tag, err)
	}
	t.Cleanup(func() { _ = reparseTagIoctl(dir, windows.FSCTL_DELETE_REPARSE_POINT, tag) })
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&(fs.ModeDir|fs.ModeIrregular) != fs.ModeDir|fs.ModeIrregular {
		skipOutsideCI(t, "the folder with the tag %#x reads as %v, %v", tag, fi, err)
	}
}

// skipOutsideCI fails the test when CI is set and skips it elsewhere, so that a
// CI leg cannot pass these tests as a silent skip.
func skipOutsideCI(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// A folder with the reparse tag IO_REPARSE_TAG_PROJFS is no link: a .git folder with it
// gets no link refusal, and a worktree folder with it below baseRepo is no link
// for git.status, as on 4c111e86 (measured on Windows). A junction still is.
func TestPlaceholderFolderIsNoLink(t *testing.T) {
	setGitDirMustBeReal(t, true)
	root := resolveTestRoot(t, t.TempDir())
	tagged := filepath.Join(root, "R", ".git")
	if err := os.Mkdir(filepath.Dir(tagged), 0o755); err != nil {
		t.Fatal(err)
	}
	tagFolder(t, tagged, ioReparseTagProjFS)
	if got := gitDirLinkDetail(tagged); got != "" {
		t.Errorf("gitDirLinkDetail of the tagged folder: %q", got)
	}
	if linkBelow(filepath.Dir(tagged), tagged) {
		t.Error("linkBelow took the tagged folder for a link")
	}
	jn := filepath.Join(root, "R", "jn")
	makeJunction(t, jn, filepath.Join(root, "target"))
	if gitDirLinkDetail(jn) == "" || !linkBelow(filepath.Dir(jn), jn) {
		t.Error("a junction is no link")
	}
}

// A folder with the reparse tag IO_REPARSE_TAG_PROJFS can be the held git
// directory of a create. A write through the held root lands in it. A junction
// to it is still no folder and is not opened as one. Without heldFolderKind
// in gitDirIdentity the tagged folder gets no identity.
func TestHeldGitDirOfATaggedFolder(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	tagged := filepath.Join(root, ".git")
	tagFolder(t, tagged, ioReparseTagProjFS)
	a := acceptGitDir(tagged, gitDirIdentity(tagged))
	if a == nil {
		t.Fatal("the tagged folder is not held")
	}
	defer a.release()
	if !a.stands() {
		t.Error("the tagged folder does not stand")
	}
	if err := a.root.Mkdir("made", 0o755); err != nil {
		t.Fatalf("a folder through the held root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tagged, "made")); err != nil {
		t.Errorf("the folder in the tagged git directory: %v", err)
	}
	jn := filepath.Join(root, "jn")
	makeJunction(t, jn, tagged)
	if gitDirIdentity(jn) != nil {
		t.Error("a junction to the tagged folder has the identity of a folder")
	}
	if held, err := openHeldGitDir(jn); err == nil {
		_ = held.Close()
		t.Error("a junction at the last name is opened as the held folder")
	}
}

// TestCreateWithATaggedGitDirOnWindows must match the Windows row on 4c111e86:
// a create in shadow whose `.git` folder has the reparse tag
// IO_REPARSE_TAG_PROJFS answers success. The registration and the branch are
// in that folder, and the leaf is made. The removal of the cleanup is a
// recorder. Without heldFolderKind in gitDirIdentity the create gets the text
// of a config behind a link.
func TestCreateWithATaggedGitDirOnWindows(t *testing.T) {
	f := newSeededShadowFixture(t)
	recordStagingRemovals(t)
	src := filepath.Join(resolveTestRoot(t, t.TempDir()), "src")
	if err := os.Rename(f.gitDir, src); err != nil {
		t.Fatal(err)
	}
	tagFolder(t, f.gitDir, ioReparseTagProjFS)
	copyGitDir(t, src, f.gitDir)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	var reply struct {
		Result struct {
			Success bool
			Path    string
		}
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatalf("the reply %s: %v", raw, err)
	}
	if !reply.Result.Success || reply.Result.Path != leaf {
		t.Fatalf("git.worktree_create = %s, want success at %s", raw, leaf)
	}
	for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("refs", "heads", "w1")} {
		if _, err := os.Lstat(filepath.Join(f.gitDir, name)); err != nil {
			t.Errorf("%s in the tagged git directory: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(leaf, ".git")); err != nil {
		t.Errorf("the .git file of the leaf: %v", err)
	}
}

// A repository below a folder with the reparse tag IO_REPARSE_TAG_WIM, which no filter
// handles, gets the text of 4c111e86 (measured on Windows).
func TestPathLookRefusalUnhandledTag(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	p := filepath.Join(root, "P")
	tagFolder(t, p, ioReparseTagWIM)
	repo := filepath.Join(p, "repo")
	want := "Claude couldn't check that `" + repo + "` stays on a local drive, so git wasn't run. Try again."
	if got := pathLookRefusal(repo); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
