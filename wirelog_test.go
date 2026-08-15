package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
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

// maxString 0 keeps every frame whole, as the order-faithful `raw` field rather
// than a decoded `body`. This is the mode that matters for reconstructing a
// session: the remote CLI's entire stdio protocol rides inside these payloads, and
// both truncation and the map re-encoding's key sorting would destroy it.
func TestWireLogUnlimitedKeepsFullPayloadInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, 0)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	payload := strings.Repeat("B", wireLogMaxString*4)
	// Keys deliberately out of alphabetical order — the map re-encoding would sort
	// them, so this pins that raw preserves the bytes as sent.
	frame := `{"method":"x","data":"` + payload + `","id":7}`
	w.record(1, "out", []byte(frame))
	w.Close()

	recs := readWireLog(t, path)
	if _, ok := recs[0]["body"]; ok {
		t.Error("body present at maxString=0; want the order-faithful raw instead")
	}
	got, ok := recs[0]["raw"].(string)
	if !ok {
		t.Fatalf("raw absent at maxString=0: %v", recs[0])
	}
	if got != frame {
		t.Fatalf("raw is not the verbatim frame:\n got %q\nwant %q", got, frame)
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

// redact recurses through arrays, so a credential nested inside one is still
// withheld and a plain array element is left verbatim.
func TestWireLogRedactsInsideArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.record(1, "in", []byte(`{"method":"x","params":["keep",{"token":"SECRET-do-not-log"}]}`))
	w.Close()

	if b, _ := os.ReadFile(path); strings.Contains(string(b), "SECRET-do-not-log") {
		t.Fatalf("array-nested credential leaked:\n%s", b)
	}
	recs := readWireLog(t, path)
	body := recs[0]["body"].(map[string]interface{})
	params := body["params"].([]interface{})
	if params[0] != "keep" {
		t.Errorf("plain array element altered: %v", params[0])
	}
	obj := params[1].(map[string]interface{})
	if obj["token"] != "[redacted]" {
		t.Errorf("array-nested token = %v, want redacted", obj["token"])
	}
}

// openWireLog succeeds and logs the mode in both the truncated and untruncated
// cases; a successful open returns a live logger.
func TestOpenWireLogSuccessBothModes(t *testing.T) {
	dir := t.TempDir()
	for _, max := range []int{0, 512} {
		w, err := openWireLog(wireLogOptions{path: filepath.Join(dir, "w.jsonl"), maxString: max})
		if err != nil || w == nil {
			t.Fatalf("openWireLog(max=%d) = %v, %v; want a logger", max, w, err)
		}
		w.Close()
	}
}

// A write that fails after the file is gone is counted, never fatal to the request,
// and the count plus the file's own Close error are reported at Close.
func TestWireLogWriteErrorCountedAndReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.f.Close() // close the fd out from under the logger; w.f stays non-nil
	w.record(1, "in", []byte(`{"method":"x"}`))
	if w.dropped.Load() == 0 {
		t.Fatal("a failed write was not counted in dropped")
	}
	w.Close() // reports the dropped count and the already-closed file's Close error
}

// record on a closed logger hits the f==nil guard rather than panicking.
func TestWireLogRecordAfterCloseIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.Close()
	w.record(1, "in", []byte(`{"method":"x"}`)) // must be a no-op, not a panic
	w.Close()                                   // second Close hits the f==nil guard, also a no-op
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

