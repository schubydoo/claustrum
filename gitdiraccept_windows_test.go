//go:build windows

package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Windows side of the held git directory of a create. No test of this file
// ran on the host of the build: a Windows run must match the cell that each
// row names.
//
// The deny rows of tables W3 and F1 of val-s3p2b2-7-windows have no test here:
// they need a deny entry in the access list of the folder of the daemon, and
// the read-only attribute of a folder does not stop a new entry in it.
// TestStagedAddWithAFolderThatTakesNoEntry has the text of that step on Linux
// and macOS.

// linkAt makes a junction at link to target.
func linkAt(t *testing.T, link, target string) {
	t.Helper()
	makeJunction(t, link, target)
}

// makeDirSymlink makes a directory symbolic link at link to target with
// mklink /D, or skips the test when the process cannot make one. That call
// needs the privilege to make symbolic links, or the developer mode.
func makeDirSymlink(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/D", link, target).CombinedOutput(); err != nil {
		t.Skipf("this process cannot make a directory symbolic link: mklink /D %s %s: %v\n%s", link, target, err, out)
	}
}

// copyGitDir copies the tree at from to the new folder to.
func copyGitDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCreateWithAJunctionAtTheGitDirOnWindows must match cells X1c-01 to
// X1c-06 and X1c-09 to X1c-11 of val-s3p2b2-7-windows. T is a copy of the git
// directory with an old entry `.claude-new-worktree-x`, its marker and one more
// file. At one step of the create, `repo\.git` goes to `repo\.git-real` and a
// junction to T takes its place. 5fd08069 then refuses the create with the
// text of the row, removes nothing in T and makes nothing in T.
//
// The last two rows are points 11 and 13 of 5fd08069 in the table of section 2
// of val-s3p2b2-12-windows: the junction comes while the first listing of the
// checkout runs, and after the read-tree call ended. The registration and the
// branch stay in the real git directory, and the registration has no index.
//
// The test removes nothing in T with the guards or without them: the removal
// of the cleanup is a recorder.
func TestCreateWithAJunctionAtTheGitDirOnWindows(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	for _, row := range []struct {
		cell, step string
		// want is the error for the refusal of the config link and the leaf.
		want func(link, leaf string) string
	}{
		{"X1c-01", "leaf", func(link, _ string) string { return link }},
		{"X1c-03", "gitdir", func(link, _ string) string {
			return "git worktree add failed: cannot locate the repository's git directory: " + link
		}},
		{"X1c-06", "answered", func(string, string) string {
			return "git worktree add failed: cannot list the repository's worktree entries: " + realDir
		}},
		{"X1c-05", "add", func(link, _ string) string { return "git worktree add failed: " + link }},
		{"X1c-09", "prepare", func(link, _ string) string {
			return "git worktree add failed: cannot prepare the repository's worktrees directory: " + link
		}},
		{"X1c-10", "added", func(link, leaf string) string {
			return "git worktree add failed (completing the new worktree's git files): " + realDir +
				"; and the undo could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
				"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
				"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + link + ")"
		}},
		{"X1c-11", "checkout", func(link, leaf string) string {
			return "git worktree add failed (checkout): " + link + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
		{"point 11", "listed", func(link, leaf string) string {
			return "git worktree add failed (checkout): " + link + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
		{"point 13", "index", func(_, leaf string) string {
			return "git worktree add failed (checkout): " + realDir + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
	} {
		t.Run(row.cell+" "+row.step, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			removals := recordStagingRemovals(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
			copyGitDir(t, f.gitDir, target)
			entry := filepath.Join(target, ".claude-new-worktree-x")
			marker := filepath.Join(entry, "marker.txt")
			mustWrite(t, marker, "marker of the old entry\n")
			mustWrite(t, filepath.Join(target, "t-own.txt"), "a file of the other folder\n")
			at := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(entry, at, at); err != nil {
				t.Fatal(err)
			}
			swapAt(t, row.step, f.gitDir, target)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			var reply struct {
				Result struct {
					Success   bool
					Error     string
					ErrorCode string
				}
			}
			if err := json.Unmarshal([]byte(raw), &reply); err != nil {
				t.Fatalf("the reply %s: %v", raw, err)
			}
			link := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
				": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
			if want := row.want(link, leaf); reply.Result.Success || reply.Result.Error != want || reply.Result.ErrorCode != "worktree_add_failed" {
				t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
			}
			if text := mustRead(t, marker); text != "marker of the old entry\n" {
				t.Errorf("the marker in T = %q", text)
			}
			if len(*removals) != 0 {
				t.Errorf("the cleanup asked to remove %q, want nothing", *removals)
			}
			for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
					t.Errorf("%s in T: %v, want none", name, err)
				}
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			if row.step != "listed" && row.step != "index" {
				return
			}
			kept := f.gitDir + "-real"
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(kept, name)); err != nil {
					t.Errorf("%s in the real git directory: %v, want it kept", name, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(kept, "worktrees", "w1", "index")); !os.IsNotExist(err) {
				t.Errorf("the index of the registration: %v, want none", err)
			}
		})
	}
}

// TestSymbolicLinkAtTheGitDirOnWindows must match cells D-symd-start and
// DT-symd-late-to-T of val-s3p2b2-12-windows (Windows VM). `repo\.git` is a
// directory symbolic link to T, a copy of the git directory with an old entry
// `.claude-new-worktree-x`. The link is there before the first request, or it
// takes the place of the git directory after one good git.info. 5fd08069
// refuses each git method with the text of a junction there, and T stays as it
// is.
//
// The test is safe without the rule of gitDirIsSymlink. The create then makes
// its worktree in T, which is a folder of the test, and the removal of the
// cleanup is a recorder.
func TestSymbolicLinkAtTheGitDirOnWindows(t *testing.T) {
	for _, row := range []struct {
		cell string
		late bool
	}{
		{"D-symd-start", false},
		{"DT-symd-late-to-T", true},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			removals := recordStagingRemovals(t)
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
			copyGitDir(t, f.gitDir, target)
			entry := filepath.Join(target, ".claude-new-worktree-x")
			marker := filepath.Join(entry, "marker.txt")
			mustWrite(t, marker, "marker of the old entry\n")
			at := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(entry, at, at); err != nil {
				t.Fatal(err)
			}
			if row.late {
				if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
					t.Fatalf("git.info before the link = %s", raw)
				}
			}
			if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
				t.Fatal(err)
			}
			makeDirSymlink(t, f.gitDir, target)
			link := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
				": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			for _, req := range []struct {
				method string
				params map[string]any
				// want is the text of the error member, or of the error of the result.
				want     string
				inResult bool
			}{
				{"git.info", map[string]any{"path": f.repo}, link, false},
				{"git.status", map[string]any{"path": f.repo, "baseRepo": f.repo}, link, false},
				{"git.list_branches", map[string]any{"path": f.repo}, link, false},
				{"git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}, link, true},
				{"git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1"},
					"failed to remove worktree: could not check whether " + leaf + " is locked: " + link, true},
			} {
				raw, _ := f.call(t, req.method, req.params)
				var reply struct {
					Error  struct{ Message string }
					Result struct {
						Success bool
						Error   string
					}
				}
				if err := json.Unmarshal([]byte(raw), &reply); err != nil {
					t.Fatalf("%s: the reply %s: %v", req.method, raw, err)
				}
				got := reply.Error.Message
				if req.inResult {
					got = reply.Result.Error
				}
				if got != req.want || reply.Result.Success {
					t.Errorf("%s = %s\nwant the error %s", req.method, raw, req.want)
				}
			}
			if text := mustRead(t, marker); text != "marker of the old entry\n" {
				t.Errorf("the marker in T = %q", text)
			}
			if len(*removals) != 0 {
				t.Errorf("the cleanup asked to remove %q, want nothing", *removals)
			}
			for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
					t.Errorf("%s in T: %v, want none", name, err)
				}
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want none", err)
			}
		})
	}
}

