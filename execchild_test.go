package main

import (
	"path/filepath"
	"testing"
)

// TestExecChildRunDir pins the run-dir derivation: the run dir is the folder of the
// socket path on every socket shape, cleaned and not made absolute. Each case is a row
// of the Linux and macOS measurement of 89cb6289 (F1 to F4, K3). Only an empty socket
// path gives no run dir. The function is the same code on every system, so the test
// runs on every system. On Windows the wanted value has the separator of that system.
func TestExecChildRunDir(t *testing.T) {
	cases := []struct {
		sock, want string
	}{
		{"/x/run/c0ffee01/rpc.sock", "/x/run/c0ffee01"}, // F1
		{"/x/b/s.sock", "/x/b"},                         // F2, K3
		{"s.sock", "."},                                 // F3: a relative socket
		{"/x//run/./c1/rpc.sock", "/x/run/c1"},          // F4: cleaned
		{"run/c1/rpc.sock", "run/c1"},                   // EV01h: stays relative
		{"", ""},
	}
	for _, c := range cases {
		if got := execChildRunDir(c.sock); got != filepath.FromSlash(c.want) {
			t.Errorf("execChildRunDir(%q) = %q, want %q", c.sock, got, c.want)
		}
	}
}