// When -wire-log opens but a LATER boot step fails, the capture must not leak. The
// daemon exits on such an error, but the in-process boot path (and any future
// caller that recovers) must see the file closed. Here the wire-log path is valid
// and the socket path is not, so newServerOnSocket fails past openWireLog.
func TestWireLogClosedWhenBootFailsAfterOpen(t *testing.T) {
	good := filepath.Join(t.TempDir(), "wire.jsonl")
	badSocket := filepath.Join(t.TempDir(), "no-such-dir", "rpc.sock")
	if _, err := newServerOnSocket(badSocket, "tok", "", wireLogOptions{path: good}, false, false); err == nil {
		t.Fatal("newServerOnSocket accepted an unlistenable socket")
	}
	// The capture opened, then boot failed; the deferred Close must have run. A
	// second Close is a no-op, so this asserts the file handle was released cleanly.
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("capture file missing after failed boot: %v", err)
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

// The RPC "auth" member is not the only credential a frame carries. Claude
// Desktop drives the remote CLI through process.spawn and puts
// CLAUDE_CODE_OAUTH_TOKEN in params.env. The frame below is the shape measured on
// a live capture 2026-08-13; both redaction paths (the shape-mode map walk and the
// reconstruct-mode raw text) must withhold the secrets and keep the diagnostic keys.
const spawnEnvFrame = `{"jsonrpc":"2.0","id":60,"method":"process.spawn","params":{` +
	`"command":"/home/u/.claude/remote/ccd-cli/2.1.227",` +
	`"env":{"CLAUDE_CODE_OAUTH_TOKEN":"sk-ant-oat01-EXAMPLE-do-not-log",` +
	`"CLAUDE_CODE_OAUTH_SCOPES":"user:inference user:profile",` +
	`"ANTHROPIC_API_KEY":"ak-EXAMPLE","ANTHROPIC_BASE_URL":"https://api.anthropic.com"}}}`

// Shape mode (maxString>0): the by-key map walk redacts the nested env, keeping
// the KEY so a capture still shows the var was set, and leaves the diagnostic keys
// (scopes, base URL) verbatim — over-redaction destroys what a capture is for.
func TestWireLogRedactsSpawnEnvCredentials_ShapeMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.record(1, "in", []byte(spawnEnvFrame))
	w.Close()

	if b, _ := os.ReadFile(path); strings.Contains(string(b), "oat01-EXAMPLE") || strings.Contains(string(b), "ak-EXAMPLE") {
		t.Fatalf("capture contains a credential:\n%s", b)
	}
	recs := readWireLog(t, path)
	body, _ := recs[0]["body"].(map[string]interface{})
	params, _ := body["params"].(map[string]interface{})
	env, ok := params["env"].(map[string]interface{})
	if !ok {
		t.Fatalf("params.env missing from record: %v", body)
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if got := env[k]; got != "[redacted]" {
			t.Errorf("env[%s] = %v, want %q", k, got, "[redacted]")
		}
	}
	if got := env["CLAUDE_CODE_OAUTH_SCOPES"]; got != "user:inference user:profile" {
		t.Errorf("env[CLAUDE_CODE_OAUTH_SCOPES] = %v, want it verbatim", got)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://api.anthropic.com" {
		t.Errorf("env[ANTHROPIC_BASE_URL] = %v, want the URL verbatim", got)
	}
}

// Reconstruct mode (maxString=0): the raw text is kept verbatim except that
// credential VALUES are masked textually. This is the mode that leaked before the
// fix, and it must both withhold the secrets and preserve field order.
func TestWireLogRedactsSpawnEnvCredentials_RawMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, 0)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.record(1, "in", []byte(spawnEnvFrame))
	w.Close()

	recs := readWireLog(t, path)
	got, ok := recs[0]["raw"].(string)
	if !ok {
		t.Fatalf("raw absent at maxString=0: %v", recs[0])
	}
	if strings.Contains(got, "oat01-EXAMPLE") || strings.Contains(got, "ak-EXAMPLE") {
		t.Fatalf("raw contains a credential: %s", got)
	}
	if !strings.Contains(got, `"CLAUDE_CODE_OAUTH_TOKEN":"[redacted]"`) ||
		!strings.Contains(got, `"ANTHROPIC_API_KEY":"[redacted]"`) {
		t.Errorf("a credential value was not masked in raw: %s", got)
	}
	if !strings.Contains(got, `"CLAUDE_CODE_OAUTH_SCOPES":"user:inference user:profile"`) {
		t.Errorf("OAUTH_SCOPES (diagnostic, not secret) was over-redacted: %s", got)
	}
	// Order preserved: the token key precedes the scopes key exactly as sent, which
	// the sorted body re-encoding would not guarantee.
	if strings.Index(got, "OAUTH_TOKEN") > strings.Index(got, "OAUTH_SCOPES") {
		t.Errorf("raw did not preserve wire order: %s", got)
	}
}

