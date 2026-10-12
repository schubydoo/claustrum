//go:build unix

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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

// TestWorktreesFolderOfASharedRepository: the folder `worktrees` that the
// daemon makes before the `worktree add` call is made with mode 0777 before
// the umask. With a `core.sharedRepository` value other than `umask` it then
// gets one mode call. The registration that git makes in it takes the
// set-group-ID bit from it. Each row is a row of group P5 of
// val-s3p2b2-6-linux (Linux VM, 5fd08069): section 0.2 item 6 of its report
// has the table. Cell H3 of val-s3p2b2-5-linux has the row `group` too.
func TestWorktreesFolderOfASharedRepository(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the rows are Linux cells")
	}
	for _, row := range []struct {
		value               string
		umask               int
		worktrees, registry os.FileMode
	}{
		{"group", 0o022, 0o2775, 0o2755},
		{"true", 0o022, 0o2775, 0o2755},
		{"all", 0o022, 0o2775, 0o2755},
		{"world", 0o022, 0o2775, 0o2755},
		{"everybody", 0o022, 0o2775, 0o2755},
		{"0660", 0o022, 0o2770, 0o2755},
		{"0640", 0o022, 0o2750, 0o2755},
		{"umask", 0o022, 0o755, 0o755},
		{"", 0o022, 0o755, 0o755},
		{"", 0o002, 0o775, 0o775},
		{"0640", 0o002, 0o2750, 0o2775},
	} {
		t.Run(row.value+" umask "+strconv.FormatInt(int64(row.umask), 8), func(t *testing.T) {
			f := newSeededShadowFixture(t)
			dropFixtureWorktree(t, f)
			if row.value != "" {
				runGit(t, f.repo, "config", "core.sharedRepository", row.value)
			}
			worktrees := filepath.Join(f.gitDir, worktreesSubdir)
			if _, err := os.Lstat(worktrees); !os.IsNotExist(err) {
				t.Fatalf("the fixture still has a worktrees folder: %v", err)
			}
			useUmask(t, row.umask)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}); !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			for name, want := range map[string]os.FileMode{worktrees: row.worktrees, filepath.Join(worktrees, "w1"): row.registry} {
				fi, err := os.Lstat(name)
				if err != nil {
					t.Fatal(err)
				}
				got := fi.Mode().Perm()
				if fi.Mode()&os.ModeSetgid != 0 {
					got |= 0o2000
				}
				if got != want {
					t.Errorf("%s has mode %o, want %o", name, got, want)
				}
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
