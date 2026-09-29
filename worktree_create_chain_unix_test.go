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

// failFrame is the whole frame of a failed create with the given error and code.
func failFrame(t *testing.T, text, code string) string {
	t.Helper()
	q, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + string(q) + `,"errorCode":"` + code + `"}}`
}

// checkoutRootText is the refusal for a worktree location inside a git checkout.
func checkoutRootText(root, dir string) string {
	return "refusing to create worktree: " + root + " is inside a git checkout (" + dir +
		" has a .git entry); a worktree location must be outside every checkout, so that no " +
		"session working in one can reach it — choose a directory that is not part of any repository"
}

// checkoutDirText is the refusal for a <directory> level that is a git checkout.
func checkoutDirText(dir string) string {
	return "refusing to create worktree: " + dir + " is itself a git checkout (it has a .git " +
		"entry); a worktree location must be outside every checkout"
}

func symlinkForTest(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// chainCase is one external create. rootRel is the worktreeRoot relative to the
// fixture base F. want gives the error text and the errorCode from F.
type chainCase struct {
	name    string
	rootRel string
	// rootSent spells the worktreeRoot as sent. When nil, the root is F/rootRel.
	rootSent func(f string) string
	denied   bool
	setup    func(t *testing.T, f string)
	want     func(f string) (text, code string)
	// absent lists paths, relative to F, that the failed create must not make.
	absent []string
	// kept lists paths, relative to F, that the failed create must leave in place.
	kept []string
}

func runChainCases(t *testing.T, cases []chainCase) {
	t.Helper()
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
			if c.rootSent != nil {
				root = c.rootSent(f)
			}
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp, "worktreeRoot": root}))
			text, code := c.want(f)
			if want := failFrame(t, text, code); raw != want {
				t.Errorf("create frame\n got %s\nwant %s", raw, want)
			}
			abs := []string{}
			for _, a := range c.absent {
				abs = append(abs, filepath.Join(f, a))
			}
			requireAbsent(t, abs...)
			for _, k := range c.kept {
				if _, err := os.Lstat(filepath.Join(f, k)); err != nil {
					t.Errorf("%s is gone after the failed create: %v", k, err)
				}
			}
		})
	}
}

func parentText(s string) (string, string) {
	return "failed to create parent directory: " + s, "mkdir_failed"
}

