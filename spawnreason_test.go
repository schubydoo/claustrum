package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// spawnErrorOf sends one process.spawn and returns the raw error member of the
// reply, or "" when the reply has none.
func spawnErrorOf(t *testing.T, params map[string]any) string {
	t.Helper()
	s := newTestServer(t)
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchRaw(t, s, string(req))
	var f struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &f); err != nil {
		t.Fatalf("reply %q: %v", out, err)
	}
	return string(f.Error)
}

// TestSpawnRefusalReasons pins the reasons of the refusals in the start path that
// are measured on both Linux and Windows. The text of each member is a whole-string
// compare, so the data member must follow the message and carry the reason.
func TestSpawnRefusalReasons(t *testing.T) {
	dir := t.TempDir()
	afile := filepath.Join(dir, "afile")
	if err := os.WriteFile(afile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, _ := helperCommand(t, "echo")
	missing := filepath.Join(dir, "nocwd")
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"cwd missing", map[string]any{"id": "a", "command": exe, "cwd": missing},
			errJSON(t, -32603, "chdir "+missing+": "+statErrText(t, missing), "cwd_missing")},
		{"cwd is a file", map[string]any{"id": "a", "command": exe, "cwd": afile},
			errJSON(t, -32603, "chdir "+afile+": not a directory", "cwd_missing")},
		{"bare name not found", map[string]any{"id": "a", "command": "no-such-cmd"}, ""},
		{"no id", map[string]any{"command": exe},
			`{"code":-32602,"message":"Process ID is required"}`},
		{"no command", map[string]any{"id": "r9"},
			`{"code":-32602,"message":"Command is required"}`},
		{"command of a wrong type", map[string]any{"id": "r9", "command": 5},
			`{"code":-32602,"message":"Invalid params"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnErrorOf(t, tc.params)
			if tc.want == "" {
				// Message text differs by system. The reason is the same.
				var e struct {
					Data struct{ Reason string } `json:"data"`
				}
				if err := json.Unmarshal([]byte(got), &e); err != nil || e.Data.Reason != "not_found" {
					t.Fatalf("error = %s, want reason not_found", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("error = %s\nwant    %s", got, tc.want)
			}
		})
	}
}

// TestSpawnFailureReasonFallback pins the reason of an error that no rule lists.
func TestSpawnFailureReasonFallback(t *testing.T) {
	if got := spawnFailureReason(errors.New("x"), ""); got.Reason != "other" {
		t.Fatalf("reason = %q, want other", got.Reason)
	}
}

func statErrText(t *testing.T, p string) string {
	t.Helper()
	_, err := os.Stat(p)
	if err == nil {
		t.Fatal("path exists")
	}
	if runtime.GOOS == "windows" {
		return "GetFileAttributesEx " + p + ": " + err.(*os.PathError).Err.Error()
	}
	return "stat " + p + ": " + err.(*os.PathError).Err.Error()
}

func errJSON(t *testing.T, code int, msg, reason string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Data    spawnReason `json:"data"`
	}{code, msg, spawnReason{reason}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
