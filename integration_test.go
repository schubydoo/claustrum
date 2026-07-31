package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"
	"time"
)

// These tests exercise the *real* AF_UNIX wire path end to end — listener,
// per-connection read loop, concurrent dispatch, in-band auth, newline framing,
// and the process stream fan-out — complementing rpc_test.go, which calls
// dispatch() in-process. The response/error battery is locked against a
// committed golden (testdata/socket_responses.golden.json); the daemon is
// independently proven byte-identical to the reference by scratch/, so the
// golden's job here is to stop future refactors from silently drifting.

var (
	reVersion  = regexp.MustCompile(`"version":"[^"]*"`)
	rePlatform = regexp.MustCompile(`"platform":"[^"]*"`)
	reArch     = regexp.MustCompile(`"arch":"[^"]*"`)
	// CT-1 opt-in fields are host/run-variable (the OS pid and a wall-clock epoch),
	// so tokenize them like version/platform to keep the golden stable.
	rePid       = regexp.MustCompile(`"pid":\d+`)
	reStartTime = regexp.MustCompile(`"startTime":[0-9.eE+-]+`)
)

// normCT1 tokenizes the CT-1 pid/startTime fields so a wantPid reply golden is
// stable across hosts and runs.
// The placeholders are quoted so the tokenized frame stays valid JSON (the golden
// encoder re-marshals it) — same convention as the version/platform tokens.
func normCT1(b []byte) json.RawMessage {
	s := string(b)
	s = rePid.ReplaceAllString(s, `"pid":"<PID>"`)
	s = reStartTime.ReplaceAllString(s, `"startTime":"<TS>"`)
	return json.RawMessage(s)
}

// normResp tokenizes the only host-variable fields a reply can carry, so the
// golden is stable across versions and platforms.
func normResp(b []byte) json.RawMessage {
	s := string(b)
	s = reVersion.ReplaceAllString(s, `"version":"<V>"`)
	s = rePlatform.ReplaceAllString(s, `"platform":"<OS>"`)
	s = reArch.ReplaceAllString(s, `"arch":"<ARCH>"`)
	return json.RawMessage(s)
}

// spawnReq builds an authed process.spawn request whose command is this test
// binary in the given helper mode (helperproc_test.go), so the socket suite
// needs no /bin/sh-style fixtures and streams identical bytes on every OS.
// json.Marshal escapes the executable path safely (Windows backslashes).
func spawnReq(t *testing.T, id int, procID, mode string) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": procID, "command": exe, "args": []string{}, "env": env},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

func respKey(b []byte) int {
	var e struct {
		ID *int `json:"id"`
	}
	_ = json.Unmarshal(b, &e)
	if e.ID == nil {
		return -1 << 62 // null id (parse error) sorts first, deterministically
	}
	return *e.ID
}

func TestSocketResponseBattery(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)

	// Drive the deterministic request set: happy server.* methods + every
	// error path (parse / version / auth / namespace / method / params).
	cl.send(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))
	cl.send(authed(`{"jsonrpc":"2.0","id":2,"method":"server.version"}`))
	cl.send(authed(`{"jsonrpc":"2.0","id":3,"method":"server.capabilities"}`))
	cl.send(authed(`{"jsonrpc":"1.0","id":4,"method":"server.ping"}`))       // bad jsonrpc version
	cl.send(`{"jsonrpc":"2.0","id":5,"method":"server.ping"}`)               // missing auth
	cl.send(`{"jsonrpc":"2.0","id":6,"method":"server.ping","auth":"nope"}`) // wrong auth
	cl.send(authed(`{"jsonrpc":"2.0","id":7,"method":"bogus"}`))             // no namespace separator
	cl.send(authed(`{"jsonrpc":"2.0","id":8,"method":"server.nope"}`))       // unknown method
	cl.send(authed(`{"jsonrpc":"2.0","id":9,"method":"files.stat"}`))        // absent params
	cl.send(`{not json`)                                                     // parse error -> id null

	const want = 10
	raw := cl.waitResponses(want)
	if len(raw) != want {
		t.Fatalf("got %d responses, want %d", len(raw), want)
	}

	sort.Slice(raw, func(i, j int) bool { return respKey(raw[i]) < respKey(raw[j]) })
	normalized := make([]json.RawMessage, len(raw))
	for i, r := range raw {
		normalized[i] = normResp(r)
	}
	assertGolden(t, "socket_responses.golden.json", encodeGolden(t, normalized))
}

