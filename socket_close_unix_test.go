//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestCloseAllKeepsSuccessorSocket: a daemon that shuts down after a new daemon bound
// the socket path leaves the socket of the new daemon in place and reachable.
func TestCloseAllKeepsSuccessorSocket(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	// The new daemon clears the path and binds it, as newServerOnSocket does.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	succ, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = succ.Close() })

	s.signalShutdown()
	s.closeAll(sock)

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("the socket of the new daemon is not reachable after the old daemon closed: %v", err)
	}
	_ = c.Close()
}
