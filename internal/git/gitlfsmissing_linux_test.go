package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lfsMissingStderr is the stderr of a checkout whose smudge filter finds no
// git-lfs (measured on Linux).
const lfsMissingStderr = "git-lfs smudge -- 'a.bin': 1: git-lfs: not found\n" +
	"error: external filter 'git-lfs smudge -- %f' failed 127\n" +
	"error: external filter 'git-lfs smudge -- %f' failed\n" +
	"fatal: a.bin: smudge filter lfs failed\n"

// TestCreateAnswersGitLFSNotFoundForTheShellPathGit: the git of the login-shell
// PATH runs, and the checkout fails for a git-lfs that it does not find. With
// git-lfs on the login-shell PATH, and neither beside that git nor on the PATH of
// the daemon, the create answers git_lfs_not_found with the text "not a git
// repository" and one log line, after the undo of a failed checkout. With
// git-lfs on no PATH it answers the failed checkout (87375cc7, measured on
// Linux). Mutation: drop the git_lfs_not_found arm (the first row answers the
// failed checkout).
func TestCreateAnswersGitLFSNotFoundForTheShellPathGit(t *testing.T) {
	for _, row := range []struct {
		name       string
		lfsOnLogin bool
	}{{"git-lfs on the login PATH", true}, {"git-lfs on no PATH", false}} {
		t.Run(row.name, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			failLfsCheckout(t, lfsMissingStderr)
			prefix := filepath.Join(t.TempDir(), "pfx")
			for _, d := range []string{"bin", filepath.Join("libexec", "git-core")} {
				if err := os.MkdirAll(filepath.Join(prefix, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(gitSlowStubDir(t), "git"), filepath.Join(prefix, "bin", "git")); err != nil {
				t.Fatal(err)
			}
			lfsDir := t.TempDir()
			if row.lfsOnLogin {
				writeFile(t, filepath.Join(lfsDir, "git-lfs"), "#!/bin/sh\ncat\n", 0o755)
			}
			shellPathGitFixture(t, filepath.Join(prefix, "bin"), lfsDir)
			logs := captureLogBuf(t)
			raw, _ := f.create(t, nil)
			if row.lfsOnLogin {
				if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"git_lfs_not_found"}}`; raw != want {
					t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
				}
				line := "[GitHandler] worktree_create: the checkout needs git-lfs, which lies neither beside the git from the login shell's PATH nor on the helper's own PATH; the worktree is undone and the answer is \"not a git repository\", as where no git is found. With git-lfs beside that git, or on the system's own PATH, sessions in this repository get a worktree: git worktree add failed (checkout): git-lfs smudge -- 'a.bin': 1: git-lfs: not found"
				if !strings.Contains(logs.String(), line) {
					t.Errorf("log:\n%s\nwant the line %q", logs, line)
				}
			} else {
				if !strings.Contains(raw, `"error":"git worktree add failed (checkout): git-lfs smudge`) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
					t.Errorf("git.worktree_create = %s, want the failed checkout", raw)
				}
				if strings.Contains(logs.String(), "the checkout needs git-lfs") {
					t.Errorf("log:\n%s\nwant no git-lfs line", logs)
				}
			}
			if _, err := os.Lstat(f.leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it undone", err)
			}
		})
	}
}
