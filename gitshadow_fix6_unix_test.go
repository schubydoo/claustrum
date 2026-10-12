//go:build unix

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestIncludeVersionCallOncePerDaemon: the `git version` call of the include
// step runs in the first create of a daemon and not in the second. On 5fd08069
// the counts per create are 1 and 0 in 4 of 4 runs (cell v5-create of
// val-s3p2b2-5-linux2, Linux VM). With the member a second create is not
// measured: each create makes the call, as before.
func TestIncludeVersionCallOncePerDaemon(t *testing.T) {
	for _, row := range []struct {
		name  string
		extra map[string]any
		want  []int
	}{
		{"no member", nil, []int{1, 0}},
		{"with the member (not measured)", map[string]any{"shadowDirOff": true}, []int{1, 1}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			includeFixture(t, f.repo)
			wd := daemonWorkDir(t)
			for i, branch := range []string{"w1", "w2"} {
				params := map[string]any{"baseRepo": f.repo, "branchName": branch, "worktreePath": filepath.Join(f.repo, ".claude", "worktrees", branch)}
				for k, v := range row.extra {
					params[k] = v
				}
				raw, calls := f.call(t, "git.worktree_create", params)
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("create %d = %s", i+1, raw)
				}
				got := 0
				for _, c := range calls {
					if c.version() && c.cwd == wd {
						got++
					}
				}
				if got != row.want[i] {
					t.Errorf("create %d made %d version calls, want %d", i+1, got, row.want[i])
				}
				if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", branch, "secret.env")); err != nil {
					t.Errorf("create %d: the included file: %v", i+1, err)
				}
			}
		})
	}
}

// TestRemoveReadsARefusedConfigTwice: a remove in a repository with a refused
// config makes the read of the config two times. Each other method makes it
// one time. After a read with a syntax error one plain `git version` call
// follows, each time. On 5fd08069 the calls per request are 1, 1, 1, 1 and 2
// with format version 3. With a syntax error they are 2, 2, 2, 2 and 4. The
// cells are x9-v3-own and x9-syntax-own of val-s3p2b2-5-linux2 (Linux VM), and
// the rows E1 and E1b of val-s3p2b2-5-macos (macOS VM).
func TestRemoveReadsARefusedConfigTwice(t *testing.T) {
	for _, row := range []struct {
		name, config string
		version      bool
	}{
		{"format version 3", "[core]\n\trepositoryformatversion = 3\n\tfilemode = true\n\tbare = false\n", false},
		{"a syntax error", "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n[alias]\n\tx = \"unterminated\n", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			setRepoConfig(t, f.gitDir, row.config)
			for _, m := range f.fiveRequests(nil) {
				raw, calls := f.call(t, m.method, m.params)
				if !strings.Contains(raw, "this repository's git config cannot be used: ") {
					t.Fatalf("%s = %s", m.method, raw)
				}
				var got []string
				for _, c := range calls {
					switch {
					case c.configRead():
						got = append(got, "read")
					case c.plainVersion(t):
						got = append(got, "version")
					case c.isProbe() || c.excludesRead():
					default:
						t.Errorf("%s made the call %q, want the reads only", m.method, c.argv)
					}
				}
				want := []string{"read"}
				if row.version {
					want = append(want, "version")
				}
				if m.method == "git.worktree_remove" {
					want = append(want, want...)
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s made %q, want %q", m.method, got, want)
				}
			}
			if _, err := os.Stat(f.wt); err != nil {
				t.Errorf("the worktree of the fixture: %v", err)
			}
		})
	}
}

// TestWorktreesFolderOfASharedRepository: the repository has
// `core.sharedRepository = group`. The folder `worktrees` that the daemon makes
// before the `worktree add` call then has the write bit of the group and the
// set-group-ID bit. On 5fd08069 it is `d 2775`. The registration that git makes
// in it is `d 2755` (cell H3 of val-s3p2b2-5-linux, Linux VM, 2 of 2 runs).
// Every other value of the key has no row: the folder keeps the mode of its
// mkdir.
func TestWorktreesFolderOfASharedRepository(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the row is a Linux cell")
	}
	for _, row := range []struct {
		value string
		group bool
	}{{"group", true}, {"true", false}} {
		t.Run(row.value, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			dropFixtureWorktree(t, f)
			runGit(t, f.repo, "config", "core.sharedRepository", row.value)
			worktrees := filepath.Join(f.gitDir, worktreesSubdir)
			if _, err := os.Lstat(worktrees); !os.IsNotExist(err) {
				t.Fatalf("the fixture still has a worktrees folder: %v", err)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}); !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			for _, name := range []string{worktrees, filepath.Join(worktrees, "w1")} {
				fi, err := os.Lstat(name)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode()&os.ModeSetgid != 0; got != row.group {
					t.Errorf("%s has the set-group-ID bit: %v, want %v (mode %v)", name, got, row.group, fi.Mode())
				}
			}
			fi, err := os.Lstat(worktrees)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm()&0o020 != 0; row.group && !got {
				t.Errorf("the worktrees folder has mode %o, want the write bit of the group", fi.Mode().Perm())
			}
		})
	}
}

// TestProbeFolderOrder: the entries of `.probe` are made in the order `refs`,
// `objects`, `info`, `config`, `HEAD`. On 5fd08069 the birth times have that
// order in 14 of 14 folders (row E2 of val-s3p2b2-5-macos, macOS VM). A folder
// that takes no new entry names the first entry that is missing. A `HEAD` that
// cannot be replaced shows that the config came before it.
func TestProbeFolderOrder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	order := []string{"refs", "objects", "info"}
	for i, want := range order {
		dir := t.TempDir()
		probe := filepath.Join(dir, shadowProbeName)
		for _, made := range order[:i] {
			if err := os.MkdirAll(filepath.Join(probe, made), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(probe, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(probe, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(probe, 0o700) })
		err := makeProbeFolder(dir)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("with %v there the error is %v, want one that names %s", order[:i], err, want)
		}
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, shadowProbeName)
	if err := os.MkdirAll(filepath.Join(probe, "HEAD", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeProbeFolder(dir); err == nil {
		t.Fatal("a HEAD that is a folder with content was replaced")
	}
	if got, err := os.ReadFile(filepath.Join(probe, "config")); err != nil || string(got) != probeConfigText {
		t.Errorf("the config before a HEAD that cannot be written = %q, %v, want it there", got, err)
	}
}
