//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// useUmask sets the umask of the test process for one test. No test that
// calls it runs in parallel with another.
func useUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

// TestNewWorktreeFilesTakeTheUmask: the daemon makes two files of a new
// worktree with mode 0666 before the umask. They are the `config.worktree` file
// of the registration and the `.git` file that replaces the one of git. On
// 5fd08069 both are `f 0664` with a daemon umask of 002, and `f 0644` with 022
// (section 0.2 item 5 of the report of val-s3p2b2-6-linux, Linux VM).
func TestNewWorktreeFilesTakeTheUmask(t *testing.T) {
	for _, row := range []struct {
		name  string
		umask int
		want  os.FileMode
	}{{"umask 002", 0o002, 0o664}, {"umask 022", 0o022, 0o644}} {
		t.Run(row.name, func(t *testing.T) {
			base := resolveTestRoot(t, t.TempDir())
			gitDir := filepath.Join(base, "repo", ".git")
			reg := filepath.Join(gitDir, worktreesSubdir, "w1")
			leaf := filepath.Join(base, "leaf")
			for _, d := range []string{reg, leaf} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			u := gitUse{folder: filepath.Join(base, "S"), repoDir: filepath.Join(base, "S", "R"), gitDir: gitDir}
			mustWrite(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n")
			mustWrite(t, filepath.Join(leaf, ".git"), "gitdir: "+u.repoDir+"/worktrees/w1\n")
			cp := checkpointCreatedWorktree(leaf)
			defer cp.release()

			useUmask(t, row.umask)
			if err := writeWorktreeConfig(u, reg, leaf, testedRegistration{}, []byte("[core]\n\tfilemode = false\n")); err != nil {
				t.Fatal(err)
			}
			repointWorktreeGitFile(u, leaf, cp)
			if got := mustRead(t, filepath.Join(leaf, ".git")); got != "gitdir: "+gitDir+"/worktrees/w1\n" {
				t.Fatalf("the .git file = %q, want it replaced", got)
			}
			for _, file := range []string{filepath.Join(reg, worktreeConfigName), filepath.Join(leaf, ".git")} {
				fi, err := os.Lstat(file)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode().Perm(); got != row.want {
					t.Errorf("%s has mode %o, want %o", file, got, row.want)
				}
			}
		})
	}
}
