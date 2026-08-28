//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// On Windows claustrum does no predecessor probe — livePredecessorIdent is a nil stub,
// since the probe has no observable effect there (matching the reference). Assert it
// returns nil even with a LIVE listener on the socket (where the unix implementation
// returns the socket's identity). Guards the build-tag split.
func TestLivePredecessorIdentNilOnWindows(t *testing.T) {
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	l, err := net.Listen("unix", sock) // a live daemon would be accepting here
	if err != nil {
		t.Fatalf("listen(unix): %v", err)
	}
	defer l.Close()
	if fi := livePredecessorIdent(sock); fi != nil {
		t.Errorf("livePredecessorIdent on Windows = %v, want nil (stub; the probe is a no-op there)", fi)
	}
}
