//go:build !windows

package main

// shutdownReplyAwaitsDrop is false on Linux and macOS. The server.shutdown reply then
// waits only for the start of the teardown (startTeardown), and its write races the
// close of the listener and the connections. The rate depends on timing. 4c111e86
// replies first in 2 to 17 % of runs on Linux and 60 to 83 % on macOS. This build
// replies first in 50 to 72 % and 87 to 95 % (measured).
const shutdownReplyAwaitsDrop = false