// The external parent step of git.worktree_create: the chain check, the parent
// mkdir and the marker. f6010b97 sends these texts on Linux and macOS VMs (F14, F16,
// F17, P3, P4, P9, P10, P11, P13, S4).
func TestWorktreeCreateExternalChain(t *testing.T) {
	runChainCases(t, []chainCase{
		{
			name: "F14_dir_is_a_file", rootRel: "R",
			setup: func(t *testing.T, f string) { writeFile(t, filepath.Join(f, "R", "cp"), "x", 0o644) },
			want: func(f string) (string, string) {
				return parentText(filepath.Join(f, "R", "cp") + " is not a directory")
			},
		},
		{
			name: "F17_root_symlink_dangles", rootRel: "R",
			setup: func(t *testing.T, f string) { symlinkForTest(t, filepath.Join(f, "nowhere"), filepath.Join(f, "R")) },
			want: func(f string) (string, string) {
				return parentText("lstat " + filepath.Join(f, "nowhere") + ": no such file or directory")
			},
			absent: []string{"nowhere"},
		},
		{
			name: "P4_root_symlink_dangles_relative", rootRel: "R",
			setup: func(t *testing.T, f string) { symlinkForTest(t, "nowhere", filepath.Join(f, "R")) },
			want: func(f string) (string, string) {
				return parentText("lstat " + filepath.Join(f, "nowhere") + ": no such file or directory")
			},
			absent: []string{"nowhere"},
		},
		{
			name: "P3_root_symlink_to_a_file", rootRel: "R",
			setup: func(t *testing.T, f string) {
				writeFile(t, filepath.Join(f, "file"), "x", 0o644)
				symlinkForTest(t, filepath.Join(f, "file"), filepath.Join(f, "R"))
			},
			want: func(f string) (string, string) { return parentText(filepath.Join(f, "file") + ": not a directory") },
		},
		{
			name: "P9_root_not_searchable", rootRel: "R", denied: true,
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R"))
				chmodForTest(t, filepath.Join(f, "R"), 0o600)
			},
			want: func(f string) (string, string) {
				return parentText("lstat " + filepath.Join(f, "R", ".git") + ": permission denied")
			},
		},
		{
			name: "P10_ancestor_not_searchable", rootRel: "a/b/R", denied: true,
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a", "b", "R"))
				chmodForTest(t, filepath.Join(f, "a"), 0o600)
			},
			want: func(f string) (string, string) {
				return parentText("lstat " + filepath.Join(f, "a", ".git") + ": permission denied")
			},
		},
		{
			name: "P11_dir_not_searchable", rootRel: "R", denied: true,
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R", "cp"))
				chmodForTest(t, filepath.Join(f, "R"), 0o700)
				chmodForTest(t, filepath.Join(f, "R", "cp"), 0o600)
			},
			want: func(string) (string, string) { return parentText("statat cp/.git: permission denied") },
		},
		{
			name: "P13_marker_cannot_be_made", rootRel: "R", denied: true,
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R", "cp"))
				chmodForTest(t, filepath.Join(f, "R", "cp"), 0o500)
			},
			want: func(f string) (string, string) {
				return parentText("cannot mark " + filepath.Join(f, "R", "cp") +
					" as a worktree location: openat .claude-managed-worktrees: permission denied")
			},
			absent: []string{"R/cp/w1", "R/cp/" + managedWorktreesMarker},
		},
		{
			name: "S4_symlinked_root_dir_is_a_file", rootRel: "R",
			setup: func(t *testing.T, f string) {
				writeFile(t, filepath.Join(f, "real", "cp"), "x", 0o644)
				symlinkForTest(t, filepath.Join(f, "real"), filepath.Join(f, "R"))
			},
			want: func(f string) (string, string) {
				return parentText(filepath.Join(f, "real", "cp") + " is not a directory")
			},
		},
		{
			name: "F16_name_too_long", rootRel: "a/" + strings.Repeat("n", 256) + "/R",
			setup: func(*testing.T, string) {},
			want: func(string) (string, string) {
				return parentText("mkdirat a/" + strings.Repeat("n", 256) + ": file name too long")
			},
			kept: []string{"a"},
		},
	})
}

