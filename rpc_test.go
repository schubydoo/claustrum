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
			"wrong jsonrpc version -> -32600, id echoed",
			`{"jsonrpc":"1.0","id":1,"method":"server.ping"}`,
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid JSON-RPC version"}}`,
		},
		{
			"missing auth -> -32001",
			`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`,
			`{"jsonrpc":"2.0","id":2,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`,
		},
		{
			"wrong auth -> -32001",
			`{"jsonrpc":"2.0","id":3,"method":"server.ping","auth":"nope"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`,
		},
		{
			"no namespace separator -> -32601 with full method",
			`{"jsonrpc":"2.0","id":4,"method":"bogus",` + auth + `}`,
			`{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"Unknown namespace: bogus"}}`,
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
