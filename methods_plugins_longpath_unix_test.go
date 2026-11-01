//go:build unix

package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

// longSibling binds and closes a sibling socket <X>/run/beef5678/rpc.sock of 120 bytes
// and returns X and the socket path. The socket file stays after the close.
func longSibling(t *testing.T) (base, sock string) {
	t.Helper()
	sock = longSocketPath(t, 120, "run/beef5678/rpc.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	return filepath.Dir(filepath.Dir(filepath.Dir(sock))), sock
}

// A long sibling that no shorter name reaches cannot be ruled out. The text names the
// limit of the system: 107 bytes on Linux with no usable /proc, 103 bytes on macOS
// (`be1c1ca5`, measured on Linux and macOS).
func TestSiblingAtALongPathWithNoShorterName(t *testing.T) {
	noFolderHandle(t)
	base, sock := longSibling(t)
	want := fmt.Sprintf("cannot rule out run/beef5678/rpc.sock: dial unix %s: the socket path is longer than the %d bytes that this system allows", sock, maxSocketPath)
	if got := siblingDaemonAlive(base, "abcd1234"); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
