package git

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// The text of the network link test, byte for byte against the message member
// of the frames of b3081b51 on Windows. The path is a neutral fixture path.
func TestNetworkLinkText(t *testing.T) {
	want := "Claude doesn't follow `C:\\work\\jn\\R` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive. Or open the folder by its network path."
	if got := networkLinkText(`C:\work\jn\R`); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// The substitute names of the rows of b3081b51 on Windows leave the local drives.
// A drive letter after the prefix, a relative target and a target rooted on the
// drive of the link do not (claustrum's own, not measured).
func TestTargetLeavesLocalDrive(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{`\??\UNC\localhost\share`, true},
		{`\??\NUL`, true},
		{`\??\Volume{00000000-0000-0000-0000-000000000001}\work\local`, true},
		{`\??\GLOBALROOT\Device\HarddiskVolume3\work\local`, true},
		{`\\?\UNC\localhost\share`, true},
		{`\\?\Volume{00000000-0000-0000-0000-000000000001}\`, true},
		{`\\localhost\share`, true},
		{`\\.\NUL`, true},
		{`//localhost/share`, true},
		{`\??\C:\work\local`, false},
		{`\??\c:`, false},
		{`\\?\D:\work`, false},
		{`C:\work\local`, false},
		{`..\local`, false},
		{`\work\local`, false},
		{`local`, false},
		{``, false},
		{`\??\C`, true},
		{`\??\1:\x`, true},
		{`\??\AB\x`, true},
		{`\??\C:x`, true},
	}
	for _, c := range cases {
		if got := targetLeavesLocalDrive(c.target); got != c.want {
			t.Errorf("%q: got %v, want %v", c.target, got, c.want)
		}
	}
}

// reparseData builds the reparse data of a link as FSCTL_GET_REPARSE_POINT
// returns it: a substitute name, then a print name.
func reparseData(tag uint32, sub, print string) []byte {
	s, p := utf16.Encode([]rune(sub)), utf16.Encode([]rune(print))
	var names []byte
	for _, u := range append(append(s, 0), append(p, 0)...) {
		names = binary.LittleEndian.AppendUint16(names, u)
	}
	var body []byte
	body = binary.LittleEndian.AppendUint16(body, 0)
	body = binary.LittleEndian.AppendUint16(body, uint16(2*len(s)))
	body = binary.LittleEndian.AppendUint16(body, uint16(2*len(s)+2))
	body = binary.LittleEndian.AppendUint16(body, uint16(2*len(p)))
	if tag == reparseTagSymlink {
		body = binary.LittleEndian.AppendUint32(body, 0)
	}
	body = append(body, names...)
	data := binary.LittleEndian.AppendUint32(nil, tag)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(body)))
	data = binary.LittleEndian.AppendUint16(data, 0)
	return append(data, body...)
}

// The substitute name is read for a junction and for a symbolic link, in the
// layout of the junctions of the rows. Any other tag and short data give none.
func TestReparseSubstituteName(t *testing.T) {
	for _, tag := range []uint32{reparseTagMountPoint, reparseTagSymlink} {
		got, ok := reparseSubstituteName(reparseData(tag, `\??\UNC\localhost\share`, `\\localhost\share`))
		if !ok || got != `\??\UNC\localhost\share` {
			t.Errorf("tag %#x: got %q, %v", tag, got, ok)
		}
	}
	if _, ok := reparseSubstituteName(reparseData(0x8000001B, `\??\UNC\h\s`, ``)); ok {
		t.Error("another tag gave a name")
	}
	if _, ok := reparseSubstituteName(reparseData(reparseTagMountPoint, `\??\NUL`, ``)[:17]); ok {
		t.Error("short data gave a name")
	}
}

// fakeLinkTargets replaces the reparse read for one test: each link in targets
// reads as its value. Other entries read as no link.
func fakeLinkTargets(t *testing.T, targets map[string]string) {
	t.Helper()
	oldRead, oldOn := readLinkTarget, networkLinkChecks
	readLinkTarget = func(path string) (string, bool) {
		v, ok := targets[path]
		return v, ok
	}
	networkLinkChecks = true
	t.Cleanup(func() { readLinkTarget, networkLinkChecks = oldRead, oldOn })
}

