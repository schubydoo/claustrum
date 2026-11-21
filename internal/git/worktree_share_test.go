package git

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
// the `.git` of the top folder (4c111e86, measured on Windows). A `.git` file,
// also one above baseRepo that ends the walk below a `.git` folder, keeps the
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
	// A folder below a folder whose `.git` is a file, inside the repository: the
	// `.git` file ends the walk before the `.git` folder of the top folder.
	belowGitFileBase := func(t *testing.T, f shadowFixture) string {
		g := filepath.Join(f.repo, "G")
		writeFile(t, filepath.Join(g, ".git"), "gitdir: "+f.gitDir+"\n", 0o644)
		sub := filepath.Join(g, "sub")
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
		{"create from below a .git file on a share", belowGitFileBase, true, true, false, false, false, "git.worktree_create", func(string, string) string { return notARepo }},
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
		{"remove from below a .git file on a share", belowGitFileBase, true, true, false, false, false, "git.worktree_remove", func(_, leaf string) string {
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

// TestShareMissingSubfolderAsksTheRepository sends the five methods on a missing
// path below a repository on a share, `<repo>\missing\deeper`. On Windows
// 4c111e86 asks git about the repository in `<repo>` there, with the calls of an
// existing subfolder: once for git.info and a create, two times for a remove
// (measured on Windows). Its replies stay those of a missing path. git.status and
// git.list_branches make no call (measured). A missing path that is not on a
// share keeps no call about the repository. A remove keeps the worktree, the
// registration that names the gone leaf and the branch (claustrum's own).
func TestShareMissingSubfolderAsksTheRepository(t *testing.T) {
	const dubious = "fatal: detected dubious ownership in repository at '//127.0.0.1/s/repo'\n"
	notRepoInfo := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
	notARepo := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
	kept := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	// rounds is the number of times the request asks git about the repository.
	for _, row := range []struct {
		method string
		share  bool
		want   string
		rounds int
	}{
		{"git.info", true, notRepoInfo, 1},
		{"git.worktree_create", true, notARepo, 1},
		{"git.worktree_remove", true, kept, 2},
		{"git.status", true, "", 0},
		{"git.list_branches", true, "", 0},
		{"git.info", false, notRepoInfo, 0},
		{"git.worktree_create", false, notARepo, 0},
		{"git.worktree_remove", false, kept, 0},
	} {
		t.Run(fmt.Sprintf("%s share=%v", row.method, row.share), func(t *testing.T) {
			// run sends the method on base, the path below the repository that
			// sub names, and gives the reply and the calls.
			run := func(t *testing.T, sub string, made bool) (string, []envCall) {
				f := newSeededShadowFixture(t)
				base := filepath.Join(f.repo, sub)
				if made {
					if err := os.MkdirAll(base, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				old := repoOnShare
				repoOnShare = func(repo string) bool { return row.share && repo == base }
				t.Cleanup(func() { repoOnShare = old })
				answerProbe(t, 128, "", dubious)
				answerSecondProbe(t, 0, "")
				leaf := filepath.Join(base, ".claude", "worktrees", "w1")
				remove := row.method == "git.worktree_remove"
				if remove {
					seedGoneRegistration(t, f, leaf)
				}
				raw, calls := f.call(t, row.method, map[string]any{"path": base, "baseRepo": base, "worktreePath": leaf, "branchName": "w1"})
				if remove {
					wantNothingRemoved(t, f)
				}
				// The calls name the folders of this fixture: give them in the
				// form of the fixture of the other row.
				f.d.Shadow.mu.Lock()
				folder := f.d.Shadow.dir
				f.d.Shadow.mu.Unlock()
				// The temp folders of the subtest differ between the two fixtures.
				mask := strings.NewReplacer(folder, "<folder>", f.repo, "<repo>", filepath.Dir(filepath.Dir(f.repo)), "<temp>")
				for i := range calls {
					calls[i].cwd = mask.Replace(calls[i].cwd)
					for j := range calls[i].env {
						calls[i].env[j] = mask.Replace(calls[i].env[j])
					}
				}
				return raw, calls
			}
			var raw string
			var calls, want []envCall
			// Each fixture gets its own subtest, so each has its own stub.
			t.Run("missing", func(t *testing.T) { raw, calls = run(t, filepath.Join("missing", "deeper"), false) })
			if row.want != "" && raw != row.want {
				t.Errorf("%s = %s\nwant %s", row.method, raw, row.want)
			}
			// Each ask has one second run, in `.probe`.
			asks := 0
			for _, c := range probeCalls(calls) {
				if filepath.Base(c.cwd) == shadowProbeName {
					asks++
				}
			}
			if asks != row.rounds {
				t.Errorf("%d asks about the repository, want %d: %v", asks, row.rounds, shareCallLines(calls))
			}
			if row.rounds == 0 {
				return
			}
			// The calls are those of an existing subfolder of the repository.
			t.Run("sub", func(t *testing.T) { _, want = run(t, "sub", true) })
			if len(probeCalls(want)) == 0 {
				t.Fatalf("the existing subfolder makes no call about the repository: %v", shareCallLines(want))
			}
			if !reflect.DeepEqual(calls, want) {
				t.Errorf("calls = %v\nwant the calls of an existing subfolder %v", shareCallLines(calls), shareCallLines(want))
			}
			for _, c := range calls {
				if c.version() {
					t.Errorf("a git version call in %s", c.cwd)
				}
			}
		})
	}
}

// seedGoneRegistration gives the fixture a branch w1 and a registration w1
// whose record names the gone leaf, so a remove that deletes shows it.
func seedGoneRegistration(t *testing.T, f shadowFixture, leaf string) {
	t.Helper()
	buildGit(t, f.repo, "branch", "w1")
	entry := filepath.Join(f.gitDir, "worktrees", "w1")
	writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(entry, "commondir"), "../..\n", 0o644)
	writeFile(t, filepath.Join(entry, "HEAD"), "ref: refs/heads/w1\n", 0o644)
}

// wantNothingRemoved fails the test unless the worktree wr, both registrations
// and both branches of the fixture after seedGoneRegistration are there.
func wantNothingRemoved(t *testing.T, f shadowFixture) {
	t.Helper()
	for _, p := range []string{f.wt, filepath.Join(f.gitDir, "worktrees", "wr"), filepath.Join(f.gitDir, "worktrees", "w1", "gitdir")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("after the remove: %v", err)
		}
	}
	for _, b := range []string{"wr", "w1"} {
		if !branchExists(t, f.repo, b) {
			t.Errorf("after the remove the branch %s is gone", b)
		}
	}
}

// TestShareMissingSubfolderRemoveAfterOneBadAnswer sends git.worktree_remove on
// a missing baseRepo below a repository on a share. After a first bad answer and
// a second good one, the remove takes the old steps. Its reply and its other
// calls equal those of the same remove that is not on a share, and it deletes
// nothing. After two answers with a text it answers the lock-check refusal with
// that text and deletes nothing (claustrum's own, not measured).
func TestShareMissingSubfolderRemoveAfterOneBadAnswer(t *testing.T) {
	const dubious = "fatal: detected dubious ownership in repository at '//127.0.0.1/s/repo'\n"
	// run sends the remove and gives its reply, with the fixture paths masked,
	// and its calls that ask no question about the repository.
	run := func(t *testing.T, share bool, answer func(*testing.T)) (string, []string) {
		f := newSeededShadowFixture(t)
		base := filepath.Join(f.repo, "missing", "deeper")
		old := repoOnShare
		repoOnShare = func(repo string) bool { return share && repo == base }
		t.Cleanup(func() { repoOnShare = old })
		answer(t)
		leaf := filepath.Join(base, ".claude", "worktrees", "w1")
		seedGoneRegistration(t, f, leaf)
		raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": leaf, "branchName": "w1"})
		wantNothingRemoved(t, f)
		if _, err := os.Lstat(base); !os.IsNotExist(err) {
			t.Errorf("the missing path: %v, want none", err)
		}
		var other []envCall
		for _, c := range calls {
			if !c.isProbe() {
				other = append(other, c)
			}
		}
		mask := strings.NewReplacer(f.repo, "REPO")
		return mask.Replace(raw), shareCallLines(other)
	}
	// oneBad answers the first call about the repository with exit 128, and the
	// later ones by the real git.
	oneBad := func(t *testing.T) {
		answerProbe(t, 128, "", dubious)
		answerSecondProbe(t, 0, "")
		t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
		t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	}
	t.Run("a bad and a good answer", func(t *testing.T) {
		var raw, want string
		var calls, wantCalls []string
		t.Run("share", func(t *testing.T) { raw, calls = run(t, true, oneBad) })
		t.Run("local", func(t *testing.T) { want, wantCalls = run(t, false, oneBad) })
		if raw != want {
			t.Errorf("git.worktree_remove = %s\nwant the reply off the share %s", raw, want)
		}
		if !reflect.DeepEqual(calls, wantCalls) || len(calls) == 0 {
			t.Errorf("other calls = %v\nwant those off the share %v", calls, wantCalls)
		}
	})
	t.Run("two answers with a text", func(t *testing.T) {
		raw, _ := run(t, true, func(t *testing.T) { answerProbe(t, 1, "", "boom\n") })
		leaf := filepath.Join("REPO", "missing", "deeper", ".claude", "worktrees", "w1")
		text := "failed to remove worktree: could not check whether " + leaf + " is locked: " +
			repoCheckFailedText("REPO", "boom")
		if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, text) + `}}`; raw != want {
			t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
		}
	})
}

// shareCallLines gives each call as its folder and its argv.
func shareCallLines(calls []envCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.cwd+" | "+strings.Join(c.argv, " "))
	}
	return out
}

