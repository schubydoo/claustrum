package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for the process-side wire surface added by the reference daemon in
// 7c2f88d: the process.stdin.offset idempotency contract (stdin "applied"/
// "duplicate" + reattach "stdinApplied"), process.killAndWait (and its
// timeoutMs/escalate params via clampKillWaitMs), and the server.capabilities
// "features" array that advertises them. See docs/PROTOCOL.md.

// rpcEnvelope is a minimal reply decoder shared by the 7c2f88d wire-surface tests
// (also used by git_repo_slug_test.go and killandwait_unix_test.go).
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeReply(t *testing.T, raw []byte, out any) *rpcEnvelope {
	t.Helper()
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal reply %s: %v", raw, err)
	}
	if out != nil && env.Result != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			t.Fatalf("unmarshal result %s: %v", env.Result, err)
		}
	}
	return &env
}

// spawnReqArgs builds a process.spawn request line (auth embedded) for the given
// helper mode with explicit args — spawnReq hardcodes an empty arg list.
func spawnReqArgs(t *testing.T, id int, procID, mode string, args ...string) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	if args == nil {
		args = []string{}
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": procID, "command": exe, "args": args, "env": env},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// clampKillWaitMs maps a caller's killAndWait timeoutMs onto the grace: non-positive
// → the default (probe-verified 0 and -100 both wait 3000ms), positive honored up
// to maxKillWaitMs.
//
// The {90000, 90000} row was removed on 2026-08-02: it asserted that 90 s passes
// through, which encoded the old 600000 ms ceiling as a fact. The reference stops
// at 30 s, so 90 s must now clamp — and a caller asking for it is a realistic
// client, not the "adversarial input" the old comment assumed.
func TestClampKillWaitMs(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultKillWaitMs},
		{-100, defaultKillWaitMs},
		{50, 50},
		{3000, 3000},
		{29500, 29500},         // below the ceiling, honored verbatim
		{90000, maxKillWaitMs}, // a plausible client value that now clamps
		{maxKillWaitMs, maxKillWaitMs},
		{maxKillWaitMs + 1, maxKillWaitMs},
		{10_000_000, maxKillWaitMs},
	}
	for _, tc := range cases {
		if got := clampKillWaitMs(tc.in); got != tc.want {
			t.Errorf("clampKillWaitMs(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// The ceiling itself is the parity claim, so pin the value and not just the
	// clamping behaviour. Measured bracket (29500, 30500]; 30000 is the only
	// round value in it. See maxKillWaitMs in process.go.
	if maxKillWaitMs != 30000 {
		t.Errorf("maxKillWaitMs = %d, want 30000 (reference bracket (29500, 30500] at 5db5e4a)", maxKillWaitMs)
	}
	// Same argument for the post-SIGKILL reap grace, and it needs the pin MORE
	// than the ceiling does: the branch killReapGrace bounds is unreachable in
	// this suite (it needs a child SIGKILL cannot reap, which took a dm-delay
	// device on a VM to build), so every escalation test reaps promptly, p.done
	// wins the select, and the timeout arm is never taken. A refactor that puts
	// this back to 5s — or folds it into exitDrainGrace, which it equalled until
	// the parity fix — would stay green with nothing else to catch it.
	if killReapGrace != 7*time.Second {
		t.Errorf("killReapGrace = %v, want 7s (measured against the reference 2026-08-06: reply at 7.51s with timeoutMs 500)", killReapGrace)
	}
}

// server.capabilities advertises process.killAndWait (between kill and reattach)
// and the process.stdin.offset feature.
func TestCapabilitiesAdvertisesNewSurface(t *testing.T) {
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "server.capabilities", map[string]any{}))
	var got capabilitiesResult
	decodeReply(t, []byte(raw), &got)

	joined := strings.Join(got.Methods, ",")
	if !strings.Contains(joined, "process.kill,process.killAndWait,process.reattach") {
		t.Errorf("methods missing killAndWait in the right slot: %v", got.Methods)
	}
	if len(got.Features) != 1 || got.Features[0] != "process.stdin.offset" {
		t.Errorf("features = %v, want [process.stdin.offset]", got.Features)
	}
}

