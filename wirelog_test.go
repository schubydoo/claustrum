package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readWireLog returns the decoded records from a capture file.
func readWireLog(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("capture line is not JSON: %v\nline: %s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// The token is equivalent to shell access on the host (SECURITY.md). A capture is
// a plain file that an operator will paste into an issue, so this is the one
// property of wire logging that must never regress.
func TestWireLogRedactsAuthToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	const secret = "SUPER-SECRET-TOKEN-do-not-log"
	w.record(1, "in", []byte(`{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"`+secret+`"}`))
	w.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("capture contains the auth token:\n%s", raw)
	}
	recs := readWireLog(t, path)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	body, _ := recs[0]["body"].(map[string]interface{})
	if got := body["auth"]; got != "[redacted]" {
		t.Errorf("auth = %v, want %q — the KEY must survive so a capture still shows which requests carried a token", got, "[redacted]")
	}
	if recs[0]["method"] != "server.ping" {
		t.Errorf("method = %v, want server.ping", recs[0]["method"])
	}
}

// Stream frames carry base64 stdout/stderr. Without truncation a capture is
// almost entirely payload; the LENGTH has to survive, because frame sizes are how
// throughput is read back out of a capture.
func TestWireLogTruncatesLongStringsButKeepsLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	payload := strings.Repeat("A", wireLogMaxString*3)
	w.record(2, "out", []byte(`{"method":"process.stream","params":{"data":"`+payload+`"}}`))
	w.Close()

	recs := readWireLog(t, path)
	body := recs[0]["body"].(map[string]interface{})
	params := body["params"].(map[string]interface{})
	got := params["data"].(string)
	if len(got) >= len(payload) {
		t.Fatalf("data not truncated: %d chars", len(got))
	}
	if !strings.Contains(got, "1536 bytes total") {
		t.Errorf("truncation marker lost the true length: %q", got)
	}
	// The frame's own byte count must be the real one, not the truncated one.
	if n := recs[0]["n"].(float64); int(n) < len(payload) {
		t.Errorf("n = %v, want >= %d (the real frame size)", n, len(payload))
	}
}

// maxString 0 keeps every string whole. This is the mode that matters for
// reconstructing a session: Claude Desktop drives the remote CLI through
// process.spawn, so the CLI's entire stdio protocol rides inside these payloads
// and any truncation destroys it.
func TestWireLogUnlimitedKeepsFullPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, 0)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	payload := strings.Repeat("B", wireLogMaxString*4)
	w.record(1, "out", []byte(`{"data":"`+payload+`"}`))
	w.Close()

	recs := readWireLog(t, path)
	body := recs[0]["body"].(map[string]interface{})
	got := body["data"].(string)
	if got != payload {
		t.Fatalf("payload altered: got %d chars, want %d verbatim", len(got), len(payload))
	}
	if strings.Contains(got, "truncated") {
		t.Error("truncation marker present with maxString=0")
	}
}

// A negative limit is normalised to 0 (unlimited) rather than producing a
// nonsensical clamp.
func TestWireLogNegativeLimitMeansUnlimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, -5)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	defer w.Close()
	if w.maxStr != 0 {
		t.Errorf("maxStr = %d, want 0", w.maxStr)
	}
}

// 0 in the config file is a real setting ("keep everything"), not "unset", so it
// must win over the non-zero default — unlike the byte caps, where 0 means off.
func TestEffectiveWireLogMaxStringZeroFromConfigWins(t *testing.T) {
	zero := int64(0)
	cfg := config{wireLogMaxString: &zero}
	if got := cfg.effectiveWireLogMaxString(wireLogMaxString, false); got != 0 {
		t.Errorf("config 0 with unset CLI: got %d, want 0 (keep everything)", got)
	}
	if got := cfg.effectiveWireLogMaxString(256, true); got != 256 {
		t.Errorf("explicit CLI: got %d, want 256", got)
	}
	if got := (config{}).effectiveWireLogMaxString(wireLogMaxString, false); got != wireLogMaxString {
		t.Errorf("neither set: got %d, want the %d default", got, wireLogMaxString)
	}
}

