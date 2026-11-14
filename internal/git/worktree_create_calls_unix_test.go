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
// worktreeRoot, <S>/.s<n> the staging folder and <id> a commit id.
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
	settle := func(leaf string) []string {
		return []string{"--attr-source=<id> version", gitDirLs, "--git-dir=<repo>/.git hash-object -t tree /dev/null",
			gitDirLs, "--attr-source=<id> --git-dir=<repo>/.git --work-tree=" + leaf + " diff-files --quiet --ignore-submodules=all"}
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
			}, settle(leaf))
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
			}, settle(leaf), location, []string{
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
