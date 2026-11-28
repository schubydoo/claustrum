//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSpawnRefusalReasonsWindows pins the reasons that Windows measured: a folder as
// the command is not_found, and any launcher is refused with the fixed text and the
// windows_host fault (c9125eb8). A text file that is not a program is not_executable,
// and a program file that a writer holds open is busy (87375cc7).
func TestSpawnRefusalReasonsWindows(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "noexec.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, _ := helperCommand(t, "echo")
	busy := filepath.Join(dir, "busy.exe")
	copyFileForTest(t, exe, busy)
	name, err := windows.UTF16PtrFromString(busy)
	if err != nil {
		t.Fatal(err)
	}
	// The row opened the file with a write access and share read only.
	held, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(held) }()
	launcherText := "the managed launcher cannot be used: a launcher cannot be applied on a Windows host"
	for _, tc := range []struct {
		name   string
		params map[string]any
		reason string
	}{
		{"folder as command", map[string]any{"id": "a", "command": dir}, "not_found"},
		{"text file", map[string]any{"id": "a", "command": txt}, "not_executable"},
		{"program held open for a write", map[string]any{"id": "a", "command": busy}, "busy"},
		{"launcher", map[string]any{"id": "a", "command": exe, "launcher": []string{exe}}, "launcher"},
		{"launcher with a bare command", map[string]any{"id": "a", "command": "whoami", "launcher": []string{exe}}, "launcher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnErrorOf(t, tc.params)
			if tc.reason == "launcher" {
				if want := `{"code":-32004,"message":"` + launcherText + `","data":{"launcherFault":"windows_host","reason":"launcher"}}`; got != want {
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

// copyFileForTest copies the file src to dst.
func copyFileForTest(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}
