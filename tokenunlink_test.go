package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests run on linux, macOS and Windows. The other tests that see the token
// unlink at the stop are unix-only for reasons beside the token: a seam of the
// group kill, a long socket path, or the EOF shape of a socket.

// ownTokenFile boots a daemon with bootLineServer and returns its daemon.token path.
// It fails the test when the start did not write the file.
func ownTokenFile(t *testing.T) (*server, string, string) {
	t.Helper()
	s, sock := bootLineServer(t, false)
	token := filepath.Join(tokenFileDir(sock), persistedTokenName)
	if s.tokenInfo == nil {
		t.Fatal("the start kept no identity of daemon.token")
	}
	if _, err := os.Stat(token); err != nil {
		t.Fatalf("daemon.token after the start: %v, want present", err)
	}
	return s, sock, token
}

// TestShutdownRemovesOwnTokenFile: server.shutdown, as -stop sends it, and the
// teardown remove the daemon.token that this daemon wrote.
func TestShutdownRemovesOwnTokenFile(t *testing.T) {
	s, sock, token := ownTokenFile(t)
	dial(t, sock).send(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown","params":{}}`)
	select {
	case <-s.shutdown:
	case <-time.After(5 * time.Second):
		t.Fatal("server.shutdown did not request the shutdown")
	}
	s.closeAll(sock)
	if _, err := os.Lstat(token); !os.IsNotExist(err) {
		t.Errorf("daemon.token after the stop: Lstat err = %v, want not exist", err)
	}
}

// TestShutdownKeepsSuccessorTokenFile: a successor wrote its own daemon.token over the
// file of this daemon before the stop. The teardown keeps the file of the successor.
func TestShutdownKeepsSuccessorTokenFile(t *testing.T) {
	s, sock, token := ownTokenFile(t)
	// The successor writes its file the way every start does: a temp file and a rename.
	if saveTokenFile(sock, "successor-tok") == nil {
		t.Fatal("the successor wrote no daemon.token")
	}
	s.signalShutdown()
	s.closeAll(sock)
	got, err := os.ReadFile(token)
	if err != nil {
		t.Fatalf("daemon.token of the successor after the stop: %v, want kept", err)
	}
	if string(got) != "successor-tok" {
		t.Errorf("daemon.token after the stop = %q, want the token of the successor", got)
	}
}
