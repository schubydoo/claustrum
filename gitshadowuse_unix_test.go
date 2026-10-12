//go:build unix

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// argOf is the first argument or variable of the calls that starts with
// prefix, without the prefix, or "".
func argOf(calls []envCall, prefix string) string {
	for _, c := range calls {
		for _, a := range append(slices.Clone(c.argv), c.env...) {
			if v, ok := strings.CutPrefix(a, prefix); ok {
				return v
			}
		}
	}
	return ""
}

// includeFixture gives the repository of f a `.worktreeinclude` file that
// names one ignored file.
func includeFixture(t *testing.T, repo string) {
	t.Helper()
	for name, text := range map[string]string{".gitignore": "secret.env\n", ".worktreeinclude": "secret.env\n", "secret.env": "k=v\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestShadowRequestEntries pins where the short-lived entries of a request
// are. It also pins that each is gone at the reply, after a good request and
// after a failed one. In the state shadow 5fd08069 has them in the folder of the
// daemon: `.gitdir-<n>` for a git.status (s3p2-linux cell B3a), `.index-<n>`
// for a checkout and `.worktreeinclude-<n>/patterns` for the include step
// (val-s3p2b1-linux cells A3-create-plain and A3-create-include, Linux VM). At
// the reply the folder of the daemon holds none of them. With
// "shadowDirOff":true they are in the temporary folder (rows D5 and D6 of that
// round), and nothing is left there.
func TestShadowRequestEntries(t *testing.T) {
	leafOf := func(f shadowFixture) string { return filepath.Join(f.repo, ".claude", "worktrees", "w1") }
	create := func(f shadowFixture, extra map[string]any) map[string]any {
		p := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leafOf(f)}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	left := func(t *testing.T, f shadowFixture) []string {
		t.Helper()
		return entryNames(t, daemonFolder(t, f.run))
	}

	t.Run("state shadow, good requests", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		includeFixture(t, f.repo)
		raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("git.status = %s", raw)
		}
		dir := daemonFolder(t, f.run)
		entries := []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}
		if got := argOf(calls, "--git-dir="+dir+"/"); !regexp.MustCompile(`^\.gitdir-[0-9]+$`).MatchString(got) {
			t.Errorf("the temporary git folder of git.status = %q, want .gitdir-<n> in %s", got, dir)
		}
		if got := left(t, f); !slices.Equal(got, entries) {
			t.Errorf("after git.status the folder of the daemon holds %v, want %v", got, entries)
		}

		raw, calls = f.call(t, "git.worktree_create", create(f, nil))
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		if got := argOf(calls, "GIT_INDEX_FILE="+dir+"/"); !regexp.MustCompile(`^\.index-[0-9]+/index$`).MatchString(got) {
			t.Errorf("GIT_INDEX_FILE of the checkout = %q, want .index-<n>/index in %s", got, dir)
		}
		if got := argOf(calls, "--exclude-from="+dir+"/"); !regexp.MustCompile(`^\.worktreeinclude-[0-9]+/patterns$`).MatchString(got) {
			t.Errorf("the pattern file of the include step = %q, want .worktreeinclude-<n>/patterns in %s", got, dir)
		}
		if got := left(t, f); !slices.Equal(got, entries) {
			t.Errorf("after git.worktree_create the folder of the daemon holds %v, want %v", got, entries)
		}
		if b, err := os.ReadFile(filepath.Join(leafOf(f), "secret.env")); err != nil || string(b) != "k=v\n" {
			t.Errorf("the included file in the new worktree: %q, %v", b, err)
		}
		// The new worktree is a worktree of the repository, with its index in its
		// registration and a `.git` file that names that registration.
		reg := filepath.Join(f.gitDir, "worktrees", "w1")
		if b, err := os.ReadFile(filepath.Join(leafOf(f), ".git")); err != nil || strings.TrimSpace(string(b)) != "gitdir: "+reg {
			t.Errorf("the .git file of the new worktree = %q, %v\nwant gitdir: %s", b, err, reg)
		}
		if _, err := os.Stat(filepath.Join(reg, "index")); err != nil {
			t.Errorf("the index of the new worktree: %v", err)
		}
	})

	t.Run("state shadow, failed status", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		slowGit(t, "status,--porcelain", "fail", 0, "fatal: forced failure\n", "")
		raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
		if !strings.Contains(raw, `"error":{"code":-32603,"message":"exit status 1"}`) {
			t.Fatalf("git.status = %s", raw)
		}
		if got := argOf(calls, "--git-dir="+daemonFolder(t, f.run)+"/"); !strings.HasPrefix(got, ".gitdir-") {
			t.Errorf("the temporary git folder of git.status = %q", got)
		}
		if got, want := left(t, f), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
			t.Errorf("after the failed git.status the folder of the daemon holds %v, want %v", got, want)
		}
	})

	t.Run("state shadow, failed checkout", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		slowGit(t, "read-tree", "fail", 0, "fatal: forced failure\n", "")
		raw, calls := f.call(t, "git.worktree_create", create(f, nil))
		if !strings.Contains(raw, `"error":"git worktree add failed (checkout): fatal: forced failure"`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		if got := argOf(calls, "GIT_INDEX_FILE="+daemonFolder(t, f.run)+"/"); !strings.HasPrefix(got, ".index-") {
			t.Errorf("GIT_INDEX_FILE of the checkout = %q", got)
		}
		if got, want := left(t, f), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
			t.Errorf("after the failed checkout the folder of the daemon holds %v, want %v", got, want)
		}
		if _, err := os.Lstat(leafOf(f)); !os.IsNotExist(err) {
			t.Errorf("the worktree after the failed checkout: %v, want it gone", err)
		}
	})

	t.Run("with the member", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		includeFixture(t, f.repo)
		tmp := resolveTestRoot(t, t.TempDir())
		t.Setenv("TMPDIR", tmp)
		resetUserExcludesCache(t)
		off := map[string]any{"shadowDirOff": true}
		raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo, "shadowDirOff": true})
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("git.status = %s", raw)
		}
		if got := argOf(calls, "--git-dir="+tmp+"/"); !regexp.MustCompile(`^claude-ssh-gitdir-[0-9]+$`).MatchString(got) {
			t.Errorf("the temporary git folder of git.status = %q, want claude-ssh-gitdir-<n> in %s", got, tmp)
		}
		raw, calls = f.call(t, "git.worktree_create", create(f, off))
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("git.worktree_create = %s", raw)
		}
		if got := argOf(calls, "GIT_INDEX_FILE="+tmp+"/"); !regexp.MustCompile(`^claude-ssh-index-[0-9]+/index$`).MatchString(got) {
			t.Errorf("GIT_INDEX_FILE of the checkout = %q, want claude-ssh-index-<n>/index in %s", got, tmp)
		}
		if got := argOf(calls, "--exclude-from="+tmp+"/"); !regexp.MustCompile(`^claude-ssh-worktreeinclude-[0-9]+/patterns$`).MatchString(got) {
			t.Errorf("the pattern file of the include step = %q, want claude-ssh-worktreeinclude-<n>/patterns in %s", got, tmp)
		}
		if got := entryNames(t, tmp); len(got) != 0 {
			t.Errorf("the temporary folder after the requests holds %v, want nothing", got)
		}
		if names := daemonFolders(t, f.run); len(names) != 0 {
			t.Errorf("git-shadow with the member on every request holds %v", names)
		}
		if b, err := os.ReadFile(filepath.Join(leafOf(f), "secret.env")); err != nil || string(b) != "k=v\n" {
			t.Errorf("the included file in the new worktree: %q, %v", b, err)
		}
	})
}

