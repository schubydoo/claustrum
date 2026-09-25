package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// Reference build 90fca6e6 made process.reattach close the connection it replaced.
//
// Before, a reattach only transferred the frame stream: the previously attached
// connection stopped receiving frames but stayed open, so a client that had lost
// the session had no way to tell its old connection was finished. The reference now
// looks up the writer currently attached to that process, and when it is a
// different connection it supersedes it: the connection is closed, once, with a
// logged reason naming the process.
//
// This is the change behind the Desktop note about messages around a disconnect
// being dropped or answered with a prompt to send them again. It is observable: the
// superseded client sees its connection close.
//
// The reason text is claustrum's own phrasing, not the reference's verbatim string,
// which is the same choice the host cleaner made for its log reasons. The
// observable being pinned here is the close, not the wording.

// activityPipeConn builds a conn whose net.Conn is wrapped the way the accept loop
// wraps it, so a test can also run the idle watcher against it.
//
// net.Pipe is synchronous and unbuffered, so the peer end needs a reader or the
// first write blocks forever. The reader discards: these tests assert on whether a
// write is accepted at all, not on what it carries.
func activityPipeConn(t *testing.T) (c *conn, ac *activityConn) {
	t.Helper()
	client, server := net.Pipe()
	ac = newActivityConn(client)
	// ac is set on BOTH fields, exactly as the accept loop does it. A conn built
	// without it has no deliberate-close flag and supersede skips it, so a helper
	// that left it nil would make these tests pass for the wrong reason.
	c = &conn{nc: ac, ac: ac, done: make(chan struct{})}
	go func() { _, _ = io.Copy(io.Discard, server) }()
	t.Cleanup(func() { client.Close(); server.Close() })
	return c, ac
}

// TestReattachSupersedesThePreviousConnection pins the close and the log line.
func TestReattachSupersedesThePreviousConnection(t *testing.T) {
	m := newTestProcManager(t)

	old, _ := activityPipeConn(t)
	exe, env := helperCommand(t, "sleep")
	if _, err := m.spawn(old, "sup", exe, []string{"30"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// The control that must keep passing: while nothing has superseded it, the old
	// connection accepts a write. Without this arm a mutant that closes every
	// connection at spawn would look correct.
	if err := old.writeLine([]byte("{}\n")); err != nil {
		t.Fatalf("the attached connection refused a write before any reattach: %v", err)
	}

	fresh, _ := activityPipeConn(t)
	out := captureLog(t, func() {
		if _, found, _, _, _, _ := m.reattach(fresh, "sup", 0); !found {
			t.Fatal("reattach did not find the live process")
		}
	})

	// The observable. Today claustrum leaves the old connection open.
	if err := old.writeLine([]byte("{}\n")); err == nil {
		t.Error("the superseded connection still accepts writes; the reference closes it")
	}
	// The reattaching connection must not be caught by its own supersede.
	if err := fresh.writeLine([]byte("{}\n")); err != nil {
		t.Errorf("the reattaching connection was closed too: %v", err)
	}
	if !strings.Contains(out, "sup") || !strings.Contains(out, "reattached from another connection") {
		t.Errorf("supersede log = %q, want a line naming the process and the reason", out)
	}
}

// TestReattachOnTheSameConnectionDoesNotSupersede pins the identity test. A client
// that reattaches on the connection it is already attached to must not have that
// connection closed underneath it.
func TestReattachOnTheSameConnectionDoesNotSupersede(t *testing.T) {
	m := newTestProcManager(t)

	c, _ := activityPipeConn(t)
	exe, env := helperCommand(t, "sleep")
	if _, err := m.spawn(c, "same", exe, []string{"30"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, found, _, _, _, _ := m.reattach(c, "same", 0); !found {
		t.Fatal("reattach did not find the live process")
	}
	if err := c.writeLine([]byte("{}\n")); err != nil {
		t.Errorf("reattaching on the same connection closed it: %v", err)
	}
}

// TestSupersededConnectionIsNotClosedTwice pins the shared deliberate-close flag.
// The reference sets the flag inside the supersede, and its idle watcher now claims
// that same flag before it logs and closes. So a connection the supersede already
// finished produces no second close and no second log line.
func TestSupersededConnectionIsNotClosedTwice(t *testing.T) {
	m := newTestProcManager(t)

	old, ac := activityPipeConn(t)
	exe, env := helperCommand(t, "sleep")
	if _, err := m.spawn(old, "twice", exe, []string{"30"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	fresh, _ := activityPipeConn(t)
	if _, found, _, _, _, _ := m.reattach(fresh, "twice", 0); !found {
		t.Fatal("reattach did not find the live process")
	}

	// Run the idle watcher against the connection the supersede already closed,
	// with a timeout short enough that it fires at once.
	s := &server{shutdown: make(chan struct{}), idleTimeout: time.Millisecond}
	done := make(chan struct{})
	out := captureLog(t, func() {
		go func() { s.closeWhenIdle(ac, done); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the idle watcher did not return")
		}
	})
	if strings.Contains(out, "idle") {
		t.Errorf("the idle watcher logged a second close for a superseded connection: %q", out)
	}
}

// TestSupersedeRefusesAConnWithNoActivityWrapper covers the guard that a
// production conn can never hit, and that a test conn silently hits all the time.
//
// A nil ac means there is no deliberate-close flag to claim, so supersede cannot
// do its job. It says so rather than returning quietly, because a quiet return is
// exactly how the socket harness ran every supersede assertion against a daemon
// shape production never has.
func TestSupersedeRefusesAConnWithNoActivityWrapper(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() { _, _ = io.Copy(io.Discard, server) }()
	c := &conn{nc: client} // no ac, the shape only a test builds

	out := captureLog(t, func() { c.supersede("whatever") })

	if !strings.Contains(out, "no activity wrapper") {
		t.Errorf("supersede log = %q, want a warning about the missing wrapper", out)
	}
	// It must not have closed the connection or marked the writer closed: with no
	// flag there is nothing to coordinate with, so a silent close would be worse.
	if err := c.writeLine([]byte("{}\n")); err != nil {
		t.Errorf("supersede closed a conn it refused to supersede: %v", err)
	}
}

// TestSupersedeLosingTheClaimStillMarksTheWriterClosed covers the arm where the
// idle watcher got there first.
//
// supersede returns without logging or closing, since the watcher owns both. It
// still marks the writer closed, because the watcher cannot: it holds the
// activityConn, not the conn. Without that, a later write would slip past the
// closed check and record a frame into a -wire-log capture that never reached the
// wire.
func TestSupersedeLosingTheClaimStillMarksTheWriterClosed(t *testing.T) {
	c, ac := activityPipeConn(t)

	if !ac.claimClose() {
		t.Fatal("a fresh connection should have its close claim available")
	}
	out := captureLog(t, func() { c.supersede("process X reattached from another connection") })

	if strings.Contains(out, "closing connection") {
		t.Errorf("supersede logged a close it did not own: %q", out)
	}
	if err := c.writeLine([]byte("{}\n")); err == nil {
		t.Error("the writer is still open after a lost claim; a later write would be recorded but never sent")
	}
}
