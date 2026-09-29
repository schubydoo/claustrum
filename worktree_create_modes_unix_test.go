//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// createModesRepo copies a fresh repository with one commit into <base>/repo.
// base is resolved, so the texts use the spelling the daemon sees (macOS /var).
func createModesRepo(t *testing.T) (base, repo string) {
	t.Helper()
	requireGit(t)
	base = resolveTestRoot(t, t.TempDir())
	repo = filepath.Join(base, "repo")
	copyFixtureTemplate(t, "worktree-create-modes", repo, func(t *testing.T, dir string) {
		runGit(t, dir, "init", "-q", "-b", "main")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	})
	return base, repo
}

// mkdirFailedFrame is the whole frame of a parent-step failure.
func mkdirFailedFrame(t *testing.T, text string) string {
	t.Helper()
	q, err := json.Marshal("failed to create parent directory: " + text)
	if err != nil {
		t.Fatal(err)
	}
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + string(q) + `,"errorCode":"mkdir_failed"}}`
}

// chmodForTest sets mode on p and restores 0755 at cleanup, so TempDir can delete it.
func chmodForTest(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
}

func mkdirForTest(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// requireAbsent fails the test for each path that exists.
func requireAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists after the failed create", p)
		}
	}
}

// The parent step of git.worktree_create fails with the texts of f6010b97 (Linux
// and macOS VMs), and it creates nothing. In the repo, a component that is a file
// reads "<path> is not a directory", and a denied create names only the component.
func TestWorktreeCreateParentFailureTextsInRepo(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string)
		text  func(repo string) string
		// absent lists paths, relative to repo, that the failed create must not make.
		absent []string
		denied bool
	}{
		{
			name:  "F1_claude_is_a_file",
			setup: func(t *testing.T, repo string) { writeFile(t, filepath.Join(repo, ".claude"), "x", 0o644) },
			text:  func(repo string) string { return filepath.Join(repo, ".claude") + " is not a directory" },
		},
		{
			name:   "F2_repo_unwritable",
			setup:  func(t *testing.T, repo string) { chmodForTest(t, repo, 0o555) },
			text:   func(string) string { return "mkdirat .claude: permission denied" },
			absent: []string{".claude"},
			denied: true,
		},
		{
			name: "F3_claude_unwritable",
			setup: func(t *testing.T, repo string) {
				mkdirForTest(t, filepath.Join(repo, ".claude"))
				chmodForTest(t, filepath.Join(repo, ".claude"), 0o555)
			},
			text:   func(string) string { return "mkdirat worktrees: permission denied" },
			absent: []string{".claude/worktrees"},
			denied: true,
		},
		{
			name: "F4_worktrees_is_a_file",
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, ".claude", "worktrees"), "x", 0o644)
			},
			text: func(repo string) string {
				return filepath.Join(repo, ".claude", "worktrees") + " is not a directory"
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.denied && os.Geteuid() == 0 {
				t.Skip("root ignores the permission bits this case relies on")
			}
			_, repo := createModesRepo(t)
			c.setup(t, repo)
			wp := filepath.Join(repo, ".claude", "worktrees", "wt")
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp}))
			if want := mkdirFailedFrame(t, c.text(repo)); raw != want {
				t.Errorf("create frame\n got %s\nwant %s", raw, want)
			}
			abs := []string{wp}
			for _, a := range c.absent {
				abs = append(abs, filepath.Join(repo, a))
			}
			requireAbsent(t, abs...)
		})
	}
}

