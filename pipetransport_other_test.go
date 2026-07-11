//go:build !windows

package main

import "testing"

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
