package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readRealCalls sends each files.read request over the socket and returns the
// replies with the temp root tokenized.
func readRealCalls(t *testing.T, root string, params []map[string]any) []json.RawMessage {
	t.Helper()
	cl := dial(t, startSocketServer(t))
	got := make([]json.RawMessage, len(params))
	for i, p := range params {
		got[i] = normPath(cl.call(req(i+1, "files.read", p)), root)
	}
	return got
}

// TestSocketFilesReadReal pins the rows of build 683cc6a9: a read through a link
// with "real":true carries real after exists, and a read without real or with
// "real":false has no real member.
func TestSocketFilesReadReal(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	d := filepath.Join(root, "d")
	writeFile(t, filepath.Join(d, "real", "f"), "hello\n", 0o644)
	if err := os.Symlink("real", filepath.Join(d, "l")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	p := filepath.Join(d, "l", "f")
	got := readRealCalls(t, root, []map[string]any{
		{"path": p, "real": true},
		{"path": p},
		{"path": p, "real": false},
	})
	assertGolden(t, "socket_files_read_real.golden.json", encodeGolden(t, got))
}

// TestSocketFilesReadRealNotMeasured pins answers that no row measures
// (claustrum's own, not measured): a missing file with real, and a relative
// path with real.
func TestSocketFilesReadRealNotMeasured(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	got := readRealCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "none"), "real": true},
		{"path": "none-such-file-for-read-real", "real": true},
	})
	for i, g := range got {
		if !strings.Contains(string(g), `"result":{"content":"","exists":false}`) {
			t.Errorf("reply %d = %s, want a missing file with no real", i+1, g)
		}
	}
}

// TestFilesReadRealRelative checks that a relative path with real gives the
// absolute real name (claustrum's own, not measured).
func TestFilesReadRealRelative(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "f"), "x", 0o644)
	t.Chdir(root)
	want := filepath.ToSlash(filepath.Join(root, "f"))
	if got := readRealName("f"); got != want {
		t.Errorf("readRealName(f) = %q, want %q", got, want)
	}
}

// TestFilesReadRealLoop checks that a name that cannot be resolved gives no
// real (claustrum's own, not measured).
func TestFilesReadRealLoop(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if got := readRealName(a); got != "" {
		t.Errorf("readRealName(loop) = %q, want empty", got)
	}
}
