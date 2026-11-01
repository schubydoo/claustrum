package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTickPath pins the path form of the worktree refusals: backticks, a cut at
// exactly 300 bytes with no ellipsis (be1c1ca5, measured on Linux with a path of
// 399 bytes), and Go %q for a path with a backtick (measured on Linux, macOS and
// Windows).
func TestTickPath(t *testing.T) {
	long := "/" + strings.Repeat("d", 200) + "/" + strings.Repeat("e", 197)
	if len(long) != 399 {
		t.Fatalf("fixture is %d bytes", len(long))
	}
	for _, tc := range []struct{ in, want string }{
		{"/r/.claude/worktrees/w1", "`/r/.claude/worktrees/w1`"},
		{long, "`" + long[:300] + "`"},
		{"/r/b`q", `"/r/b` + "`" + `q"`},
	} {
		if got := tickPath(tc.in); got != tc.want {
			t.Errorf("tickPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// createErrorField decodes a git.worktree_create frame and returns result.error
// and result.errorCode.
func createErrorField(t *testing.T, raw string) (text, code string) {
	t.Helper()
	var resp struct {
		Result struct {
			Error     string `json:"error"`
			ErrorCode string `json:"errorCode"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return resp.Result.Error, resp.Result.ErrorCode
}

// onlyEntry returns the one name in <repo>/.git/worktrees other than skip.
func onlyEntry(t *testing.T, repo, skip string) string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(repo, ".git", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if e.Name() != skip {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("registrations = %q, want one beside %q", names, skip)
	}
	return names[0]
}

// TestWorktreeCreateRegisteredLockedLeaf pins the refusal of a create at a gone
// leaf that a locked entry still names (be1c1ca5, measured on Linux, macOS and
// Windows).
// A path with a blank keeps the quoted hint. A path with a `$`, a `'` or a
// backslash gets the hint with no path. The entry and its files stay, and
// no branch is made.
func TestWorktreeCreateRegisteredLockedLeaf(t *testing.T) {
	type leafCase struct{ name, leaf string }
	cases := []leafCase{{"plain path", "w"}, {"blank in the path", "w x"}, {"dollar in the path", "d$x"}}
	if runtime.GOOS != "windows" { // a `'` on Windows is not measured, and `\` is the separator there
		cases = append(cases, leafCase{"quote in the path", "w'x"}, leafCase{"backslash in the path", `w\x`})
	}
	for _, tc := range cases {
		leafName := tc.leaf
		t.Run(tc.name, func(t *testing.T) {
			f := newRmFixture(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", leafName)
			runGit(t, f.repo, "worktree", "add", "-q", "-b", "old", leaf)
			runGit(t, f.repo, "worktree", "lock", leaf)
			if err := os.RemoveAll(leaf); err != nil {
				t.Fatal(err)
			}
			entry := onlyEntry(t, f.repo, "wt1")
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": f.repo, "branchName": "b", "worktreePath": leaf}))
			hint := "unlock it first (git worktree unlock " + unlockQuote + leaf + unlockQuote +
				", then git worktree prune), or use another folder"
			if strings.ContainsAny(leaf, genericHintChars) {
				hint = "unlock it with git worktree unlock, giving the folder's path (git worktree list shows it), " +
					"then git worktree prune, or use another folder"
			}
			want := "cannot add a worktree: `" + leaf + "` is still registered in this repository as a locked " +
				"worktree (entry \"" + entry + "\"); " + hint
			if text, code := createErrorField(t, raw); text != want || code != "worktree_add_failed" {
				t.Errorf("reply = %s\nwant error %q with worktree_add_failed", raw, want)
			}
			mustExist(t, filepath.Join(f.repo, ".git", "worktrees", entry, "locked"))
			// The refusal comes before the add, so no branch is made (measured on
			// Linux and macOS).
			if gitExitOK(f.repo, "show-ref", "--verify", "--quiet", "refs/heads/b") {
				t.Error("branch b exists, want no branch")
			}
			if _, err := os.Lstat(leaf); err == nil {
				t.Errorf("leaf %s exists after the refusal", leaf)
			}
		})
	}
}

// TestWorktreeRemoveLockTexts pins the four lock refusals of a remove (be1c1ca5,
// measured on Linux, macOS and Windows). A lock with
// the reason `initializing` names a create that did not finish, and its hint
// quotes the path as unlockQuote does. Each path is in backticks. Nothing is
// deleted.
func TestWorktreeRemoveLockTexts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		gone   bool
		tail   func(wt string) string
	}{
		{"initializing", "initializing", false, func(wt string) string {
			return " was left locked by a create that did not finish; unlock it first (git worktree unlock " +
				unlockQuote + wt + unlockQuote + ", then remove it again)"
		}},
		{"initializing gone", "initializing", true, func(wt string) string {
			return " is gone but a create that did not finish left its registration locked; unlock it first " +
				"(git worktree unlock " + unlockQuote + wt + unlockQuote + ", then remove it again) to drop the " +
				"registration and branch"
		}},
		{"reason x", "x", false, func(string) string {
			return " is locked (git worktree lock); unlock it to remove it"
		}},
		{"reason x gone", "x", true, func(string) string {
			return " is gone but its registration is locked (git worktree lock); unlock it to remove the " +
				"registration and branch"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRmFixture(t)
			runGit(t, f.repo, "worktree", "lock", "--reason", tc.reason, f.wt)
			if tc.gone {
				if err := os.RemoveAll(f.wt); err != nil {
					t.Fatal(err)
				}
			}
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
			wantRemoveError(t, raw, "refusing to remove worktree: `"+f.wt+"`"+tc.tail(f.wt))
			mustExist(t, filepath.Join(f.entry(), "locked"))
			if !tc.gone {
				mustExist(t, f.wt)
			}
			if !f.hasBranch("wt1") {
				t.Error("branch wt1 was deleted")
			}
		})
	}
}

// TestRegisteredLeafEntrySkipsLiveFolder pins that an entry whose recorded
// folder holds an entry does not count as a registration of the leaf. That keeps
// the frame of D27 on Windows (claustrum's own, not measured).
func TestRegisteredLeafEntrySkipsLiveFolder(t *testing.T) {
	common := t.TempDir()
	live := filepath.Join(t.TempDir(), "W1")
	writeFile(t, filepath.Join(live, "f.txt"), "f\n", 0o644)
	writeFile(t, filepath.Join(common, worktreesSubdir, "W1", "gitdir"), filepath.Join(live, ".git")+"\n", 0o644)
	sp := newWorktreePathSet(live)
	if name, _ := registeredLeafEntry(common, sp); name != "" {
		t.Errorf("entry %q counts for a live folder, want none", name)
	}
	if err := os.Remove(filepath.Join(live, "f.txt")); err != nil {
		t.Fatal(err)
	}
	if name, _ := registeredLeafEntry(common, sp); name != "W1" {
		t.Errorf("entry = %q for an empty folder, want W1", name)
	}
}
