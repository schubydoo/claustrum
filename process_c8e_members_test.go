package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestSocketC8eStdinAttachAndReattachProof pins the replies of c8e2f083 to the
// members attach, fresh and proof on a process without a terminal (measured on
// Linux). Claustrum has no terminal process, so every process is one.
func TestSocketC8eStdinAttachAndReattachProof(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	// sleep never writes, so the reattach result stays free of stream frames.
	cl.call(spawnReqArgs(t, 1, "C8E", "sleep", "30"))

	data := base64.StdEncoding.EncodeToString([]byte("hello\n"))
	stdin := func(id int, procID, extra string) string {
		return authed(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) +
			`,"method":"process.stdin","params":{"id":"` + procID + `","data":"` + data + `"` + extra + `}}`)
	}
	reattach := func(id int, procID, extra string) string {
		return authed(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) +
			`,"method":"process.reattach","params":{"id":"` + procID + `"` + extra + `}}`)
	}

	got := []json.RawMessage{
		cl.call(stdin(2, "C8E", `,"attach":0`)),       // -32602 Process not found
		cl.call(stdin(3, "C8E", `,"attach":7`)),       // same
		cl.call(stdin(4, "C8E", `,"attach":"x"`)),     // -32602 Invalid params
		cl.call(stdin(5, "NOPE", `,"attach":0`)),      // unknown id, same as 2
		cl.call(stdin(6, "C8E", `,"attach":null`)),    // absent: writes, applied 6
		cl.call(stdin(7, "C8E", `,"attach":0`)),       // refused again
		cl.call(stdin(8, "C8E", ``)),                  // control: applied 12, so nothing was written above
		cl.call(reattach(9, "C8E", `,"proof":"p"`)),   // zero result
		cl.call(reattach(10, "NOPE", `,"proof":"p"`)), // zero result
		cl.call(reattach(11, "C8E", `,"proof":""`)),   // absent: found result, stdinApplied 12
		cl.call(reattach(12, "C8E", `,"fresh":true`)), // -32602 Invalid params
		cl.call(reattach(13, "C8E", `,"attach":"x"`)), // -32602 Invalid params
		cl.call(reattach(14, "C8E", `,"proof":5`)),    // -32602 Invalid params
	}
	assertGolden(t, "socket_c8e_attach_proof.golden.json", encodeGolden(t, got))
}

// A reattach with a proof is the zero result and does not supersede the
// connection that holds the process (c8e2f083, Linux).
func TestSocketReattachProofKeepsHolder(t *testing.T) {
	sock := startSocketServer(t)
	holder := dial(t, sock)
	holder.call(spawnReqArgs(t, 1, "HOLD", "sleep", "30"))

	other := dial(t, sock)
	reply := string(other.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.reattach","params":{"id":"HOLD","proof":"p"}}`)))
	if !strings.Contains(reply, `"found":false`) {
		t.Fatalf("reattach with proof = %s, want found false", reply)
	}
	if got := string(holder.call(authed(`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`))); !strings.Contains(got, `"pong":true`) {
		t.Fatalf("holder ping = %q, want a pong: the proof reattach superseded it", got)
	}
}