// A .git entry of any kind at or above the root refuses the create with
// unsafe_path, and so does one in the <directory> level. Nothing is created.
// f6010b97 sends these frames on Linux and macOS VMs (Q1 to Q7, Q10, S2, S3, S5,
// S6, G1, G2). The two cases without a case name are not measured.
func TestWorktreeCreateExternalCheckoutRefusal(t *testing.T) {
	rootRefusal := func(rootRel, dirRel string) func(f string) (string, string) {
		return func(f string) (string, string) {
			return checkoutRootText(filepath.Join(f, rootRel), filepath.Join(f, dirRel)), "unsafe_path"
		}
	}
	runChainCases(t, []chainCase{
		{
			name: "G1_git_five_levels_up", rootRel: "a/b/c/d/e/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a", "b", "c", "d", "e", "R"))
				mkdirForTest(t, filepath.Join(f, "a", ".git"))
			},
			want:   rootRefusal("a/b/c/d/e/R", "a"),
			absent: []string{"a/b/c/d/e/R/cp"},
		},
		{
			name: "G2_two_git_dirs_outer_named", rootRel: "a/b/c/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a", "b", "c", "R"))
				mkdirForTest(t, filepath.Join(f, "a", ".git"))
				mkdirForTest(t, filepath.Join(f, "a", "b", ".git"))
			},
			want:   rootRefusal("a/b/c/R", "a"),
			absent: []string{"a/b/c/R/cp"},
		},
		{
			name: "Q1_root_is_a_repo", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R"))
				runGit(t, filepath.Join(f, "R"), "init", "-q")
			},
			want:   rootRefusal("R", "R"),
			absent: []string{"R/cp"},
		},
		{
			name: "Q2_root_has_empty_git_dir", rootRel: "R",
			setup:  func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, "R", ".git")) },
			want:   rootRefusal("R", "R"),
			absent: []string{"R/cp"},
		},
		{
			name: "Q3_root_has_git_file", rootRel: "R",
			setup: func(t *testing.T, f string) {
				writeFile(t, filepath.Join(f, "R", ".git"), "gitdir: /nonexistent", 0o644)
			},
			want:   rootRefusal("R", "R"),
			absent: []string{"R/cp"},
		},
		{
			name: "Q4_ancestor_is_a_repo_below_missing", rootRel: "a/b/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a"))
				runGit(t, filepath.Join(f, "a"), "init", "-q")
			},
			want:   rootRefusal("a/b/R", "a"),
			absent: []string{"a/b"},
		},
		{
			name: "Q5_ancestor_has_empty_git_dir", rootRel: "a/b/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a", ".git"))
				mkdirForTest(t, filepath.Join(f, "a", "b", "R"))
			},
			want:   rootRefusal("a/b/R", "a"),
			absent: []string{"a/b/R/cp"},
		},
		{
			name: "Q7_root_has_dangling_git_symlink", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R"))
				symlinkForTest(t, filepath.Join(f, "gone"), filepath.Join(f, "R", ".git"))
			},
			want:   rootRefusal("R", "R"),
			absent: []string{"R/cp"},
		},
		{
			name: "Q6_dir_has_git", rootRel: "R",
			setup: func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, "R", "cp", ".git")) },
			want: func(f string) (string, string) {
				return checkoutDirText(filepath.Join(f, "R", "cp")), "unsafe_path"
			},
			absent: []string{"R/cp/w1", "R/cp/" + managedWorktreesMarker},
		},
		{
			name: "base_dir_has_empty_git_dir", rootRel: "R",
			setup:  func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, ".git")) },
			want:   rootRefusal("R", "."),
			absent: []string{"R"},
		},
		{
			name: "root_inside_another_git_dir", rootRel: "co/.git/wl",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "co"))
				runGit(t, filepath.Join(f, "co"), "init", "-q")
			},
			want:   rootRefusal("co/.git/wl", "co"),
			absent: []string{"co/.git/wl"},
		},
		{
			name: "Q10_base_dir_is_a_repo_three_levels_up", rootRel: "a/b/R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "a", "b", "R"))
				runGit(t, f, "init", "-q")
			},
			want:   rootRefusal("a/b/R", "."),
			absent: []string{"a/b/R/cp"},
		},
		{
			name: "S2_symlinked_root_names_resolved_dir", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "real", ".git"))
				symlinkForTest(t, filepath.Join(f, "real"), filepath.Join(f, "R"))
			},
			want: func(f string) (string, string) {
				return checkoutRootText(filepath.Join(f, "R"), filepath.Join(f, "real")), "unsafe_path"
			},
		},
		{
			name: "S3_symlinked_root_dir_checkout", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "real", "cp", ".git"))
				symlinkForTest(t, filepath.Join(f, "real"), filepath.Join(f, "R"))
			},
			want: func(f string) (string, string) {
				return checkoutDirText(filepath.Join(f, "real", "cp")), "unsafe_path"
			},
		},
		{
			name: "S5_root_sent_with_trailing_slash", rootRel: "R",
			rootSent: func(f string) string { return filepath.Join(f, "R") + "/" },
			setup:    func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, "R", ".git")) },
			want: func(f string) (string, string) {
				return checkoutRootText(filepath.Join(f, "R")+"/", filepath.Join(f, "R")), "unsafe_path"
			},
		},
		{
			name: "S6_root_sent_with_double_slash", rootRel: "R",
			rootSent: func(f string) string { return f + "//R" },
			setup:    func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, "R", ".git")) },
			want: func(f string) (string, string) {
				return checkoutRootText(f+"//R", filepath.Join(f, "R")), "unsafe_path"
			},
		},
	})
}

