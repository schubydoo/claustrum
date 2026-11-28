package main

import (
	"encoding/json"
	"testing"
)

// hostCredsReq is a process.spawn request with the raw hostCreds member hc. extra
// members replace those of the base params, and a nil value removes one.
func hostCredsReq(t *testing.T, id int, procID, command string, env map[string]string, hc string, extra map[string]any) string {
	t.Helper()
	params := map[string]any{"id": procID, "command": command, "args": []string{"x"}, "env": env, "hostCreds": json.RawMessage(hc)}
	for k, v := range extra {
		if v == nil {
			delete(params, k)
			continue
		}
		params[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// TestHostCredsValueForm pins the form test of the hostCreds member (13047a51): null,
// a text, a list and an env value that is a number fail (measured on Linux, macOS and
// Windows). An env that is a text or a list and an env value of null fail (measured on
// Linux and macOS). {}, an empty env and an extra member pass.
func TestHostCredsValueForm(t *testing.T) {
	cases := []struct {
		raw      string
		set, ok  bool
		wantSize int
	}{
		{"", false, true, 0},
		{"null", true, false, 0},
		{`"x"`, true, false, 0},
		{`[]`, true, false, 0},
		{`{"env":"x"}`, true, false, 0},
		{`{"env":[]}`, true, false, 0},
		{`{"env":{"A":1}}`, true, false, 0},
		{`{"env":{"A":null}}`, true, false, 0},
		{`{"env":{"A":null,"B":"c"}}`, true, false, 0},
		{`{}`, true, true, 0},
		{`{"env":{}}`, true, true, 0},
		{`{"env":{"A":"b"},"extra":1,"unless":"B"}`, true, true, 1},
	}
	for _, c := range cases {
		env, set, ok := hostCredsValue(json.RawMessage(c.raw))
		if set != c.set || ok != c.ok || len(env) != c.wantSize {
			t.Errorf("hostCredsValue(%s) = %v, %v, %v, want a size of %d, %v, %v", c.raw, env, set, ok, c.wantSize, c.set, c.ok)
		}
	}
}

// TestSocketHostCredsInvalidParams pins Invalid params with no data for a hostCreds
// member of the wrong form (13047a51, measured on Linux and macOS, and on Windows for
// null, a text, a list and a number value).
func TestSocketHostCredsInvalidParams(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "echo")
	for i, hc := range []string{`null`, `"x"`, `[]`, `{"env":"x"}`, `{"env":[]}`, `{"env":{"FOO":1}}`, `{"env":{"A":null}}`} {
		got := string(cl.call(hostCredsReq(t, i+1, "hc-bad", exe, env, hc, nil)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":{"code":-32602,"message":"Invalid params"}}`
		if got != want {
			t.Errorf("hostCreds %s: reply = %s\nwant %s", hc, got, want)
		}
	}
}
