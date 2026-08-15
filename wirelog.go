package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// wireLog is the opt-in JSON-RPC frame recorder behind -wire-log. It is a pure
// side channel: it observes frames that have ALREADY been marshaled (outbound) or
// read (inbound) and never touches the bytes on the socket, so a daemon with
// logging on emits frames byte-identical to one without it. That is the whole
// design constraint — see docs/ARCHITECTURE.md.
//
// Off unless -wire-log (or the wire-log key in claustrum.conf) names a path, the
// same shape as -metrics-addr: Claude Desktop owns the -serve argv, so the config
// file is usually the reachable knob.
//
// The record is one JSON object per line (JSONL), so a capture is greppable and
// streamable without a parser holding the whole file.
type wireLog struct {
	mu     sync.Mutex
	f      *os.File
	maxStr int // longest string value kept verbatim; longer ones are summarized

	// pid stamps every record. Connection ids come from a per-process counter
	// (server.go's connSeq), so they are unique only within one daemon — while
	// the capture is opened O_APPEND and outlives any restart. Two daemons, or
	// one daemon restarted, therefore both number their connections from 1 into
	// the same file. Without pid a reader cannot tell those apart, and anything
	// correlating a reply to its request by (conn, id) silently mis-pairs across
	// the boundary. Measured on a live capture 2026-08-10: conn 2 held 20,222
	// frames from one daemon and 10,638 from its successor.
	pid int

	dropped atomic.Uint64 // frames a write error lost, reported once at Close
}

// wireLogMaxString is the DEFAULT bound on a single string value in a logged
// frame. Stream frames carry base64 stdout/stderr, so without a bound the capture
// is ~95% payload and the method-level shape is buried. The full length is kept in
// the summary, so nothing about SIZE is lost — only the bytes themselves.
//
// ⚠️ The default is right for reading protocol SHAPE and wrong for RECONSTRUCTING
// a session: Claude Desktop drives the remote CLI through process.spawn, so the
// CLI's whole stdio protocol rides inside those truncated strings. Set
// -wire-log-max-string=0 for an untruncated capture when the payload is the point.
const wireLogMaxString = 512

// wireLogOptions carries the -wire-log settings as one value, so enabling a
// capture does not add a parameter per knob to the server constructor.
type wireLogOptions struct {
	// path is the capture file; "" means logging is off.
	path string
	// maxString bounds one string value. 0 means unlimited — deliberately NOT
	// "some huge number", so the unlimited path skips the clamp entirely rather
	// than imposing a different, larger, still-observable bound.
	maxString int
}

// newWireLog opens path for append, creating it 0600: a capture contains whatever
// the client sent, which for files.write or process.stdin is arbitrary user data.
// Append rather than truncate so a daemon restart during a capture session (the
// reference respawns instantly) does not silently discard the earlier half — which
// is exactly why each record carries pid; see the field's comment.
func newWireLog(path string, maxString int) (*wireLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	if maxString < 0 {
		maxString = 0
	}
	return &wireLog{f: f, maxStr: maxString, pid: os.Getpid()}, nil
}

// record writes one frame. dir is "in" for a client request and "out" for
// anything the daemon sends (replies, stream notifications, replay frames).
//
// b is NOT retained or modified: redaction works on a decoded copy. A nil
// receiver is a no-op so every call site can be an unconditional method call
// rather than a nil check.
func (w *wireLog) record(connID uint64, dir string, b []byte) {
	if w == nil {
		return
	}
	// Outbound frames reach record with the framing '\n' appended; inbound do not.
	// Strip one so n and the recorded frame mean the same in both directions.
	frame := b
	if n := len(frame); n > 0 && frame[n-1] == '\n' {
		frame = frame[:n-1]
	}
	rec := map[string]interface{}{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"pid":  w.pid,
		"conn": connID,
		"dir":  dir,
		"n":    len(frame),
	}
	var body interface{}
	if err := json.Unmarshal(frame, &body); err != nil {
		// A frame that is not JSON is still worth recording — it is exactly the
		// kind of thing a capture exists to catch. Keep it as a bounded string,
		// with credential values masked textually: the by-key map walk cannot run
		// on bytes that do not parse, and a truncated or control-char frame can
		// still carry the auth member verbatim.
		rec["parse_error"] = err.Error()
		rec["raw"] = w.clampString(redactRawSecrets(string(frame)))
	} else {
		if m, ok := body.(map[string]interface{}); ok {
			if v, ok := m["method"].(string); ok {
				rec["method"] = v
			}
			if v, ok := m["id"]; ok {
				rec["id"] = v
			}
			if _, ok := m["error"]; ok {
				rec["is_error"] = true
			}
		}
		if w.maxStr == 0 {
			// Reconstruct mode: nothing is truncated, so keep the frame verbatim
			// (credential values masked) as raw. Unlike the decoded body it
			// preserves field order and number formatting — which for this daemon
			// IS the wire contract a capture exists to check (results.go).
			rec["raw"] = redactRawSecrets(string(frame))
		} else {
			// Shape mode: a per-value-truncated, by-key-redacted decode. json.Marshal
			// sorts map keys, so body is a NORMALISED view — field order is not
			// significant here; use -wire-log-max-string=0 when order matters.
			rec["body"] = w.redact(body)
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		w.dropped.Add(1)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return
	}
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		// Never fail a request because logging failed: the wire contract does not
		// depend on this file existing. Count it and report at Close.
		w.dropped.Add(1)
	}
}

