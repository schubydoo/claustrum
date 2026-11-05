//go:build windows

package main

// shutdownReplyAwaitsDrop makes the server.shutdown reply wait for the start of the
// connection drop (awaitDropStart). b3081b51 answers in 0 of 60 plain runs, 17 of 60
// after a ping and 2 of 60 with a second idle connection (measured on Windows).
const shutdownReplyAwaitsDrop = true
