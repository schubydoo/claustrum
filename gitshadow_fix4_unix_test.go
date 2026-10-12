//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// duringRequest sends one request and calls look again and again until the
// reply is there. It returns the reply. look runs at least one time after the
// request started.
func duringRequest(t *testing.T, f shadowFixture, method string, params map[string]any, look func()) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- dispatchRaw(t, f.s, rpcLine(t, method, params)) }()
	for {
		select {
		case raw := <-done:
			return raw
		default:
			look()
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// entriesWith are the entries of dir whose name starts with prefix.
func entriesWith(dir, prefix string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestShadowIncludeStepEntries: during the include step of a create the
// pattern file has mode 0644, and the index folder of the checkout is still
// there. 5fd08069 keeps `.index-<n>` until the reply, and its `patterns` file
// is `f 0644 11` (val-s3p2b2-3-linux2 rows D2 and D3, Linux VM, and
// val-s3p2b2-3-macos rows D1 and D2, macOS VM). The stub holds each `ls-files`
// call, and the test looks at the folder of the daemon in that time.
func TestShadowIncludeStepEntries(t *testing.T) {
	f := newSeededShadowFixture(t)
	includeFixture(t, f.repo)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	slowGit(t, "ls-files", "pre", 150*time.Millisecond, "", "")
	var modes []os.FileMode
	withIndex := 0
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw := duringRequest(t, f, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}, func() {
		for _, name := range entriesWith(dir, shadowIncludeDirPrefix) {
			fi, err := os.Lstat(filepath.Join(dir, name, includePatternsName))
			if err != nil {
				continue
			}
			modes = append(modes, fi.Mode().Perm())
			if len(entriesWith(dir, shadowIndexDirPrefix)) == 1 {
				withIndex++
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
	if withIndex != len(modes) {
		t.Errorf("the index folder was there at %d of %d looks at the pattern file, want all", withIndex, len(modes))
	}
	if got, want := entryNames(t, dir), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("after the create the folder of the daemon holds %v, want %v", got, want)
	}
}

// TestShadowStatusFolderComesBeforeTheVersionCall: the temporary git folder of
// a first git.status is there while the one-time `--attr-source=… version`
// call runs (val-s3p2b2-3-linux2 row D4: 6 of 6 on 5fd08069, Linux VM).
func TestShadowStatusFolderComesBeforeTheVersionCall(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	resetAttr := func() {
		attrSourceOnce = sync.Once{}
		attrSourceOK = false
	}
	resetAttr()
	t.Cleanup(resetAttr)
	marker := filepath.Join(t.TempDir(), "version-ran")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "version")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION2", "touch")
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF2", marker)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", "200")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "0")
	seen, held := 0, 0
	raw := duringRequest(t, f, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}, func() {
		if _, err := os.Lstat(marker); err != nil {
			return
		}
		held++
		if len(entriesWith(dir, shadowStatusDirPrefix)) == 1 {
			seen++
		}
		_ = os.Remove(marker)
	})
	if !strings.Contains(raw, `"isRepo"`) {
		t.Fatalf("git.status = %s", raw)
	}
	if held == 0 {
		t.Fatal("the test never saw the held version call")
	}
	if seen == 0 {
		t.Errorf("the temporary git folder was not there while the version call ran (%d looks)", held)
	}
}

// TestShadowHeadThatIsALink: a HEAD that is a symbolic link to a branch ref
// gets a copy that is a plain file with the text `ref: <target>` and a newline,
// and the methods answer normally. 5fd08069 writes `ref: refs/heads/main`, 21
// bytes, mode 0600 (cells N4-headlink of val-s3p2b2-3-linux2 and
// val-s3p2b2-3-macos, Linux and macOS VMs). A link with another target is not
// measured: the request is refused.
func TestShadowHeadThatIsALink(t *testing.T) {
	f := newSeededShadowFixture(t)
	head := filepath.Join(f.gitDir, "HEAD")
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("refs/heads/main", head); err != nil {
		t.Fatal(err)
	}
	raw, _ := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true,"repo":"repo","branch":"main"`) || !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	copyOf := filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "HEAD")
	fi, err := os.Lstat(copyOf)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() != 21 {
		t.Fatalf("the copy of HEAD: %v, %v, want a plain file of mode 0600 with 21 bytes", fi, err)
	}
	if got := mustRead(t, copyOf); got != "ref: refs/heads/main\n" {
		t.Errorf("the copy of HEAD = %q", got)
	}
	if raw, _ := f.call(t, "git.list_branches", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true,"branches":["main","wr"]`) {
		t.Errorf("git.list_branches = %s", raw)
	}

	// A link with a target outside refs/heads/ has no row: no copy, a refusal.
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.gitDir, "refs", "other"), mustRead(t, filepath.Join(f.gitDir, "refs", "heads", "main")))
	if err := os.Symlink("refs/other", head); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, "the HEAD of the repository cannot be read") {
		t.Errorf("git.info with a link to refs/other = %s", raw)
	}
}

