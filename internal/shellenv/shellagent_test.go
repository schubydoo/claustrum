package shellenv

import (
	"strings"
	"testing"
)

// stubShellAgentSocket replaces the login-shell lookup for one test and counts
// the calls, so a test can tell "not needed" from "needed, found nothing".
func stubShellAgentSocket(t *testing.T, sock string) *int {
	t.Helper()
	calls := 0
	old := AgentSocket
	AgentSocket = func() string { calls++; return sock }
	t.Cleanup(func() { AgentSocket = old })
	return &calls
}

func TestAddShellAgentSocket(t *testing.T) {
	base := []string{"HOME=/h", "PATH=/bin"}
	cases := []struct {
		name      string
		env       []string
		disabled  bool
		stub      string
		want      []string
		wantCalls int
	}{
		{"appended last when absent", base, false, "/tmp/a.sock",
			[]string{"HOME=/h", "PATH=/bin", "SSH_AUTH_SOCK=/tmp/a.sock"}, 1},
		{"nothing to hand on", base, false, "", base, 1},
		{"disabled", base, true, "/tmp/a.sock", base, 0},
		{"present wins", []string{"SSH_AUTH_SOCK=/mine"}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCK=/mine"}, 0},
		{"present and empty still wins", []string{"SSH_AUTH_SOCK="}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCK="}, 0},
		{"a longer name is not the key", []string{"SSH_AUTH_SOCKET=/x"}, false, "/tmp/a.sock",
			[]string{"SSH_AUTH_SOCKET=/x", "SSH_AUTH_SOCK=/tmp/a.sock"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubShellAgentSocket(t, tc.stub)
			env := append([]string(nil), tc.env...)
			got := AddAgentSocket(env, tc.disabled)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("env = %q, want %q", got, tc.want)
			}
			if *calls != tc.wantCalls {
				t.Errorf("lookup ran %d times, want %d", *calls, tc.wantCalls)
			}
		})
	}
}
