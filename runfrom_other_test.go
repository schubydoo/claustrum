//go:build !linux

package main

import (
	"path/filepath"
	"testing"
)

// TestCheckPlaceHost pins the host refusal off Linux, with the cleaned path.
func TestCheckPlaceHost(t *testing.T) {
	p := filepath.Join(t.TempDir(), "srv", "v", "server")
	r := examineRunFrom(filepath.ToSlash(p), "v")
	want := "claustrum: refusing to run (host) from " + p + ": Only a Linux host's file can be checked.\n"
	if r == nil || r.line() != want {
		t.Fatalf("got %+v, want %q", r, want)
	}
}