// The raw fallback for an UNPARSEABLE frame must also mask credentials. Go's
// decoder rejects an unescaped control byte inside a string, so a frame carrying
// the auth member plus a stray 0x01 fails json.Unmarshal and lands in raw — the
// branch a capture (switched on for a misbehaving client) is most likely to hold.
func TestWireLogRawFallbackRedactsMalformedFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	const token = "SECRET-TOKEN-do-not-log"
	// The \x01 makes the frame unparseable; the auth member is otherwise intact.
	w.record(1, "in", []byte("{\"auth\":\""+token+"\",\"method\":\"files.write\",\"x\":\"\x01\"}"))
	w.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if strings.Contains(string(b), token) {
		t.Fatalf("malformed-frame fallback stored the token verbatim:\n%s", b)
	}
	recs := readWireLog(t, path)
	if recs[0]["parse_error"] == nil {
		t.Error("parse_error absent — the frame was not treated as the raw fallback")
	}
	if raw, _ := recs[0]["raw"].(string); !strings.Contains(raw, `"auth":"[redacted]"`) {
		t.Errorf("auth value not masked in the raw fallback: %q", raw)
	}
}

// A reply carrying an error member is flagged is_error at the top level, so a
// capture is greppable for failures without decoding every body.
func TestWireLogFlagsErrorFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	w.record(1, "out", []byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"unauthorized"}}`))
	w.Close()

	recs := readWireLog(t, path)
	if recs[0]["is_error"] != true {
		t.Errorf("is_error = %v, want true", recs[0]["is_error"])
	}
}

// A frame truncated INSIDE a credential value (a client dying mid-write, which the
// read loop delivers as an unterminated final token) has no closing quote, so the
// mask must also fire on a value that runs to end-of-frame — bare or on a lone
// backslash — not only on a fully quoted one.
func TestWireLogRawFallbackRedactsTruncatedValue(t *testing.T) {
	const token = "SECRET-TOKEN-do-not-log"
	cases := map[string]string{
		"eof mid-value":  `{"jsonrpc":"2.0","method":"files.write","auth":"` + token,
		"lone backslash": `{"jsonrpc":"2.0","method":"files.write","auth":"` + token + `\`,
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wire.jsonl")
			w, err := newWireLog(path, wireLogMaxString)
			if err != nil {
				t.Fatalf("newWireLog: %v", err)
			}
			w.record(1, "in", []byte(frame))
			w.Close()

			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read capture: %v", err)
			}
			if strings.Contains(string(b), token) {
				t.Fatalf("truncated-value frame stored the token verbatim:\n%s", b)
			}
			if raw, _ := readWireLog(t, path)[0]["raw"].(string); !strings.Contains(raw, `"auth":"[redacted]"`) {
				t.Errorf("auth value not masked in truncated raw: %q", raw)
			}
		})
	}
}

// n counts the frame body, not the framing newline, in both directions — otherwise
// throughput read out of a capture is biased by one byte per outbound frame.
func TestWireLogNExcludesFramingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	w, err := newWireLog(path, wireLogMaxString)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	frame := `{"method":"server.ping"}`
	w.record(1, "out", []byte(frame+"\n")) // outbound carries the framing newline
	w.record(2, "in", []byte(frame))       // inbound does not
	w.Close()

	recs := readWireLog(t, path)
	out := recs[0]["n"].(float64)
	in := recs[1]["n"].(float64)
	if int(out) != len(frame) || int(in) != len(frame) {
		t.Fatalf("n out=%v in=%v, want both %d (frame body, no newline)", out, in, len(frame))
	}
}

// clampString cuts on a rune boundary, so a truncated multi-byte value keeps the
// exact leading bytes the client sent rather than a split rune json.Marshal would
// rewrite to U+FFFD.
func TestWireLogClampStringCutsOnRuneBoundary(t *testing.T) {
	w := &wireLog{maxStr: 3}
	// "é" is 2 bytes (0xC3 0xA9); a byte-3 cut lands on a continuation byte, so the
	// backoff must retreat to byte 2 and keep exactly one "é".
	got := w.clampString("ééé")
	prefix, _, _ := strings.Cut(got, "…")
	if !utf8.ValidString(prefix) {
		t.Errorf("clamped prefix is not valid UTF-8: %q", prefix)
	}
	if prefix != "é" {
		t.Errorf("prefix = %q, want %q (backed off to the rune boundary)", prefix, "é")
	}
}
