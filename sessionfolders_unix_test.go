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
	_ = sessiondirs.Remember(s)
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
	_ = sessiondirs.Remember(s)
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

// A spawned bare git gets the three trace names of git set to 0, and none with the
// off switch.
func TestBareGitSpawnGetsTheTraceNames(t *testing.T) {
	bin := linkTestBinary(t, t.TempDir(), "git")
	t.Setenv("PATH", bin)
	t.Setenv("GIT_TRACE2", "/nowhere")
	cl := dial(t, startSocketServer(t))
	names := []string{"GIT_TRACE2", "GIT_TRACE2_EVENT", "GIT_TRACE2_PERF"}
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "printenvs"}
	if out := spawnOut(t, cl, map[string]any{"id": "g1", "command": "git", "args": names, "env": env}); out != "GIT_TRACE2=0\nGIT_TRACE2_EVENT=0\nGIT_TRACE2_PERF=0\n" {
		t.Errorf("the trace names of the git child = %q", out)
	}
	if out := spawnOut(t, cl, map[string]any{"id": "g2", "command": "git", "args": names, "env": env, "sessionFoldersOff": true}); out != "GIT_TRACE2=/nowhere\nGIT_TRACE2_EVENT=\nGIT_TRACE2_PERF=\n" {
		t.Errorf("with the off switch the trace names = %q", out)
	}
}

// cliPathCommand links this test binary as <dir>/ccd-cli/2.0.0, a command of the
// CLI path.
func cliPathCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "ccd-cli", "2.0.0")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, cli); err != nil {
		t.Fatal(err)
	}
	return cli
}

// A spawn of the CLI path whose session folder cannot be saved, with the first
// PATH entry of the daemon in that folder, gets the refusal frame of b3081b51,
// byte for byte, and starts no child. The log has the line of
// the record and then the Failed to start line. The control with a record folder
// that works starts the child.
func TestUnsavedSessionFolderRefusesTheSpawn(t *testing.T) {
	cl := dial(t, startSocketServer(t))
	cli := cliPathCommand(t)
	_, env := helperCommand(t, "echo")
	for _, broken := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
		var err error
		if broken {
			err = os.WriteFile(dir, []byte("x"), 0o600)
		} else {
			err = os.Mkdir(dir, 0o700)
		}
		if err != nil {
			t.Fatal(err)
		}
		restore := sessiondirs.UseForTest(dir)
		buf := captureLogBuf(t)
		id := fmt.Sprintf("cc-%v", broken)
		// The first entry of the daemon PATH lies in the cwd.
		cwd := t.TempDir()
		bin := filepath.Join(cwd, "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		raw := string(cl.call(rpcLine(t, "process.spawn", map[string]any{"id": id, "command": cli, "args": []string{"hi"}, "cwd": cwd, "env": env})))
		restore()
		if !broken {
			if raw != `{"jsonrpc":"2.0","id":1,"result":{"success":true}}` {
				t.Fatalf("control spawn = %s", raw)
			}
			if out := streamBytes(t, cl.waitExit(id), "stdout"); out != "hi\n" {
				t.Fatalf("the control child wrote %q, want hi", out)
			}
			continue
		}
		const text = "Claude couldn't save its list of session folders to ~/.claude/remote on this host. Free up disk space or fix that folder's permissions"
		if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + text + `","data":{"reason":"other"}}}`; raw != want {
			t.Errorf("spawn = %s\nwant %s", raw, want)
		}
		logs := buf.String()
		record := strings.Index(logs, "[sessiondirs] cannot write to the record of session folders in "+fmt.Sprintf("%q", filepath.ToSlash(dir))+": ")
		failed := strings.Index(logs, "[process.Manager] Failed to start process "+id+": "+text+"\n")
		if record < 0 || failed < record {
			t.Errorf("log = %q, want the record line and then the Failed to start line", logs)
		}
		if strings.Contains(logs, "Process "+id+" started") {
			t.Errorf("the refused spawn started a child: %q", logs)
		}
	}
}
