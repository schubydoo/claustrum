package main

import (
	"path/filepath"
	"testing"
)

// TestFilesMkdirUnknownOnWindows: Windows has no files.mkdir. Each shape gets
// -32601 and makes no folder (c9125eb8, measured on Windows).
func TestFilesMkdirUnknownOnWindows(t *testing.T) {
	dir := t.TempDir()
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unknown method: files.mkdir"}}`
	s := newTestServer(t)
	for _, line := range []string{
		rpcLine(t, "files.mkdir", map[string]any{"parent": dir, "name": "new1"}),
		rpcLine(t, "files.mkdir", map[string]any{"parent": 1, "name": "x"}),
		`{"jsonrpc":"2.0","id":1,"method":"files.mkdir","auth":"` + testToken + `"}`,
	} {
		if got := dispatchRaw(t, s, line); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", line, got, want)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*")); len(m) != 0 {
		t.Errorf("the folder holds %v, want nothing", m)
	}
}