// TestCreateWithAnotherGitDirAtThePathOnWindows: at one step of the create,
// `repo\.git` goes to `repo\.git-real`, and the path gets another entry that
// leads to the git directory X of a second repository, or is a copy of the
// git directory as a real directory. X gets no registration and no branch, and
// no `worktree add` runs.
//
// Five rows are of 5fd08069 in part C and cells CX of val-s3p2b2-12-windows
// (Windows VM): the file after `rev-parse --absolute-git-dir`, the file
// before the add and at the listing of the add, and the directory symbolic
// link after the first call and after `rev-parse --absolute-git-dir`. The
// three other rows have no row of 5fd08069: their texts are those of the
// junction rows of val-s3p2b2-7-windows (cells X1c-01, X1c-03 and X1c-06).
func TestCreateWithAnotherGitDirAtThePathOnWindows(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	notPrepared := func(string) string {
		return "git worktree add failed: cannot prepare the repository's worktrees directory: " + realDir
	}
	noEntries := func(string) string {
		return "git worktree add failed: cannot list the repository's worktree entries: " + realDir
	}
	for _, row := range []struct {
		name, step, entry string
		want              func(link string) string
	}{
		{"a file that names X", "leaf", "file", func(link string) string { return link }},
		{"a file that names X before the gitdir call", "gitdir", "file", func(link string) string {
			return "git worktree add failed: cannot locate the repository's git directory: " + link
		}},
		{"C-file-06 after the gitdir call", "answered", "file", notPrepared},
		{"a file that names X before the add", "add", "file", notPrepared},
		{"CX-file at the listing of the add", "prepare", "file", notPrepared},
		{"C-symd-01 a symbolic link to X", "leaf", "symlink", func(link string) string { return link }},
		{"C-symd-06 a symbolic link to X after the gitdir call", "answered", "symlink", noEntries},
		{"another real directory", "gitdir", "copy", noEntries},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			_, other := configRepo(t)
			if row.entry == "symlink" {
				// The skip of a process that cannot make such a link comes here,
				// in the goroutine of the test.
				probe := resolveTestRoot(t, t.TempDir())
				makeDirSymlink(t, filepath.Join(probe, "link"), probe)
			}
			copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			copyGitDir(t, f.gitDir, copied)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			atStep(t, row.step, func() {
				if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
					t.Errorf("the rename of the git directory: %v", err)
					return
				}
				switch row.entry {
				case "file":
					mustWrite(t, f.gitDir, "gitdir: "+filepath.ToSlash(other)+"\n")
				case "symlink":
					if out, err := exec.Command("cmd", "/c", "mklink", "/D", f.gitDir, other).CombinedOutput(); err != nil {
						t.Errorf("mklink /D %s %s: %v\n%s", f.gitDir, other, err, out)
					}
				default:
					if err := os.Rename(copied, f.gitDir); err != nil {
						t.Errorf("the rename of the copy: %v", err)
					}
				}
			})
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			var reply struct {
				Result struct {
					Success bool
					Error   string
				}
			}
			if err := json.Unmarshal([]byte(raw), &reply); err != nil {
				t.Fatalf("the reply %s: %v", raw, err)
			}
			link := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
				": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
			if want := row.want(link); reply.Result.Success || reply.Result.Error != want {
				t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
			}
			for _, c := range calls {
				if slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") {
					t.Error("a worktree add call ran")
				}
			}
			for _, dir := range []string{other, f.gitDir + "-real"} {
				for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
					if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
						t.Errorf("%s in %s: %v, want none", name, dir, err)
					}
				}
			}
		})
	}
}

