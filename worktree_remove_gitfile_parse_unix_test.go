//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git.worktree_remove through a baseRepo whose `.git` is a file that the walk reads
// with the strict parse. The worktree folder is gone. The reference answers success in
// each case below (measured on Linux):
//   - a well-formed file: the registration and the branch go.
//   - a NUL after the path: both go.
//   - a second blank after "gitdir:": both go.
//   - a blank after the path: the branch goes and the registration stays.
func TestWorktreeRemoveCraftedGitFile(t *testing.T) {
	requireGit(t)
	for _, c := range []struct {
		name, gitDir, text string // text gets the git directory path or name
		regGoes            bool
	}{
		{"well formed", "r.git", "gitdir: <abs>\n", true},
		{"NUL", "r.git", "gitdir: <abs>\x00junk", true},
		{"leading blank", " r.git", "gitdir:  r.git", true},
		{"trailing blank", "t.git ", "gitdir: t.git \n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := resolveTestRoot(t, t.TempDir())
			b := filepath.Join(root, "B")
			gd := filepath.Join(b, c.gitDir)
			runGit(t, root, "init", "-q", "-b", "main", "--separate-git-dir", gd, b)
			writeFile(t, filepath.Join(b, "t0.txt"), "t0\n", 0o644)
			runGit(t, b, "add", "t0.txt")
			runGit(t, b, "commit", "-q", "-m", "t0")
			wp := filepath.Join(b, ".claude", "worktrees", "w")
			runGit(t, b, "worktree", "add", "-q", "-b", "w", wp)
			if err := os.RemoveAll(wp); err != nil {
				t.Fatal(err)
			}
			text := strings.ReplaceAll(c.text, "<abs>", gd)
			writeFile(t, filepath.Join(b, ".git"), text, 0o644)
			s, _ := newShadowServer(t)
			raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{
				"baseRepo": b, "worktreePath": wp, "branchName": "w"}))
			if raw != removeOK {
				t.Fatalf("frame = %s\nwant %s", raw, removeOK)
			}
			if got := exists(filepath.Join(gd, "worktrees", "w")); got == c.regGoes {
				t.Errorf("registration present = %v, want %v", got, !c.regGoes)
			}
			if gitDirHasBranch(t, gd, "w") {
				t.Error("the branch w stays, want it deleted")
			}
			if !gitDirHasBranch(t, gd, "main") {
				t.Error("the branch main is gone")
			}
		})
	}
}

// gitDirHasBranch asks git whether the git directory gd holds the branch, without the
// GIT_DIR and GIT_COMMON_DIR of the environment.
func gitDirHasBranch(t *testing.T, gd, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+gd, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") && !strings.HasPrefix(kv, "GIT_COMMON_DIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd.Run() == nil
}