// The walk refuses a path with a link to a network or device path at any name,
// the last name too. A link to a local drive, a path with no link and the test
// turned off pass. A name that cannot be read ends the walk.
func TestNetworkLinkRefusalWalk(t *testing.T) {
	root := realTempDir(t)
	local := filepath.Join(root, "local")
	if err := os.MkdirAll(filepath.Join(local, "R"), 0o755); err != nil {
		t.Fatal(err)
	}
	unc, vol, loc := filepath.Join(root, "unc"), filepath.Join(root, "vol"), filepath.Join(root, "loc")
	for _, l := range []string{unc, vol, loc} {
		symlinkOrSkip(t, local, l)
	}
	fakeLinkTargets(t, map[string]string{
		unc: `\??\UNC\localhost\share`,
		vol: `\??\Volume{00000000-0000-0000-0000-000000000001}\work\local`,
		loc: `\??\C:\work\local`,
	})
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(unc, "R"), true},
		{unc, true},
		{filepath.Join(vol, "R"), true},
		{filepath.Join(loc, "R"), false},
		{filepath.Join(local, "R"), false},
		{filepath.Join(root, "gone", "unc"), false},
	} {
		want := ""
		if c.want {
			want = networkLinkText(c.path)
		}
		if got := networkLinkRefusal(c.path); got != want {
			t.Errorf("%s: got %q, want %q", c.path, got, want)
		}
	}
	// The text names the path cleaned (claustrum's own, not measured).
	if got := networkLinkRefusal(unc + string(filepath.Separator) + "R" + string(filepath.Separator)); got != networkLinkText(filepath.Join(unc, "R")) {
		t.Errorf("trailing separator: got %q", got)
	}
	networkLinkChecks = false
	if got := networkLinkRefusal(filepath.Join(unc, "R")); got != "" {
		t.Errorf("test off: got %q", got)
	}
}

// fakeLstat turns the network link test on and makes lstatPath fail for each path
// in failed with its error. It counts the looks.
func fakeLstat(t *testing.T, failed map[string]error) *int {
	t.Helper()
	oldLstat, oldOn := lstatPath, networkLinkChecks
	looks := new(int)
	lstatPath = func(path string) (fs.FileInfo, error) {
		*looks++
		if err, ok := failed[path]; ok {
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: err}
		}
		return os.Lstat(os.DevNull)
	}
	networkLinkChecks = true
	t.Cleanup(func() { lstatPath, networkLinkChecks = oldLstat, oldOn })
	return looks
}

// A \\?\UNC\ path and a path on a share that cannot be found are refused. A
// missing folder on a share, a missing drive and a share that answers pass, as on
// b3081b51 (measured on Windows). The other \\?\ and \\.\ forms, the share
// root alone, a forward slash and the letter case of UNC are claustrum's own, not
// measured.
func TestUnlookablePathRefusal(t *testing.T) {
	share, gone := `\\127.0.0.1\nosuchshare\repo`, `\\localhost\c$\repo\nope\R`
	looks := fakeLstat(t, map[string]error{
		share:                          errors.New("network name not found"),
		gone:                           fs.ErrNotExist,
		`//127.0.0.1/nosuchshare/repo`: errors.New("network name not found"),
	})
	long := `\\?\UNC\localhost\c$\repo\R`
	for _, c := range []struct {
		path, want string
		looks      int
	}{
		{share, unlookableShareText(share), 1},
		{`//127.0.0.1/nosuchshare/repo`, unlookableShareText(`//127.0.0.1/nosuchshare/repo`), 1},
		{gone, "", 1},
		{`\\localhost\c$\repo\R`, "", 3},
		{`\\127.0.0.1\nosuchshare`, "", 0},
		{long, longUNCText(long), 0},
		{`\\?\unc\localhost\c$\R`, longUNCText(`\\?\unc\localhost\c$\R`), 0},
		{`\\?\C:\repo\R`, "", 0},
		{`\\?\UNCX\h\s\R`, "", 0},
		{`\\.\UNC\localhost\c$\R`, "", 0},
		{`\\.\C:\R`, "", 0},
		{`Q:\nope\repo`, "", 0},
		{``, "", 0},
	} {
		*looks = 0
		if got := unlookablePathRefusal(c.path); got != c.want || *looks != c.looks {
			t.Errorf("%s: got %q and %d looks, want %q and %d", c.path, got, *looks, c.want, c.looks)
		}
	}
	// networkLinkRefusal runs the test first.
	if got := networkLinkRefusal(share); got != unlookableShareText(share) {
		t.Errorf("networkLinkRefusal: got %q", got)
	}
	networkLinkChecks = false
	if got := unlookablePathRefusal(share) + unlookablePathRefusal(long); got != "" {
		t.Errorf("test off: got %q", got)
	}
}

