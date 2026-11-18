//go:build !windows

package main

// shutdownReplyAwaitsDrop is false on Linux and macOS. The server.shutdown reply then
// waits only for the start of the teardown (startTeardown), and its write races the
// close of the listener and the connections. The rate depends on timing. On Linux
// this build still replies first far more often than 4c111e86. On macOS both reply
// first in most runs (measured).
const shutdownReplyAwaitsDrop = false