// The root-chain checkout test comes before the root refusals. The <directory>
// .git test comes after them, and before the non-empty and "already exists"
// refusals. f6010b97 has this order on Linux and macOS VMs (O1, O3, O4, O6, O8,
// G4, G7).
func TestWorktreeCreateExternalCheckoutOrder(t *testing.T) {
	gitInRoot := func(t *testing.T, f string) { mkdirForTest(t, filepath.Join(f, "R", ".git")) }
	rootRefusal := func(f string) (string, string) {
		return checkoutRootText(filepath.Join(f, "R"), filepath.Join(f, "R")), "unsafe_path"
	}
	runChainCases(t, []chainCase{
		{
			name: "O1_before_writable_root", rootRel: "R",
			setup: func(t *testing.T, f string) {
				gitInRoot(t, f)
				chmodForTest(t, filepath.Join(f, "R"), 0o777)
			},
			want: rootRefusal,
		},
		{
			name: "O6_before_symlinked_dir", rootRel: "R",
			setup: func(t *testing.T, f string) {
				gitInRoot(t, f)
				mkdirForTest(t, filepath.Join(f, "elsewhere"))
				symlinkForTest(t, filepath.Join(f, "elsewhere"), filepath.Join(f, "R", "cp"))
			},
			want: rootRefusal,
		},
		{
			name: "O3_before_non_empty_dir", rootRel: "R",
			setup: func(t *testing.T, f string) {
				gitInRoot(t, f)
				writeFile(t, filepath.Join(f, "R", "cp", "other"), "x", 0o644)
			},
			want: rootRefusal,
		},
		{
			name: "O4_before_existing_leaf", rootRel: "R",
			setup: func(t *testing.T, f string) {
				gitInRoot(t, f)
				mkdirForTest(t, filepath.Join(f, "R", "cp", "w1"))
			},
			want: rootRefusal,
		},
		{
			name: "O8_dir_checkout_before_existing_leaf", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R", "cp", ".git"))
				mkdirForTest(t, filepath.Join(f, "R", "cp", "w1"))
			},
			want: func(f string) (string, string) {
				return checkoutDirText(filepath.Join(f, "R", "cp")), "unsafe_path"
			},
		},
		{
			name: "G7_dir_checkout_with_marker", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R", "cp", ".git"))
				writeFile(t, filepath.Join(f, "R", "cp", managedWorktreesMarker), managedWorktreesMarkerBody, 0o644)
			},
			want: func(f string) (string, string) {
				return checkoutDirText(filepath.Join(f, "R", "cp")), "unsafe_path"
			},
		},
		{
			// The text of the writable-root refusal depends on the group of the temp
			// dir, so the test takes it from worktreeRootShareRefusal.
			name: "G4_writable_root_before_dir_checkout", rootRel: "R",
			setup: func(t *testing.T, f string) {
				mkdirForTest(t, filepath.Join(f, "R", "cp", ".git"))
				chmodForTest(t, filepath.Join(f, "R"), 0o777)
			},
			want: func(f string) (string, string) {
				return worktreeRootShareRefusal(filepath.Join(f, "R")), "unsafe_path"
			},
		},
	})
}

// An existing marker entry of any kind is left as it is, and the create goes on.
// f6010b97 does the same on Linux and macOS VMs (P14, P16, P17, F22).
func TestWorktreeCreateKeepsAnyMarkerEntry(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, base, marker string)
		check func(t *testing.T, base, marker string)
	}{
		{
			name:  "P14_directory",
			setup: func(t *testing.T, _, m string) { mkdirForTest(t, m) },
			check: func(t *testing.T, _, m string) {
				if fi, err := os.Lstat(m); err != nil || !fi.IsDir() {
					t.Errorf("marker directory changed: %v", err)
				}
			},
		},
		{
			name:  "F22_dangling_symlink",
			setup: func(t *testing.T, b, m string) { symlinkForTest(t, filepath.Join(b, "gone"), m) },
			check: func(t *testing.T, b, m string) {
				if got, err := os.Readlink(m); err != nil || got != filepath.Join(b, "gone") {
					t.Errorf("marker link = %q, %v", got, err)
				}
				requireAbsent(t, filepath.Join(b, "gone"))
			},
		},
		{
			name: "P16_symlink_to_a_file",
			setup: func(t *testing.T, b, m string) {
				writeFile(t, filepath.Join(b, "target"), "x\n", 0o644)
				symlinkForTest(t, filepath.Join(b, "target"), m)
			},
			check: func(t *testing.T, b, m string) {
				if got, err := os.ReadFile(filepath.Join(b, "target")); err != nil || string(got) != "x\n" {
					t.Errorf("marker target = %q, %v", got, err)
				}
			},
		},
		{
			name:  "P17_file_mode_0",
			setup: func(t *testing.T, _, m string) { writeFile(t, m, "x\n", 0o000) },
			check: func(t *testing.T, _, m string) {
				fi, err := os.Lstat(m)
				if err != nil || fi.Size() != 2 || fi.Mode().Perm() != 0 {
					t.Errorf("marker changed: %v %v", fi, err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, repo := createModesRepo(t)
			requireTempOutsideCheckout(t, base)
			root := filepath.Join(base, "mine")
			mkdirForTest(t, filepath.Join(root, "d"))
			marker := filepath.Join(root, "d", managedWorktreesMarker)
			c.setup(t, base, marker)
			wp := filepath.Join(root, "d", "wt")
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "k", "worktreePath": wp, "worktreeRoot": root}))
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("create = %s, want success", raw)
			}
			c.check(t, base, marker)
		})
	}
}

