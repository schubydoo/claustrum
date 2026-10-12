//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestIncludePatternsModeWithTheMember: in a create with "shadowDirOff":true
// the copy of the manifest is `<tmp>/claude-ssh-worktreeinclude-<n>/patterns`
// with mode 0644. On 5fd08069 it is `f 0644` in 9 of 9 such creates (section
// 0.2 item 7 of the report of val-s3p2b2-6-linux, Linux VM). The stub holds
// each `ls-files` call, and the test looks at the temporary folder in that
// time.
func TestIncludePatternsModeWithTheMember(t *testing.T) {
	f := newSeededShadowFixture(t)
	includeFixture(t, f.repo)
	tmp := resolveTestRoot(t, t.TempDir())
	t.Setenv("TMPDIR", tmp)
	slowGit(t, "ls-files", "pre", 150*time.Millisecond, "", "")
	var modes []os.FileMode
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw := duringRequest(t, f, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "shadowDirOff": true}, func() {
		for _, name := range entriesWith(tmp, includeTempPrefix) {
			if fi, err := os.Lstat(filepath.Join(tmp, name, includePatternsName)); err == nil {
				modes = append(modes, fi.Mode().Perm())
			}
		}
	})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if len(modes) == 0 {
		t.Fatal("the test never saw the pattern file")
	}
	if i := slices.IndexFunc(modes, func(m os.FileMode) bool { return m != 0o644 }); i >= 0 {
		t.Errorf("the pattern file has mode %o, want 0644", modes[i])
	}
	if got := entriesWith(tmp, includeTempPrefix); len(got) != 0 {
		t.Errorf("after the create the temporary folder holds %v, want no such folder", got)
	}
}

// TestShutdownBeforeTheRepoFolderLeavesNoFolder: the shutdown removes the
// folder of the daemon after a request passed the test for a closed daemon and
// before it makes the folder of its repository. The request then makes no
// folder: the mkdir goes through the root at the folder of the daemon and
// makes no parent. The reply is the refusal of a folder that cannot be made.
// That state is not measured on 5fd08069, and no client gets the reply.
func TestShutdownBeforeTheRepoFolderLeavesNoFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	old := shadowRepoBeforeMkdir
	shadowRepoBeforeMkdir = f.s.gitShadow.close
	t.Cleanup(func() { shadowRepoBeforeMkdir = old })
	raw, _ := f.info(t, f.repo, nil)
	want := "cannot write its copy of the repository's git config: mkdir " + filepath.Join(dir, shadowRepoName(f.gitDir)) + ": no such file or directory"
	if !strings.Contains(raw, want) {
		t.Errorf("git.info = %s\nwant the cause %s", raw, want)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder of the daemon after the shutdown: %v, want none", err)
	}
	if got := daemonFolders(t, f.run); len(got) != 0 {
		t.Errorf("git-shadow holds %v after the shutdown, want nothing", got)
	}
}
