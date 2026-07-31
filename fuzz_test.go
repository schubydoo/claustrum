package main

import (
	"encoding/json"
	"testing"
)

// fuzzSkipMethods are the methods FuzzDispatch must NOT actually execute, because
// their bodies have side effects a coverage-guided fuzzer could weaponize:
// process.* spawns/signals real processes (arbitrary code execution), files.read
// does an unbounded read (OOM), files.extract_tar wipes destDir (RemoveAll), the
// git.* methods shell out, files.stat/list/validate hit the filesystem, and
// server.shutdown closes a channel. Their request-validation is covered by the
// unit/integration suites and by FuzzBindParams; here we only need the parser,
// auth/version gates, routing, and param-presence checks to survive any input.
var fuzzSkipMethods = map[string]bool{
	"server.shutdown":     true,
	"files.stat":          true,
	"files.list":          true,
	"files.read":          true,
	"files.validate":      true,
	"files.extract_tar":   true,
	"git.info":            true,
	"git.status":          true,
	"git.list_branches":   true,
	"git.worktree_create": true,
	"git.worktree_remove": true,
	"process.spawn":       true,
	"process.stdin":       true,
	"process.kill":        true,
	"process.reattach":    true,
}

// FuzzDispatch asserts the invariant that, for ANY input line, dispatch never
// panics and any response it produces is a well-formed JSON-RPC frame: valid
// JSON, jsonrpc "2.0", an id, and exactly one of result/error (a well-shaped
// error object when present). It runs against a valid token so the routing,
// method-format, and param-presence paths are reachable; methods with real side
// effects are skipped (see fuzzSkipMethods) so the fuzzer can't drive them.
func FuzzDispatch(f *testing.F) {
	auth := `"auth":"` + testToken + `"`
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"server.ping",` + auth + `}`,
		`{"jsonrpc":"2.0","id":"x","method":"server.capabilities",` + auth + `}`,
		`{"jsonrpc":"2.0","id":1,"method":"server.version"}`, // no auth
		`{"jsonrpc":"1.0","id":1,"method":"server.ping",` + auth + `}`,
		`{"jsonrpc":"2.0","id":1,"method":"files.bogus",` + auth + `}`,
		`{"jsonrpc":"2.0","id":1,"method":"bogus.method",` + auth + `}`,
		`{"jsonrpc":"2.0","id":1,"method":"nodot",` + auth + `}`,
		`{"jsonrpc":"2.0","id":1,"method":"server.ping","params":123,` + auth + `}`,
		`{"jsonrpc":"2.0","method":"server.ping",` + auth + `}`, // notification (no id)
		`{"jsonrpc":"2.0","id":null,"method":"server.ping",` + auth + `}`,
		`{}`, `[]`, ``, `   `, `{not json`, `{"id":1}`, "\x00", `{"method":7}`,
	} {
		f.Add([]byte(seed))
	}

	s := newTestServer(f) // only read-only methods execute, so one server is safe
	f.Fuzz(func(t *testing.T, raw []byte) {
		var peek struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &peek)
		if fuzzSkipMethods[peek.Method] {
			return
		}
		resp := s.dispatch(nil, raw)
		if resp == nil {
			return // only the (skipped) server.shutdown is silent
		}
		assertWellFormedResponse(t, resp, raw)
	})
}

// FuzzBindParams fuzzes the param-decode layer (bindParams) across every param
// struct. It is pure (json.Unmarshal into a struct, no IO): the only acceptable
// outcomes are nil (decoded) or an Invalid-params response — never a panic and
// never any other error code.
func FuzzBindParams(f *testing.F) {
	for _, seed := range []string{
		`{"path":"/x","maxBytes":5}`, `{"path":123}`, `{"maxBytes":"4"}`,
		`{"id":"a","command":"c","args":["y"],"env":{"K":"V"}}`,
		`{"archivePath":"a","destDir":"/d"}`, `{"id":"a","fromSeq":2}`,
		`[]`, `"x"`, `null`, `true`, `123`, ``, `{`, `{"x":[1,2,{}]}`,
	} {
		f.Add([]byte(seed))
	}

	// pathExpander, not interface{}: bindParams now takes the interface so a
	// params struct cannot skip `~` expansion (see expandpath.go). Fuzzing
	// through the same type keeps this exercising the real signature.
	makers := []func() pathExpander{
		func() pathExpander { return &pathParams{} },
		func() pathExpander { return &gitParams{} },
		func() pathExpander { return &spawnParams{} },
		func() pathExpander { return &extractTarParams{} },
		func() pathExpander { return &stdinParams{} },
		func() pathExpander { return &killParams{} },
		func() pathExpander { return &killAndWaitParams{} },
		func() pathExpander { return &reattachParams{} },
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		req := &request{ID: json.RawMessage(`1`), Params: json.RawMessage(raw)}
		for _, mk := range makers {
			if bad := bindParams(req, mk()); bad != nil {
				if bad.Error == nil || bad.Error.Code != codeInvalidParam {
					t.Errorf("bindParams(%q) returned a non-Invalid-params response: %+v", raw, bad)
				}
			}
		}
	})
}

func assertWellFormedResponse(t *testing.T, resp *response, raw []byte) {
	t.Helper()
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("response failed to marshal (input %q): %v", raw, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("response is not valid JSON: %s (input %q)", b, raw)
	}
	if string(m["jsonrpc"]) != `"2.0"` {
		t.Errorf("jsonrpc != \"2.0\": %s (input %q)", b, raw)
	}
	if _, ok := m["id"]; !ok {
		t.Errorf("response missing id: %s (input %q)", b, raw)
	}
	_, hasResult := m["result"]
	_, hasError := m["error"]
	if hasResult == hasError {
		t.Errorf("response must have exactly one of result/error: %s (input %q)", b, raw)
	}
	if hasError {
		var e struct {
			Code    *int    `json:"code"`
			Message *string `json:"message"`
		}
		if err := json.Unmarshal(m["error"], &e); err != nil || e.Code == nil || e.Message == nil {
			t.Errorf("malformed error object: %s (input %q)", b, raw)
		}
	}
}