// The in-repo parent step: an existing directory without search permission reads
// "statat .: <errno>", and texts name the resolved, cleaned repo path. f6010b97
// sends these on Linux and macOS VMs (F18, P7, P8, F20, S7, S8).
func TestWorktreeCreateInRepoChainTexts(t *testing.T) {
	cases := []struct {
		name   string
		denied bool
		setup  func(t *testing.T, repo string)
		// send gives baseRepo and worktreePath from the fixture.
		send func(base, repo string) (string, string)
		text func(repo string) string
	}{
		{
			name: "P7_claude_not_searchable", denied: true,
			setup: func(t *testing.T, repo string) {
				mkdirForTest(t, filepath.Join(repo, ".claude"))
				chmodForTest(t, filepath.Join(repo, ".claude"), 0o600)
			},
			text: func(string) string { return "statat .: permission denied" },
		},
		{
			name: "P8_worktrees_not_searchable", denied: true,
			setup: func(t *testing.T, repo string) {
				mkdirForTest(t, filepath.Join(repo, ".claude", "worktrees"))
				chmodForTest(t, filepath.Join(repo, ".claude", "worktrees"), 0o600)
			},
			text: func(string) string { return "statat .: permission denied" },
		},
		{
			name: "F18_claude_not_searchable_worktrees_exists", denied: true,
			setup: func(t *testing.T, repo string) {
				mkdirForTest(t, filepath.Join(repo, ".claude", "worktrees"))
				chmodForTest(t, filepath.Join(repo, ".claude"), 0o600)
			},
			text: func(string) string { return "statat .: permission denied" },
		},
		{
			name:  "F20_unclean_path",
			setup: func(t *testing.T, repo string) { writeFile(t, filepath.Join(repo, ".claude"), "x", 0o644) },
			send: func(_, repo string) (string, string) {
				return repo, repo + "//.claude/worktrees/wt"
			},
			text: func(repo string) string { return filepath.Join(repo, ".claude") + " is not a directory" },
		},
		{
			name:  "S8_dot_in_path",
			setup: func(t *testing.T, repo string) { writeFile(t, filepath.Join(repo, ".claude"), "x", 0o644) },
			send: func(_, repo string) (string, string) {
				return repo, repo + "/./.claude/worktrees/wt"
			},
			text: func(repo string) string { return filepath.Join(repo, ".claude") + " is not a directory" },
		},
		{
			name: "S7_base_repo_sent_through_a_symlink",
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, ".claude"), "x", 0o644)
				symlinkForTest(t, repo, filepath.Join(filepath.Dir(repo), "link"))
			},
			send: func(base, _ string) (string, string) {
				link := filepath.Join(base, "link")
				return link, filepath.Join(link, ".claude", "worktrees", "wt")
			},
			text: func(repo string) string { return filepath.Join(repo, ".claude") + " is not a directory" },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.denied && os.Geteuid() == 0 {
				t.Skip("root ignores the permission bits this case relies on")
			}
			base, repo := createModesRepo(t)
			c.setup(t, repo)
			sendRepo, wp := repo, filepath.Join(repo, ".claude", "worktrees", "wt")
			if c.send != nil {
				sendRepo, wp = c.send(base, repo)
			}
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": sendRepo, "branchName": "b", "worktreePath": wp}))
			if want := mkdirFailedFrame(t, c.text(repo)); raw != want {
				t.Errorf("create frame\n got %s\nwant %s", raw, want)
			}
			requireAbsent(t, filepath.Join(repo, ".claude", "worktrees", "wt"))
		})
	}
}

