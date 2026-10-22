package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// git.worktree_remove lists the configuration again once when its first plain
// listing fails. The cells are those of round tier3-13 (macOS VM), round tier3-17
// (Linux VM, also G08b and CaP3) and round task111-09 (Linux VM) on 5fd08069.

// relistFixture is a listing fixture with the worktree w1 at f.wt().
func relistFixture(t *testing.T) listingFixture {
	t.Helper()
	f := newListingFixture(t)
	buildGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.wt())
	return f
}

// failPlainListings makes the plain listings fail with code and stderr. With
// nth set, only the first one fails.
func failPlainListings(t *testing.T, code, stderr string, nth bool) {
	t.Helper()
	slowGit(t, "config,-z,--list", "fail", 0, stderr, "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", code)
	t.Setenv("CLAUSTRUM_GITSTUB_ARGLAST", "--list")
	if nth {
		t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
		t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	}
}

// relistCalls names the calls of a remove from its first plain listing on.
func relistCalls(calls []envCall) []string {
	var out []string
	for _, c := range calls {
		gitDir := slices.ContainsFunc(c.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
		var name string
		switch {
		case c.listing() && gitDir:
			name = "gitdir-list"
		case c.listing():
			name = "list"
		case c.version():
			name = "version"
		case slices.Contains(c.argv, "--absolute-git-dir"):
			name = "absolute-git-dir"
		case slices.Contains(c.argv, "--show-toplevel"):
			name = "show-toplevel"
		default:
			name = strings.Join(c.argv, " ")
		}
		if name == "list" || len(out) > 0 {
			out = append(out, name)
		}
	}
	return out
}

// A second listing that succeeds lets the remove go on: the leaf and the
// registration go, and the branch stays. After exit 1 `git version` runs before
// the second listing (cell F1). After exit 128 with the text of git where it
// finds no repository it does not (cell F1n). After exit 128 with another text it
// does (cell G08b of round tier3-17, Linux VM). Mutations: call
// hostileConfigRefusal in place of removeListingCheck in methods_git.go, keep the
// "no repository" answer in removeListingCheck, or skip `git version` after any
// exit 128 there.
func TestRemoveListsAgainAfterFailedListing(t *testing.T) {
	for _, tc := range []struct {
		name, code, stderr string
		want               []string
	}{
		{"F1", "1", "WRAP-ERR-EARLY", []string{"list", "version", "list", "absolute-git-dir", "gitdir-list", "show-toplevel"}},
		{"F1n", "128", "fatal: not a git repository (or any of the parent directories): .git",
			[]string{"list", "list", "absolute-git-dir", "gitdir-list", "show-toplevel"}},
		{"G08b", "128", "fatal: bad config line 8 in file .git/config",
			[]string{"list", "version", "list", "absolute-git-dir", "gitdir-list", "show-toplevel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := relistFixture(t)
			failPlainListings(t, tc.code, tc.stderr, true)
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt()})
			if got := replyPart(t, raw); got != `{"success":true}` {
				t.Errorf("remove = %s", got)
			}
			if got := relistCalls(calls); !slices.Equal(got, tc.want) {
				t.Errorf("calls = %q\nwant %q", got, tc.want)
			}
			reg := filepath.Join(f.regDir, "w1")
			if exists(f.wt()) || exists(reg) {
				t.Errorf("leaf or registration stays: leaf %v, registration %v", exists(f.wt()), exists(reg))
			}
			if !branchExists(t, f.top, "w1") {
				t.Error("the branch w1 went")
			}
		})
	}
}

// A second listing that fails too answers its refusal after the words of the lock
// check, after `git version`, with no third listing and nothing removed. In cell
// F2 every plain listing exits 1. The case second-text is claustrum's own: the
// first one exits 2 and the second one exits 1, so the text is that of the second
// listing. Which listing's text 5fd08069 quotes is not measured. Mutation: answer
// lockCheckRefusal for a retried check in methods_git.go.
func TestRemoveSecondFailedListingRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		stub func(t *testing.T)
	}{
		{"F2", func(t *testing.T) { failPlainListings(t, "1", "WRAP-ERR-EARLY", false) }},
		{"second-text", func(t *testing.T) {
			on := filepath.Join(t.TempDir(), "second")
			failPlainListings(t, "2", "FIRST", true)
			t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
			t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "write\x1f"+on+"\x1f")
			t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "config,-z,--list")
			t.Setenv("CLAUSTRUM_GITSTUB_AFTER2", on)
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
			t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "WRAP-ERR-EARLY")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := relistFixture(t)
			tc.stub(t)
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt()})
			want := `{"success":false,"error":` + jsonString(t, "failed to remove worktree: could not check whether "+f.wt()+
				" is locked: "+hooksRefusalPrefix+"exit status 1: WRAP-ERR-EARLY") + `}`
			if got := replyPart(t, raw); got != want {
				t.Errorf("remove = %s\nwant %s", got, want)
			}
			if got, w := relistCalls(calls), []string{"list", "version", "list", "version"}; !slices.Equal(got, w) {
				t.Errorf("calls = %q\nwant %q", got, w)
			}
			reg := filepath.Join(f.regDir, "w1")
			if !exists(f.wt()) || !exists(reg) {
				t.Errorf("leaf or registration went: leaf %v, registration %v", exists(f.wt()), exists(reg))
			}
		})
	}
}

// A folder where the trust check finds no repository gets no second listing. The
// first one fails, `git version` follows, and its text comes after the words of
// the lock check. Nothing is removed. 5fd08069 shows it for a plain folder (cell
// CaP3 of round tier3-17, Linux VM, an empty daemon GIT_CONFIG_KEY_0). The other
// no-repository states are claustrum's own. Mutation: pass false for noRepo to removeListingCheck
// in methods_git.go.
func TestRemoveListsOnceInPlainFolder(t *testing.T) {
	f := newListingFixture(t)
	base := t.TempDir()
	requireTempOutsideCheckout(t, base)
	wp := filepath.Join(base, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(wp, "keep.txt")
	writeFile(t, keep, "k\n", 0o644)
	failPlainListings(t, "128", "fatal: unable to parse command-line config", false)
	raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": wp})
	want := `{"success":false,"error":` + jsonString(t, "failed to remove worktree: could not check whether "+wp+
		" is locked: "+hooksRefusalPrefix+"exit status 128: fatal: unable to parse command-line config") + `}`
	if got := replyPart(t, raw); got != want {
		t.Errorf("remove = %s\nwant %s", got, want)
	}
	if got, w := relistCalls(calls), []string{"list", "version"}; !slices.Equal(got, w) {
		t.Errorf("calls = %q\nwant %q", got, w)
	}
	if !exists(keep) {
		t.Error("keep.txt went")
	}
}