// The golden tokenizes platform/arch, so assert their real values separately.
func TestSocketVersionReportsHostPlatform(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(authed(`{"jsonrpc":"2.0","id":1,"method":"server.version"}`))

	raw := cl.waitResponses(1)
	var env struct {
		Result versionResult `json:"result"`
	}
	if err := json.Unmarshal(raw[0], &env); err != nil {
		t.Fatalf("unmarshal version: %v", err)
	}
	if env.Result.Platform != runtime.GOOS {
		t.Errorf("platform = %q, want %q", env.Result.Platform, runtime.GOOS)
	}
	if env.Result.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", env.Result.Arch, runtime.GOARCH)
	}
	if env.Result.Version != Version {
		t.Errorf("version = %q, want %q", env.Result.Version, Version)
	}
}

func TestSocketProcessSpawnStreamsStdout(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(spawnReq(t, 1, "EM", "stdout3")) // emits "l0\nl1\nl2\n", exit 0

	cl.waitResponses(1) // spawn ack
	frames := cl.waitExit("EM")

	if out := streamBytes(t, frames, "stdout"); out != "l0\nl1\nl2\n" {
		t.Errorf("stdout = %q, want %q", out, "l0\nl1\nl2\n")
	}
	exit := lastExit(t, frames)
	if exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", exit.ExitCode)
	}
	assertSeqMonotonic(t, frames)
}

func TestSocketProcessStderrAndNonZeroExit(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(spawnReq(t, 1, "NZ", "stderr-exit5")) // emits "err\n" on stderr, exit 5

	cl.waitResponses(1)
	frames := cl.waitExit("NZ")

	if errOut := streamBytes(t, frames, "stderr"); errOut != "err\n" {
		t.Errorf("stderr = %q, want %q", errOut, "err\n")
	}
	exit := lastExit(t, frames)
	if exit.ExitCode == nil || *exit.ExitCode != 5 {
		t.Errorf("exit code = %v, want 5", exit.ExitCode)
	}
}

func TestSocketProcessStdinEcho(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	// The cat helper echoes stdin to stdout until its stdin closes or it is killed.
	cl.send(spawnReq(t, 1, "CAT", "cat"))
	cl.waitResponses(1)

	payload := base64.StdEncoding.EncodeToString([]byte("hello\n"))
	cl.send(authed(`{"jsonrpc":"2.0","id":2,"method":"process.stdin","params":{"id":"CAT","data":"` + payload + `"}}`))

	// Wait until the echoed stdout shows up.
	cl.wait(func() bool {
		for _, f := range cl.fr {
			if f.ProcessID == "CAT" && f.Stream == "stdout" {
				if b, _ := base64.StdEncoding.DecodeString(f.Data); string(b) == "hello\n" {
					return true
				}
			}
		}
		return false
	})

	// A second chunk after a successful first write: the stdin writer must
	// keep running across successful writes (a negated error guard in
	// stdinWriter would stop after one chunk and silently drop the rest).
	payload2 := base64.StdEncoding.EncodeToString([]byte("world\n"))
	cl.send(authed(`{"jsonrpc":"2.0","id":3,"method":"process.stdin","params":{"id":"CAT","data":"` + payload2 + `"}}`))
	cl.wait(func() bool {
		var got []byte
		for _, f := range cl.fr {
			if f.ProcessID == "CAT" && f.Stream == "stdout" {
				b, _ := base64.StdEncoding.DecodeString(f.Data)
				got = append(got, b...)
			}
		}
		return string(got) == "hello\nworld\n"
	})

	cl.send(authed(`{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"CAT","signal":"KILL"}}`))
	frames := cl.waitExit("CAT")
	if out := streamBytes(t, frames, "stdout"); out != "hello\nworld\n" {
		t.Errorf("echoed stdout = %q, want %q", out, "hello\nworld\n")
	}
}

