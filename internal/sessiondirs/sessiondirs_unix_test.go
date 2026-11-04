//go:build unix

package sessiondirs

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The refusal text on Linux and macOS, byte for byte.
func TestNoOtherGitTextUnix(t *testing.T) {
	want := "For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as /usr/bin"
	if got := (NoOtherGitError{}).Error(); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// A PATH entry that is a link to a session folder is passed over too, and the
// line names the path through the link. The cut of the PATH keeps that entry,
// as it goes by text.
func TestPickGitPassesOverALinkToASessionFolder(t *testing.T) {
	t.Cleanup(UseForTest(""))
	buf := captureLog(t)
	s := t.TempDir()
	bin := binWithGit(t, s)
	lnk := filepath.Join(t.TempDir(), "lnk")
	if err := os.Symlink(bin, lnk); err != nil {
		t.Fatal(err)
	}
	other := binWithGit(t, t.TempDir())
	_ = Remember(s)
	t.Setenv("PATH", lnk+":"+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, "git"); cmd.Err != nil || cmd.Path != want {
		t.Fatalf("git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
	if !strings.Contains(buf.String(), `passing over "`+filepath.Join(lnk, "git")+`"`) {
		t.Errorf("log = %q, want the line of the link path", buf.String())
	}
	env := []string{"PATH=" + lnk + ":" + bin + ":" + other}
	if got, want := CutPath(env), []string{"PATH=" + lnk + ":" + other}; !slices.Equal(got, want) {
		t.Errorf("CutPath = %q, want %q", got, want)
	}
}

// A record folder that is a link takes no record: no write goes through it.
func TestNoRecordThroughALinkedFolder(t *testing.T) {
	target := t.TempDir()
	dir := filepath.Join(t.TempDir(), RecordFolderName)
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseForTest(dir))
	captureLog(t)
	_ = Remember(t.TempDir())
	if got := names(t, target); len(got) != 0 {
		t.Errorf("the target of the link holds %v", got)
	}
}

// A folder given through a link counts when the first PATH entry names its
// resolved path, so its unsaved record refuses the spawn.
func TestUnsavedRecordOfALinkedFolderRefuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RecordFolderName)
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseForTest(dir))
	s, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(s, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binWithGit(t, s))
	if err := Remember(link); err == nil {
		t.Error("Remember of a link to the folder of the first PATH entry = nil, want SaveError")
	}
}
