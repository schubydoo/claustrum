package main

import (
	"testing"
	"time"
)

// The production idle limit is 30 days.
func TestIdleExitProductionLimit(t *testing.T) {
	if idleExitAfter != 720*time.Hour {
		t.Errorf("idleExitAfter = %s, want 720h0m0s", idleExitAfter)
	}
}

// Each request past the auth step restarts the idle time, except server.capabilities.
// A parse error and an auth failure do not restart it either.
func TestGateCountsRequestsForIdleExit(t *testing.T) {
	_ = captureLogBuf(t)
	s := &server{token: "tok"}
	for _, c := range []struct {
		name, raw string
		counts    bool
	}{
		{"server.ping", `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"tok"}`, true},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"server.nope","auth":"tok"}`, true},
		{"server.shutdown with a wrong token", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"bad"}`, true},
		{"server.capabilities", `{"jsonrpc":"2.0","id":1,"method":"server.capabilities","auth":"tok"}`, false},
		{"parse error", `{"jsonrpc":`, false},
		{"auth failure", `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"bad"}`, false},
	} {
		s.lastCounted.Store(-1)
		_, _ = s.gate([]byte(c.raw))
		if got := s.lastCounted.Load() != -1; got != c.counts {
			t.Errorf("%s: restarts the idle time = %v, want %v", c.name, got, c.counts)
		}
	}
}
