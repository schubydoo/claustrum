//go:build windows

package files

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// writeJunction makes link an empty folder with the reparse data of a junction
// to target, which need not exist: mklink /J makes no junction to a missing
// drive such as Q:, so the data is written by FSCTL_SET_REPARSE_POINT.
func writeJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	sub, pr := utf16.Encode([]rune(`\??\`+target)), utf16.Encode([]rune(target))
	var body []byte
	for _, v := range []int{0, 2 * len(sub), 2*len(sub) + 2, 2 * len(pr)} {
		body = binary.LittleEndian.AppendUint16(body, uint16(v))
	}
	for _, u := range append(append(sub, 0), append(pr, 0)...) {
		body = binary.LittleEndian.AppendUint16(body, u)
	}
	data := binary.LittleEndian.AppendUint32(nil, 0xA0000003) // IO_REPARSE_TAG_MOUNT_POINT
	data = binary.LittleEndian.AppendUint16(data, uint16(len(body)))
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = append(data, body...)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &n, nil); err != nil {
		t.Fatal(err)
	}
}

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
	writeJunction(t, link, `Q:\sub`)
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

// A link whose target is on a mapped network drive gives errNoUsableName, also
// when the drive of the link is a local drive (683cc6a9 and 4c111e86, Windows).
// Only Q: is a network drive here, so the answer comes from the target.
func TestRealpathWindowsLinkToMappedDrive(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	old := pathcanon.DriveType
	pathcanon.DriveType = func(r string) uint32 {
		if r == `Q:\` {
			return windows.DRIVE_REMOTE
		}
		return windows.DRIVE_FIXED
	}
	t.Cleanup(func() { pathcanon.DriveType = old })
	link := filepath.Join(root, "lz")
	writeJunction(t, link, `Q:\real`)
	// The network drive is reached through a link, so the reply has
	// stoppedShort and no atLetter (87375cc7, measured on Windows).
	for _, p := range []string{filepath.Join(link, "f"), link} {
		if _, err := realpathEval(p); !errors.Is(err, errNoUsableName) || errors.Is(err, errNoUsableNameAtLetter) {
			t.Errorf("realpathEval(%q) = %v, want errNoUsableName after a link", p, err)
		}
	}
	// The path on the network drive itself gives atLetter.
	if _, err := realpathEval(`Q:\real\f`); !errors.Is(err, errNoUsableNameAtLetter) {
		t.Errorf("realpathEval on Q: = %v, want errNoUsableNameAtLetter", err)
	}
}

// A name below a junction loop and the loop itself give error 1921, with and
// without missingLeaf, and the reply has stoppedShort true (4c111e86 gives the
// error and 87375cc7 the member, measured on Windows).
func TestRealpathWindowsJunctionLoop(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeJunction(t, filepath.Join(root, "j1"), filepath.Join(root, "j2"))
	writeJunction(t, filepath.Join(root, "j2"), filepath.Join(root, "j1"))
	const want = `"data":{"reason":"too_many_links","errno":1921,"stoppedShort":true}`
	for _, p := range []string{filepath.Join(root, "j1", "x"), filepath.Join(root, "j1")} {
		for _, ml := range []bool{false, true} {
			res, err := resolveRealpath(p, ml)
			if !errors.Is(err, errnoCantResolve) {
				t.Errorf("resolveRealpath(%q, %t) = %+v, %v; want error 1921", p, ml, res, err)
				continue
			}
			b, _ := json.Marshal(realpathFailure(json.RawMessage(`1`), err))
			if !strings.Contains(string(b), want) {
				t.Errorf("reply of %q = %s, want %s", p, b, want)
			}
		}
	}
}