// TestShareMissingSubfolderFailedStartTest sends git.info, a create and a
// remove on a missing path below a repository on a share, with a start test that
// fails. The request answers the text of the failed test in the frame of its
// method (claustrum's own, not measured). A missing path that is not on a share
// starts no test for git.info and a create, and keeps the replies of a missing
// path. A remove answers the text there too, as before. Each remove deletes
// nothing.
func TestShareMissingSubfolderFailedStartTest(t *testing.T) {
	text := failedTestText("this repository's git config cannot be used: boom")
	q := jsonQuoted(t, text)
	for _, row := range []struct {
		method string
		share  bool
		want   string
	}{
		{"git.info", true, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + q + `}}`},
		{"git.worktree_create", true, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + q + `,"errorCode":"worktree_add_failed"}}`},
		{"git.info", false, `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`},
		{"git.worktree_create", false, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`},
		{"git.worktree_remove", true, ""},
		{"git.worktree_remove", false, ""},
	} {
		t.Run(fmt.Sprintf("%s share=%v", row.method, row.share), func(t *testing.T) {
			f := newShadowFixture(t)
			base := filepath.Join(f.repo, "missing", "deeper")
			old := repoOnShare
			repoOnShare = func(repo string) bool { return row.share && repo == base }
			t.Cleanup(func() { repoOnShare = old })
			failCallSeven(t, "boom\n")
			leaf := filepath.Join(base, ".claude", "worktrees", "w1")
			remove := row.method == "git.worktree_remove"
			if remove {
				seedGoneRegistration(t, f, leaf)
			}
			raw, _ := f.call(t, row.method, map[string]any{"path": base, "baseRepo": base, "worktreePath": leaf, "branchName": "w1"})
			// The want of a remove names the leaf of this fixture.
			want := row.want
			if want == "" {
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
					jsonQuoted(t, "failed to remove worktree: could not check whether "+leaf+" is locked: "+text) + `}}`
			}
			if raw != want {
				t.Errorf("%s = %s\nwant %s", row.method, raw, want)
			}
			if remove {
				wantNothingRemoved(t, f)
			}
			if _, err := os.Lstat(base); !os.IsNotExist(err) {
				t.Errorf("the missing path: %v, want none", err)
			}
		})
	}
}
