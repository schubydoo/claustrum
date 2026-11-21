//go:build windows

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSubst makes the drive Q: a SUBST drive of dir for the rest of the test.
func fakeSubst(t *testing.T, dir string) {
	t.Helper()
	old := substTarget
	substTarget = func(drive string) (string, bool) {
		if drive == "Q:" {
			return dir, true
		}
		return "", false
	}
	t.Cleanup(func() { substTarget = old })
}

// A path on a SUBST drive resolves to its real path on the drive below: a file,
// a drive letter in lower case, the drive root, and the wouldBe of a missing
// name (4c111e86, Windows). A link whose target is on the SUBST drive resolves
// the same way (4c111e86, Windows).
func TestRealpathWindowsSubstDrive(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "dr", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dr", "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeSubst(t, filepath.Join(root, "dr"))
	want := filepath.ToSlash(filepath.Join(root, "dr"))
	for _, tc := range []struct {
		path        string
		missingLeaf bool
		real        string
		wouldBe     string
	}{
		{path: `Q:\sub\f.txt`, real: want + "/sub/f.txt"},
		{path: `q:\sub\f.txt`, real: want + "/sub/f.txt"},
		{path: `Q:\`, real: want},
		{path: `Q:\sub\nope`, missingLeaf: true, wouldBe: want + "/sub/nope"},
	} {
		res, err := resolveRealpath(tc.path, tc.missingLeaf)
		if err != nil || res.Real != tc.real || res.WouldBe != tc.wouldBe {
			t.Errorf("resolveRealpath(%q, %t) = %+v, %v; want real %q wouldBe %q", tc.path, tc.missingLeaf, res, err, tc.real, tc.wouldBe)
		}
	}
	link := filepath.Join(root, "ls")
	if err := os.Symlink(`Q:\sub`, link); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	got, err := realpathEval(filepath.Join(link, "f.txt"))
	if err != nil || !strings.EqualFold(filepath.ToSlash(got), want+"/sub/f.txt") {
		t.Errorf("realpathEval through a link to Q: = %q, %v; want %s/sub/f.txt", got, err, want)
	}
}

// realpathPathRefusal gives the stream text for a colon after the drive letter
// and nothing for a plain path (4c111e86, Windows).
func TestRealpathPathRefusalWindowsStream(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{`C:\a\f.txt:stream`, realpathStreamText},
		{`C:\a\f.txt:nope:$DATA`, realpathStreamText},
		{`C:/a/f.txt:s`, realpathStreamText},
		{`C:\a\f.txt`, ""},
	} {
		if got := realpathPathRefusal(tc.path); got != tc.want {
			t.Errorf("realpathPathRefusal(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
