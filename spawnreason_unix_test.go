//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSpawnRefusalReasonsUnix pins the reasons that Linux measured: a missing path,
// a file of mode 0644, a folder as the command, a launcher that is missing, a
// launcher with a bare command, and a cwd of mode 000 (also measured on macOS).
// The mode 000 case needs a user other than root.
func TestSpawnRefusalReasonsUnix(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, _ := helperCommand(t, "echo")
	missing := filepath.Join(dir, "no", "such", "cmd")
	denied := filepath.Join(dir, "denied")
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"absolute path missing", map[string]any{"id": "a", "command": missing},
			errJSON(t, -32603, "fork/exec "+missing+": no such file or directory", "not_found")},
		{"file of mode 0644", map[string]any{"id": "a", "command": plain},
			errJSON(t, -32603, "fork/exec "+plain+": permission denied", "not_executable")},
		{"folder as command", map[string]any{"id": "a", "command": dir},
			errJSON(t, -32603, "fork/exec "+dir+": permission denied", "not_executable")},
		{"launcher missing", map[string]any{"id": "a", "command": exe, "launcher": []string{missing}},
			errJSON(t, -32004, "the managed launcher cannot be used: launcher `"+missing+"` does not exist or is not an executable regular file", "launcher")},
		{"launcher with a bare command", map[string]any{"id": "a", "command": "true", "launcher": []string{exe}},
			errJSON(t, -32602, "command must be an absolute path when a launcher is given", "other")},
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(denied, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(denied, 0o755) })
		cases = append(cases, struct {
			name   string
			params map[string]any
			want   string
		}{"cwd of mode 000", map[string]any{"id": "a", "command": exe, "cwd": denied},
			errJSON(t, -32603, "fork/exec "+exe+": permission denied", "cwd_unusable")})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spawnErrorOf(t, tc.params); got != tc.want {
				t.Fatalf("error = %s\nwant    %s", got, tc.want)
			}
		})
	}
}