// reattach on a fresh connection must replay the finished process's buffered
// frames (seq > fromSeq) and report found/!running with the buffer bounds.
func TestSocketProcessReattachReplay(t *testing.T) {
	sock := startSocketServer(t)
	a := dial(t, sock)
	a.send(spawnReq(t, 1, "EM", "stdout3"))
	a.waitResponses(1)
	first := a.waitExit("EM")
	lastSeq := first[len(first)-1].Seq

	b := dial(t, sock)
	b.send(authed(`{"jsonrpc":"2.0","id":9,"method":"process.reattach","params":{"id":"EM","fromSeq":0}}`))
	raw := b.waitResponses(1)

	var env struct {
		Result reattachResult `json:"result"`
	}
	if err := json.Unmarshal(raw[0], &env); err != nil {
		t.Fatalf("unmarshal reattach: %v", err)
	}
	if !env.Result.Found || env.Result.Running {
		t.Errorf("reattach = %+v, want found && !running", env.Result)
	}
	if env.Result.FirstSeq != 1 || env.Result.LastSeq != lastSeq {
		t.Errorf("reattach seq bounds = [%d,%d], want [1,%d]", env.Result.FirstSeq, env.Result.LastSeq, lastSeq)
	}

	// The replay should have delivered the same stdout to connection B.
	replayed := b.waitExit("EM")
	if out := streamBytes(t, replayed, "stdout"); out != "l0\nl1\nl2\n" {
		t.Errorf("replayed stdout = %q, want %q", out, "l0\nl1\nl2\n")
	}
}

// rawResult extracts the verbatim "result" object of a JSON-RPC reply, so a test
// can assert its exact bytes (field presence + order), not just parsed values.
func rawResult(t *testing.T, resp json.RawMessage) string {
	t.Helper()
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return string(env.Result)
}

// procSpawnReq builds a process.spawn request, optionally setting the CT-1
// "wantPid" opt-in. With wantPid omitted entirely the params are identical to a
// pre-CT-1 client's — the basis for the byte-identical default-path proof.
func procSpawnReq(t *testing.T, id int, procID string, wantPid bool) string {
	t.Helper()
	exe, env := helperCommand(t, "stdout3")
	params := map[string]any{"id": procID, "command": exe, "args": []string{}, "env": env}
	if wantPid {
		params["wantPid"] = true
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// TestSocketProcessWantPid covers the CT-1 opt-in on process.spawn and
// process.reattach: the default (no wantPid) replies must stay byte-for-byte the
// pre-CT-1 frames, while "wantPid":true appends pid + startTime. The deterministic
// spawn replies are locked against a golden; reattach is asserted structurally
// because its seq bounds are not frozen (frame chunking varies by host).
func TestSocketProcessWantPid(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)

	// (1) Default spawn — no wantPid. The reply MUST be byte-identical to the
	// pre-CT-1 {"success":true}; this is the core compatibility guarantee.
	defResp := cl.call(procSpawnReq(t, 1, "DEF", false))
	if got := rawResult(t, defResp); got != `{"success":true}` {
		t.Fatalf("default spawn result = %s, want {\"success\":true} (default path must stay byte-identical)", got)
	}

	// (2) Opt-in spawn — wantPid:true adds pid + startTime, in that order.
	wpResp := cl.call(procSpawnReq(t, 2, "WP", true))
	var wp spawnResult
	if err := json.Unmarshal([]byte(rawResult(t, wpResp)), &wp); err != nil {
		t.Fatalf("unmarshal wantPid spawn result: %v", err)
	}
	if wp.Pid <= 0 {
		t.Errorf("wantPid spawn pid = %d, want > 0", wp.Pid)
	}
	if wp.StartTime <= 0 {
		t.Errorf("wantPid spawn startTime = %v, want > 0", wp.StartTime)
	}

	// (3) Reattach (default) on the exited process: no pid/startTime fields.
	cl.waitExit("WP")
	raDefReq := authed(`{"jsonrpc":"2.0","id":3,"method":"process.reattach","params":{"id":"WP","fromSeq":0}}`)
	var raDef reattachResult
	raDefRaw := rawResult(t, cl.call(raDefReq))
	if err := json.Unmarshal([]byte(raDefRaw), &raDef); err != nil {
		t.Fatalf("unmarshal default reattach: %v", err)
	}
	if !raDef.Found || raDef.Running || raDef.FirstSeq != 1 {
		t.Errorf("default reattach = %+v, want found && !running && firstSeq==1", raDef)
	}
	if raDef.Pid != 0 || raDef.StartTime != 0 {
		t.Errorf("default reattach leaked pid/startTime: %s", raDefRaw)
	}

	// (4) Reattach with wantPid:true reports the SAME pid/startTime the spawn did —
	// the cross-call consistency a client relies on for PID-reuse detection.
	raWPReq := authed(`{"jsonrpc":"2.0","id":4,"method":"process.reattach","params":{"id":"WP","fromSeq":0,"wantPid":true}}`)
	var raWP reattachResult
	if err := json.Unmarshal([]byte(rawResult(t, cl.call(raWPReq))), &raWP); err != nil {
		t.Fatalf("unmarshal wantPid reattach: %v", err)
	}
	if raWP.Pid != wp.Pid {
		t.Errorf("reattach pid = %d, want %d (must match spawn)", raWP.Pid, wp.Pid)
	}
	if raWP.StartTime != wp.StartTime {
		t.Errorf("reattach startTime = %v, want %v (must match spawn)", raWP.StartTime, wp.StartTime)
	}

	// (5) Lock the deterministic spawn replies (pid/startTime tokenized) so the
	// field set and order can't silently drift.
	golden := []json.RawMessage{
		normCT1([]byte(rawResult(t, defResp))),
		normCT1([]byte(rawResult(t, wpResp))),
	}
	assertGolden(t, "socket_process_wantpid.golden.json", encodeGolden(t, golden))
}

func lastExit(t *testing.T, frames []streamFrame) streamFrame {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Stream == "exit" {
			return frames[i]
		}
	}
	t.Fatal("no exit frame")
	return streamFrame{}
}

