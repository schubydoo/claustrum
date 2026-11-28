//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSpawnRefusalReasonBusyLinux pins the reason busy for a program file that a
// writer holds open (87375cc7 and 13047a51, measured on Linux for an ELF file and a
// script). The test holds a script open for a write, so execve fails with ETXTBSY.
func TestSpawnRefusalReasonBusyLinux(t *testing.T) {
	scr := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(scr, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(scr, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	want := errJSON(t, -32603, "fork/exec "+scr+": text file busy", "busy")
	if got := spawnErrorOf(t, map[string]any{"id": "a", "command": scr}); got != want {
		t.Fatalf("error = %s\nwant    %s", got, want)
	}
}
