//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateRefusesABareBaseRepo: a create with a bare repository as its
// baseRepo answers "not a git repository" after the `rev-parse
// --is-inside-work-tree` call, and makes nothing. On 5fd08069 that call is the
// last of 15 and prints `true`. The reply is the `not_a_repo` line (cell
// x2-bare-create of val-s3p2b2-5-linux2, Linux VM, 3 of 3 runs). With the
// member the git of this host prints `false` there, and claustrum answers the
// same line. 5fd08069 is not measured with the member.
func TestCreateRefusesABareBaseRepo(t *testing.T) {
	const want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
	for _, extra := range []map[string]any{nil, {"shadowDirOff": true}} {
		name := "no member"
		if extra != nil {
			name = "with the member"
		}
		t.Run(name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
			runGit(t, filepath.Dir(f.repo), "init", "-q", "--bare", "-b", "main", bare)
			runGit(t, f.repo, "push", "-q", bare, "main")
			params := map[string]any{"baseRepo": bare, "branchName": "w1", "worktreePath": filepath.Join(bare, ".claude", "worktrees", "w1")}
			for k, v := range extra {
				params[k] = v
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			if raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if len(calls) == 0 || !argvEndsWith(calls[len(calls)-1].argv, []string{"rev-parse", "--is-inside-work-tree"}) {
				t.Errorf("the last call is %q, want rev-parse --is-inside-work-tree", calls[len(calls)-1].argv)
			}
			for _, made := range []string{".claude", worktreesSubdir, filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(bare, made)); !os.IsNotExist(err) {
					t.Errorf("%s in the bare repository: %v, want nothing made", made, err)
				}
			}
		})
	}
}
