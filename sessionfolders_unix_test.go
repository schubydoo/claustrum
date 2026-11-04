//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// linkTestBinary puts a link named name to this test binary in a new bin folder
// below dir, and returns that folder.
func linkTestBinary(t *testing.T, dir, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
	return bin
}

// spawnOut spawns one process and returns its stdout once it exited.
func spawnOut(t *testing.T, cl *testClient, params map[string]any) string {
	t.Helper()
	raw := string(cl.call(rpcLine(t, "process.spawn", params)))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("spawn = %s", raw)
	}
	return streamBytes(t, cl.waitExit(params["id"].(string)), "stdout")
}

// A spawn of a bare git does not get the eight editor and pager names of the daemon
// environment. A name in the env of the spawn stays, and so does another name of
// the daemon.
func TestBareGitSpawnLosesTheDaemonEditors(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	bin := linkTestBinary(t, t.TempDir(), "git")
	t.Setenv("PATH", bin)
	for _, n := range sessiondirs.EditorNames {
		t.Setenv(n, "x")
	}
	t.Setenv("FOOCTL", "x")
	cl := dial(t, startSocketServer(t))
	names := append(append([]string{}, sessiondirs.EditorNames...), "FOOCTL")
	out := spawnOut(t, cl, map[string]any{"id": "g1", "command": "git", "args": names,
		"env": map[string]string{"CLAUSTRUM_TEST_HELPER": "printenvs"}})
	want := ""
	for _, n := range sessiondirs.EditorNames {
		want += n + "=\n"
	}
	if want += "FOOCTL=x\n"; out != want {
		t.Errorf("the env of the git child:\n%s\nwant\n%s", out, want)
	}
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "printenvs"}
	for _, n := range sessiondirs.EditorNames {
		env[n] = "y"
	}
	out = spawnOut(t, cl, map[string]any{"id": "g2", "command": "git", "args": sessiondirs.EditorNames, "env": env})
	if want := strings.Repeat("=y\n", 1); strings.Count(out, want) != len(sessiondirs.EditorNames) {
		t.Errorf("the names of the spawn env did not stay:\n%s", out)
	}
}

// A bare git spawn passes over the git in a session folder and runs the next one
// on PATH. A bare command of another name found in a session folder runs, with
// the warning line.
func TestBareSpawnInASessionFolder(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	buf := captureLogBuf(t)
	s := t.TempDir()
	sessionBin := emptyGitIn(t, s)
	if err := os.Symlink(mustExe(t), filepath.Join(sessionBin, "hello")); err != nil {
		t.Fatal(err)
	}
	other := linkTestBinary(t, t.TempDir(), "git")
	t.Setenv("PATH", sessionBin+":"+other)
	cl := dial(t, startSocketServer(t))
	sessiondirs.Remember(s)
	if out := spawnOut(t, cl, map[string]any{"id": "g1", "command": "git", "args": []string{"ran"},
		"env": map[string]string{"CLAUSTRUM_TEST_HELPER": "echo"}}); out != "ran\n" {
		t.Errorf("the next git wrote %q, want ran", out)
	}
	if out := spawnOut(t, cl, map[string]any{"id": "h1", "command": "hello", "args": []string{"hi"},
		"env": map[string]string{"CLAUSTRUM_TEST_HELPER": "echo"}}); out != "hi\n" {
		t.Errorf("hello wrote %q, want hi", out)
	}
	p := filepath.Join(sessionBin, "hello")
	line := fmt.Sprintf(`[sessiondirs] WARNING: starting "hello" from %q, which is in or reached through a folder that a Claude Code session can write. A sandboxed session could replace it with a program of its own, which would then run outside the sandbox. To prevent this, see that PATH on this host finds "hello" outside such folders, or, where the command is configured (an MCP server's settings, for one), give the full path of a copy outside them.`+"\n", p)
	if !strings.Contains(buf.String(), line) {
		t.Errorf("log = %q, want the warning line", buf.String())
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// A bare git spawn gets the session folders cut from its PATH, also from the PATH
// of the spawn env, and git is found on the PATH of the daemon. With the off switch
// the PATH of the child stays as given.
func TestBareGitSpawnPathLosesSessionFolders(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	s := t.TempDir()
	sessionBin := emptyGitIn(t, s)
	other := linkTestBinary(t, t.TempDir(), "git")
	t.Setenv("PATH", other)
	cl := dial(t, startSocketServer(t))
	sessiondirs.Remember(s)
	given := sessionBin + ":" + other
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "printenvs", "PATH": given}
	if out := spawnOut(t, cl, map[string]any{"id": "g1", "command": "git", "args": []string{"PATH"}, "env": env}); out != "PATH="+other+"\n" {
		t.Errorf("the PATH of the git child = %q, want %q", out, "PATH="+other)
	}
	out := spawnOut(t, cl, map[string]any{"id": "g2", "command": "git", "args": []string{"PATH"}, "env": env, "sessionFoldersOff": true})
	if out != "PATH="+given+"\n" {
		t.Errorf("with the off switch the PATH = %q, want %q", out, "PATH="+given)
	}
}
