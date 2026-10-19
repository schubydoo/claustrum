package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The pair before the add of a create in the state shadow (pairBeforeAdd).
// The repository of the fixture has a linked worktree, so the folder of the
// registrations exists and no entry names the new leaf.

// Cell R-Q4e of round task084-01 (Linux VM). A pair whose second call fails
// changes nothing: the create succeeds.
func TestCreatePairThatFailsChangesNothing(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	slowGit(t, "--show-toplevel,--work-tree="+f.repo, "fail", 0, "fatal: the stub fails the pair\n", "")
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if !ranPair(calls, f.repo) {
		t.Error("no pair ran, so the test cannot see a pair that fails")
	}
}

// Cell R-Q4d of round task084-01 (Linux VM, 3 of 3 runs). The second call of
// the pair is held past the timeoutMs of the caller. It gets no signal, the add
// runs after it, and the create answers the timeout before the checkout with
// the rollback: the leaf, the registration and the branch go.
func TestCreatePairPastTheDeadline(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	const hold = 2500 * time.Millisecond
	slowGit(t, "--show-toplevel,--work-tree="+f.repo, "pre", hold, "", "")
	start := time.Now()
	raw, calls := f.create(t, map[string]any{"timeoutMs": 1000})
	took := time.Since(start)
	want := `"result":{"success":false,"error":"git worktree add timed out after 1000ms (deadline expired before the checkout started)","errorCode":"timeout"}}`
	if !strings.HasSuffix(raw, want) {
		t.Fatalf("git.worktree_create = %s\nwant the result %s", raw, want)
	}
	if took < hold {
		t.Errorf("the create answered after %s, want it after the held call (%s)", took, hold)
	}
	if !ranPair(calls, f.repo) {
		t.Fatal("no pair ran")
	}
	added := false
	for _, c := range calls {
		added = added || argvHas(c.argv, "worktree", "add", "-b", "w1")
	}
	if !added {
		t.Error("no worktree add ran after the held pair")
	}
	f.wantGone(t)
}

// TestCreatePairCases is the portable table of the pair. It runs for an entry
// of another worktree, in the state shadow (cell R-Q2b of round task084-01) and
// with "shadowDirOff":true (cell 50a of s3p2b-edges-linux). It does not run for
// a stale entry of the leaf (cell R-Q2c) or with no registrations folder (cell
// R-Q2a with the member).
func TestCreatePairCases(t *testing.T) {
	stale := func(t *testing.T, f checkoutRuleFixture) {
		old := filepath.Join(f.gitDir, worktreesSubdir, "old")
		mustWrite(t, filepath.Join(old, "gitdir"), filepath.Join(f.leaf, ".git")+"\n")
	}
	noFolder := func(t *testing.T, f checkoutRuleFixture) {
		if err := os.RemoveAll(filepath.Join(f.gitDir, worktreesSubdir)); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		cell   string
		member bool
		stage  func(t *testing.T, f checkoutRuleFixture)
		pair   bool
	}{
		{"R-Q2b", false, nil, true},
		{"50a with the member", true, nil, true},
		{"R-Q2c a stale entry", false, stale, false},
		{"no registrations folder", false, noFolder, false},
		{"R-Q2a with the member and no folder", true, noFolder, false},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			if row.stage != nil {
				row.stage(t, f)
			}
			var extra map[string]any
			if row.member {
				extra = map[string]any{"shadowDirOff": true}
			}
			raw, calls := f.create(t, extra)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if got := ranPair(calls, f.repo); got != row.pair {
				t.Errorf("the pair ran = %v, want %v", got, row.pair)
			}
		})
	}
}

// ranPair reports whether calls hold the second call of the pair.
func ranPair(calls []envCall, repo string) bool {
	for _, c := range calls {
		if argvHas(c.argv, "rev-parse", "--show-toplevel", "--work-tree="+repo) {
			return true
		}
	}
	return false
}

// argvHas reports whether argv holds each of words.
func argvHas(argv []string, words ...string) bool {
	for _, w := range words {
		if !slices.Contains(argv, w) {
			return false
		}
	}
	return true
}
