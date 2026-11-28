package supervisor

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestExecChildRunDir pins the run-dir derivation: the run dir is the folder of the
// socket path on every socket shape, cleaned and not made absolute. Each case is a row
// of the Linux and macOS measurement of 89cb6289. Only an empty socket
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
		if got := ExecChildRunDir(c.sock); got != filepath.FromSlash(c.want) {
			t.Errorf("ExecChildRunDir(%q) = %q, want %q", c.sock, got, c.want)
		}
	}
}

// TestDropMarkerEnvDropsBothNames pins the filter of the two marker names: every entry of
// either name goes, and the other entries keep their order. A name that only starts
// with a marker name stays. The function is the same code on every system.
func TestDropMarkerEnvDropsBothNames(t *testing.T) {
	env := []string{
		"A=1", EnvChildMarker + "=x", EnvRunDir + `=C:\x`, "B=2",
		EnvChildMarker + "=y", EnvRunDir + "=/y", "CLAUDE_SSH_DAEMON_CHILD=1", "CLAUDE_SSH_CHILDREN=3",
	}
	want := []string{"A=1", "B=2", "CLAUDE_SSH_DAEMON_CHILD=1", "CLAUDE_SSH_CHILDREN=3"}
	if got := DropMarkerEnv(env); !slices.Equal(got, want) {
		t.Errorf("DropMarkerEnv = %q, want %q", got, want)
	}
}
