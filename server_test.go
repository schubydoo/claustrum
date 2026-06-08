package main

import "testing"

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
