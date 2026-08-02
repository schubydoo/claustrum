//go:build unix

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// -stop removes the socket PATH unconditionally, on every path out of runStop.
//
// Measured 2026-08-02 against 5db5e4a, three arms:
//
//	live daemon (control)   both: socket gone — the DAEMON unlinks it on
//	                        graceful shutdown, so this arm attributes nothing
//	stale socket, no        reference: gone      claustrum (before): present
//	  listener
//	live FOREIGN listener   reference: gone, listener still ALIVE
//	                        claustrum (before): present, listener alive
//
// The happy path is blind here, which is the whole reason this test exists: a
// live daemon unlinks its own socket during shutdown, so "socket gone" after
// -stop proves nothing about who removed it. Only the two arms where NO
// claustrum daemon is involved can attribute the unlink to -stop itself.
//
// unix-only on purpose: both arms turn on POSIX AF_UNIX semantics — a bound
// socket path is an ordinary directory entry that can be unlinked out from
// under a live listener. Windows does not promise that, and asserting it there
// would test the platform rather than runStop.

// stopLeavesNoSocket is the shared assertion: after runStop returns, the path
// must be gone.
func stopLeavesNoSocket(t *testing.T, sock string) {
	t.Helper()
	if err := runStop(sock); err != nil {
		t.Fatalf("runStop = %v, want nil (best-effort)", err)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("socket %s still present after -stop (Lstat err = %v); the unlink is not unconditional", sock, err)
	}
}

// Arm 1 — a stale socket file with nothing listening. runStop's dial fails and
// it returns early; the unlink must still happen. This is the arm that proves
// the unlink is -stop's own act: no daemon ever ran, so nothing else could have
// removed the path.
func TestRunStopUnlinksStaleSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "rpc.sock")

	// Bind then close, which leaves the path behind exactly as a killed daemon
	// would. Writing a plain file would test os.Remove, not the stale-socket
	// case the reference was measured on.
	//
	// SetUnlinkOnClose(false) is required: Go's UnixListener unlinks a path it
	// created when it closes, so a plain Close would clean up the very thing
	// this arm needs staged and the test would silently have nothing to assert.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("net.Listen(unix) returned %T, want *net.UnixListener", ln)
	}
	ul.SetUnlinkOnClose(false)
	if err := ul.Close(); err != nil {
		t.Fatal(err)
	}
	// Fail rather than skip: if the stale socket cannot be staged, this arm
	// proves nothing and must say so loudly.
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("could not stage a stale socket at %s: %v", sock, err)
	}

	stopLeavesNoSocket(t, sock)
}

// Arm 2 — a live listener that is NOT a claustrum daemon. The path must be
// removed and the listener must SURVIVE, which is the destructive half of this
// behaviour stated plainly: -stop unlinks a path it did not create and cannot
// identify the owner of. The listener is not torn down; the path it was
// reachable through is, so anyone dialing by path can no longer find it.
//
// Asserting the listener stays alive is what stops an over-correction: a "fix"
// that tore the listener down, or that closed it before unlinking, would pass
// the path check alone and fail here.
func TestRunStopUnlinksForeignListenerPathAndLeavesItAlive(t *testing.T) {
	// The foreign listener accepts and never replies, so runStop also has to sit
	// out its reply deadline. Shrink it so the test does not pay the full 2s.
	old := stopReplyTimeout
	stopReplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopReplyTimeout = old })

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()
	t.Cleanup(func() {
		select {
		case c := <-accepted:
			_ = c.Close()
		default:
		}
	})

	stopLeavesNoSocket(t, sock)

	select {
	case c := <-accepted:
		// Close it here rather than leaving it to the cleanup: the cleanup's
		// non-blocking drain finds the channel empty once this case has taken
		// the conn, so nothing else would ever close it.
		_ = c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("the foreign listener never accepted -stop's connection")
	}

	// Now prove the listener SURVIVED, which is the over-correction guard.
	//
	// Neither obvious check does that. The accept above happened BEFORE the
	// unlink, so it says nothing about the listener's state after it; and a fresh
	// net.Listen on the same path succeeds purely because the path is free —
	// equally true if runStop had torn the old listener down.
	//
	// The discriminator is what Accept does on a CLOSED listener: it returns
	// net.ErrClosed immediately, where a live one blocks with nothing dialing it.
	// So a short deadline separates the two — a timeout means alive.
	if ul, ok := ln.(*net.UnixListener); ok {
		if err := ul.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatalf("SetDeadline: %v", err)
		}
		_, err := ul.Accept()
		if errors.Is(err, net.ErrClosed) {
			t.Error("the foreign listener was closed by -stop; it must only lose its " +
				"path, keeping its bound descriptor")
		} else if !isTimeout(err) {
			t.Errorf("Accept after the unlink = %v, want a timeout (proving the "+
				"listener is still bound and waiting)", err)
		}
	}
}

// isTimeout reports whether err is a deadline expiry, which is the "listener is
// alive and idle" signal above.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