// The process.stdin.offset contract: applied advances by fresh bytes; a wholly
// covered write is a no-op flagged duplicate; a partial overlap applies only the
// fresh tail; an offset ahead of applied is a gap error; reattach reports the
// cumulative stdinApplied. Only the fresh bytes reach the child (cat).
func TestSocketProcessStdinOffset(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(spawnReq(t, 1, "CAT", "cat"))
	cl.waitResponses(1)

	stdin := func(id int, data string, offset *int) rpcEnvelope {
		p := map[string]any{"id": "CAT", "data": base64.StdEncoding.EncodeToString([]byte(data))}
		if offset != nil {
			p["offset"] = *offset
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "process.stdin", "params": p})
		raw := cl.call(authed(string(body)))
		var sr stdinResult
		env := decodeReply(t, raw, &sr)
		if env.Error == nil {
			env.Result, _ = json.Marshal(sr) // canonicalize field order for string compare
		}
		return *env
	}
	off := func(n int) *int { return &n }

	if env := stdin(2, "AAA\n", nil); string(env.Result) != `{"success":true,"applied":4}` {
		t.Errorf("stdin no-offset result = %s", env.Result)
	}
	if env := stdin(3, "BBB\n", off(4)); string(env.Result) != `{"success":true,"applied":8}` {
		t.Errorf("stdin offset==applied result = %s", env.Result)
	}
	if env := stdin(4, "DUP\n", off(4)); string(env.Result) != `{"success":true,"applied":8,"duplicate":true}` {
		t.Errorf("stdin duplicate result = %s", env.Result)
	}
	if env := stdin(5, "PART\n", off(6)); string(env.Result) != `{"success":true,"applied":11}` {
		t.Errorf("stdin partial result = %s", env.Result)
	}
	if env := stdin(6, "GAP\n", off(99)); env.Error == nil || env.Error.Code != codeStdinOffsetGap {
		t.Errorf("stdin gap = %+v, want code %d", env.Error, codeStdinOffsetGap)
	}

	// Only the fresh bytes were echoed by cat: AAA + BBB + RT (never DUP/PART-head/
	// GAP). Assert this on the live stream BEFORE any reattach — a reattach would
	// replay these same frames and duplicate them in cl.fr, breaking the match.
	cl.wait(func() bool {
		var got []byte
		for _, f := range cl.fr {
			if f.ProcessID == "CAT" && f.Stream == "stdout" {
				b, _ := base64.StdEncoding.DecodeString(f.Data)
				got = append(got, b...)
			}
		}
		return string(got) == "AAA\nBBB\nRT\n"
	})

	// stdinApplied is checked on a SEPARATE connection so its replay doesn't
	// pollute cl's frames.
	b := dial(t, sock)
	var ra reattachResult
	decodeReply(t, b.call(authed(`{"jsonrpc":"2.0","id":7,"method":"process.reattach","params":{"id":"CAT","fromSeq":0}}`)), &ra)
	if ra.StdinApplied != 11 {
		t.Errorf("reattach stdinApplied = %d, want 11", ra.StdinApplied)
	}

	// The reattach above TRANSFERRED the frame stream to b, so the exit frame
	// arrives there and not on cl. Measured at 5db5e4a: after a reattach the
	// previously attached connection stops receiving.
	cl.send(authed(`{"jsonrpc":"2.0","id":8,"method":"process.kill","params":{"id":"CAT","signal":"KILL"}}`))
	b.waitExit("CAT")
}

