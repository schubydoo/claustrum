package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// realpathCalls sends each request over the socket and returns the replies with
// the temp root tokenized.
func realpathCalls(t *testing.T, root string, params []map[string]any) []json.RawMessage {
	t.Helper()
	cl := dial(t, startSocketServer(t))
	got := make([]json.RawMessage, len(params))
	for i, p := range params {
		got[i] = normPath(cl.call(req(i+1, "files.realpath", p)), root)
	}
	return got
}

// TestSocketFilesRealpath pins the frames that need no link: a path that exists,
// a missing leaf with and without missingLeaf, and the -32602 texts that are the
// same on every system (build 683cc6a9). The Invalid params frame has Linux and
// macOS rows only.
func TestSocketFilesRealpath(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "f"), "x", 0o644)
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "f")},
		{"path": filepath.Join(root, "new"), "missingLeaf": true},
		{"path": filepath.Join(root, "new")},
		{"path": filepath.Join(root, "nodir", "new")},
		{"path": "~/a/../b"},
		{"path": filepath.Join(root, "a") + "\x00b"},
		{"path": 5},
	})
	assertGolden(t, "socket_files_realpath.golden.json", encodeGolden(t, got))
}

// TestSocketFilesRealpathLinks pins the link rows: a link in the middle of the
// path, a missing leaf behind a link, and a dangling link whose target has a
// parent or has none.
func TestSocketFilesRealpathLinks(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	d := filepath.Join(root, "d")
	writeFile(t, filepath.Join(d, "real", "f"), "x", 0o644)
	if err := os.Symlink("real", filepath.Join(d, "l")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if err := os.Symlink("nowhere"+string(filepath.Separator)+"zz", filepath.Join(d, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gone", filepath.Join(d, "y")); err != nil {
		t.Fatal(err)
	}
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(d, "l", "f")},
		{"path": filepath.Join(d, "l", "new"), "missingLeaf": true},
		{"path": filepath.Join(d, "x"), "missingLeaf": true},
		{"path": filepath.Join(d, "y"), "missingLeaf": true},
	})
	assertGolden(t, "socket_files_realpath_links.golden.json", encodeGolden(t, got))
}

// TestSocketFilesRealpathNotMeasured pins two answers that no row measures
// (claustrum's own, not measured): a missing folder with missingLeaf, and a
// dangling link without missingLeaf.
func TestSocketFilesRealpathNotMeasured(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	want := `"result":{"exists":false}`
	g := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "nodir", "new"), "missingLeaf": true},
	})[0]
	if !strings.Contains(string(g), want) {
		t.Errorf("missing folder reply = %s, want %s", g, want)
	}
	if err := os.Symlink("gone", filepath.Join(root, "y")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	g = realpathCalls(t, root, []map[string]any{{"path": filepath.Join(root, "y")}})[0]
	if !strings.Contains(string(g), want) {
		t.Errorf("dangling reply = %s, want %s", g, want)
	}
}

// TestSocketFilesRealpathRefusesRelative pins the system-specific -32602 text
// of a relative path.
func TestSocketFilesRealpathRefusesRelative(t *testing.T) {
	want := "files.realpath: the path must be absolute"
	if runtime.GOOS == "windows" {
		want = "files.realpath: the path must begin with a drive letter, a colon and a slash"
	}
	got := realpathCalls(t, t.TempDir(), []map[string]any{{"path": "rel/x"}, {"path": ""}})
	for i, g := range got {
		if !strings.Contains(string(g), `"code":-32602`) || !strings.Contains(string(g), `"message":"`+want+`"`) {
			t.Errorf("reply %d = %s, want -32602 %q", i+1, g, want)
		}
	}
}