// TestShadowAddMakesTheWorktreesFolder: in a repository with no `worktrees`
// folder the daemon makes it, empty and with mode 0755, before the `worktree
// add` call, and it stays after an add that fails. 5fd08069 does so in 6 of 6
// runs (cell N2 of val-s3p2b2-3-linux, Linux VM, and of val-s3p2b2-3-macos,
// macOS VM). The stub fails the add, so git never makes the folder here. With
// the member no folder is made (89cb6289 and the build before make none).
func TestShadowAddMakesTheWorktreesFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	worktrees := filepath.Join(f.gitDir, "worktrees")
	_ = os.Remove(worktrees)
	if _, err := os.Lstat(worktrees); !os.IsNotExist(err) {
		t.Fatalf("the fixture still has a worktrees folder: %v", err)
	}
	slowGit(t, "worktree,add", "fail", 0, "fatal: forced failure by the test wrapper\n", "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	create := func(extra map[string]any) string {
		p := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
		for k, v := range extra {
			p[k] = v
		}
		raw, _ := f.call(t, "git.worktree_create", p)
		if !strings.Contains(raw, `"error":"git worktree add failed: fatal: forced failure by the test wrapper","errorCode":"worktree_add_failed"`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		return raw
	}
	create(map[string]any{"shadowDirOff": true})
	if _, err := os.Lstat(worktrees); !os.IsNotExist(err) {
		t.Errorf("with the member the worktrees folder: %v, want none", err)
	}
	create(nil)
	fi, err := os.Lstat(worktrees)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("the worktrees folder after the failed add: %v, %v, want a folder of mode 0755", fi, err)
	}
	if got := entryNames(t, worktrees); len(got) != 0 {
		t.Errorf("the worktrees folder holds %v, want nothing", got)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the new worktree folder after the failed add: %v, want it gone", err)
	}
}

// TestRepointWorktreeGitFile: the `.git` file of a new worktree is replaced by
// a new file only when its text is one `gitdir:` line that names a
// registration through the folder of the copy. 5fd08069 replaces that text on
// a Linux VM and leaves the text of git 2.50.1 on a macOS VM (cells N1 of
// val-s3p2b2-3-linux and val-s3p2b2-3-macos).
func TestRepointWorktreeGitFile(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	u := gitUse{folder: filepath.Join(base, "S"), repoDir: filepath.Join(base, "S", "R"), gitDir: filepath.Join(base, "repo", ".git")}
	viaCopy := "gitdir: " + u.repoDir + "/worktrees/w1\n"
	inRepo := "gitdir: " + u.gitDir + "/worktrees/w1\n"
	for _, row := range []struct {
		name, text, want string
		otherLeaf        bool
	}{
		{"through the copy", viaCopy, inRepo, false},
		{"in the repository already", inRepo, inRepo, false},
		{"a name with a separator", "gitdir: " + u.repoDir + "/worktrees/a/b\n", "", false},
		{"the name ..", "gitdir: " + u.repoDir + "/worktrees/..\n", "", false},
		{"no newline", strings.TrimSuffix(viaCopy, "\n"), "", false},
		{"two lines", viaCopy + "x\n", "", false},
		{"another folder", "gitdir: " + base + "/elsewhere/worktrees/w1\n", "", false},
		{"a leaf that the create did not make", viaCopy, "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			leaf := filepath.Join(t.TempDir(), "leaf")
			if err := os.Mkdir(leaf, 0o755); err != nil {
				t.Fatal(err)
			}
			cp := checkpointCreatedWorktree(leaf)
			defer cp.release()
			if row.otherLeaf {
				// Another folder stands at the path of the leaf.
				if err := os.Rename(leaf, leaf+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(leaf, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(leaf, ".git")
			mustWrite(t, file, row.text)
			before := pinnedInfo(t, file)
			repointWorktreeGitFile(u, leaf, cp)
			want := row.want
			if want == "" {
				want = row.text
			}
			if got := mustRead(t, file); got != want {
				t.Errorf("the .git file = %q, want %q", got, want)
			}
			if replaced := !os.SameFile(before, pinnedInfo(t, file)); replaced != (want != row.text) {
				t.Errorf("the .git file is a new file: %v, want %v", replaced, want != row.text)
			}
		})
	}
}
