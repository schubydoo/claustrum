package git

import (
	"slices"
	"strings"
	"testing"
)

// TestCheckoutSSHDoorRefusalAfterAGoodListingOnWindows: the config read of the
// door fails with exit 128, and the second listing then runs and succeeds. The
// reply is the refusal of the door read, and no read-tree call runs. This case
// is claustrum's own, not measured: 87375cc7 showed this order only with a
// listing that failed too (TestCreateWithAFileAtTheGitDirOnWindows, X2).
// Mutations: return the refusal of the read at once, or drop the return of
// doorRefusal after the listing.
func TestCheckoutSSHDoorRefusalAfterAGoodListingOnWindows(t *testing.T) {
	unsetDaemonSSH(t)
	f := newCheckoutRuleFixture(t)
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--get-regexp")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "128")
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "boom\n")
	raw, calls := f.create(t, nil)
	want := failedFrame(t, "git worktree add failed (checkout): Claude could not read your own git config on the remote machine "+
		"(exit status 128). Run git config --list there to see the problem.")
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	read := slices.IndexFunc(calls, func(c envCall) bool {
		return argvEndsWith(c.argv, []string{"config", "-z", "--get-regexp", sshDoorRegexp})
	})
	if read < 0 {
		t.Fatal("no config read of the door")
	}
	if read+1 >= len(calls) || !calls[read+1].listing() {
		t.Fatalf("the calls after the read of the door = %q, want the second listing first", argvs(calls[read+1:]))
	}
	if _, ok := lookupExact(calls[read+1].env, "GIT_INDEX_FILE"); !ok {
		t.Errorf("the call after the read of the door has no GIT_INDEX_FILE: %q", calls[read+1].argv)
	}
	// The rollback makes listings of its own, with no GIT_INDEX_FILE.
	for _, c := range calls[read+2:] {
		if _, ok := lookupExact(c.env, "GIT_INDEX_FILE"); ok || slices.Contains(c.argv, "read-tree") {
			t.Errorf("a call after the second listing = %q, want none of the checkout", c.argv)
		}
	}
	f.wantGone(t)
}

// argvs is the argv of each call, joined by spaces.
func argvs(calls []envCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.Join(c.argv, " "))
	}
	return out
}
