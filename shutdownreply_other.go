//go:build !windows

package main

// shutdownReplyAwaitsDrop is false on Linux and macOS: the server.shutdown reply is
// written as soon as the handler returns. A test turns it on through replyRule.
const shutdownReplyAwaitsDrop = false
