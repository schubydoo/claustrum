package git

import (
	"fmt"
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

// Measured on Linux. A pair whose second call fails changes nothing:
// the create succeeds.
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

// Measured on Linux, 3 of 3 runs. The second call of the pair is held past the
// timeoutMs of the caller. It gets no signal, the add runs after it, and the
// create answers the timeout before the checkout with the rollback: the leaf,
// the registration and the branch go.
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
// of another worktree, in the state shadow (measured) and with
// "shadowDirOff":true. On 5fd08069 it does not run for a stale entry of the leaf
// (measured) or with the member and no registrations folder (measured). In the
// state shadow a missing folder gives the pair (b3081b51 and c9125eb8, measured).
// On Linux and macOS a stale entry that the step removed gives the pair in the
// state shadow and none with the member (c9125eb8 and 4c111e86, measured on
// Linux). Windows keeps no pair there (not measured).
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
		{"plain", false, nil, true},
		{"with the member", true, nil, true},
		{"a stale entry", false, stale, !stagedAddWindowsSteps},
		{"with the member and a stale entry", true, stale, false},
		{"no registrations folder", false, noFolder, true},
		{"with the member and no folder", true, noFolder, false},
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

// TestCreatePairAfterTheAddListingWithNoFolder: in the state shadow a first create
// with no registrations folder runs the pair after the listing of the add, right
// before the add (c9125eb8 and b3081b51, measured on Linux and macOS, 87375cc7 on
// Windows). Mutation: on Windows, or with the Windows steps on, run the pair at
// once in pairBeforeAdd for a missing folder (the listing comes last).
func TestCreatePairAfterTheAddListingWithNoFolder(t *testing.T) {
	wantPairRightBeforeAdd(t)
}

// TestCreatePairInFallbackGit: in the state fallback-git a create without the
// member, in a repository with a registrations folder, runs the pair on
// Windows. 87375cc7 runs it right after `rev-parse --absolute-git-dir` and
// before the listing of the add (measured on Windows). Linux and macOS run no
// pair there (claustrum's own, not measured). Mutation: drop fallbackPair from
// the test of the pair in gitWorktreeCreate (no pair runs on Windows).
func TestCreatePairInFallbackGit(t *testing.T) {
	wantFallbackPair(t, stagedAddWindowsSteps)
}

// wantFallbackPair runs a create in the state fallback-git and wants the pair
// right after `rev-parse --absolute-git-dir`, or no pair.
func wantFallbackPair(t *testing.T, pair bool) {
	t.Helper()
	f := newCheckoutRuleFixture(t)
	h := f.d.Shadow
	h.mu.Lock()
	h.state = controlFallback
	h.mu.Unlock()
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if got := ranPair(calls, f.repo); got != pair {
		t.Fatalf("the pair ran = %v, want %v", got, pair)
	}
	if !pair {
		return
	}
	gitDirFlag := func(c envCall) bool {
		return slices.ContainsFunc(c.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
	}
	at := slices.IndexFunc(calls, func(c envCall) bool { return argvHas(c.argv, "rev-parse", "--absolute-git-dir") })
	if at < 0 || at+4 >= len(calls) {
		t.Fatalf("no four calls after rev-parse --absolute-git-dir (at %d of %d)", at, len(calls))
	}
	next := calls[at+1 : at+5]
	if !argvHas(next[0].argv, "config", "-z", "--list") || !gitDirFlag(next[0]) ||
		!argvHas(next[1].argv, "rev-parse", "--show-toplevel") || !gitDirFlag(next[1]) ||
		!argvHas(next[2].argv, "config", "-z", "--list") || gitDirFlag(next[2]) ||
		!argvHas(next[3].argv, "worktree", "add") {
		t.Errorf("the calls after rev-parse --absolute-git-dir = %q, %q, %q, %q, want the pair, the listing and the add",
			next[0].argv, next[1].argv, next[2].argv, next[3].argv)
	}
	// Both calls of the pair run in the git directory with the variables of the
	// row: GIT_COMMON_DIR of the repository, GIT_SSH_COMMAND=false, and no
	// GIT_DIR or GIT_OBJECT_DIRECTORY.
	for _, c := range next[:2] {
		if filepath.Clean(c.cwd) != filepath.Clean(f.gitDir) {
			t.Errorf("%q runs in %s, want %s", c.argv, c.cwd, f.gitDir)
		}
		if !slices.Contains(c.env, "GIT_COMMON_DIR="+f.gitDir) || !slices.Contains(c.env, "GIT_SSH_COMMAND=false") ||
			slices.ContainsFunc(c.env, func(e string) bool {
				return strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_OBJECT_DIRECTORY=")
			}) {
			t.Errorf("%q has the variables %q", c.argv, c.env)
		}
	}
}

// wantPairRightBeforeAdd runs a create with no registrations folder and wants the
// pair as the two calls right before the add.
func wantPairRightBeforeAdd(t *testing.T) {
	t.Helper()
	f := newCheckoutRuleFixture(t)
	if err := os.RemoveAll(filepath.Join(f.gitDir, worktreesSubdir)); err != nil {
		t.Fatal(err)
	}
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	add := slices.IndexFunc(calls, func(c envCall) bool { return argvHas(c.argv, "worktree", "add") })
	if add < 2 {
		t.Fatalf("no add after two calls: %d", add)
	}
	if !argvHas(calls[add-1].argv, "rev-parse", "--show-toplevel") || !argvHas(calls[add-2].argv, "config", "-z", "--list") ||
		!slices.ContainsFunc(calls[add-2].argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") }) {
		t.Errorf("the two calls before the add = %q, %q, want the pair", calls[add-2].argv, calls[add-1].argv)
	}
}

// TestCreatePairAfterStaleEntryResolvesALinkedBase: in the state shadow, after
// the stale step removed an entry of the leaf, the pair of a baseRepo given
// through a link has the resolved top of the repository as its work tree, as
// 4c111e86 does (measured on Linux). The link is in a t.TempDir(). Mutation:
// keep repo as the work tree of that pair (the link path shows).
func TestCreatePairAfterStaleEntryResolvesALinkedBase(t *testing.T) {
	if stagedAddWindowsSteps {
		t.Skip("Windows keeps no pair after a stale entry (not measured)")
	}
	f := newCheckoutRuleFixture(t)
	link := filepath.Join(resolveTestRoot(t, t.TempDir()), "lnk")
	if err := os.Symlink(f.repo, link); err != nil {
		t.Skipf("no symlink: %v", err)
	}
	mustWrite(t, filepath.Join(f.gitDir, worktreesSubdir, "old", "gitdir"), filepath.Join(f.leaf, ".git")+"\n")
	raw, calls := f.create(t, map[string]any{"baseRepo": link, "worktreePath": filepath.Join(link, ".claude", "worktrees", "w1")})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if !ranPair(calls, f.repo) || ranPair(calls, link) {
		t.Errorf("the pair work tree: resolved top %v, link %v, want only the resolved top", ranPair(calls, f.repo), ranPair(calls, link))
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

// TestCreatePairBeforeTheLockedStaleRefusal: in the state shadow one stale entry
// of the leaf that holds a `locked` file stays. After `rev-parse
// --absolute-git-dir` the listing and the pair run, and then the create answers
// the locked refusal. 4c111e86 makes these calls and no other (measured on
// macOS). With "shadowDirOff":true no pair runs, and git answers (measured on
// macOS). Mutation: drop staleLockedPair from the test of the pair (the pair
// does not run).
func TestCreatePairBeforeTheLockedStaleRefusal(t *testing.T) {
	if stagedAddWindowsSteps {
		t.Skip("Windows keeps no pair for a locked stale entry (not measured)")
	}
	for _, member := range []bool{false, true} {
		t.Run(fmt.Sprintf("shadowDirOff %v", member), func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			old := filepath.Join(f.gitDir, worktreesSubdir, "old")
			mustWrite(t, filepath.Join(old, "gitdir"), filepath.Join(f.leaf, ".git")+"\n")
			mustWrite(t, filepath.Join(old, "locked"), "")
			var extra map[string]any
			if member {
				extra = map[string]any{"shadowDirOff": true}
			}
			raw, calls := f.create(t, extra)
			text, _ := createErrorField(t, raw)
			if member {
				if !strings.Contains(text, "missing but locked worktree") || ranPair(calls, f.repo) {
					t.Errorf("reply = %s, pair %v\nwant git's own text and no pair", raw, ranPair(calls, f.repo))
				}
				mustExist(t, filepath.Join(old, "locked"))
				return
			}
			if !strings.HasPrefix(text, "cannot add a worktree: ") || !strings.Contains(text, `as a locked worktree (entry "old")`) {
				t.Fatalf("reply = %s\nwant the locked refusal", raw)
			}
			at := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--absolute-git-dir") })
			if at < 0 {
				t.Fatal("no rev-parse --absolute-git-dir call")
			}
			gitDirWord := "--git-dir=" + filepath.Join(f.repo, ".git")
			want := [][]string{
				{"config", "-z", "--list"},
				{gitDirWord, "config", "-z", "--list"},
				{gitDirWord, "--work-tree=" + f.repo, "rev-parse", "--show-toplevel"},
			}
			tail := calls[at+1:]
			ok := len(tail) == len(want)
			for i := 0; ok && i < len(want); i++ {
				ok = argvEndsWith(tail[i].argv, want[i][len(want[i])-2:]) && argvHas(tail[i].argv, want[i]...)
			}
			if !ok {
				var got [][]string
				for _, c := range tail {
					got = append(got, c.argv)
				}
				t.Errorf("the calls after rev-parse --absolute-git-dir =\n%q\nwant three calls ending in\n%q", got, want)
			}
			mustExist(t, filepath.Join(old, "locked"))
		})
	}
}
