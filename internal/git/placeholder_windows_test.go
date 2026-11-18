//go:build windows

package git

import (
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
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
// again at the end of the test, so the temp folder can be removed. The test is
// skipped where the system does not set the tag or does not read it as a folder
// that is irregular.
func tagFolder(t *testing.T, dir string, tag uint32) {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reparseTagIoctl(dir, windows.FSCTL_SET_REPARSE_POINT, tag); err != nil {
		t.Skipf("set the tag %#x: %v", tag, err)
	}
	t.Cleanup(func() { _ = reparseTagIoctl(dir, windows.FSCTL_DELETE_REPARSE_POINT, tag) })
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&(fs.ModeDir|fs.ModeIrregular) != fs.ModeDir|fs.ModeIrregular {
		t.Skipf("the folder with the tag %#x reads as %v, %v", tag, fi, err)
	}
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
