package main

import "testing"

// TestSocketPlainSpawnCLITempRootShape pins a cliTempRoot text or object on a spawn
// with no gitRead. 4c111e86 and c9125eb8 answer Invalid params and start no child
// (a text and an object, measured on Linux). The other systems are claustrum's
// own, not measured. false runs the child (b3081b51, measured on Windows).
func TestSocketPlainSpawnCLITempRootShape(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	refused := []struct {
		name  string
		value any
	}{
		{"text", t.TempDir()},
		{"object", map[string]any{"x": 1}},
	}
	for i, c := range refused {
		got := string(cl.call(linkSpawnReq(t, i+1, "ct"+itoa(i), "echo", []string{"hi"}, map[string]any{"cliTempRoot": c.value})))
		if want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":{"code":-32602,"message":"Invalid params"}}`; got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
	}
	got := string(cl.call(linkSpawnReq(t, 9, "cf", "echo", []string{"hi"}, map[string]any{"cliTempRoot": false})))
	if want := `{"jsonrpc":"2.0","id":9,"result":{"success":true}}`; got != want {
		t.Fatalf("false: reply = %s\nwant %s", got, want)
	}
	if out := streamBytes(t, cl.waitExit("cf"), "stdout"); out != "hi\n" {
		t.Errorf("false: stdout = %q, want %q", out, "hi\n")
	}
	// The child of the run exited, so a refused spawn that had started a child had
	// its frames by now on this connection.
	for i, c := range refused {
		if f := cl.framesOf("ct" + itoa(i)); len(f) != 0 {
			t.Errorf("%s: %d stream frames, want none", c.name, len(f))
		}
	}
}
