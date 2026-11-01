//go:build linux || darwin

package main

import "testing"

// A long socket that no shorter name reaches reads as unknown: Linux with no usable
// /proc, and macOS (claustrum's own, not measured).
func TestProbeSocketAtALongPathWithNoShorterName(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "run/00000002/rpc.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // a listener bound by another name keeps its file
	if state, _, _ := probeSocket(sock); state != hcSockUnknown {
		t.Errorf("dead long socket with no shorter name: state %d, want %d", state, hcSockUnknown)
	}
}
