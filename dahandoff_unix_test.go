//go:build unix

package main

import (
	"net"
	"os"
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

// A socket FILE left behind by a crashed daemon (path present, nobody accepting)
// dials ECONNREFUSED: that is a stale socket, so there is no predecessor to wait
// for. Any other dial failure — here EACCES on an unwritable socket file — is
// treated conservatively as "possibly live" and reports the inode.
func TestLivePredecessorIdentStaleAndUnclassifiedSocket(t *testing.T) {
	// Short directory, not t.TempDir(): this test's name pushes the socket path past
	// macOS's 104-byte sun_path limit (see harness_test.go for the same pattern).
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket file should survive Close with unlink disabled: %v", err)
	}
	if livePredecessorIdent(sock) != nil {
		t.Error("a stale socket file (connection refused) should report no predecessor")
	}

	skipIfRoot(t)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sock, 0o600) })
	if livePredecessorIdent(sock) == nil {
		t.Error("a permission-denied dial is not proof of death; must report the inode")
	}
}
