//go:build windows

package main

import "testing"

// On Windows the server.shutdown reply waits for the start of the connection drop,
// as b3081b51 mostly closes with no reply (measured on Windows).
func TestShutdownReplyWaitsForTheDropOnWindows(t *testing.T) {
	checkShutdownReplyWaitsForTheDrop(t)
}

// With no drop, the reply goes out once the bound passes.
func TestShutdownReplyWaitEndsAtItsBoundOnWindows(t *testing.T) {
	checkShutdownReplyWaitEndsAtItsBound(t)
}
