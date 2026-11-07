//go:build unix

package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// stubMkdirAt makes mkdirAtFd return err for the rest of the test.
func stubMkdirAt(t *testing.T, err error) {
	t.Helper()
	old := mkdirAtFd
	t.Cleanup(func() { mkdirAtFd = old })
	mkdirAtFd = func(int, string, uint32) error { return err }
}

// mkdirCall sends one files.mkdir and returns the frame and the mkdir log lines.
func mkdirCall(t *testing.T, parent, name string) (string, string) {
	t.Helper()
	logs := captureLogBuf(t)
	got := dispatchRaw(t, nil, rpcLine(t, "files.mkdir", map[string]any{"parent": parent, "name": name}))
	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if i := strings.Index(l, "[Files] mkdir: "); i >= 0 {
			lines = append(lines, l[i:])
		}
	}
	return got, strings.Join(lines, "\n")
}

// mkdirLine is the log line of a code with an errno.
func mkdirLine(code string, en unix.Errno) string {
	return fmt.Sprintf("[Files] mkdir: %s (error %d: %s)", code, int(en), en.Error())
}

// mkdirFrame is the frame of a code with an errno.
func mkdirFrame(code string, en unix.Errno) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"code":%q,"errno":%d}}`, code, int(en))
}

// TestFilesMkdirErrnoCodes: each errno of the mkdir of the name gives its code
// with the errno member and the log line (c9125eb8, measured on Linux). An
// errno not in the list gives failed, and an error with no errno counts as EIO.
func TestFilesMkdirErrnoCodes(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	for _, tc := range []struct {
		err  error
		code string
		en   unix.Errno
	}{
		{unix.EPERM, "not_permitted", unix.EPERM},
		{unix.EROFS, "read_only_fs", unix.EROFS},
		{unix.ENOSPC, "no_space", unix.ENOSPC},
		{unix.EDQUOT, "over_quota", unix.EDQUOT},
		{unix.EINVAL, "name_unsupported", unix.EINVAL},
		{unix.EILSEQ, "name_unsupported", unix.EILSEQ},
		{unix.ENOENT, "parent_removed", unix.ENOENT},
		{unix.EIO, "failed", unix.EIO},
		{unix.ENAMETOOLONG, "name_too_long", unix.ENAMETOOLONG},
		{unix.EACCES, "denied", unix.EACCES},
		{errors.New("no errno"), "failed", unix.EIO},
	} {
		stubMkdirAt(t, tc.err)
		got, line := mkdirCall(t, dir, "n")
		if want := mkdirFrame(tc.code, tc.en); got != want {
			t.Errorf("%v: frame = %s, want %s", tc.err, got, want)
		}
		if w := mkdirLine(tc.code, tc.en); line != w {
			t.Errorf("%v: log = %q, want %q", tc.err, line, w)
		}
	}
}

// TestFilesMkdirParentUnverified: a failed read of the real path of the open
// parent gives parent_unverified with its errno, and makes nothing (c9125eb8,
// measured on Linux).
func TestFilesMkdirParentUnverified(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	old := mkdirParentPath
	t.Cleanup(func() { mkdirParentPath = old })
	mkdirParentPath = func(int) (string, error) { return "", unix.EIO }
	got, line := mkdirCall(t, dir, "n")
	if want := mkdirFrame("parent_unverified", unix.EIO); got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	if w := mkdirLine("parent_unverified", unix.EIO); line != w {
		t.Errorf("log = %q, want %q", line, w)
	}
	if _, err := os.Lstat(filepath.Join(dir, "n")); err == nil {
		t.Error("the call made a folder")
	}
}

// TestFilesMkdirNames: a space at the start is allowed. A space or a dot at the
// end, "...", a backslash, a control character, U+0085, U+2028, U+202E, U+FFFD
// and a byte that is not UTF-8 give name_refused and make nothing (c9125eb8,
// measured on Linux and macOS).
func TestFilesMkdirNames(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	created := `{"jsonrpc":"2.0","id":1,"result":{"code":"created"}}`
	refused := `{"jsonrpc":"2.0","id":1,"result":{"code":"name_refused"}}`
	for _, tc := range []struct {
		name string
		want string
	}{
		{" a", created}, {"a b", created}, {"\u00e9", created}, {".hidden", created}, {"-x", created}, {"~", created},
		{"a ", refused}, {"a.", refused}, {"...", refused}, {"a\\b", refused}, {"a\tb", refused},
		{"a\u007f", refused}, {"a\u0085", refused}, {"a\u2028", refused}, {"a\u202e", refused},
		{"a\ufffd", refused}, {"a\xffb", refused},
	} {
		// A name that is not UTF-8 goes into the line as it is, so the byte 0xff
		// reaches the decoder.
		text := tc.name
		if utf8.ValidString(text) {
			b, _ := json.Marshal(text)
			text = string(b[1 : len(b)-1])
		}
		line := strings.Replace(rpcLine(t, "files.mkdir", map[string]any{"parent": dir, "name": "NAME"}), "NAME", text, 1)
		if got := dispatchRaw(t, nil, line); got != tc.want {
			t.Errorf("name %q: frame = %s, want %s", tc.name, got, tc.want)
		}
	}
	var made []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		made = append(made, e.Name())
	}
	if w := " a -x .hidden a b ~ \u00e9"; strings.Join(made, " ") != w {
		t.Errorf("folder holds %q, want %q", strings.Join(made, " "), w)
	}
}

// TestFilesMkdirParents: a parent that does not open for reading gives
// parent_denied, a dangling link and a link to a file give parent_gone, a link
// at or above the parent gives parent_elsewhere, and a parent with U+FFFD gives
// parent_refused. None of them makes a folder (c9125eb8, measured on Linux and
// macOS). A link loop above the parent gives failed with its errno (claustrum's
// own, not measured).
func TestFilesMkdirParents(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	for _, d := range []string{"real/sub", "fffd\ufffdp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "file"), "x", 0o644)
	for name, target := range map[string]string{"lnk": "real", "dangling": "gone", "tofile": "file", "loop": "loop"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		parent string
		want   string
		line   string
	}{
		{"dangling", mkdirFrame("parent_gone", unix.ENOENT), mkdirLine("parent_gone", unix.ENOENT)},
		{"tofile", mkdirFrame("parent_gone", unix.ENOTDIR), mkdirLine("parent_gone", unix.ENOTDIR)},
		{"lnk", `{"jsonrpc":"2.0","id":1,"result":{"code":"parent_elsewhere"}}`, "[Files] mkdir: parent_elsewhere"},
		{"lnk/sub", `{"jsonrpc":"2.0","id":1,"result":{"code":"parent_elsewhere"}}`, "[Files] mkdir: parent_elsewhere"},
		{"fffd\ufffdp", `{"jsonrpc":"2.0","id":1,"result":{"code":"parent_refused"}}`, "[Files] mkdir: parent_refused"},
		{"loop/p", mkdirFrame("failed", unix.ELOOP), mkdirLine("failed", unix.ELOOP)},
	}
	if os.Geteuid() != 0 {
		for _, m := range []os.FileMode{0o300, 0o111} {
			d := "mode" + strconv.FormatUint(uint64(m), 8)
			if err := os.Mkdir(filepath.Join(root, d), m); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, d), 0o700) })
			cases = append(cases, struct{ parent, want, line string }{d, mkdirFrame("parent_denied", unix.EACCES), mkdirLine("parent_denied", unix.EACCES)})
		}
	}
	for _, tc := range cases {
		logs := captureLogBuf(t)
		line := rpcLine(t, "files.mkdir", map[string]any{"parent": root + "/" + tc.parent, "name": "n"})
		if got := dispatchRaw(t, nil, line); got != tc.want {
			t.Errorf("parent %s: frame = %s, want %s", tc.parent, got, tc.want)
		}
		if !strings.Contains(logs.String(), tc.line+"\n") {
			t.Errorf("parent %s: log = %q, want %q", tc.parent, logs.String(), tc.line)
		}
	}
	for _, d := range []string{"real", "real/sub", "fffd\ufffdp", "mode300", "mode111"} {
		if _, err := os.Lstat(filepath.Join(root, d, "n")); err == nil {
			t.Errorf("the call made %s/n", d)
		}
	}
}

// TestFilesMkdirModeFollowsUmask: the folder asks for mode 0777, so the umask
// alone sets its mode (c9125eb8, measured on Linux and macOS).
func TestFilesMkdirModeFollowsUmask(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	for _, tc := range []struct {
		mask int
		want os.FileMode
	}{{0o002, 0o775}, {0o000, 0o777}, {0o022, 0o755}} {
		old := unix.Umask(tc.mask)
		name := "u" + strconv.Itoa(tc.mask)
		got, _ := mkdirCall(t, dir, name)
		unix.Umask(old)
		if got != `{"jsonrpc":"2.0","id":1,"result":{"code":"created"}}` {
			t.Fatalf("umask %03o: frame = %s", tc.mask, got)
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != tc.want {
			t.Errorf("umask %03o: mode = %v, want %v", tc.mask, fi.Mode().Perm(), tc.want)
		}
	}
}

// TestFilesMkdirExistsCodes: a name that holds a folder, a file, a link to a
// folder or a dangling link gives exists_folder, exists_other or exists_link,
// with no link followed (c9125eb8, measured on Linux and macOS, the dangling
// link on Linux only).
func TestFilesMkdirExistsCodes(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "f"), "x", 0o644)
	for name, target := range map[string]string{"ld": "d", "lx": "gone"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name, code := range map[string]string{"d": "exists_folder", "f": "exists_other", "ld": "exists_link", "lx": "exists_link"} {
		want := `{"jsonrpc":"2.0","id":1,"result":{"code":"` + code + `"}}`
		if got, line := mkdirCall(t, dir, name); got != want || line != "[Files] mkdir: "+code {
			t.Errorf("name %s: frame = %s, log = %q, want %s", name, got, line, want)
		}
	}
}

// TestFilesMkdirExistsGone: a name that the mkdir found and the stat did not
// gives exists_other (claustrum's own, not measured).
func TestFilesMkdirExistsGone(t *testing.T) {
	stubMkdirAt(t, unix.EEXIST)
	if got, _ := mkdirCall(t, resolveTestRoot(t, t.TempDir()), "n"); !strings.Contains(got, `"result":{"code":"exists_other"}`) {
		t.Errorf("frame = %s, want exists_other", got)
	}
}

// TestFilesMkdirGateKeepsUnknownMethods: after the nil test that keeps
// files.mkdir off Windows, an unknown files method still gets -32601.
func TestFilesMkdirGateKeepsUnknownMethods(t *testing.T) {
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unknown method: files.nope"}}`
	if got := dispatchRaw(t, nil, rpcLine(t, "files.nope", nil)); got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
}
