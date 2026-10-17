//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// livePredecessorPID reports the pid of a daemon that answers on the socket, and 0
// when no connect works. The listener here is this test process, so its pid is ours.
func TestLivePredecessorPID(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "rpc.sock")

	if pid := livePredecessorPID(sock); pid != 0 {
		t.Errorf("no socket: pid %d, want 0", pid)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if pid := livePredecessorPID(sock); pid != os.Getpid() {
		t.Errorf("a live listener of this process: pid %d, want %d", pid, os.Getpid())
	}

	// Closing the UnixListener unlinks the socket file, so no predecessor is left.
	_ = ln.Close()
	if pid := livePredecessorPID(sock); pid != 0 {
		t.Errorf("a closed socket: pid %d, want 0", pid)
	}

	// The socket file of a killed daemon stays on disk, and a connect is refused.
	staleSocketFile(t, sock)
	if pid := livePredecessorPID(sock); pid != 0 {
		t.Errorf("a stale socket file: pid %d, want 0", pid)
	}
}

// connPeerPID is 0 for a connection that is not a unix socket and for one whose
// peer pid cannot be read.
func TestConnPeerPIDUnknownIsZero(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if pid := connPeerPID(a); pid != 0 {
		t.Errorf("a pipe connection: pid %d, want 0", pid)
	}
	if pid := connPeerPID(&net.UnixConn{}); pid != 0 {
		t.Errorf("a unix connection with no socket: pid %d, want 0", pid)
	}
}
