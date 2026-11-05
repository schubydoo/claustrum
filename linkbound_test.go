package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// linkSpawnReq builds a process.spawn request of a helper mode with extra params.
func linkSpawnReq(t *testing.T, id int, procID, mode string, args []string, extra map[string]any) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	params := map[string]any{"id": procID, "command": exe, "args": args, "env": env}
	for k, v := range extra {
		params[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// framesOf returns the frames of processID that cl got so far.
func (cl *testClient) framesOf(processID string) []streamFrame {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	var out []streamFrame
	for _, f := range cl.fr {
		if f.ProcessID == processID {
			out = append(out, f)
		}
	}
	return out
}

// TestSocketLinkBoundRefusals pins the two refusals of 683cc6a9 (measured on
// Linux): linkBound with a launcher gets its own text and reason other, and with
// "pty":true plain Invalid params with no data.
func TestSocketLinkBoundRefusals(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	launcher := string(cl.call(linkSpawnReq(t, 1, "lb1", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{}, "launcher": []string{"/usr/bin/env"},
	})))
	if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"linkBound cannot be combined with pty or launcher","data":{"reason":"other"}}}`; launcher != want {
		t.Errorf("launcher reply = %s\nwant %s", launcher, want)
	}
	pty := string(cl.call(linkSpawnReq(t, 2, "lb2", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{}, "pty": true,
	})))
	if want := `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Invalid params"}}`; pty != want {
		t.Errorf("pty reply = %s\nwant %s", pty, want)
	}
	ok := string(cl.call(linkSpawnReq(t, 3, "lb3", "sleep", []string{"0"}, map[string]any{
		"linkBound": map[string]any{"unattendedSeconds": 4},
	})))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"success":true}}`; ok != want {
		t.Errorf("spawn reply = %s\nwant %s", ok, want)
	}
	assertGolden(t, "socket_process_linkbound_refusals.golden.json",
		encodeGolden(t, []json.RawMessage{json.RawMessage(launcher), json.RawMessage(pty), json.RawMessage(ok)}))
}

// TestSocketLinkBoundStaysOnLink pins the link of 683cc6a9 (measured on Linux):
// stdin from another connection answers Process not found, the link writes it,
// and a reattach from another connection gets found true with the seq range of
// the link. That B gets the replay, no live frame, and that A stays open is
// claustrum's own, not measured.
func TestSocketLinkBoundStaysOnLink(t *testing.T) {
	sock := startSocketServer(t)
	a, b := dial(t, sock), dial(t, sock)
	if got := string(a.call(linkSpawnReq(t, 1, "lc", "cat", []string{}, map[string]any{
		"linkBound": map[string]any{"unattendedSeconds": 120},
	}))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("spawn = %s", got)
	}
	notFound := string(b.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`)))
	if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Process not found"}}`; notFound != want {
		t.Errorf("stdin from B = %s\nwant %s", notFound, want)
	}
	applied := string(a.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`)))
	if want := `{"jsonrpc":"2.0","id":2,"result":{"success":true,"applied":3}}`; applied != want {
		t.Errorf("stdin from A = %s\nwant %s", applied, want)
	}
	a.wait(func() bool { return len(a.fr) >= 1 })
	if f := a.framesOf("lc"); len(f) != 1 || f[0].Data != "aGkK" || f[0].Seq != 1 {
		t.Fatalf("frames on A = %+v, want one frame of aGkK at seq 1", f)
	}
	reB := string(b.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.reattach","params":{"id":"lc","fromSeq":0}}`)))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"found":true,"running":true,"firstSeq":1,"lastSeq":1,"stdinApplied":3}}`; reB != want {
		t.Errorf("reattach from B = %s\nwant %s", reB, want)
	}
	// A is still the link: it writes stdin and gets the next frame, B does not.
	a.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.stdin","params":{"id":"lc","data":"aGkK"}}`))
	a.wait(func() bool { return len(a.fr) >= 2 })
	reA := string(a.call(authed(`{"jsonrpc":"2.0","id":5,"method":"process.reattach","params":{"id":"lc","fromSeq":2}}`)))
	if want := `{"jsonrpc":"2.0","id":5,"result":{"found":true,"running":true,"firstSeq":1,"lastSeq":2,"stdinApplied":6}}`; reA != want {
		t.Errorf("reattach from A = %s\nwant %s", reA, want)
	}
	for _, f := range b.framesOf("lc") {
		if f.Seq > 1 {
			t.Errorf("B got live frame %+v, want the stream on the link only", f)
		}
	}
	// B ends the process. The exit frame goes to the link first (process.go, the
	// emit of the exit frame), and the record goes right after it, in a separate
	// step (forgetLinked). So a reattach that lands between the two still finds the
	// record with running false; the test waits for the drop, then B's reattach
	// answers found false (from the code, not measured).
	b.call(authed(`{"jsonrpc":"2.0","id":6,"method":"process.kill","params":{"id":"lc","signal":"KILL"}}`))
	a.waitExit("lc")
	want := `{"jsonrpc":"2.0","id":7,"result":{"found":false,"running":false,"firstSeq":0,"lastSeq":0,"stdinApplied":0}}`
	var gone string
	for deadline := time.Now().Add(5 * time.Second); ; {
		gone = string(b.call(authed(`{"jsonrpc":"2.0","id":7,"method":"process.reattach","params":{"id":"lc","fromSeq":0}}`)))
		if gone == want || time.Now().After(deadline) {
			break
		}
		if inGap := `{"jsonrpc":"2.0","id":7,"result":{"found":true,"running":false,`; !strings.HasPrefix(gone, inGap) {
			t.Fatalf("reattach between the exit frame and the drop = %s\nwant found true, running false", gone)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gone != want {
		t.Errorf("reattach after the exit = %s\nwant %s", gone, want)
	}
}

// TestSocketLinkBoundSpentID pins the exit frame and the spent id of 683cc6a9
// (measured on Linux): the exit frame has no killedBy and no signal, a plain spawn
// of the id is refused and its child does not run, and process.kill answers
// success.
func TestSocketLinkBoundSpentID(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(linkSpawnReq(t, 1, "x", "sleep", []string{"0"}, map[string]any{"linkBound": map[string]any{}}))
	frames := cl.waitExit("x")
	exit, err := json.Marshal(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"stream","processId":"x","stream":"exit","seq":1,"exitCode":0}`; string(exit) != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	mark := filepath.Join(t.TempDir(), "ran")
	refused := string(cl.call(linkSpawnReq(t, 2, "x", "filter-mark", []string{mark}, nil)))
	if want := `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"this id is in use, or was a terminal's or a link-bound process's: take a fresh one","data":{"reason":"other"}}}`; refused != want {
		t.Errorf("spawn of a spent id = %s\nwant %s", refused, want)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Errorf("the child of a refused spawn ran: stat = %v", err)
	}
	kill := string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.kill","params":{"id":"x"}}`)))
	if want := `{"jsonrpc":"2.0","id":3,"result":{"success":true}}`; kill != want {
		t.Errorf("kill of a spent id = %s\nwant %s", kill, want)
	}
	assertGolden(t, "socket_process_linkbound_spent.golden.json",
		encodeGolden(t, []json.RawMessage{exit, json.RawMessage(refused), json.RawMessage(kill)}))
}

