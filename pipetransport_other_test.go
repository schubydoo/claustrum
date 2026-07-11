//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHonorListenPipeNonWindows: the named-pipe transport is Windows-only, so on
// every other platform the flag is forced OFF (with a warning) rather than
// failing — mirroring honorKeepChildren's Windows no-op.
func TestHonorListenPipeNonWindows(t *testing.T) {
	if honorListenPipe(true) {
		t.Error("honorListenPipe(true) = true, want false off Windows")
	}
	if honorListenPipe(false) {
		t.Error("honorListenPipe(false) = true, want false")
	}
}

// TestStartPipeTransportNonWindows: the stub exists only so server.go compiles
// everywhere; it must return an error (it is never reached at runtime because
// honorListenPipe forces the flag false first).
func TestStartPipeTransportNonWindows(t *testing.T) {
	ln, err := startPipeTransport("/tmp/whatever/rpc.sock")
	if err == nil {
		t.Fatal("startPipeTransport off Windows should error")
	}
	if ln != nil {
		t.Fatal("startPipeTransport off Windows returned a non-nil listener")
	}
}

// TestEnablePipeNonWindows covers the enablePipe seam off Windows: the false guard
// is a no-op, and the requested path hits startPipeTransport's error arm (which
// errors everywhere but Windows), logs, and leaves pipeLn nil — the socket still
// serves. The success arm (a live pipe listener stored on s) is Windows-only and
// exercised by the windows-latest integration test.
func TestEnablePipeNonWindows(t *testing.T) {
	s := &server{}
	s.enablePipe(filepath.Join(t.TempDir(), "rpc.sock"), false)
	if s.pipeLn != nil {
		t.Fatal("enablePipe with listenPipe=false should leave pipeLn nil")
	}
	s.enablePipe(filepath.Join(t.TempDir(), "rpc.sock"), true)
	if s.pipeLn != nil {
		t.Fatal("enablePipe off Windows (startPipeTransport errors) should leave pipeLn nil")
	}
}

// TestEnablePipeClearsStaleFileOnError: off Windows startPipeTransport always
// errors, exercising enablePipe's error arm — which must also clear a stale
// rpc.pipe so a client can't dial a dead pipe after a failed enable.
func TestEnablePipeClearsStaleFileOnError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	stale := pipeNameFilePath(sock)
	if err := os.WriteFile(stale, []byte(`\\.\pipe\claustrum-dead`), 0o600); err != nil {
		t.Fatalf("seed stale rpc.pipe: %v", err)
	}
	s := &server{}
	s.enablePipe(sock, true) // startPipeTransport errors off Windows → error arm
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale rpc.pipe not removed on enable failure: err=%v", err)
	}
	if s.pipeLn != nil {
		t.Fatal("failed enablePipe must leave pipeLn nil")
	}
}
