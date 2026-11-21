package git

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestShareGitDirThatGitDoesNotUse runs a create and a remove where git finds no
// repository in a folder that the trust check accepted, and repoOnShare counts
// baseRepo as a network share. On Windows 4c111e86 answers the create with
// unusableGitText for `<baseRepo>\.git`, and a remove of a gone worktree with
// success and a kept branch (measured on a share). Nothing is made or deleted.
// A lock that names the gone worktree gets the same success, and the lock and
// the registration stay. 4c111e86 answers so for a record in the share and in
// the local drive spelling (measured on Windows). The rows with a link record
// the resolved path or the sent path. On Windows the link is a junction. A
// worktree that exists keeps the lock-check refusal. A baseRepo one folder below
// the top folder gets the same create and remove answers, and the create names
// the `.git` of the top folder (4c111e86, measured on Windows). A `.git` file keeps the
// answers of a local folder (claustrum's own, not measured). A folder that is
// not on a share keeps not_a_repo and the lock-check refusal.
func TestShareGitDirThatGitDoesNotUse(t *testing.T) {
	const dubious = "fatal: detected dubious ownership in repository at '//127.0.0.1/s/repo'\n"
	notARepo := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
	refused := func(text string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, text) + `}}`
	}
	// Each base returns baseRepo for the fixture.
	repoBase := func(t *testing.T, f shadowFixture) string { return f.repo }
	// A folder whose `.git` file names the git directory of the fixture.
	gitFileBase := func(t *testing.T, f shadowFixture) string {
		g := filepath.Join(resolveTestRoot(t, t.TempDir()), "G")
		writeFile(t, filepath.Join(g, ".git"), "gitdir: "+f.gitDir+"\n", 0o644)
		return g
	}
	// A folder one level below the top folder of the repository.
	subBase := func(t *testing.T, f shadowFixture) string {
		sub := filepath.Join(f.repo, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		return sub
	}
	linkedBase := func(t *testing.T, f shadowFixture) string {
		link := filepath.Join(resolveTestRoot(t, t.TempDir()), "via")
		shareTestLink(t, link, filepath.Dir(f.repo))
		return filepath.Join(link, filepath.Base(f.repo))
	}
	for _, row := range []struct {
		name        string
		base        func(*testing.T, shadowFixture) string
		share, gone bool
		locked      bool
		// goneParent puts the gone leaf below a folder that does not exist.
		goneParent bool
		// recordSent makes the lock record name the leaf below the sent baseRepo.
		recordSent bool
		method     string
		want       func(base, leaf string) string
	}{
		{"create on a share", repoBase, true, true, false, false, false, "git.worktree_create", func(base, _ string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonQuoted(t, fmt.Sprintf(unusableGitText, filepath.Join(base, ".git"))) + `,"errorCode":"worktree_add_failed"}}`
		}},
		{"create from a subfolder on a share", subBase, true, true, false, false, false, "git.worktree_create", func(base, _ string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonQuoted(t, fmt.Sprintf(unusableGitText, filepath.Join(filepath.Dir(base), ".git"))) + `,"errorCode":"worktree_add_failed"}}`
		}},
		{"create on a local folder", repoBase, false, true, false, false, false, "git.worktree_create", func(string, string) string { return notARepo }},
		{"create from a .git file on a share", gitFileBase, true, true, false, false, false, "git.worktree_create", func(string, string) string { return notARepo }},
		{"remove of a gone worktree on a share", repoBase, true, true, false, false, false, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone worktree from a subfolder on a share", subBase, true, true, false, false, false, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone locked worktree on a share", repoBase, true, true, true, false, false, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone locked worktree on a share through a link", linkedBase, true, true, true, false, false, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone locked worktree with a gone parent on a share through a link", linkedBase, true, true, true, true, false, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone worktree whose lock names the sent spelling on a share through a link", linkedBase, true, true, true, false, true, "git.worktree_remove", func(string, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a worktree that exists on a share", repoBase, true, false, false, false, false, "git.worktree_remove", func(_, leaf string) string {
			return refused(lockCheckRefusal(leaf))
		}},
		{"remove of a gone worktree on a local folder", repoBase, false, true, false, false, false, "git.worktree_remove", func(_, leaf string) string {
			return refused(lockCheckRefusal(leaf))
		}},
		{"remove from a .git file on a share", gitFileBase, true, true, false, false, false, "git.worktree_remove", func(_, leaf string) string {
			return refused(lockCheckRefusal(leaf))
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			base := row.base(t, f)
			old := repoOnShare
			repoOnShare = func(repo string) bool { return row.share && repo == base }
			t.Cleanup(func() { repoOnShare = old })
			answerProbe(t, 128, "", dubious)
			answerSecondProbe(t, 0, "")
			leaf := f.wt
			// rel is the gone leaf below baseRepo.
			rel := filepath.Join(".claude", "worktrees", "w1")
			if row.goneParent {
				rel = filepath.Join(".claude", "gone", "w1")
			}
			if row.gone {
				leaf = filepath.Join(base, rel)
			}
			entry := filepath.Join(f.gitDir, "worktrees", "w1")
			if row.locked {
				// The record names the resolved path of the gone worktree.
				record := filepath.Join(f.repo, rel, ".git")
				if row.recordSent {
					record = filepath.Join(base, rel, ".git")
				}
				writeFile(t, filepath.Join(entry, "gitdir"), record+"\n", 0o644)
				writeFile(t, filepath.Join(entry, "locked"), "held\n", 0o644)
			}
			raw, _ := f.call(t, row.method, map[string]any{"baseRepo": base, "worktreePath": leaf, "branchName": "w1"})
			if want := row.want(base, leaf); raw != want {
				t.Errorf("%s = %s\nwant %s", row.method, raw, want)
			}
			for _, p := range []string{f.wt, filepath.Join(f.gitDir, "worktrees", "wr")} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("the fixture: %v", err)
				}
			}
			if row.gone {
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the gone worktree: %v, want none", err)
				}
			}
			if row.locked {
				for _, name := range []string{"locked", "gitdir"} {
					if _, err := os.Stat(filepath.Join(entry, name)); err != nil {
						t.Errorf("the locked registration: %v", err)
					}
				}
			}
		})
	}
}
