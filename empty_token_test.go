package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty -token-file must be fatal. Otherwise the daemon comes up healthy and
// listening while nothing can ever authenticate to it: every request answers
// -32001 forever and the operator sees a running service.
//
// Measured at 5db5e4a with a zero-byte -token-file: the reference refuses to
// start (no socket; its launcher reports "timeout waiting for daemon to accept"
// and exits 1), where claustrum served a permanently unauthenticatable socket.
//
// There is no auth BYPASS either way — a request carrying no auth against an
// empty token is still rejected, measured directly — so this is a
// dead-on-arrival daemon rather than an open one.
func TestChildTokenRejectsEmptyTokenFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, content string }{
		{"zero bytes", ""},
		{"newline only", "\n"},
		{"crlf only", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tf := filepath.Join(dir, "tok-"+strings.ReplaceAll(tc.name, " ", "_"))
			if err := os.WriteFile(tf, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := childToken(tf)
			if err == nil {
				t.Fatal("childToken accepted an empty token, want an error")
			}
			if !strings.Contains(err.Error(), "token is empty") {
				t.Errorf("error = %q, want it to mention an empty token", err)
			}
		})
	}

	// Deliberately NOT covered: a whitespace-only token. normalizeToken trims
	// only "\r\n", so "   " survives as a real (if silly) token, and the
	// reference's handling of that case was never measured — asserting either way
	// would be a guess.

	// A real token still works, and the file is consumed.
	tf := filepath.Join(dir, "good")
	if err := os.WriteFile(tf, []byte("real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := childToken(tf)
	if err != nil || got != "real-token" {
		t.Fatalf("childToken(valid) = %q, %v; want \"real-token\", nil", got, err)
	}
	if _, err := os.Stat(tf); err == nil {
		t.Error("the token file should have been unlinked")
	}
}