// TestSocketLinkBoundSpentIDExpires: once the spent age has passed, a spawn of the
// id starts again. 683cc6a9 starts it 931.8 s after the exit (measured on Linux).
func TestSocketLinkBoundSpentIDExpires(t *testing.T) {
	old := linkSpentAge
	linkSpentAge = 50 * time.Millisecond
	t.Cleanup(func() { linkSpentAge = old })
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(linkSpawnReq(t, 1, "y", "sleep", []string{"0"}, map[string]any{"linkBound": map[string]any{}}))
	cl.waitExit("y")
	deadline := time.Now().Add(5 * time.Second)
	for id := 2; ; id++ {
		got := string(cl.call(linkSpawnReq(t, id, "y", "sleep", []string{"0"}, nil)))
		if strings.Contains(got, `"success":true`) {
			return
		}
		if !strings.Contains(got, "take a fresh one") || time.Now().After(deadline) {
			t.Fatalf("spawn after the spent age = %s", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUnattendedTTLAndTick pins the clamp of 683cc6a9 (measured on Linux for each
// value here but -3) and the tick, which is claustrum's own.
func TestUnattendedTTLAndTick(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
		tick time.Duration
	}{
		{-3, "1m0s", 15 * time.Second},
		{0, "1m0s", 15 * time.Second},
		{1, "1s", 250 * time.Millisecond},
		{2, "2s", 500 * time.Millisecond},
		{4, "4s", time.Second},
		{120, "2m0s", 30 * time.Second},
		{599, "9m59s", time.Minute},
		{600, "10m0s", time.Minute},
		{100000, "10m0s", time.Minute},
	} {
		ttl := unattendedTTL(c.n)
		if ttl.String() != c.want {
			t.Errorf("unattendedTTL(%d) = %s, want %s", c.n, ttl, c.want)
		}
		if got := unattendedTick(ttl); got != c.tick {
			t.Errorf("unattendedTick(%s) = %s, want %s", ttl, got, c.tick)
		}
	}
}

// TestUnattendedEndGraceIsSeven pins the wait between the SIGHUP and the SIGKILL.
// 683cc6a9 logs the SIGKILL exit about 7 s after the ending line (Linux).
func TestUnattendedEndGraceIsSeven(t *testing.T) {
	if unattendedEndGrace != 7*time.Second {
		t.Errorf("unattendedEndGrace = %s, want 7s", unattendedEndGrace)
	}
}
