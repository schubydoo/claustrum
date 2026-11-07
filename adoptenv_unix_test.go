//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSocketAdoptEnvTestsTheDaemonTempFolder: after a good adopt a child takes
// TMPDIR from the daemon, not from the sent entries, and the daemon value takes the
// test of a spawn. A folder that takes no file is not handed on (c9125eb8, measured
// on Linux).
func TestSocketAdoptEnvTestsTheDaemonTempFolder(t *testing.T) {
	clearClaudeSSHEnv(t)
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "gone"), link); err != nil {
		t.Fatal(err)
	}
	_, sock := newRunningServer(t)
	t.Setenv("TMPDIR", link) // after the server, whose start makes folders in TMPDIR
	logs := captureLogBuf(t)
	cl := dial(t, sock)
	adopt := `{"jsonrpc":"2.0","id":1,"method":"server.adoptEnv","params":{"env":["A=1","TMPDIR=/s/tmp"]}}`
	if got := string(cl.call(authed(adopt))); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("adopt: %s", got)
	}
	env := adoptChildEnv(t, cl, "tmp", 2)
	if v, ok := env["TMPDIR"]; ok {
		t.Errorf("the child has TMPDIR=%q, want it not handed on", v)
	}
	if env["A"] != "1" {
		t.Errorf("the child has A=%q, want 1", env["A"])
	}
	if want := "the inherited TMPDIR=" + link + " is not handed on to a child"; !strings.Contains(logs.String(), want) {
		t.Errorf("no line %q in the log:\n%s", want, logs.String())
	}
}
