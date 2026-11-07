//go:build unix

package files

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

// TestFilesMkdirOtherErrno: an errno that no code covers gives -32603 with its
// text, and the failed log line (claustrum's own, not measured). An error with
// no errno counts as EIO.
func TestFilesMkdirOtherErrno(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		err  error
		text string
	}{
		{unix.EROFS, "error 30: read-only file system"},
		{errors.New("no errno"), "error 5: input/output error"},
	} {
		stubMkdirAt(t, tc.err)
		got, line := mkdirCall(t, dir, "n")
		want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"files.mkdir: ` + tc.text + `"}}`
		if got != want {
			t.Errorf("frame = %s, want %s", got, want)
		}
		if w := "[Files] mkdir: failed (" + tc.text + ")"; line != w {
			t.Errorf("log = %q, want %q", line, w)
		}
	}
}

// TestFilesMkdirExistsGone: a name that the mkdir found and the stat did not
// gives exists_other (claustrum's own, not measured).
func TestFilesMkdirExistsGone(t *testing.T) {
	stubMkdirAt(t, unix.EEXIST)
	if got, _ := mkdirCall(t, t.TempDir(), "n"); !strings.Contains(got, `"result":{"code":"exists_other"}`) {
		t.Errorf("frame = %s, want exists_other", got)
	}
}

// TestFilesMkdirParentOpenErrors: a parent that does not open for reading gives
// denied with errno 13, and a link loop above the parent gives -32603 with
// errno 40 (claustrum's own, not measured).
func TestFilesMkdirParentOpenErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	got, _ := mkdirCall(t, filepath.Join(root, "loop", "p"), "n")
	if want := `"error":{"code":-32603,"message":"files.mkdir: error ` + strconv.Itoa(int(unix.ELOOP)) + ": " + unix.ELOOP.Error() + `"}`; !strings.Contains(got, want) {
		t.Errorf("frame = %s, want %s", got, want)
	}
	if os.Geteuid() == 0 {
		return
	}
	np := filepath.Join(root, "np")
	if err := os.Mkdir(np, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(np, 0o700) })
	got, line := mkdirCall(t, np, "n")
	if !strings.Contains(got, `"result":{"code":"denied","errno":13}`) {
		t.Errorf("frame = %s, want denied 13", got)
	}
	if w := "[Files] mkdir: denied (error 13: permission denied)"; line != w {
		t.Errorf("log = %q, want %q", line, w)
	}
	if _, err := os.Lstat(filepath.Join(np, "n")); err == nil {
		t.Error("the call made a folder")
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