// linkInfo is the fs.FileInfo of a fake entry with one mode.
type linkInfo fs.FileMode

func (m linkInfo) Name() string       { return "e" }
func (m linkInfo) Size() int64        { return 0 }
func (m linkInfo) Mode() fs.FileMode  { return fs.FileMode(m) }
func (m linkInfo) ModTime() time.Time { return time.Time{} }
func (m linkInfo) IsDir() bool        { return fs.FileMode(m).IsDir() }
func (m linkInfo) Sys() any           { return nil }

// The three shapes of the share row of 4c111e86 on Windows, byte for byte: a
// share that does not exist logs one "looking for links" line, a share that
// denies every account gets the permission text with no line, and a directory
// symbolic link on a share that answers is refused. A junction and a folder
// with a reparse tag that is no link are claustrum's own, not measured. So is
// the end of the walk at a name that fails its look, with a link below it.
func TestUnlookableShareRow(t *testing.T) {
	missing, denied := `\\127.0.0.1\nosuchsharecl\repo`, `\\127.0.0.1\cldeny\sub\repo`
	missingDeep, deniedDeep, denyMe := `\\127.0.0.1\nosuchsharecl\a\b\c`, `\\127.0.0.1\cldeny\sub\repo\x\y`, `\\127.0.0.1\cldeny2\repo`
	lnk, deep, jn, tagged := `\\127.0.0.1\clok\lnk`, `\\127.0.0.1\clok\lnk\sub`, `\\127.0.0.1\clok\jn`, `\\127.0.0.1\clok\tag`
	unread, belowUnread := `\\127.0.0.1\clok\unread`, `\\127.0.0.1\clok\unread\lnk`
	modes := map[string]fs.FileMode{
		lnk:         fs.ModeSymlink,
		belowUnread: fs.ModeSymlink,
		jn:          fs.ModeIrregular,
		tagged:      fs.ModeDir | fs.ModeIrregular,
	}
	oldLstat, oldOn := lstatPath, networkLinkChecks
	lstatPath = func(path string) (fs.FileInfo, error) {
		switch {
		case path == missing || strings.HasPrefix(path, `\\127.0.0.1\nosuchsharecl\a`):
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: errors.New("The network name cannot be found.")}
		case strings.HasPrefix(path, `\\127.0.0.1\cldeny\sub`), path == denyMe:
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: fs.ErrPermission}
		case path == unread:
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: errors.New("unreadable")}
		}
		if m, ok := modes[path]; ok {
			return linkInfo(m), nil
		}
		return linkInfo(fs.ModeDir), nil
	}
	networkLinkChecks = true
	t.Cleanup(func() { lstatPath, networkLinkChecks = oldLstat, oldOn })
	buf := captureLogBuf(t)
	const missingText = "Claude couldn't look at `\\\\127.0.0.1\\nosuchsharecl\\repo` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again."
	const deniedText = "Claude couldn't check `\\\\127.0.0.1\\cldeny\\sub\\repo` for links because Windows didn't let your account read `\\\\127.0.0.1\\cldeny\\sub`, so git wasn't run. Ask for read access to that folder."
	const linkText = "Claude doesn't follow links on a network share, and `\\\\127.0.0.1\\clok\\lnk` is a symlink or junction, so git wasn't run. Open the folder it points to instead, or replace the link with a real folder."
	const missingLine = `[GitHandler] looking for links in "\\\\127.0.0.1\\nosuchsharecl\\repo": "CreateFile \\\\127.0.0.1\\nosuchsharecl\\repo: The network name cannot be found."`
	// A deeper path names the first folder below the share root that fails, and
	// the line quotes the error of that folder (4c111e86, measured on Windows).
	const missingDeepText = "Claude couldn't look at `\\\\127.0.0.1\\nosuchsharecl\\a` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again."
	const missingDeepLine = `[GitHandler] looking for links in "\\\\127.0.0.1\\nosuchsharecl\\a\\b\\c": "CreateFile \\\\127.0.0.1\\nosuchsharecl\\a: The network name cannot be found."`
	const deniedDeepText = "Claude couldn't check `\\\\127.0.0.1\\cldeny\\sub\\repo\\x\\y` for links because Windows didn't let your account read `\\\\127.0.0.1\\cldeny\\sub`, so git wasn't run. Ask for read access to that folder."
	// A path one name below the root of a share that denies the account names
	// the path twice (4c111e86, measured on Windows).
	const denyMeText = "Claude couldn't check `\\\\127.0.0.1\\cldeny2\\repo` for links because Windows didn't let your account read `\\\\127.0.0.1\\cldeny2\\repo`, so git wasn't run. Ask for read access to that folder."
	for _, c := range []struct{ path, want string }{
		{missing, missingText},
		{denied, deniedText},
		{missingDeep, missingDeepText},
		{deniedDeep, deniedDeepText},
		{denyMe, denyMeText},
		{lnk, linkText},
		{deep, linkText},
		{jn, shareLinkText(jn)},
		{tagged, ""},
		{belowUnread, ""},
	} {
		if got := unlookablePathRefusal(c.path); got != c.want {
			t.Errorf("%s: got  %q\nwant %q", c.path, got, c.want)
		}
		if got := networkLinkRefusal(c.path); got != c.want {
			t.Errorf("networkLinkRefusal %s: got %q", c.path, got)
		}
	}
	// Two looks at each missing share path, two lines each. The other paths log none.
	for _, line := range []string{missingLine, missingDeepLine} {
		if n := strings.Count(buf.String(), line+"\n"); n != 2 {
			t.Errorf("got %d lines %q, want 2", n, line)
		}
	}
	if n := strings.Count(buf.String(), "looking for links in"); n != 4 {
		t.Errorf("got %d lines of the look, want 4:\n%s", n, buf.String())
	}
}

