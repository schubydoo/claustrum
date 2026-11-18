package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// dropConns marks each free connection closed before it closes the socket. A
// write after the drop then stops at the closed check and records nothing in
// the -wire-log capture. Without the mark the write records a frame that never
// reaches the wire, because the record comes before the socket write.
func TestDropConnsMarksConnClosedForWireLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	wl, err := wirelog.Open(wirelog.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wl.Close)

	srv, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go func() { _, _ = io.Copy(io.Discard, client) }()

	s := newTestServer(t)
	c := conn.New(srv, nil, 1, wl, nil, nil)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	// A write before the drop records one frame, so the capture works.
	if err := c.WriteLine([]byte(`{"jsonrpc":"2.0","method":"before"}` + "\n")); err != nil {
		t.Fatalf("write before the drop: %v", err)
	}
	if got := capturedFrames(t, path); got != 1 {
		t.Fatalf("capture holds %d frames before the drop, want 1", got)
	}

	s.dropConns()

	if err := c.WriteLine([]byte(`{"jsonrpc":"2.0","method":"after"}` + "\n")); err == nil {
		t.Error("WriteLine after the drop returned no error")
	}
	if failed := c.WriteResponse(map[string]int{"after": 1}); failed {
		t.Error("WriteResponse after the drop reported a failed socket write, want the closed check to stop it")
	}
	if got := capturedFrames(t, path); got != 1 {
		t.Errorf("capture holds %d frames after the drop, want 1: a write after the drop was recorded", got)
	}
}

// capturedFrames counts the records in the capture file at path.
func capturedFrames(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return bytes.Count(b, []byte("\n"))
}