// TestAttachWithAFileAtWorktreesOnWindows must match table S9 of
// val-s3p2b2-12-windows (Windows VM, 2 of 2 runs). A create with
// `existingBranch` finds a file at `.git\worktrees`. 5fd08069 answers the
// text of that step one time and makes no second add with a new branch.
// Without the rule of addNotPrepared the text has a second part, `(attaching
// to the existing branch eb was refused first: …)`.
func TestAttachWithAFileAtWorktreesOnWindows(t *testing.T) {
	f := newSeededShadowFixture(t)
	runGit(t, f.repo, "branch", "eb")
	worktrees := filepath.Join(f.gitDir, "worktrees")
	if err := os.Rename(worktrees, worktrees+"-kept"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	mustWrite(t, worktrees, "a file\n")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{
		"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "existingBranch": "eb"})
	var reply struct {
		Result struct {
			Success   bool
			Error     string
			ErrorCode string
		}
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatalf("the reply %s: %v", raw, err)
	}
	want := "git worktree add failed: cannot prepare the repository's worktrees directory: " + quoteOperand(worktrees) + " is not a directory"
	if reply.Result.Success || reply.Result.Error != want || reply.Result.ErrorCode != "worktree_add_failed" {
		t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
	}
	for _, c := range calls {
		if slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") {
			t.Error("a worktree add call ran")
		}
	}
	if got := mustRead(t, worktrees); got != "a file\n" {
		t.Errorf("the file at worktrees = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
		t.Errorf("the branch w1: %v, want none", err)
	}
}

// TestHeldGitDirTakesARenameOnWindows: another process can rename the git
// directory that a create holds. On 5fd08069 a rename of `repo\.git` passes
// during a create (60 swaps of val-s3p2b2-9-windows, Windows VM). With a hold
// that os.OpenRoot opened by the path, the rename failed with `The process
// cannot access the file because it is being used by another process`
// (section 1.1 of that round). After the rename the held root still acts in
// the folder, at its new name. A junction at the last name is not opened.
func TestHeldGitDirTakesARenameOnWindows(t *testing.T) {
	dir := filepath.Join(resolveTestRoot(t, t.TempDir()), "g")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := openHeldGitDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Rename(dir, dir+"-real"); err != nil {
		t.Fatalf("the rename of the held folder: %v", err)
	}
	if err := root.Mkdir("made", 0o755); err != nil {
		t.Fatalf("a folder through the held root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir+"-real", "made")); err != nil {
		t.Errorf("the folder in the renamed git directory: %v", err)
	}
	linkAt(t, dir, dir+"-real")
	if linked, err := openHeldGitDir(dir); err == nil {
		_ = linked.Close()
		t.Error("a junction at the last name is opened as the held folder")
	}
}

// TestKeptProbeWithAJunctionAtTheGitDirOnWindows must match part Q of
// val-s3p2b2-9-windows and its control cell Qk (Windows VM). With a junction
// at `repo\.git`, a change of the folder behind it leaves the kept answer in
// place: 5fd08069 makes no git call in the seven later requests. With a real
// `.git` directory the same change ends the kept answer. The test sets the
// time of the folder, where the driver of the cells ran a plain `git status`.
// With probeStat for the `.git` entry the first row fails.
func TestKeptProbeWithAJunctionAtTheGitDirOnWindows(t *testing.T) {
	for _, row := range []struct {
		name     string
		junction bool
		fresh    bool
	}{
		{"part Q with a junction", true, true},
		{"control Qk with a real directory", false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			base := resolveTestRoot(t, t.TempDir())
			repo := filepath.Join(base, "repo")
			gitDir := filepath.Join(repo, ".git")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			if row.junction {
				gitDir = filepath.Join(base, "gdreal")
				linkAt(t, filepath.Join(repo, ".git"), gitDir)
			} else if err := os.Mkdir(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			h := &shadowHost{}
			h.keepProbe(repo)
			if !h.probes[repo].fresh(repo) {
				t.Fatal("the answer is not kept right after the call")
			}
			at := time.Now().Add(time.Hour)
			if err := os.Chtimes(gitDir, at, at); err != nil {
				t.Fatal(err)
			}
			if got := h.probes[repo].fresh(repo); got != row.fresh {
				t.Errorf("the kept answer after a change of the git directory: fresh = %v, want %v", got, row.fresh)
			}
		})
	}
}

// TestCreateWithAFileAtTheGitDirOnWindows must match cells X1b, X1c and X2 of
// round task077-03 (Windows VM, 2 of 2 runs each). At one step of the create,
// `repo\.git` goes to `repo\.git-real`, and a regular file `gitdir: <path>`
// takes its place.
//
//   - X1b and X1c: the file comes right after the add and names the git
//     directory of a second repository with a branch w1. claustrum answers the
//     link text in the undo, runs no git call after the swap and keeps w1 of
//     the second repository. 5fd08069 deletes that branch in X1b. That is an
//     intentional difference (decision of 2026-10-19).
//   - X2: the file comes while the first listing of the checkout runs.
//     5fd08069 runs the second listing, which fails with exit 128, and answers
//     the text of ownConfigListing. Without the file rule of gitUse.listedSwap the reply
//     has the link text. The file here names a folder that does not exist, so
//     that git fails on every host. In the cell it names the git directory of
//     a second repository.
func TestCreateWithAFileAtTheGitDirOnWindows(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	for _, row := range []struct {
		cell, step string
		ahead      bool
		// after is the number of git calls after the swap.
		after int
		want  func(link, leaf string) string
	}{
		{"X1b", "added", false, 0, func(link, leaf string) string {
			return "git worktree add failed (completing the new worktree's git files): " + realDir +
				"; and the undo could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
				"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
				"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + link + ")"
		}},
		{"X1c", "added", true, 0, func(link, leaf string) string {
			return "git worktree add failed (completing the new worktree's git files): " + realDir +
				"; and the undo could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
				"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
				"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + link + ")"
		}},
		{"X2", "listed", false, 1, func(_, leaf string) string {
			return "git worktree add failed (checkout): Claude could not read your own git config on the remote machine " +
				"(exit status 128). Run git config --list there to see the problem.; and the undo could not finish for " +
				leaf + ": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			otherRepo, other := configRepo(t)
			runGit(t, otherRepo, "branch", "w1")
			if row.ahead {
				runGit(t, otherRepo, "checkout", "-q", "w1")
				runGit(t, otherRepo, "commit", "-q", "--allow-empty", "-m", "ahead")
				runGit(t, otherRepo, "checkout", "-q", "main")
			}
			w1 := filepath.Join(other, "refs", "heads", "w1")
			before := mustRead(t, w1)
			named := other
			if row.step == "listed" {
				named = filepath.Join(resolveTestRoot(t, t.TempDir()), "none", ".git")
			}
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swappedAt := -1
			atStep(t, row.step, func() {
				swappedAt = len(readEnvCalls(t, f.log))
				if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
					t.Errorf("the rename of the git directory: %v", err)
					return
				}
				mustWrite(t, f.gitDir, "gitdir: "+filepath.ToSlash(named)+"\n")
			})
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			var reply struct {
				Result struct {
					Success   bool
					Error     string
					ErrorCode string
				}
			}
			if err := json.Unmarshal([]byte(raw), &reply); err != nil {
				t.Fatalf("the reply %s: %v", raw, err)
			}
			link := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
				": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
			if want := row.want(link, leaf); reply.Result.Success || reply.Result.Error != want || reply.Result.ErrorCode != "worktree_add_failed" {
				t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
			}
			if swappedAt < 0 {
				t.Fatalf("the create did not reach the step %s", row.step)
			}
			if got := len(calls) - swappedAt; got != row.after {
				t.Errorf("%d git calls after the swap, want %d: %q", got, row.after, calls[swappedAt:])
			}
			if row.after == 1 && len(calls) > swappedAt && !calls[swappedAt].listing() {
				t.Errorf("the call after the swap = %q, want the second listing", calls[swappedAt].argv)
			}
			if got := mustRead(t, w1); got != before {
				t.Errorf("w1 of the second repository = %q, want %q", got, before)
			}
			kept := f.gitDir + "-real"
			if _, err := os.Lstat(filepath.Join(kept, "refs", "heads", "w1")); err != nil {
				t.Errorf("the branch w1 in the real git directory: %v, want it kept", err)
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
		})
	}
}
