//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSpawnRefusalReasonsWindows pins the reasons that Windows measured: a folder as
// the command is not_found, a text file that is not a program is other, and any
// launcher is refused with the fixed text.
func TestSpawnRefusalReasonsWindows(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "noexec.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, _ := helperCommand(t, "echo")
	launcherText := "the managed launcher cannot be used: a launcher cannot be applied on a Windows host"
	for _, tc := range []struct {
		name   string
		params map[string]any
		reason string
	}{
		{"folder as command", map[string]any{"id": "a", "command": dir}, "not_found"},
		{"text file", map[string]any{"id": "a", "command": txt}, "other"},
		{"launcher", map[string]any{"id": "a", "command": exe, "launcher": []string{exe}}, "launcher"},
		{"launcher with a bare command", map[string]any{"id": "a", "command": "whoami", "launcher": []string{exe}}, "launcher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnErrorOf(t, tc.params)
			if tc.reason == "launcher" {
				if want := errJSON(t, -32004, launcherText, "launcher"); got != want {
					t.Fatalf("error = %s\nwant    %s", got, want)
				}
				return
			}
			var e struct {
				Data struct{ Reason string } `json:"data"`
			}
			if err := json.Unmarshal([]byte(got), &e); err != nil || e.Data.Reason != tc.reason {
				t.Fatalf("error = %s, want reason %s", got, tc.reason)
			}
		})
	}
}
