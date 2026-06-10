package main

import (
	"encoding/json"
	"testing"
)

const testToken = "s3cret-token"

func newTestServer() *server {
	return &server{
		token:    testToken,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
}

// dispatchRaw feeds a raw line through dispatch and returns the marshaled
// response (or "" when the method is silent, e.g. server.shutdown).
func dispatchRaw(t *testing.T, s *server, line string) string {
	t.Helper()
	resp := s.dispatch(nil, []byte(line))
	if resp == nil {
		return ""
	}
	b, err := json.Marshal(*resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(b)
}

// TestAuthTokenComparison locks the auth-token check: the exact token is accepted
// and every near-miss (wrong byte, prefix, extra suffix, empty, wrong length) is
// rejected with -32001. Guards the constant-time comparison (crypto/subtle)
// against a regression to a short-circuiting or loose compare.
func TestAuthTokenComparison(t *testing.T) {
	s := newTestServer()
	unauth := `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`
	ping := func(tok string) string {
		return dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"`+tok+`"}`)
	}
	// The exact token is accepted: server.ping returns a result, not the -32001 frame.
	if got := ping(testToken); got == unauth {
		t.Fatalf("correct token rejected: %s", got)
	}
	// Every near-miss is rejected identically.
	for _, bad := range []string{
		"",              // empty (fast-rejected before the compare)
		"nope",          // wrong, shorter
		"s3cret-toke",   // a prefix of the token (one byte short)
		"s3cret-token ", // the token plus a trailing byte
		"s3cret-tokeX",  // same length, last byte differs
		"S3cret-token",  // same length, first byte differs
	} {
		if got := ping(bad); got != unauth {
			t.Errorf("token %q: got %s, want unauthorized", bad, got)
		}
	}
}

func TestDispatchErrorPaths(t *testing.T) {
	s := newTestServer()
	auth := `"auth":"` + testToken + `"`

	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"parse error -> null id, -32700",
			`{not json`,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`,
		},
		{
			"valid auth + wrong jsonrpc version -> -32600 (version checked AFTER auth)",
			`{"jsonrpc":"1.0","id":1,"method":"server.ping",` + auth + `}`,
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid JSON-RPC version"}}`,
		},
		{
			"missing auth -> -32001",
			`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`,
			`{"jsonrpc":"2.0","id":2,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`,
		},
		{
			// Precedence: when BOTH auth and version are bad, auth wins. The frame
			// battery never sent this combo, which is how the order divergence hid.
			"wrong version AND no auth -> auth wins (-32001)",
			`{"jsonrpc":"1.0","id":8,"method":"server.ping"}`,
			`{"jsonrpc":"2.0","id":8,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`,
		},
		{
			"wrong auth -> -32001",
			`{"jsonrpc":"2.0","id":3,"method":"server.ping","auth":"nope"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`,
		},
		{
			// A method without a "." has no namespace.method shape -> format error,
			// distinct from a well-formed method naming an unknown namespace.
			"no namespace separator -> -32601 Invalid method format",
			`{"jsonrpc":"2.0","id":4,"method":"bogus",` + auth + `}`,
			`{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"Invalid method format: bogus"}}`,
		},
		{
			"unknown namespace -> -32601 with namespace only",
			`{"jsonrpc":"2.0","id":5,"method":"foo.bar",` + auth + `}`,
			`{"jsonrpc":"2.0","id":5,"error":{"code":-32601,"message":"Unknown namespace: foo"}}`,
		},
		{
			"unknown method in known namespace -> -32601",
			`{"jsonrpc":"2.0","id":6,"method":"server.nope",` + auth + `}`,
			`{"jsonrpc":"2.0","id":6,"error":{"code":-32601,"message":"Unknown method: server.nope"}}`,
		},
		{
			"known method, absent params -> -32602",
			`{"jsonrpc":"2.0","id":7,"method":"files.stat",` + auth + `}`,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"Invalid params"}}`,
		},
		{
			// Mistyped params (present, but wrong value type) -> -32602, matching the
			// reference. The frame battery only ever sent ABSENT params, so this
			// whole class of divergence (we silently ignored the decode error) hid.
			"mistyped params: string where number expected -> -32602",
			`{"jsonrpc":"2.0","id":9,"method":"files.read","params":{"path":"/x","maxBytes":"4"},` + auth + `}`,
			`{"jsonrpc":"2.0","id":9,"error":{"code":-32602,"message":"Invalid params"}}`,
		},
		{
			"mistyped params: number where string expected -> -32602",
			`{"jsonrpc":"2.0","id":10,"method":"files.stat","params":{"path":123},` + auth + `}`,
			`{"jsonrpc":"2.0","id":10,"error":{"code":-32602,"message":"Invalid params"}}`,
		},
		{
			"mistyped params: non-object params value -> -32602",
			`{"jsonrpc":"2.0","id":11,"method":"git.status","params":"x",` + auth + `}`,
			`{"jsonrpc":"2.0","id":11,"error":{"code":-32602,"message":"Invalid params"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchRaw(t, s, tc.line); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestDispatchHappyServerMethods(t *testing.T) {
	s := newTestServer()
	auth := `"auth":"` + testToken + `"`

	got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"server.ping",`+auth+`}`)
	want := `{"jsonrpc":"2.0","id":1,"result":{"pong":true}}`
	if got != want {
		t.Errorf("ping:\n got: %s\nwant: %s", got, want)
	}

	// server.* methods don't decode params, so a mistyped params value is ignored
	// (the reference still pongs) — unlike files/git/process, which reject it.
	if got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":9,"method":"server.ping","params":123,`+auth+`}`); got != `{"jsonrpc":"2.0","id":9,"result":{"pong":true}}` {
		t.Errorf("server.ping with mistyped params should still pong, got: %s", got)
	}

	// server.shutdown is silent: dispatch returns nil and signals stop.
	if got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":2,"method":"server.shutdown",`+auth+`}`); got != "" {
		t.Errorf("shutdown should be silent, got: %s", got)
	}
	select {
	case <-s.shutdown:
	default:
		t.Error("server.shutdown did not signal the shutdown channel")
	}
}

// The id is echoed verbatim — numbers stay numbers, strings stay strings — so a
// client can correlate replies regardless of its id type.
func TestDispatchEchoesIDVerbatim(t *testing.T) {
	s := newTestServer()
	auth := `"auth":"` + testToken + `"`
	for _, id := range []string{`42`, `"req-a"`, `null`} {
		line := `{"jsonrpc":"2.0","id":` + id + `,"method":"server.ping",` + auth + `}`
		want := `{"jsonrpc":"2.0","id":` + id + `,"result":{"pong":true}}`
		if got := dispatchRaw(t, s, line); got != want {
			t.Errorf("id %s:\n got: %s\nwant: %s", id, got, want)
		}
	}
}

func TestDecodeAndNeedParams(t *testing.T) {
	// Absent params is rejected by needParams.
	if needParams(&request{}) == nil {
		t.Error("needParams: empty params should be rejected")
	}
	if needParams(&request{Params: json.RawMessage(`{}`)}) != nil {
		t.Error("needParams: an empty object {} must be accepted")
	}

	// decodeParams treats absent params as a no-op (leaves zero value).
	var p pathParams
	if err := decodeParams(&request{}, &p); err != nil {
		t.Fatalf("decodeParams empty: %v", err)
	}
	if p.Path != "" {
		t.Errorf("expected zero-value path, got %q", p.Path)
	}
	if err := decodeParams(&request{Params: json.RawMessage(`{"path":"/tmp","maxBytes":5}`)}, &p); err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if p.Path != "/tmp" || p.MaxBytes != 5 {
		t.Errorf("decoded params = %+v", p)
	}
}
