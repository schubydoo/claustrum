//go:build unix

package git

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// rowArgv is the argv of one call without its -c items, with the paths masked.
// A leading --attr-source stays. <repo> is the repository, <root> the
// worktreeRoot, <S>/.s<n> the staging folder, <S>/.checkout-<n> the folder of
// the checkout and <id> a commit id.
func rowArgv(c envCall, repo, root string) string {
	argv := c.argv
	var out []string
	if len(argv) > 0 && strings.HasPrefix(argv[0], "--attr-source=") {
		out, argv = append(out, argv[0]), argv[1:]
	}
	for len(argv) > 1 && argv[0] == "-c" {
		argv = argv[2:]
	}
	text := strings.Join(append(out, argv...), " ")
	if root != "" {
		text = strings.ReplaceAll(text, root, "<root>")
	}
	text = strings.ReplaceAll(text, repo, "<repo>")
	text = regexp.MustCompile(`\S*/\.s[0-9]+/leaf/`).ReplaceAllString(text, "<S>/.s<n>/leaf/")
	text = regexp.MustCompile(`=\S*/\.checkout-[0-9]+/`).ReplaceAllString(text, "=<S>/.checkout-<n>/")
	return shapeCommit.ReplaceAllString(text, "<id>")
}

// The git calls of a git.worktree_create after a first git.info, in two rows
// of c9125eb8 and 4c111e86 on Linux (measured, the argv of each call). In the
// state fallback-git the create makes the commit call and the settle step. In
// the state shadow with a worktreeRoot the pair before the add names the leaf
// as its work tree, and the three location calls run again after the add and
// after the settle step. Both rows start in a repository with no
// registrations folder. The arm of fallback-git with "shadowDirOff":true is
// pinned by TestFallbackCreateWithTheMemberKeepsItsCalls.
func TestWorktreeCreateCallsOfTheRows(t *testing.T) {
	requireGit(t)
	const (
		listing  = "config -z --list"
		gitDirLs = "--git-dir=<repo>/.git config -z --list"
	)
	location := []string{listing, "rev-parse --show-toplevel", listing, "rev-parse --absolute-git-dir",
		listing, "worktree list --porcelain -z"}
	// In the state shadow the settle call names the LFS store of the checkout
	// folder (4c111e86, measured on Linux).
	settle := func(leaf, lfs string) []string {
		return []string{"--attr-source=<id> version", gitDirLs, "--git-dir=<repo>/.git hash-object -t tree /dev/null",
			gitDirLs, "--attr-source=<id> --git-dir=<repo>/.git --work-tree=" + leaf + lfs + " diff-files --quiet --ignore-submodules=all"}
	}
	for _, row := range []struct {
		name  string
		state shapeState
		root  bool
		want  func(leaf string) []string
	}{
		{"fallback-git", stateFallback, false, func(leaf string) []string {
			return slices.Concat([]string{
				listing, "rev-parse --is-inside-work-tree",
				listing, "rev-parse --abbrev-ref HEAD",
				listing, "rev-parse --absolute-git-dir",
				listing, "worktree add --no-track --no-checkout -b wt1 " + leaf,
				listing, "rev-parse --verify --quiet refs/heads/wt1^{commit}",
				gitDirLs, "--git-dir=<repo>/.git --work-tree=" + leaf + " read-tree -u --reset --no-recurse-submodules <id>",
			}, settle(leaf, ""))
		}},
		{"shadow with worktreeRoot", stateShadow, true, func(leaf string) []string {
			return slices.Concat([]string{listing, "rev-parse --is-inside-work-tree"}, location, []string{
				listing, "rev-parse --abbrev-ref HEAD",
				listing, "rev-parse --absolute-git-dir",
				listing, gitDirLs, "--git-dir=<repo>/.git --work-tree=" + leaf + " rev-parse --show-toplevel",
				"worktree add --no-track --no-checkout -b wt1 <S>/.s<n>/leaf/wt1",
				"rev-parse --git-dir",
			}, location, []string{
				listing, "rev-parse --verify --quiet refs/heads/wt1^{commit}",
				gitDirLs, `--git-dir=<repo>/.git config -z --get-regexp ^(core\.sshcommand|ssh\.variant)$`,
				gitDirLs, "--git-dir=<repo>/.git --work-tree=" + leaf + " read-tree -u --reset --no-recurse-submodules <id>",
			}, settle(leaf, " -c lfs.storage=<S>/.checkout-<n>/lfs"), location, []string{
				listing, "cat-file --batch",
				listing, "hash-object -t commit --literally --no-filters --stdin-paths -w",
				listing, "hash-object -t tree --literally --no-filters --stdin-paths -w",
			})
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShapeFixture(t, row.state)
			if err := os.RemoveAll(filepath.Join(f.gitDir, "worktrees")); err != nil {
				t.Fatal(err)
			}
			if raw, _ := f.call(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			params := map[string]any{"baseRepo": f.repo, "branchName": "wt1"}
			root, leaf := "", "<repo>/.claude/worktrees/wt1"
			if row.root {
				root = dropGroupWrite(t, resolveTestRoot(t, t.TempDir()))
				requireTempOutsideCheckout(t, root)
				params["worktreeRoot"], params["worktreePath"] = root, filepath.Join(root, "proj", "wt1")
				leaf = "<root>/proj/wt1"
			} else {
				params["worktreePath"] = filepath.Join(f.repo, ".claude", "worktrees", "wt1")
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			var got []string
			for _, c := range calls {
				got = append(got, rowArgv(c, f.repo, root))
			}
			if want := row.want(leaf); !slices.Equal(got, want) {
				t.Errorf("%d calls, want %d", len(got), len(want))
				for i := range max(len(got), len(want)) {
					g, w := "(no call)", "(no call)"
					if i < len(got) {
						g = got[i]
					}
					if i < len(want) {
						w = want[i]
					}
					if g != w {
						t.Errorf("call %d\n got  %s\n want %s", i+1, g, w)
					}
				}
			}
		})
	}
}

// A create from a linked worktree L outside the repository names the `.git` of
// the main repository in the pair before the add and in the two calls of the
// empty tree, with L as the work tree of the pair. 4c111e86 does so (the pair
// measured on Linux and macOS, the empty tree on macOS). The other calls keep
// the folder of L.
func TestWorktreeCreateFromALinkedBaseNamesTheMainGitDir(t *testing.T) {
	requireGit(t)
	f := newShapeFixture(t, stateShadow)
	linked := filepath.Join(filepath.Dir(f.repo), "linked")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "base1", linked)
	if raw, _ := f.call(t, "git.info", map[string]any{"path": linked}); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	raw, calls := f.call(t, "git.worktree_create", map[string]any{
		"baseRepo": linked, "branchName": "w1", "worktreePath": filepath.Join(linked, ".claude", "worktrees", "w1")})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	main := "--git-dir=" + filepath.Join(f.repo, ".git")
	pairs, trees := 0, 0
	for i, c := range calls {
		switch {
		case slices.Contains(c.argv, "--show-toplevel") && slices.ContainsFunc(c.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") }):
			pairs++
			if !slices.Contains(c.argv, main) || !slices.Contains(c.argv, "--work-tree="+linked) {
				t.Errorf("the pair = %q, want %s and the work tree %s", c.argv, main, linked)
			}
		case slices.Contains(c.argv, "hash-object") && slices.Contains(c.argv, os.DevNull):
			trees++
			if !slices.Contains(c.argv, main) {
				t.Errorf("the empty-tree call = %q, want %s", c.argv, main)
			}
		default:
			continue
		}
		if i == 0 || !slices.Contains(calls[i-1].argv, main) {
			t.Errorf("the listing before %q does not name %s", c.argv, main)
		}
	}
	if pairs != 1 {
		t.Errorf("%d pairs before the add, want 1", pairs)
	}
	if trees == 0 && attrSourceOK {
		t.Error("no empty-tree call ran")
	}
}
