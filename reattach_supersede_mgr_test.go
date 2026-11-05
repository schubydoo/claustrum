package main

import (
	"strings"
	"testing"
)

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
	if err := old.WriteLine([]byte("{}\n")); err != nil {
		t.Fatalf("the attached connection refused a write before any reattach: %v", err)
	}

	fresh, _ := activityPipeConn(t)
	out := captureLog(t, func() {
		if _, found, _, _, _, _ := m.reattach(fresh, "sup", 0); !found {
			t.Fatal("reattach did not find the live process")
		}
	})

	// The observable. Today claustrum leaves the old connection open.
	if err := old.WriteLine([]byte("{}\n")); err == nil {
		t.Error("the superseded connection still accepts writes; the reference closes it")
	}
	// The reattaching connection must not be caught by its own supersede.
	if err := fresh.WriteLine([]byte("{}\n")); err != nil {
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
	if err := c.WriteLine([]byte("{}\n")); err != nil {
		t.Errorf("reattaching on the same connection closed it: %v", err)
	}
}
