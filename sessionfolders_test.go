package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// The folders that a spawn records: the cwd of a Claude Code shape, and for the
// CLI path the --add-dir folders before `--`. Another command records nothing,
// and so does a spawn with no cwd.
func TestSessionFoldersOf(t *testing.T) {
	cwd := t.TempDir()
	e1, e2, e3 := filepath.Join(cwd, "e1"), filepath.Join(cwd, "e2"), filepath.Join(cwd, "e3")
	cli := filepath.Join(t.TempDir(), "ccd-cli", "2.0.0")
	for _, c := range []struct {
		name    string
		command string
		args    []string
		cwd     string
		want    []string
	}{
		{"cli path", cli, []string{"--add-dir=" + e1, "--add-dir", e2, "--", "--add-dir=" + e3}, cwd, []string{cwd, e1, e2}},
		{"relative add-dir", cli, []string{"--add-dir=e1"}, cwd, []string{cwd, e1}},
		{"several values", cli, []string{"--add-dir", e1, e2, e3}, cwd, []string{cwd, e1, e2, e3}},
		{"two flags", cli, []string{"--add-dir", e1, "--add-dir", e2}, cwd, []string{cwd, e1, e2}},
		{"next flag ends", cli, []string{"--add-dir", e1, "-x", e2}, cwd, []string{cwd, e1}},
		{"equals takes one", cli, []string{"--add-dir=" + e1, e2}, cwd, []string{cwd, e1}},
		{"empty value", cli, []string{"--add-dir", "", e1}, cwd, []string{cwd, e1}},
		{"dashes end", cli, []string{"--add-dir", e1, "--", e2}, cwd, []string{cwd, e1}},
		{"stream-json shape", "/bin/sh", []string{"plainhold.sh", "stream-json", "--add-dir=" + e1}, cwd, []string{cwd}},
		{"output format", "sh", []string{"--output-format=stream-json"}, cwd, []string{cwd}},
		{"plain command", "sleep", []string{"300"}, cwd, nil},
		{"no cwd", cli, []string{"--add-dir=" + e1}, "", nil},
	} {
		if got := sessionFoldersOf(c.command, c.args, c.cwd); !slices.Equal(got, c.want) {
			t.Errorf("%s: sessionFoldersOf = %q, want %q", c.name, got, c.want)
		}
	}
}

// emptyGitIn makes <dir>/bin with an empty executable git. A start of it fails, so
// a test sees when it ran.
func emptyGitIn(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// With the only git on PATH in a session folder, git.status and a spawn of a bare
// git get the refusal frames of b3081b51, byte for byte, twice. The folder becomes
// a session folder by a spawn of a Claude Code shape with that cwd.
func TestNoOtherGitFrames(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	s := t.TempDir()
	bin := emptyGitIn(t, s)
	sock := startSocketServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "echo")
	spawn := rpcLine(t, "process.spawn", map[string]any{"id": "cc", "command": exe, "args": []string{"stream-json"}, "cwd": s, "env": env})
	if raw := string(cl.call(spawn)); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("spawn = %s", raw)
	}
	t.Setenv("PATH", bin)
	refusalBytes, _ := json.Marshal(sessiondirs.NoOtherGitError{}.Error())
	refusal := string(refusalBytes)
	status := strings.Replace(rpcLine(t, "git.status", map[string]any{"path": s, "baseRepo": s}), `"id":1`, `"id":2`, 1)
	wantStatus := `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"git cannot run on this host; git not run: ` + refusal[1:len(refusal)-1] + `"}}`
	for range 2 {
		if raw := string(cl.call(status)); raw != wantStatus {
			t.Errorf("git.status = %s\nwant %s", raw, wantStatus)
		}
	}
	gitSpawn := strings.Replace(rpcLine(t, "process.spawn", map[string]any{"id": "g1", "command": "git", "args": []string{"--version"}}), `"id":1`, `"id":3`, 1)
	wantSpawn := `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":` + refusal + `,"data":{"reason":"other"}}}`
	if raw := string(cl.call(gitSpawn)); raw != wantSpawn {
		t.Errorf("spawn of git = %s\nwant %s", raw, wantSpawn)
	}
}

// "sessionFoldersOff":true turns the check off while the request runs: the git in
// the session folder starts, and its start error is the frame.
func TestSessionFoldersOffStartsTheSessionGit(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	buf := captureLogBuf(t)
	s := t.TempDir()
	bin := emptyGitIn(t, s)
	t.Setenv("PATH", bin)
	cl := dial(t, startSocketServer(t))
	sessiondirs.Remember(s)
	raw := string(cl.call(rpcLine(t, "git.status", map[string]any{"path": s, "baseRepo": s, "sessionFoldersOff": true})))
	if strings.Contains(raw, "For security") || !strings.Contains(raw, `"error"`) {
		t.Errorf("git.status with the off switch = %s, want the start error of the session git", raw)
	}
	raw = string(cl.call(rpcLine(t, "process.spawn", map[string]any{"id": "g1", "command": "git", "sessionFoldersOff": true})))
	if strings.Contains(raw, "For security") {
		t.Errorf("spawn with the off switch = %s", raw)
	}
	// One OFF line for both requests, and no on-again line before a request
	// without the switch.
	const offLine, onLine = "[sessiondirs] the check of session folders is OFF:", "[sessiondirs] the check of session folders is on again:"
	if n := strings.Count(buf.String(), offLine); n != 1 || strings.Contains(buf.String(), onLine) {
		t.Errorf("before the plain request: %d OFF lines, log %q", n, buf.String())
	}
	raw = string(cl.call(rpcLine(t, "process.spawn", map[string]any{"id": "g2", "command": "git"})))
	if !strings.Contains(raw, "For security") {
		t.Errorf("spawn after the off switch = %s, want the refusal", raw)
	}
	if n := strings.Count(buf.String(), onLine); n != 1 {
		t.Errorf("the on-again line came %d times, want once at the plain request", n)
	}
}