// With a worktreeRoot, a root or a path above it that is a file reads "<path>: not
// a directory". A denied create of the first missing directory names that
// directory. Nothing is created. Measured against f6010b97 on Linux and macOS VMs.
func TestWorktreeCreateParentFailureTextsExternal(t *testing.T) {
	cases := []struct {
		name string
		// rootRel is the worktreeRoot, relative to the fixture base F.
		rootRel string
		setup   func(t *testing.T, f string)
		text    func(f string) string
		// absent lists paths, relative to F, that the failed create must not make.
		absent []string
		denied bool
	}{
		{
			name:    "F5_root_parent_unwritable",
			rootRel: "p/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "p"))
				chmodForTest(t, filepath.Join(f, "p"), 0o555)
			},
			text:   func(string) string { return "mkdirat R: permission denied" },
			absent: []string{"p/R"},
			denied: true,
		},
		{
			name:    "F6_root_unwritable",
			rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R"))
				chmodForTest(t, filepath.Join(f, "R"), 0o555)
			},
			text:   func(string) string { return "mkdirat cp: permission denied" },
			absent: []string{"R/cp"},
			denied: true,
		},
		{
			name:    "F7_root_is_a_file",
			rootRel: "R",
			setup:   func(t *testing.T, f string) { writeFile(t, filepath.Join(f, "R"), "x", 0o644) },
			text:    func(f string) string { return filepath.Join(f, "R") + ": not a directory" },
		},
		{
			name:    "F11_ancestor_is_a_file",
			rootRel: "a/b/R",
			setup:   func(t *testing.T, f string) { writeFile(t, filepath.Join(f, "a"), "x", 0o644) },
			text:    func(f string) string { return filepath.Join(f, "a") + ": not a directory" },
		},
		{
			name:    "F12_ancestor_unwritable",
			rootRel: "a/b/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a"))
				chmodForTest(t, filepath.Join(f, "a"), 0o555)
			},
			text:   func(string) string { return "mkdirat b: permission denied" },
			absent: []string{"a/b"},
			denied: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.denied && os.Geteuid() == 0 {
				t.Skip("root ignores the permission bits this case relies on")
			}
			base, repo := createModesRepo(t)
			requireTempOutsideCheckout(t, base)
			f := filepath.Join(base, "F")
			mkdirForTest(t, f)
			c.setup(t, f)
			root := filepath.Join(f, c.rootRel)
			wp := filepath.Join(root, "cp", "w1")
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp, "worktreeRoot": root}))
			if want := mkdirFailedFrame(t, c.text(f)); raw != want {
				t.Errorf("create frame\n got %s\nwant %s", raw, want)
			}
			abs := []string{wp}
			for _, a := range c.absent {
				abs = append(abs, filepath.Join(f, a))
			}
			requireAbsent(t, abs...)
		})
	}
}

// permOf returns the permission bits of p.
func permOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A successful create makes the leaf with mode 0777, the in-repo parents with
// 0755, and every missing external parent with 0700. The umask applies, so the
// test sets it to 0. An existing directory keeps its mode. The umask is
// process-wide, so this test does not run in parallel.
func TestWorktreeCreateDirModes(t *testing.T) {
	base, repo := createModesRepo(t)
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	t.Run("in_repo", func(t *testing.T) {
		wp := filepath.Join(repo, ".claude", "worktrees", "wt")
		raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "m1", "worktreePath": wp}))
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("create = %s, want success", raw)
		}
		for p, want := range map[string]os.FileMode{
			filepath.Join(repo, ".claude"):              0o755,
			filepath.Join(repo, ".claude", "worktrees"): 0o755,
			wp: 0o777,
		} {
			if got := permOf(t, p); got != want {
				t.Errorf("mode of %s = %#o, want %#o", p, got, want)
			}
		}
	})

	t.Run("external", func(t *testing.T) {
		requireTempOutsideCheckout(t, base)
		kept := filepath.Join(base, "kept")
		if err := os.Mkdir(kept, 0o751); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(kept, "x", "R")
		wp := filepath.Join(root, "cp", "w1")
		raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "m2", "worktreePath": wp, "worktreeRoot": root}))
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("create = %s, want success", raw)
		}
		for p, want := range map[string]os.FileMode{
			kept:                      0o751,
			filepath.Join(kept, "x"):  0o700,
			root:                      0o700,
			filepath.Join(root, "cp"): 0o700,
			wp:                        0o777,
		} {
			if got := permOf(t, p); got != want {
				t.Errorf("mode of %s = %#o, want %#o", p, got, want)
			}
		}
	})
}

// An external create leaves an existing .claude-managed-worktrees marker as it
// is, content and mode. f6010b97 does the same on Linux and macOS VMs.
func TestWorktreeCreateKeepsExistingMarker(t *testing.T) {
	base, repo := createModesRepo(t)
	requireTempOutsideCheckout(t, base)
	root := filepath.Join(base, "mine")
	mkdirForTest(t, filepath.Join(root, "d"))
	marker := filepath.Join(root, "d", managedWorktreesMarker)
	const custom = "kept by the operator\n"
	if err := os.WriteFile(marker, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	wp := filepath.Join(root, "d", "wt")
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "k", "worktreePath": wp, "worktreeRoot": root}))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s, want success", raw)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != custom {
		t.Errorf("marker = %q, want it kept as %q", got, custom)
	}
	if m := permOf(t, marker); m != 0o600 {
		t.Errorf("marker mode = %#o, want 0600 kept", m)
	}
}
