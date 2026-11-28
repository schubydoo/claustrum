//go:build !linux

package main

import "testing"

// TestSocketHostCredsUnsupported pins the refusal of every hostCreds of the right form
// on macOS and Windows (13047a51, measured on macOS and Windows): {}, an empty env, a
// valid env and an extra member. No child starts (measured on macOS).
func TestSocketHostCredsUnsupported(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "echo")
	for i, hc := range []string{`{}`, `{"env":{}}`, `{"env":{"A":"b"}}`, `{"env":{"A":"b"},"extra":1}`} {
		got := string(cl.call(hostCredsReq(t, i+1, "hc-u", exe, env, hc, nil)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":{"code":-32602,"message":"hostCreds is not supported on this host","data":{"reason":"other"}}}`
		if got != want {
			t.Errorf("hostCreds %s: reply = %s\nwant %s", hc, got, want)
		}
	}
	cl.mu.Lock()
	n := len(cl.fr)
	cl.mu.Unlock()
	if n != 0 {
		t.Errorf("%d stream frames, want none: no child starts", n)
	}
}
