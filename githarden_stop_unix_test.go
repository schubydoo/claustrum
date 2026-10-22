//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The stop of the second listing of the checkout at its bound: SIGTERM to the
// git that still runs, and SIGKILL gitStopGrace later (cell A4x of round
// task111-05, and the strace of round task111-06, Linux VM). The answer comes from how git ended. An exit 0 at the
// SIGTERM is a success (cell A4x-t of round task111-06, Linux VM). An exit 1
// there gives the stop text (cell A4x-x1 of round task111-09, Linux VM). The
// bound is 3 s here, so the stub catches SIGTERM before it comes, and the
// grace is 3 s. The stub logs each SIGTERM. Mutation: in ownConfigListing,
// Kill git at the bound in the place of stopThenKill (every row), or give the
// stop text to every stopped git (the row "exits 0 at SIGTERM").
func TestOwnConfigListingTermThenKill(t *testing.T) {
	const grace = 3 * time.Second
	for _, row := range []struct {
		name, termExit string
		success        bool
	}{
		{"ignores SIGTERM", "", false},
		{"exits 0 at SIGTERM", "0", true},
		{"exits 1 at SIGTERM", "1", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			setDuration(t, &ownConfigBound, 3*time.Second)
			setDuration(t, &gitStopGrace, grace)
			slowGit(t, "config,-z,--list", "logterm", 60*time.Second, "", "")
			t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_INDEX_FILE=index")
			termLog := filepath.Join(t.TempDir(), "term.log")
			t.Setenv("CLAUSTRUM_GITSTUB_TERMLOG", termLog)
			t.Setenv("CLAUSTRUM_GITSTUB_TERMEXIT", row.termExit)
			start := time.Now()
			raw, calls := f.create(t, nil)
			took := time.Since(start)
			want := failedFrame(t, ownConfigText(ownConfigStopText))
			if row.success {
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, f.leaf) + `,"sourceBranch":"main","branch":"w1"}}`
			}
			if raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if b, _ := os.ReadFile(termLog); string(b) != "ready\nterm\n" {
				t.Errorf("the stub logged %q, want one SIGTERM after it was ready", b)
			}
			if ranReadTree(calls) != row.success {
				t.Errorf("a read-tree call ran = %v, want %v", ranReadTree(calls), row.success)
			}
			if row.termExit == "" && (took < ownConfigBound+grace || took > ownConfigBound+grace+20*time.Second) {
				t.Errorf("the create took %v, want the SIGKILL %v after the bound", took, grace)
			}
			if row.termExit != "" && took > ownConfigBound+grace+20*time.Second {
				t.Errorf("the create took %v, want the reply soon after the SIGTERM", took)
			}
			if !row.success {
				f.wantGone(t)
			}
		})
	}
}
