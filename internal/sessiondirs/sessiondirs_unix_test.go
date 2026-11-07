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

// A session folder through a link gets a record of the path as given and one
// of its resolved path. The resolve follows the link before a `..`, so a git
// in the resolved folder of `<home>/lnk2/../x` is passed over. A folder with no
// link gets one record.
func TestRememberRecordsTheResolvedFolder(t *testing.T) {
	dir := recordDir(t)
	captureLog(t)
	data, home := realTempDir(t), realTempDir(t)
	for _, d := range []string{"new", "sub", "x"} {
		if err := os.Mkdir(filepath.Join(data, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkAt := func(link, target string) {
		if err := os.Symlink(target, filepath.Join(home, link)); err != nil {
			t.Fatal(err)
		}
	}
	linkAt("lnk", data)
	linkAt("lnk2", filepath.Join(data, "sub"))
	plain := filepath.Join(home, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := binWithGit(t, filepath.Join(data, "x"))
	_ = Remember(filepath.Join(home, "lnk", "new"), filepath.Join(home, "lnk2")+"/../x", plain)
	var want []string
	for _, p := range []string{filepath.Join(home, "lnk", "new"), filepath.Join(data, "new"),
		filepath.Join(home, "x"), filepath.Join(data, "x"), plain} {
		want = append(want, RecordName(p))
	}
	slices.Sort(want)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Errorf("the record folder holds %v, want %v", got, want)
	}
	other := binWithGit(t, t.TempDir())
	t.Setenv("PATH", bin+":"+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, "git"); cmd.Err != nil || cmd.Path != want {
		t.Errorf("git is %q (%v), want %q", cmd.Path, cmd.Err, want)
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

// A folder through a link that does not exist yet gets the record of its
// resolved path too: the leading part that exists is resolved, and the missing
// name is joined to it. Both records hold `0 0`. A git made there later is
// passed over.
func TestRememberRecordsTheResolvedMissingFolder(t *testing.T) {
	dir := recordDir(t)
	captureLog(t)
	data, home := realTempDir(t), realTempDir(t)
	if err := os.Mkdir(filepath.Join(data, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(data, "sub"), filepath.Join(home, "lnk2")); err != nil {
		t.Fatal(err)
	}
	_ = Remember(filepath.Join(home, "lnk2") + "/../x")
	for _, p := range []string{filepath.Join(home, "x"), filepath.Join(data, "x")} {
		b, err := os.ReadFile(filepath.Join(dir, RecordName(p)))
		if err != nil || !strings.HasSuffix(string(b), " 0 0\n"+p) {
			t.Errorf("the record of %s holds %q (%v), want `<ms> 0 0` and the path", p, b, err)
		}
	}
	bin := binWithGit(t, filepath.Join(data, "x"))
	other := binWithGit(t, t.TempDir())
	t.Setenv("PATH", bin+":"+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, "git"); cmd.Err != nil || cmd.Path != want {
		t.Errorf("git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
}

// A folder below a regular file does not resolve: its resolve fails with
// "not a directory", and only the path as given gets a record, also through a
// link (claustrum's own, not measured).
func TestRememberBelowAFileRecordsThePathOnly(t *testing.T) {
	dir := recordDir(t)
	captureLog(t)
	data, home := realTempDir(t), realTempDir(t)
	if err := os.WriteFile(filepath.Join(data, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "lnk", "file", "x")
	_ = Remember(p)
	if got := names(t, dir); !slices.Equal(got, []string{RecordName(p)}) {
		t.Errorf("the record folder holds %v, want the record of %s only", got, p)
	}
}

// A missing folder right below the root resolves the root and keeps its name,
// so it gets one record (claustrum's own, not measured). No folder is made.
func TestRememberMissingBelowTheRoot(t *testing.T) {
	dir := recordDir(t)
	captureLog(t)
	p := "/" + filepath.Base(filepath.Dir(t.TempDir())) + "-claustrum-missing"
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Skipf("%s exists: %v", p, err)
	}
	_ = Remember(p)
	if got := names(t, dir); !slices.Equal(got, []string{RecordName(p)}) {
		t.Errorf("the record folder holds %v, want the record of %s only", got, p)
	}
}