// With umask 0277 a new directory has no write permission, so the next create in
// it fails. The directory made first stays, as on f6010b97 (Linux and macOS VMs,
// F15 and F15i). The umask is process-wide, so this test does not run in parallel.
func TestWorktreeCreateKeepsDirsMadeBeforeAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	base, repo := createModesRepo(t)
	f := filepath.Join(base, "F")
	mkdirForTest(t, f)
	old := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(old) })

	t.Run("F15_external", func(t *testing.T) {
		requireTempOutsideCheckout(t, base)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(f, "a"), 0o755) })
		root := filepath.Join(f, "a", "b", "R")
		wp := filepath.Join(root, "cp", "w1")
		raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp, "worktreeRoot": root}))
		if want := mkdirFailedFrame(t, "mkdirat a/b: permission denied"); raw != want {
			t.Errorf("create frame\n got %s\nwant %s", raw, want)
		}
		if m := permOf(t, filepath.Join(f, "a")); m != 0o500 {
			t.Errorf("mode of the kept a = %#o, want 0500", m)
		}
	})

	t.Run("F15i_in_repo", func(t *testing.T) {
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(repo, ".claude"), 0o755) })
		wp := filepath.Join(repo, ".claude", "worktrees", "wt")
		raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp}))
		if want := mkdirFailedFrame(t, "mkdirat worktrees: permission denied"); raw != want {
			t.Errorf("create frame\n got %s\nwant %s", raw, want)
		}
		if m := permOf(t, filepath.Join(repo, ".claude")); m != 0o500 {
			t.Errorf("mode of the kept .claude = %#o, want 0500", m)
		}
	})
}

// A symlink loop in the root chain refuses the create with unsafe_path. The text
// names the cleaned root. f6010b97 sends it on Linux and macOS VMs (S1, S1b, S1c,
// S1d).
func TestWorktreeCreateExternalSymlinkLoop(t *testing.T) {
	loop := func(rootRel string) func(f string) (string, string) {
		return func(f string) (string, string) {
			return "refusing to create worktree: " + filepath.Join(f, rootRel) +
				" passes through too many symbolic links (a loop?)", "unsafe_path"
		}
	}
	runChainCases(t, []chainCase{
		{
			name: "S1_root_loops", rootRel: "R",
			setup:  func(t *testing.T, f string) { symlinkForTest(t, filepath.Join(f, "R"), filepath.Join(f, "R")) },
			want:   loop("R"),
			absent: []string{"R/cp"},
		},
		{
			name: "S1b_ancestor_loops", rootRel: "a/b/R",
			setup: func(t *testing.T, f string) { symlinkForTest(t, filepath.Join(f, "a"), filepath.Join(f, "a")) },
			want:  loop("a/b/R"),
		},
		{
			name: "S1c_two_links_loop", rootRel: "a/R",
			setup: func(t *testing.T, f string) {
				symlinkForTest(t, filepath.Join(f, "b"), filepath.Join(f, "a"))
				symlinkForTest(t, filepath.Join(f, "a"), filepath.Join(f, "b"))
			},
			want: loop("a/R"),
		},
		{
			name: "S1d_root_loops_sent_with_trailing_slash", rootRel: "R",
			rootSent: func(f string) string { return filepath.Join(f, "R") + "/" },
			setup:    func(t *testing.T, f string) { symlinkForTest(t, filepath.Join(f, "R"), filepath.Join(f, "R")) },
			want:     loop("R"),
		},
	})
}

// An in-repo .claude that loops gets the symlinked_component refusal. f6010b97
// sends the same frame on Linux and macOS VMs (S1e).
func TestWorktreeCreateInRepoClaudeLoop(t *testing.T) {
	_, repo := createModesRepo(t)
	claude := filepath.Join(repo, ".claude")
	symlinkForTest(t, claude, claude)
	wp := filepath.Join(claude, "worktrees", "w1")
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp}))
	text := "refusing to create worktree: " + claude + " is a symbolic link; a symlinked .claude or " +
		".claude/worktrees inside the repository is not supported for SSH sessions, because a " +
		"repository can plant such a link and a planted one cannot reliably be told apart from " +
		"your own. Replace it with a real directory (or delete it and it will be recreated)"
	if want := failFrame(t, text, "symlinked_component"); raw != want {
		t.Errorf("create frame\n got %s\nwant %s", raw, want)
	}
}

// requireTempOutsideCheckout fails the test when the temp dir sits inside a git
// checkout. The create then refuses every worktreeRoot under it, so set TMPDIR
// outside any checkout. It walks the resolved path, as the create does.
func requireTempOutsideCheckout(t *testing.T, dir string) {
	t.Helper()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			t.Fatalf("temp dir %s is inside a git checkout (%s has a .git entry); set TMPDIR outside it", dir, d)
		}
		if filepath.Dir(d) == d {
			return
		}
	}
}
