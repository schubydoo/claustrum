//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin cells T3 (Linux and macOS VMs), T3M and T3C (Linux VM) of
// round tier3-02. A create of w1 runs through the main repository T, and the
// stub holds one of its git calls at a gate file. During that hold a remove of
// the worktree r1 of the same git directory runs, through the linked worktree LW
// (T3) or through T itself (T3M).
//
//   - T3 and T3M: the create is held in its `worktree add`. The leaf r1 goes
//     during the hold. The registration r1, the branch rb and the reply come
//     after the add ended.
//   - T3C: the create is held at its read-tree checkout, after the add. The
//     remove answers during the hold.

// dispatchAsync runs line through dispatch in a goroutine. The channel gets the
// marshaled reply.
func dispatchAsync(s *server, line string) <-chan string {
	out := make(chan string, 1)
	go func() {
		var b []byte
		if resp := s.dispatch(nil, []byte(line)); resp != nil {
			b, _ = json.Marshal(*resp)
		}
		out <- string(b)
	}()
	return out
}

// waitFor fails the test unless ready holds within 60 s.
func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()
	if !pollUntil(60*time.Second, ready) {
		t.Fatalf("%s: not within 60 s", what)
	}
}

// receive waits at most 60 s for the reply on ch.
func receive(t *testing.T, what string, ch <-chan string) string {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(60 * time.Second):
		t.Fatalf("%s: no reply within 60 s", what)
		return ""
	}
}

// heldRemoveFixture makes the fixture of the cells: r1 on branch rb inside
// baseRepo, with baseRepo the linked worktree LW or the main repository T. The
// stub holds the call that match picks at a gate file. It returns the server,
// baseRepo, the leaf r1 and the gate.
func heldRemoveFixture(t *testing.T, linked bool, match string) (wtFixture, *server, string, string, string) {
	t.Helper()
	f := newWTFixture(t, true)
	base := f.top
	if linked {
		base = f.base
	}
	r1 := filepath.Join(base, ".claude", "worktrees", "r1")
	runGit(t, base, "worktree", "add", "-q", "-b", "rb", r1)
	installGitSlowStub(t, f.realGit)
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv("CLAUSTRUM_GITSTUB_GATE", gate)
	// The gate opens at the end in every case, so a held create ends.
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	slowGit(t, match, "gate", 60*time.Second, "", "")
	return f, newTestServer(t), base, r1, gate
}

func removeLine(t *testing.T, base, leaf, branch string) string {
	t.Helper()
	return rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": leaf, "branchName": branch})
}

// TestWorktreeRemoveWaitsForAddOfCreate pins cells T3 and T3M. Mutation: drop
// the lock of the add step in gitWorktreeRemoveLocked. In both rows the
// registration and the branch then go during the hold, and the reply comes
// during it. Mutation for T3M: take the per-repo lock in
// withWorktreeCreateLock again. The leaf r1 then stays during the hold.
func TestWorktreeRemoveWaitsForAddOfCreate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		linked bool
	}{{"T3", true}, {"T3M", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, base, r1, gate := heldRemoveFixture(t, tc.linked, "worktree,add,w1")
			w1 := filepath.Join(f.top, ".claude", "worktrees", "w1")
			created := dispatchAsync(s, rpcLine(t, "git.worktree_create", createParams(f.top, w1, "w1")))
			waitFor(t, "the add of the create reached the gate", func() bool { return exists(gate + ".in") })
			removed := dispatchAsync(s, removeLine(t, base, r1, "rb"))
			waitFor(t, "the remove deleted the leaf r1 during the held add", func() bool { return !exists(r1) })
			// The remove must not answer while the add is held. A remove that
			// does not wait answers within a few calls of the stub.
			select {
			case got := <-removed:
				t.Fatalf("the remove answered during the held add: %s", got)
			case <-time.After(2 * time.Second):
			}
			if !exists(filepath.Join(f.regDir, "r1")) {
				t.Error("the registration r1 went during the held add")
			}
			if !branchExists(t, f.top, "rb") {
				t.Error("the branch rb went during the held add")
			}
			writeFile(t, gate, "", 0o644)
			if got := receive(t, "the remove", removed); !strings.Contains(got, `"success":true`) {
				t.Errorf("the remove = %s\nwant success", got)
			}
			if exists(filepath.Join(f.regDir, "r1")) {
				t.Error("the registration r1 is still there after the remove")
			}
			if branchExists(t, f.top, "rb") {
				t.Error("the branch rb is still there after the remove")
			}
			if got := receive(t, "the create", created); !strings.Contains(got, `"success":true`) {
				t.Errorf("the create = %s\nwant success", got)
			}
		})
	}
}

// TestWorktreeRemoveDoesNotWaitForCheckout pins cell T3C. The create is held at
// its read-tree checkout, after the add, and the remove answers during that
// hold. Mutation: hold the lock of the add step for the whole create in
// gitWorktreeCreate. The remove then waits for the hold, and both rows fail.
// Without this change the row through T fails too: the per-repo lock of the
// create kept the remove out.
func TestWorktreeRemoveDoesNotWaitForCheckout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		linked bool
	}{{"through LW", true}, {"through T", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, base, r1, gate := heldRemoveFixture(t, tc.linked, "read-tree")
			w1 := filepath.Join(f.top, ".claude", "worktrees", "w1")
			created := dispatchAsync(s, rpcLine(t, "git.worktree_create", createParams(f.top, w1, "w1")))
			waitFor(t, "the checkout of the create reached the gate", func() bool { return exists(gate + ".in") })
			removed := dispatchAsync(s, removeLine(t, base, r1, "rb"))
			select {
			case got := <-removed:
				if !strings.Contains(got, `"success":true`) {
					t.Errorf("the remove = %s\nwant success", got)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("the remove did not answer during the held checkout")
			}
			if exists(gate + ".out") {
				t.Error("the gate opened before the remove answered")
			}
			for _, p := range []string{r1, filepath.Join(f.regDir, "r1")} {
				if exists(p) {
					t.Errorf("%s is still there after the remove", p)
				}
			}
			if branchExists(t, f.top, "rb") {
				t.Error("the branch rb is still there after the remove")
			}
			writeFile(t, gate, "", 0o644)
			if got := receive(t, "the create", created); !strings.Contains(got, `"success":true`) {
				t.Errorf("the create = %s\nwant success", got)
			}
		})
	}
}
