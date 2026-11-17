//go:build !windows

package main

// shutdownReplyAwaitsDrop is false on Linux and macOS. The server.shutdown reply then
// waits only for the start of the teardown (startTeardown), and its write races the
// close of the listener and the connections. 4c111e86 replies in 14, 10 and 15 of 200
// runs (plain, after a ping, with an idle connection, measured on Linux). On macOS it
// replies in 67, 82 and 83 of 100 (measured).
const shutdownReplyAwaitsDrop = false
