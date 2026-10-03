package main

import (
	"testing"
	"time"
)

// normalizeToken must match the reference daemon's token-file handling exactly
// (pinned by scratch/probe/contract_probe.sh): a single trailing newline / CRLF
// is stripped, but spaces and leading whitespace are preserved. A mismatch here
// silently breaks auth for every request when the uploaded token file ends in a
// newline.
func TestNormalizeToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"raw", "TKNabc123", "TKNabc123"},
		{"trailing-lf", "TKNabc123\n", "TKNabc123"},
		{"trailing-crlf", "TKNabc123\r\n", "TKNabc123"},
		{"trailing-spaces-kept", "TKNabc123  ", "TKNabc123  "},
		{"surrounding-ws-kept", "  TKNabc123  ", "  TKNabc123  "},
		{"interior-space-kept", "TKN abc\n", "TKN abc"},
		{"only-newline", "\n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := normalizeToken([]byte(c.in)); got != c.want {
			t.Errorf("%s: normalizeToken(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestLauncherBoundIs12Seconds pins the value of the launcher bound. 89cb6289 gives up
// after 12.06 s when the daemon child ends before its bind (Linux row P7, three forms).
// The macOS rows P7a to P7c give 12.08 to 12.16 s. The two Windows rows of a zero-byte
// -token-file give 12.058 s. The bound is shared code, so the test has no build tag.
func TestLauncherBoundIs12Seconds(t *testing.T) {
	if daemonStartTimeout != 12*time.Second {
		t.Errorf("daemonStartTimeout = %v, want 12s", daemonStartTimeout)
	}
}