// secretKeyParts name the substrings that mark a map key as carrying a
// credential. The RPC "auth" member is not the only secret a frame can hold:
// Claude Desktop drives the remote CLI through process.spawn and passes
// CLAUDE_CODE_OAUTH_TOKEN in params.env, so an untruncated capture stores a live
// OAuth credential in plaintext — measured on a live capture 2026-08-13.
//
// Matching is on the key, case-insensitively, at any depth. No result struct or
// request field claustrum defines is named for any of these (rpc.go's "auth" is
// the repo's only such json tag), so the rule can only ever fire on a key the
// CLIENT chose — an env var name or free-form JSON — which is the target.
var secretKeyParts = []string{"token", "secret", "password", "passwd", "credential", "api_key"}

// isSecretKey reports whether a map key names a credential. "auth" is matched
// exactly; the rest are substrings, so CLAUDE_CODE_OAUTH_TOKEN is caught while
// CLAUDE_CODE_OAUTH_SCOPES — which is diagnostic, not secret — is not.
func isSecretKey(k string) bool {
	lower := strings.ToLower(k)
	if lower == "auth" {
		return true
	}
	for _, part := range secretKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// rawSecretRe masks the string VALUE of a credential-named key in raw frame text.
// It is the textual analogue of isSecretKey, built from the same key names, for
// the two paths the by-key map walk cannot reach: a frame that fails to parse,
// and the maxString=0 raw field (which is kept verbatim to preserve wire order).
//
// The key arm uses `[^"]` — the closing quote is the fence — so it mirrors
// isSecretKey's whole-key substring match rather than a narrower character class:
// `auth-token`, `x-auth-token`, `api.token` are masked here just as the map walk
// masks them, and an *embedded* \"api_key\": inside a payload string still does not
// match, because that value's opening quote is escaped and the value arm's literal
// `"` fails. "auth" stays a whole-key match; the rest are substrings (OAUTH_TOKEN
// caught, OAUTH_SCOPES not).
//
// The value arm closes on either the terminating quote OR end-of-frame (`\\?$`),
// so a value truncated mid-write — the read loop hands an unterminated final token
// to record before anything validates it — is masked too, whether it ends bare or
// on a lone backslash. A terminated value cannot reach the $ branch because
// `[^"\\]` stops at its closing quote first, so a single pass covers both.
//
// Two residues stay best-effort by construction, both narrower than any real
// credential shape and both still covered by the map walk in shape mode: a key
// written with a JSON escape (`"token"`), and a secret whose VALUE is not a
// string (`"token":123`, or an object). A credential is a high-entropy string
// under a literal key in every frame the probe has seen, so this is not widened
// into fragile value-type or unescaping logic.
var rawSecretRe = regexp.MustCompile(
	`(?i)("(?:auth|[^"]*(?:` + strings.Join(secretKeyParts, "|") +
		`)[^"]*)"\s*:\s*)"(?:[^"\\]|\\.)*(?:"|\\?$)`)

// redactRawSecrets replaces the value of each credential-named key with
// [redacted], leaving the surrounding bytes — and their order — untouched.
func redactRawSecrets(s string) string {
	return rawSecretRe.ReplaceAllString(s, `${1}"[redacted]"`)
}

// redact returns a copy of v with credentials removed and long strings
// summarized. It must never return the auth token: a capture is a plain file, and
// the token is equivalent to shell access on this host (see SECURITY.md).
//
// ⚠️ Redaction is BY KEY, not by value. A secret that a client embeds inside a
// free-form string — a process.stdin payload, a --settings JSON blob — is not
// caught, because finding it would mean scanning every byte of every stream frame.
// A capture is therefore still sensitive: treat it as such rather than as scrubbed.
func (w *wireLog) redact(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if isSecretKey(k) {
				// Recorded as present-but-withheld rather than dropped, so a
				// capture still shows WHICH requests carried a token — that is
				// the interesting part (server.shutdown carries none).
				out[k] = "[redacted]"
				continue
			}
			out[k] = w.redact(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = w.redact(val)
		}
		return out
	case string:
		return w.clampString(t)
	default:
		return v
	}
}

// clampString keeps short strings verbatim and replaces a long one with a prefix
// plus its true length. The length is the point: stream-frame sizes are how you
// read throughput out of a capture.
func (w *wireLog) clampString(s string) string {
	limit := w.maxStr
	if limit <= 0 || len(s) <= limit {
		return s
	}
	// Back off to a rune boundary so the kept prefix is the exact leading bytes the
	// client sent, not a split rune json.Marshal would rewrite to U+FFFD.
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + "…[truncated, " + strconv.Itoa(len(s)) + " bytes total]"
}

// Close flushes and reports any frames lost to write errors. Safe on nil.
func (w *wireLog) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return
	}
	if n := w.dropped.Load(); n > 0 {
		logWarnf("[WireLog] %d frame(s) dropped on write error", n)
	}
	if err := w.f.Close(); err != nil {
		logWarnf("[WireLog] close: %v", err)
	}
	w.f = nil
}

// openWireLog resolves the -wire-log setting into a logger, or nil when off.
// A path that cannot be opened is fatal rather than silently ignored: an operator
// who asked for a capture and got none would read the empty result as "the client
// sent nothing", which is the worst possible failure for a diagnostic tool.
func openWireLog(opt wireLogOptions) (*wireLog, error) {
	if opt.path == "" {
		return nil, nil
	}
	w, err := newWireLog(opt.path, opt.maxString)
	if err != nil {
		return nil, fmt.Errorf("wire log %s: %w", opt.path, err)
	}
	if opt.maxString == 0 {
		logInfof("[WireLog] recording JSON-RPC frames to %s (untruncated)", opt.path)
	} else {
		logInfof("[WireLog] recording JSON-RPC frames to %s (strings over %d bytes truncated)", opt.path, opt.maxString)
	}
	return w, nil
}
