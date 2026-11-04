package git

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
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