// TestShadowFilterRuleReadsTheRepositoryConfig pins the filter rule of the
// checkout in the state shadow, where git reads the daemon's copy. The rule
// still sees a filter that only the repository's own config names. The create is refused
// with the text of that rule and the filter program does not start, as on
// 5fd08069 (s3p2-linux2 cell G2, Linux VM). The read-tree call carries the
// three pairs of the filter before the lfs.storage item (the argv of that
// cell).
func TestShadowFilterRuleReadsTheRepositoryConfig(t *testing.T) {
	f := newSeededShadowFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(f.repo, ".gitattributes"), []byte("*.txt filter=m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "add", ".gitattributes", "a.txt")
	runGit(t, f.repo, "commit", "-q", "-m", "filter")
	runGit(t, f.repo, "config", "filter.m.smudge", "sh -c 'touch "+marker+"; cat'")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"errorCode":"repository_only_filter"`) || !strings.Contains(raw, `Claude doesn't run the \"m\" git filter`) {
		t.Fatalf("git.worktree_create = %s\nwant the refusal of the filter rule", raw)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the filter program ran: %v", err)
	}
	var readTree []string
	for _, c := range calls {
		if slices.Contains(c.argv, "read-tree") {
			readTree = c.argv
		}
	}
	want := []string{"-c", "filter.m.process=", "-c", "filter.m.required=true", "-c", "filter.m.smudge=false",
		"-c", "lfs.storage=" + filepath.Join(f.gitDir, "lfs"), "-c", "core.hooksPath=/dev/null"}
	if len(readTree) < len(want) || !slices.Equal(readTree[:len(want)], want) {
		t.Errorf("the read-tree call starts %q\nwant %q", readTree[:min(len(readTree), len(want))], want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the worktree after the refusal: %v, want it gone", err)
	}
}
