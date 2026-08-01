package main

import (
	"strings"
	"testing"
)

// server.shutdown is the ONE method the reference does not authenticate, and
// claustrum must match it: the Desktop client tears the daemon down by shelling
// out to `server --stop --socket <sock>` with no CLAUDE_RPC_TOKEN in its
// environment. A claustrum that demands auth here cannot be stopped by it at
// all, which defeats being swappable for the reference.
//
// Measured at 5db5e4a: a shutdown frame whose auth member is absent, wrong, or
// arbitrary all stop the daemon.
func TestShutdownNeedsNoAuth(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"auth absent", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`},
		{"auth empty", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":""}`},
		{"auth wrong", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"WRONG"}`},
		{"auth valid", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"` + testToken + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			if got := dispatchRaw(t, s, tc.frame); got != "" {
				t.Errorf("shutdown reply = %s, want silence (the daemon just stops)", got)
			}
			select {
			case <-s.shutdown:
			default:
				t.Error("server.shutdown did not signal the shutdown channel")
			}
		})
	}
}

// The exemption covers AUTH ONLY. A shutdown frame with a bad or absent jsonrpc
// version is still rejected -32600 and the daemon stays up — measured against
// the reference, which answers exactly that and keeps running.
func TestShutdownStillRequiresTheJSONRPCVersion(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"version 1.0", `{"jsonrpc":"1.0","id":1,"method":"server.shutdown"}`},
		{"version absent", `{"id":1,"method":"server.shutdown"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			got := dispatchRaw(t, s, tc.frame)
			if !strings.Contains(got, "-32600") || !strings.Contains(got, "Invalid JSON-RPC version") {
				t.Errorf("reply = %s, want -32600 Invalid JSON-RPC version", got)
			}
			select {
			case <-s.shutdown:
				t.Error("the daemon shut down on a bad-version frame; the version gate must still apply")
			default:
			}
		})
	}
}

// server.shutdown is the ONLY exemption. Every other method must still reject an
// unauthenticated request with -32001 — swept against both daemons, and all 18
// were identical. A regression that widened the exemption would be a real hole,
// so pin the whole surface rather than a sample.
func TestEveryOtherMethodStillRequiresAuth(t *testing.T) {
	methods := []string{
		"server.ping", "server.version", "server.capabilities",
		"files.list", "files.validate", "files.stat", "files.read", "files.extract_tar",
		"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove",
		"process.spawn", "process.stdin", "process.kill", "process.killAndWait", "process.reattach",
	}
	s := newTestServer(t)
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"`+m+`","params":{}}`)
			if !strings.Contains(got, "-32001") {
				t.Errorf("%s without auth = %s, want -32001 Unauthorized", m, got)
			}
		})
	}
}
