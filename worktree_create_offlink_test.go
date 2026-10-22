package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// offLinkKind stages a link or a file at the registrations folder at, and
// target is a folder with the file MARK. Each system has its own kinds
// (offLinkKinds).
type offLinkKind struct {
	name  string
	stage func(t *testing.T, at, target string)
}

// TestOffCreateWithALinkedRegistrationsFolder pins these cells of round
// tier3-02: C2off (Linux, macOS and Windows VMs), C2offA (Linux and macOS),
// C2offF (Linux) and C2offJ (Windows). The attach rows on Windows are not
// measured. A create with "shadowDirOff":true finds a link or a file at the
// registrations folder. 5fd08069 runs `rev-parse --absolute-git-dir`, then one
// listing in the repository (two in attach mode), and then refuses. It answers
// the text one time, runs no add, removes the leaf it made and makes nothing
// behind the link.
//
// Mutations: in worktreeAdd, test offWorktreesRefusal before hardenedGitCmd.
// The listing then does not run. Drop the call of offLinkListing: attach mode
// then has one listing. On Windows, drop offMember from the test in
// worktreeAdd: the create then succeeds through the link.
func TestOffCreateWithALinkedRegistrationsFolder(t *testing.T) {
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed: cannot list the repository's worktree entries: \"worktrees\" is a link or a file where Claude needs a real directory; put the directory itself in its place","errorCode":"worktree_add_failed"}}`
	for _, kind := range offLinkKinds {
		for _, attach := range []bool{false, true} {
			t.Run(kind.name+" attach "+strconv.FormatBool(attach), func(t *testing.T) {
				f := newSeededShadowFixture(t)
				runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
				runGit(t, f.repo, "branch", "-D", "wr")
				runGit(t, f.repo, "branch", "eb")
				target := filepath.Join(resolveTestRoot(t, t.TempDir()), "wt")
				mustWrite(t, filepath.Join(target, "MARK"), "")
				kind.stage(t, filepath.Join(f.gitDir, worktreesSubdir), target)
				leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
				params := map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "b1", "shadowDirOff": true}
				listings := 1
				if attach {
					params["existingBranch"] = "eb"
					listings = 2
				}
				raw, calls := f.call(t, "git.worktree_create", params)
				if raw != want {
					t.Fatalf("create = %s\n  want %s", raw, want)
				}
				at := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "rev-parse,--absolute-git-dir") })
				if at < 0 {
					t.Fatalf("no rev-parse --absolute-git-dir among %d calls", len(calls))
				}
				after := calls[at+1:]
				if len(after) != listings {
					var argvs [][]string
					for _, c := range after {
						argvs = append(argvs, c.argv)
					}
					t.Fatalf("the calls after absolute-git-dir are %q, want %d listings", argvs, listings)
				}
				for _, c := range after {
					if !c.listing() || !sameDir(c.cwd, f.repo) {
						t.Errorf("call %q in %s, want a listing in %s", c.argv, c.cwd, f.repo)
					}
				}
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the leaf: %v, want none", err)
				}
				if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "b1")); !os.IsNotExist(err) {
					t.Errorf("the branch b1: %v, want none", err)
				}
				ents, err := os.ReadDir(target)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				if strings.Join(names, ",") != "MARK" {
					t.Errorf("the folder behind the link holds %q, want MARK only", names)
				}
			})
		}
	}
}
