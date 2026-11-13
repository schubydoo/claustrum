package main

import "testing"

// TestFilesFindUnknownOnWindows: Windows has no files.find. Each shape gets
// -32601 (4c111e86, measured on Windows).
func TestFilesFindUnknownOnWindows(t *testing.T) {
	dir := t.TempDir()
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unknown method: files.find"}}`
	s := newTestServer(t)
	for _, line := range []string{
		rpcLine(t, "files.find", map[string]any{"roots": []string{dir}, "suffix": "settings.json"}),
		rpcLine(t, "files.find", map[string]any{"roots": []string{}, "suffix": 5}),
		`{"jsonrpc":"2.0","id":1,"method":"files.find","auth":"` + testToken + `"}`,
	} {
		if got := dispatchRaw(t, s, line); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", line, got, want)
		}
	}
}
