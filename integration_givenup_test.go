package main

import (
	"fmt"
	"sort"
	"testing"
)

// process.kill, process.killAndWait and process.reattach of an id that no process has
// answer as before and give that id up. A process.spawn of each given-up id then gets
// the refusal frame of 4c111e86, and a spawn of another id starts (measured on Linux,
// macOS and Windows). Mutation: drop the GiveUpUnknown call of a method.
func TestSocketGivenUpIDRefusesSpawn(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(authed(`{"jsonrpc":"2.0","id":1,"method":"process.kill","params":{"id":"G1"}}`))
	cl.send(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"G2"}}`))
	cl.send(authed(`{"jsonrpc":"2.0","id":3,"method":"process.reattach","params":{"id":"G3"}}`))
	cl.waitResponses(3)
	cl.send(spawnReq(t, 4, "G1", "stdout3"))
	cl.send(spawnReq(t, 5, "G2", "stdout3"))
	cl.send(spawnReq(t, 6, "G3", "stdout3"))
	raw := cl.waitResponses(6)
	if len(raw) != 6 {
		t.Fatalf("got %d replies, want 6", len(raw))
	}
	sort.Slice(raw, func(i, j int) bool { return respKey(raw[i]) < respKey(raw[j]) })
	want := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"found":false,"died":false}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"found":false,"running":false,"firstSeq":0,"lastSeq":0,"stdinApplied":0}}`,
	}
	for id := 4; id <= 6; id++ {
		want = append(want, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"this id was asked after, or asked to be ended, while no process had it: take a fresh one","data":{"reason":"other"}}}`, id))
	}
	for i, w := range want {
		if got := string(raw[i]); got != w {
			t.Errorf("reply %d = %s\nwant %s", i+1, got, w)
		}
	}
	cl.send(spawnReq(t, 7, "G4", "stdout3"))
	cl.waitResponses(7)
	cl.waitExit("G4")
}
