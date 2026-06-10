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
)

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
		if f.Seq != i+1 {
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