// A git.status refused for a junction on a share is the first request: it makes
// no excludes read, and the next git.info makes it. Both replies are the share
// link text (4c111e86, measured on Windows). A directory symbolic link and a
// junction above the last name keep the read at the status (claustrum's own, not
// measured).
func TestShareLinkStatusLeavesTheExcludesRead(t *testing.T) {
	run := installEnvStub(t)
	jct, lnk := `\\127.0.0.1\clok\jct`, `\\127.0.0.1\clok\lnk`
	oldLstat, oldOn := lstatPath, networkLinkChecks
	lstatPath = func(path string) (fs.FileInfo, error) {
		switch path {
		case jct:
			return linkInfo(fs.ModeIrregular), nil
		case lnk:
			return linkInfo(fs.ModeSymlink), nil
		}
		return linkInfo(fs.ModeDir), nil
	}
	networkLinkChecks = true
	t.Cleanup(func() { lstatPath, networkLinkChecks = oldLstat, oldOn })
	read := []string{"config", "--includes", "--path", "core.excludesFile"}
	for _, c := range []struct {
		base, link string
		atStatus   bool
	}{
		{jct, jct, false},
		{lnk, lnk, true},
		{jct + `\sub`, jct, true},
	} {
		resetUserExcludesCache(t)
		want := `"error":{"code":-32603,"message":` + jsonString(t, shareLinkText(c.link)) + `}`
		raw, calls := run("git.status", map[string]any{"path": c.base, "baseRepo": c.base})
		if !strings.Contains(raw, want) {
			t.Fatalf("%s: status reply = %s\nwant %s", c.base, raw, want)
		}
		if got := len(calls) == 1 && slices.Equal(calls[0].argv, read); got != c.atStatus || len(calls) > 1 {
			t.Errorf("%s: status calls = %v, want the excludes read %v", c.base, calls, c.atStatus)
		}
		raw, calls = run("git.info", map[string]any{"path": c.base})
		if !strings.Contains(raw, want) {
			t.Fatalf("%s: info reply = %s\nwant %s", c.base, raw, want)
		}
		if got := len(calls) == 1 && slices.Equal(calls[0].argv, read); got == c.atStatus || len(calls) > 1 {
			t.Errorf("%s: info calls = %v, want the excludes read %v", c.base, calls, !c.atStatus)
		}
	}
}
