package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// runBridge is a dumb stdio<->unix-socket relay. It does NOT inject auth; the
// stream it relays must already carry "auth" per request. This is what an SSH
// session attaches to. A dial failure is a hard error (wrapped "dial server:",
// matching the reference). -stop instead reports a failed dial as a word.
func runBridge(socket string) error {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer nc.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(nc, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, nc); done <- struct{}{} }()
	<-done
	return nil
}

// The -stop stdout words. -stop prints exactly one of them, then a newline, and
// exits 0 in every state that these words name. Measured against f6010b97 and
// 90fca6e6 on a Linux VM.
const (
	stopWordStopped    = "stopped"    // the connect succeeded
	stopWordNone       = "none"       // the connect failed and daemon.lock is free or missing. claustrum's own rule: also when the lock cannot be opened or locked
	stopWordTerminated = "terminated" // the lock holder exited after SIGTERM
	stopWordKilled     = "killed"     // the lock holder ignored SIGTERM and got SIGKILL
	// stopWordSurvivor: the lock holder is a live process that -stop does not
	// signal, or the lock is still held 1.5 s after SIGKILL. claustrum also prints
	// it if the holder check fails again before SIGKILL. It prints it too when a
	// signal cannot reach a holder that is still there.
	stopWordSurvivor = "survivor"
)

// stopReplyTimeout bounds how long -stop waits for the daemon's reply to its
// shutdown request. 2s, measured against the reference. var so tests can shrink it.
var stopReplyTimeout = 2 * time.Second

// runStop sends an unauthenticated server.shutdown RPC to a running daemon and
// prints one outcome word on stdout. ⚠️ this stops the daemon and drops its
// sessions. In every state that the stopWord constants name, it prints a word
// and main exits 0.
func runStop(socket string, stdout io.Writer) {
	_, _ = fmt.Fprintln(stdout, stopDaemon(socket))
}

// stopDaemon does the work of -stop and returns the word to print.
//
// Measured against f6010b97 and 90fca6e6 on a Linux VM:
//
//   - If the connect succeeds, -stop sends the request, does one read with a 2 s
//     deadline and prints "stopped". A reply, garbage, an EOF, a reset and the
//     deadline all give "stopped". -stop does not unlink the socket path on this
//     path. A live daemon removes its own socket when it stops.
//   - If the connect fails, -stop checks daemon.lock (stopRunDirHolder). Then it
//     removes the socket path and daemon.token beside it, and prints the word
//     from the lock check.
//   - If the word is "survivor", -stop removes neither file. This is measured
//     for a holder that -stop does not signal and for a lock that outlives
//     SIGKILL. The other survivor arms follow the same rule.
//
// The unlink on a failed connect uses os.Remove. It removes a stale socket, a
// socket that refuses the connect with EACCES, a regular file and an empty
// directory. It keeps a non-empty directory and a path in a directory that
// this user cannot write. The references do the same (measured).
func stopDaemon(socket string) string {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		word := stopRunDirHolder(socket)
		if word != stopWordSurvivor {
			_ = os.Remove(socket)
			_ = os.Remove(filepath.Join(persistTokenDir(socket), persistedTokenName))
		}
		return word
	}
	defer nc.Close()
	// No auth member. The reference's -stop sends exactly this frame, and its
	// daemon does not authenticate server.shutdown. Captured off a fake listener
	// with CLAUDE_RPC_TOKEN deliberately SET, so the omission is known to be real
	// and not just an unset variable:
	//
	//	reference : {"jsonrpc":"2.0","id":1,"method":"server.shutdown"}
	//	claustrum : {"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"…"}
	//
	// CLAUDE_RPC_TOKEN is no longer read here at all — -stop needs no credential,
	// which is what lets `server --stop --socket <sock>` work from a bare SSH
	// command line the way the Desktop client invokes it.
	_, _ = fmt.Fprint(nc, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`+"\n")
	// Bound the wait for the shutdown reply. A daemon that accepts the connection
	// and then never answers — wedged, or a stale socket now owned by something
	// else — would otherwise hang -stop forever, since a bare Read has no
	// deadline. Measured at 5db5e4a: against a socket that accepts and never
	// replies, the reference returns in 2.030s over three runs. claustrum was still
	// blocked when killed at 45s.
	_ = nc.SetReadDeadline(time.Now().Add(stopReplyTimeout))
	// One read, and its result does not matter. The reply frame is never
	// relayed to stdout: -stop is a control command, not a relay.
	buf := make([]byte, 4096)
	_, _ = nc.Read(buf)
	return stopWordStopped
}