func assertSeqMonotonic(t *testing.T, frames []streamFrame) {
	t.Helper()
	for i, f := range frames {
		if f.Seq != uint64(i+1) {
			t.Errorf("frame %d has seq %d, want %d (seqs must be 1..N contiguous)", i, f.Seq, i+1)
		}
	}
}

// encodeGolden renders a response slice as indented JSON without HTML escaping,
// so normalization tokens stay readable as <V>/<DIR>/… in the committed fixture
// (matches the reference battery's convention).
func encodeGolden(t *testing.T, msgs []json.RawMessage) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(msgs); err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	return buf.Bytes()
}

// assertGolden compares got against testdata/<name>, or rewrites it under -update.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run `go test -run Socket -update` to create): %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestSocketExitFrameBoundsTheDrain pins the cap on how long the exit frame
// waits for stdout to reach EOF after the spawned process has exited.
//
// Only a grandchild that inherited the pipe can hold it open past the process's
// own exit, so that is what the fixture builds: "orphan-stdout" starts a
// 20-second sleeper on its own stdout, prints one line, and exits 7. Before the
// bounded drain, the daemon waited for EOF, so the exit frame was hostage to the
// grandchild — the whole 20 seconds here, and forever for a real dev server. The
// reference caps that at 5s (exitDrainGrace), closes the read ends, and emits
// exit; the grandchild's next write then fails with EPIPE.
//
// The cap is shrunk to 300ms so the test measures the CAP rather than the clock.
// waitExit's own deadline is 5s, well under the grandchild's 20s, so without the
// fix this test fails by timeout rather than by hanging the suite. Restoring the
// var is registered BEFORE startSocketServer so the LIFO cleanup order shuts the
// daemon down first — otherwise a live waiter goroutine reads it as it changes.
func TestSocketExitFrameBoundsTheDrain(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = old })

	sock := startSocketServer(t)
	cl := dial(t, sock)

	exe, env := helperCommand(t, "orphan-stdout")
	spawn, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{
			"id": "ORPH", "command": exe, "args": []string{"20"}, "env": env,
		},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	cl.send(string(spawn))
	cl.waitResponses(1)

	start := time.Now()
	frames := cl.waitExit("ORPH")
	elapsed := time.Since(start)

	// The grandchild lives 20s. Anything near that means the drain was unbounded.
	if elapsed > 5*time.Second {
		t.Errorf("exit frame took %v; the drain is not bounded by exitDrainGrace", elapsed)
	}
	exit := lastExit(t, frames)
	if exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Errorf("exit code = %v, want 7", exit.ExitCode)
	}
	// The line printed before the process exited must still arrive: the cap drops
	// what the grandchild writes later, not what the process already wrote.
	if out := streamBytes(t, frames, "stdout"); out != "early\n" {
		t.Errorf("stdout = %q, want %q", out, "early\n")
	}
	// Ordering is the second half of the contract. Nothing may follow the exit
	// frame, and no seq may be burnt on a frame that was dropped — the reference
	// emits exactly stdout seq 1 then exit seq 2 for this fixture.
	assertSeqMonotonic(t, frames)
	if n := len(frames); n != 2 {
		t.Fatalf("got %d frames, want 2 (stdout, exit): %+v", n, frames)
	}
	if frames[0].Stream != "stdout" || frames[0].Seq != 1 {
		t.Errorf("frame 0 = %+v, want stdout seq 1", frames[0])
	}
	if frames[1].Stream != "exit" || frames[1].Seq != 2 {
		t.Errorf("frame 1 = %+v, want exit seq 2", frames[1])
	}
}
