package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSocketReapedInDrainIsNotRunning drives the 90fca6e6 reap test through a real
// socket, in a real exit-drain window. The unit test in reaped_not_running_test.go
// hand-builds the flags; this one lets the daemon set them.
//
// The fixture is the one the bounded-drain test uses: "orphan-stdout" starts a
// long sleeper on its own stdout, prints a line, and exits. The process is reaped
// at once, and the grandchild holds the pipe, so the drain window stays open.
// exitDrainGrace is RAISED here, not shrunk, so there is a window to call into.
//
// Measured against both reference binaries in scratch/probe/ref90-capture.md
// section A: 19f30c46 answers {"success":true,"applied":6} and running:true
// inside this window, 90fca6e6 answers -32602 and running:false.
func TestSocketReapedInDrainIsNotRunning(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 3 * time.Second
	t.Cleanup(func() { exitDrainGrace = old })

	sock := startSocketServer(t)
	cl := dial(t, sock)

	req := func(id int, method string, params map[string]any) string {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method,
			"auth": testToken, "params": params,
		})
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
		return string(b)
	}

	exe, env := helperCommand(t, "orphan-stdout")
	payload := base64.StdEncoding.EncodeToString([]byte("hello\n"))

	// CONTROL 1: a plainly live process. Without it, a daemon that answered
	// running:false to everything would pass the drain assertions below. Keep both
	// of its checks as t.Fatalf, and keep them first.
	sleeper, senv := helperCommand(t, "sleep")
	cl.call(req(1, "process.spawn", map[string]any{
		"id": "LIVE", "command": sleeper, "args": []string{"30"}, "env": senv,
	}))
	if got := string(cl.call(req(2, "process.reattach", map[string]any{"id": "LIVE"}))); !strings.Contains(got, `"running":true`) {
		t.Fatalf("reattach to a live process = %s, want running true", got)
	}
	if got := string(cl.call(req(3, "process.stdin", map[string]any{"id": "LIVE", "data": payload}))); strings.Contains(got, "Process not running") {
		t.Fatalf("stdin to a live process = %s, want no running error", got)
	}

	// The drain window. The helper exits almost at once, so a short settle is
	// enough to be inside it, and exitDrainGrace keeps it open for 3 seconds.
	cl.call(req(4, "process.spawn", map[string]any{
		"id": "DRAIN", "command": exe, "args": []string{"20"}, "env": env,
	}))
	time.Sleep(300 * time.Millisecond)

	if got := string(cl.call(req(5, "process.reattach", map[string]any{"id": "DRAIN"}))); !strings.Contains(got, `"running":false`) {
		t.Errorf("reattach inside the drain = %s, want running false", got)
	}
	if got := string(cl.call(req(6, "process.stdin", map[string]any{"id": "DRAIN", "data": payload}))); !strings.Contains(got, "Process not running") {
		t.Errorf("stdin inside the drain = %s, want the not-running error", got)
	}

	// After the exit frame, the same reattach must report stdinApplied 0. The zero
	// is what shows the counter never moved, rather than the process merely being
	// gone, and it is the assertion no unit test here can make.
	//
	// The running:false line below it is documentation, not a control: running is
	// false past the exit frame with or without the fix, so no mutant moves it.
	// The load-bearing control is CONTROL 1 above, which is a t.Fatalf and runs
	// first. Every "new arm" assertion in this test is one-sided and cannot tell a
	// correct refusal from a daemon that refuses everything.
	cl.waitExit("DRAIN")
	got := string(cl.call(req(7, "process.reattach", map[string]any{"id": "DRAIN"})))
	if !strings.Contains(got, `"running":false`) {
		t.Errorf("reattach after the drain = %s, want running false", got)
	}
	if !strings.Contains(got, `"stdinApplied":0`) {
		t.Errorf("reattach after the drain = %s, want stdinApplied 0", got)
	}
}
