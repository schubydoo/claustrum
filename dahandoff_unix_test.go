//go:build unix

package main

import (
	"net"
	"path/filepath"
	"testing"
)

// livePredecessorIdent reports a live daemon on the socket (returning its inode) and
// nil when there is none — the launcher uses it to tell a predecessor's socket from
// the successor's during a handoff.
func TestLivePredecessorIdent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rpc.sock")

	if livePredecessorIdent(sock) != nil {
		t.Error("no socket present should report no predecessor")
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi := livePredecessorIdent(sock); fi == nil {
		t.Error("a live listening socket should report a predecessor")
	}

	// Closing the UnixListener unlinks the socket file, so there is no live
	// predecessor afterward.
	_ = ln.Close()
	if livePredecessorIdent(sock) != nil {
		t.Error("a closed socket should report no predecessor")
	}
}
