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
// CLI path the --add-dir folders before `--`. A command named ccd-cli and a spawn
// with a launcher are the CLI path. A bare git with stream-json, another command
// and a spawn with no cwd record nothing.
func TestSessionFoldersOf(t *testing.T) {
	cwd := t.TempDir()
	e1, e2, e3 := filepath.Join(cwd, "e1"), filepath.Join(cwd, "e2"), filepath.Join(cwd, "e3")
	cli := filepath.Join(t.TempDir(), "ccd-cli", "2.0.0")
	named := filepath.Join(t.TempDir(), "x", "ccd-cli")
	tool := filepath.Join(t.TempDir(), "y", "tool")
	for _, c := range []struct {
		name     string
		command  string
		args     []string
		cwd      string
		want     []string
		launched bool
	}{
		{"named ccd-cli", named, []string{"--x"}, cwd, []string{cwd}, false},
		{"named ccd-cli add-dir", named, []string{"--add-dir", e1, "--"}, cwd, []string{cwd, e1}, false},
		{"launcher", tool, []string{"--x"}, cwd, []string{cwd}, true},
		{"launcher add-dir", tool, []string{"--add-dir", e1, "--"}, cwd, []string{cwd, e1}, true},
		{"bare git stream-json", "git", []string{"--version", "stream-json"}, cwd, nil, false},
		{"git path stream-json", "/usr/bin/git", []string{"--version", "stream-json"}, cwd, []string{cwd}, false},
		{"bare git", "git", []string{"--version"}, cwd, nil, false},
		{"plain path", tool, []string{"--x"}, cwd, nil, false},
		{"cli path", cli, []string{"--add-dir=" + e1, "--add-dir", e2, "--", "--add-dir=" + e3}, cwd, []string{cwd, e1, e2}, false},
		{"relative add-dir", cli, []string{"--add-dir=e1"}, cwd, []string{cwd, e1}, false},
		{"several values", cli, []string{"--add-dir", e1, e2, e3}, cwd, []string{cwd, e1, e2, e3}, false},
		{"two flags", cli, []string{"--add-dir", e1, "--add-dir", e2}, cwd, []string{cwd, e1, e2}, false},
		{"next flag ends", cli, []string{"--add-dir", e1, "-x", e2}, cwd, []string{cwd, e1}, false},
		{"equals takes one", cli, []string{"--add-dir=" + e1, e2}, cwd, []string{cwd, e1}, false},
		{"empty value", cli, []string{"--add-dir", "", e1}, cwd, []string{cwd, e1}, false},
		{"dashes end", cli, []string{"--add-dir", e1, "--", e2}, cwd, []string{cwd, e1}, false},
		{"stream-json shape", "/bin/sh", []string{"plainhold.sh", "stream-json", "--add-dir=" + e1}, cwd, []string{cwd}, false},
		{"output format", "sh", []string{"--output-format=stream-json"}, cwd, []string{cwd}, false},
		{"plain command", "sleep", []string{"300"}, cwd, nil, false},
		{"no cwd", cli, []string{"--add-dir=" + e1}, "", nil, false},
	} {
		if got := sessionFoldersOf(c.command, c.args, c.cwd, c.launched); !slices.Equal(got, c.want) {
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
	_ = sessiondirs.Remember(s)
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

// A Claude Code spawn without the switch gets its record also while a request with
// the switch holds the check off. A spawn with the switch gets none.
func TestSpawnDuringTheHoldIsRecorded(t *testing.T) {
	cl := dial(t, startSocketServer(t))
	dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	exe, env := helperCommand(t, "echo")
	release := sessiondirs.PauseCheck()
	n := t.TempDir()
	raw := string(cl.call(rpcLine(t, "process.spawn", map[string]any{"id": "cc", "command": exe, "args": []string{"stream-json"}, "cwd": n, "env": env})))
	release()
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("spawn = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, sessiondirs.RecordName(n))); err != nil {
		t.Errorf("the record of the spawn during the hold: %v", err)
	}
	o := t.TempDir()
	raw = string(cl.call(rpcLine(t, "process.spawn", map[string]any{"id": "co", "command": exe, "args": []string{"stream-json"}, "cwd": o, "env": env, "sessionFoldersOff": true})))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("spawn = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, sessiondirs.RecordName(o))); !os.IsNotExist(err) {
		t.Errorf("a spawn with the switch wrote a record: %v", err)
	}
}

// Only the exact name `git` is a bare git, also on Windows: `git.exe`, `GIT` and
// `Git.EXE` run as other commands do.
func TestBareGitNameIsExact(t *testing.T) {
	for name, want := range map[string]bool{"git": true, "git.exe": false, "GIT": false, "Git.EXE": false, "/usr/bin/git": false} {
		if got := bareGitName(name); got != want {
			t.Errorf("bareGitName(%q) = %v, want %v", name, got, want)
		}
	}
}