// process.killAndWait: missing id is an error; an unknown id is a non-error
// {found:false,died:false}; an already-exited process reports alreadyExited; a
// live process is signalled and reported died (no escalation for a cooperative
// child). Grace/escalate params (timeoutMs, escalate:false) need a stubborn child
// and are covered in killandwait_unix_test.go.
func TestSocketProcessKillAndWait(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)

	if env := decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.killAndWait","params":{}}`)), nil); env.Error == nil {
		t.Error("killAndWait without id = no error, want Process ID is required")
	}

	var kw killAndWaitResult
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"ghost"}}`)), &kw)
	if kw.Found || kw.Died {
		t.Errorf("killAndWait unknown = %+v, want found:false died:false", kw)
	}

	// already-exited: echo exits immediately; wait for its exit, then killAndWait.
	cl.send(spawnReq(t, 3, "Q", "echo"))
	cl.waitExit("Q")
	kw = killAndWaitResult{}
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.killAndWait","params":{"id":"Q"}}`)), &kw)
	if !kw.Found || !kw.Died || !kw.AlreadyExited {
		t.Errorf("killAndWait already-exited = %+v, want found&died&alreadyExited", kw)
	}

	// live process: the sleep helper responds to the default SIGTERM and dies well
	// within the grace window, so no escalation. call() waits for the spawn reply
	// before we kill, so the process is registered.
	cl.call(spawnReqArgs(t, 5, "SL", "sleep", "30"))
	kw = killAndWaitResult{}
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":6,"method":"process.killAndWait","params":{"id":"SL"}}`)), &kw)
	if !kw.Found || !kw.Died || kw.AlreadyExited || kw.Escalated {
		t.Errorf("killAndWait live = %+v, want found&died only", kw)
	}
}

// TestSocketStdinOffsetUint64Edges pins the W2 typing contract: the reference
// declares StdinParams.Offset as *uint64 and ReattachParams.FromSeq as uint64,
// so a negative value is a decode failure (-32602), not an accepted offset, and
// 2^64-1 is a legal value that lands in the offset-gap path (-32003).
//
// claustrum used *int / int, which silently ACCEPTED offset:-1 (applying the
// data as if no offset were given) and rejected 2^64-1 as invalid params —
// inverting the reference on both ends of the range.
func TestSocketStdinOffsetUint64Edges(t *testing.T) {
	const maxU64 = "18446744073709551615" // 2^64-1

	sock := startSocketServer(t)
	cl := dial(t, sock)
	// sleep, not cat: a process that never writes keeps the reattach reply's
	// firstSeq/lastSeq deterministically 0, with no stream frames racing.
	cl.call(spawnReqArgs(t, 1, "U64", "sleep", "30"))

	data := base64.StdEncoding.EncodeToString([]byte("hello\n")) // 6 bytes
	stdin := func(id int, offset string) string {
		off := ""
		if offset != "" {
			off = `,"offset":` + offset
		}
		return authed(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) +
			`,"method":"process.stdin","params":{"id":"U64","data":"` + data + `"` + off + `}}`)
	}
	reattach := func(id int, fromSeq string) string {
		return authed(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) +
			`,"method":"process.reattach","params":{"id":"U64","fromSeq":` + fromSeq + `}}`)
	}

	got := []json.RawMessage{
		cl.call(stdin(2, "-1")),      // negative offset -> -32602, NOT accepted
		cl.call(reattach(3, "-1")),   // negative fromSeq -> -32602
		cl.call(stdin(4, maxU64)),    // 2^64-1 is valid and ahead of applied=0 -> -32003
		cl.call(stdin(5, "")),        // no offset: appends, applied=6
		cl.call(stdin(6, "0")),       // offset 0 with applied=6 -> wholly duplicate
		cl.call(stdin(7, maxU64)),    // still a gap now that applied=6
		cl.call(reattach(8, maxU64)), // valid; nothing buffered to replay
		cl.call(reattach(9, "0")),    // control: same shape, ordinary fromSeq
	}
	assertGolden(t, "socket_stdin_uint64_edges.golden.json", encodeGolden(t, got))
}