// A frame that is not JSON is exactly what a capture exists to catch, so it must
// be recorded rather than dropped.
func TestWireLogRecordsNonJSONFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.record(3, "in", []byte("this is not json"))
	w.Close()

	recs := readWireLog(t, path)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0]["parse_error"] == nil {
		t.Error("parse_error absent — a malformed frame was silently normalized")
	}
	if recs[0]["raw"] != "this is not json" {
		t.Errorf("raw = %v, want the original bytes", recs[0]["raw"])
	}
}

// Off is the default, and off must mean no file and no work.
func TestWireLogOffByDefault(t *testing.T) {
	w, err := openWireLog(wireLogOptions{})
	if err != nil {
		t.Fatalf("openWireLog(\"\"): %v", err)
	}
	if w != nil {
		t.Fatal("openWireLog(\"\") returned a logger; off must be nil")
	}
	// Every call site calls unconditionally, so nil must be a safe receiver.
	w.record(1, "in", []byte(`{"method":"x"}`))
	w.Close()
}

// A path that cannot be opened must fail the boot. A daemon that serves happily
// while recording nothing produces an empty capture, and an empty capture reads
// as "the client sent nothing" — the worst failure mode for a diagnostic.
func TestWireLogUnopenablePathIsFatalNotSilent(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "no-such-dir", "wire.jsonl")
	if _, err := openWireLog(wireLogOptions{path: bad}); err == nil {
		t.Fatal("openWireLog on an unopenable path returned nil error")
	}
	if _, err := newServerOnSocket(filepath.Join(t.TempDir(), "s.sock"), "tok", "", wireLogOptions{path: bad}, false, false); err == nil {
		t.Fatal("newServerOnSocket accepted an unopenable -wire-log path")
	}
}

// The capture is created 0600: it contains whatever the client sent, which for
// files.write or process.stdin is arbitrary user data.
func TestWireLogFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not an owner-only DACL on Windows")
	}
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	defer w.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}
}

// Config precedence mirrors -metrics-addr: CLI wins when explicitly set,
// otherwise the file value applies.
func TestEffectiveWireLogPrecedence(t *testing.T) {
	cfg := config{wireLog: "/from/file.jsonl"}
	if got := cfg.effectiveWireLog("", false); got != "/from/file.jsonl" {
		t.Errorf("unset CLI: got %q, want the config value", got)
	}
	if got := cfg.effectiveWireLog("/from/cli.jsonl", true); got != "/from/cli.jsonl" {
		t.Errorf("explicit CLI: got %q, want the CLI value", got)
	}
	// An explicitly-empty CLI value must be able to turn it back off.
	if got := cfg.effectiveWireLog("", true); got != "" {
		t.Errorf("explicit empty CLI: got %q, want \"\" (off)", got)
	}
	if got := (config{}).effectiveWireLog("", false); got != "" {
		t.Errorf("neither set: got %q, want \"\" (off)", got)
	}
}

// Connection ids come from a per-process counter, while the capture is opened
// O_APPEND and outlives a restart — so two daemons writing one file both number
// their connections from 1. Without pid, a reader correlating a reply to its
// request by (conn, id) mis-pairs across the boundary. This asserts the record
// carries the discriminator, and that two writers on one file stay separable.
func TestWireLogRecordsPIDSoConnIDsStaySeparable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")

	w1, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w1.record(1, "in", []byte(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))

	// A second writer on the same path, standing in for a restarted daemon: it
	// reuses conn 1, which is precisely the collision pid exists to resolve.
	w2, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog (second writer): %v", err)
	}
	w2.record(1, "in", []byte(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))
	w1.Close()
	w2.Close()

	recs := readWireLog(t, path)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 — the second writer must append, not truncate", len(recs))
	}
	for i, rec := range recs {
		pid, ok := rec["pid"].(float64)
		if !ok {
			t.Fatalf("record %d has no numeric pid field: %v", i, rec)
		}
		if int(pid) != os.Getpid() {
			t.Errorf("record %d pid = %d, want %d", i, int(pid), os.Getpid())
		}
		if got := rec["conn"]; got != float64(1) {
			t.Errorf("record %d conn = %v, want 1 (the collision this test models)", i, got)
		}
	}
}
