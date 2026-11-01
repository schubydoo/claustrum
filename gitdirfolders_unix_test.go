//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The cells C1 to C8 of round tier0-24 (Linux VM, git 2.43.0) and L1 to L5
// of round tier0-26 (Linux VM): the states of the git directory of baseRepo
// for git.worktree_create and git.worktree_remove. Each test stages the cell
// of the round, sends the request of the cell to a server in the state
// shadow, and compares the whole frame with the frame of 5fd08069 in that
// cell. The disk checks are those of the report of the round. H stands for
// the home folder of the VM, a temp folder here. Cells C2, C3, C5 and C6 also
// ran on a macOS VM (round tier0-25m). The other cells are not measured on
// macOS.

// cellFixture is a server in the state shadow with a plain repository R: the
// linked worktree of newSeededShadowFixture is gone, and main has one commit
// with README.
type cellFixture struct {
	shadowFixture
	home string
}

func newCellFixture(t *testing.T) cellFixture {
	t.Helper()
	f := newSeededShadowFixture(t)
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	runGit(t, f.repo, "branch", "-D", "wr")
	mustWrite(t, filepath.Join(f.repo, "README"), "r\n")
	runGit(t, f.repo, "add", "README")
	runGit(t, f.repo, "commit", "-q", "-m", "one")
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("the registrations folder of the fixture: %v, want none", err)
	}
	return cellFixture{shadowFixture: f, home: resolveTestRoot(t, t.TempDir())}
}

// leaf is the worktreePath of the default request.
func (f cellFixture) leaf() string {
	return filepath.Join(f.repo, ".claude", "worktrees", "w1")
}

// create sends the default request of the round with the extra members.
func (f cellFixture) create(t *testing.T, extra map[string]any) (string, []envCall) {
	t.Helper()
	params := map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf(), "branchName": "b1", "sourceBranch": "main"}
	for k, v := range extra {
		params[k] = v
	}
	return f.call(t, "git.worktree_create", params)
}

// frame is the whole reply frame with the result body.
func frame(body string) string {
	return `{"jsonrpc":"2.0","id":1,"result":` + body + `}`
}

// wantPaths fails the test unless each path exists (there) or does not exist.
func wantPaths(t *testing.T, there bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		_, err := os.Lstat(p)
		if there && err != nil {
			t.Errorf("%s: %v, want it there", p, err)
		}
		if !there && !os.IsNotExist(err) {
			t.Errorf("%s: %v, want none", p, err)
		}
	}
}

// addRan reports whether a `worktree add` call ran.
func addRan(calls []envCall) bool {
	return slices.ContainsFunc(calls, func(c envCall) bool {
		return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add")
	})
}

// fileDigests is the sorted list of `<path> <sha256>` of every regular file
// below dir.
func fileDigests(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// linkRegistrationsTo makes the folder dir with the file MARK and puts a
// symbolic link to it at the registrations folder of the fixture.
func (f cellFixture) linkRegistrationsTo(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "MARK"), "")
	linkAt(t, filepath.Join(f.gitDir, "worktrees"), dir)
}

// TestCreateWithALinkedRegistrationsFolder: cells C1 and C2. The registrations
// folder is a symbolic link to H/wt, which holds MARK. Both requests are refused
// before `worktree add`. Only R/.claude/worktrees is made, and nothing is made
// behind the link. Without offWorktreesRefusal, C2 answers success, and git
// makes the registration in H/wt.
func TestCreateWithALinkedRegistrationsFolder(t *testing.T) {
	for _, row := range []struct {
		cell  string
		extra map[string]any
		error func(f cellFixture) string
	}{
		{"C1", nil, func(f cellFixture) string {
			return `git worktree add failed: cannot prepare the repository's worktrees directory: \"` + f.gitDir + `/worktrees\" is not a directory`
		}},
		{"C2 shadowDirOff", map[string]any{"shadowDirOff": true}, func(cellFixture) string {
			return `git worktree add failed: cannot list the repository's worktree entries: \"worktrees\" is a link or a file where Claude needs a real directory; put the directory itself in its place`
		}},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newCellFixture(t)
			wt := filepath.Join(f.home, "wt")
			f.linkRegistrationsTo(t, wt)
			raw, calls := f.create(t, row.extra)
			wantFrame(t, raw, frame(`{"success":false,"error":"`+row.error(f)+`","errorCode":"worktree_add_failed"}`))
			if addRan(calls) {
				t.Error("a worktree add call ran")
			}
			wantPaths(t, true, filepath.Join(f.repo, ".claude", "worktrees"))
			wantPaths(t, false, f.leaf(), filepath.Join(f.gitDir, "refs", "heads", "b1"))
			if got := pathsBelow(t, wt); !slices.Equal(got, []string{"MARK"}) {
				t.Errorf("H/wt holds %q, want MARK only", got)
			}
		})
	}
}

// TestRemoveWithALinkedRegistrationsFolder: cell C3, a delete guard test.
// The worktree w1 of branch b1 exists, and its registration is behind a
// symbolic link at the registrations folder (H/wt2). 5fd08069 refuses the
// remove and deletes nothing. Without the test of registrationsLinked and the
// link refusal of openRegistrationsFolder, the remove answers success and
// deletes the leaf, H/wt2/w1 and b1. All three are in temp folders.
func TestRemoveWithALinkedRegistrationsFolder(t *testing.T) {
	f := newCellFixture(t)
	runGit(t, f.repo, "worktree", "add", "-q", f.leaf(), "-b", "b1")
	wt2 := filepath.Join(f.home, "wt2")
	if err := os.Rename(filepath.Join(f.gitDir, "worktrees"), wt2); err != nil {
		t.Fatal(err)
	}
	f.linkRegistrationsTo(t, wt2)
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf(), "branchName": "b1"})
	wantFrame(t, raw, frame(`{"success":false,"error":"failed to remove worktree: could not check whether `+f.leaf()+
		` is locked (its registrations could not be examined); retry"}`))
	wantPaths(t, true, filepath.Join(f.leaf(), "README"), filepath.Join(f.leaf(), ".git"),
		filepath.Join(wt2, "w1", "gitdir"), filepath.Join(wt2, "w1", "HEAD"), filepath.Join(wt2, "MARK"),
		filepath.Join(f.gitDir, "refs", "heads", "b1"), filepath.Join(f.repo, "README"))
	if fi, err := os.Lstat(filepath.Join(f.gitDir, "worktrees")); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link at the registrations folder: %v, want it kept", err)
	}
}

// TestCreateWithAFileAtTheRegistrationsFolder: cell C4. A regular file is at
// the registrations folder. The create gets the text of C1, and the file
// stays as it is.
func TestCreateWithAFileAtTheRegistrationsFolder(t *testing.T) {
	f := newCellFixture(t)
	file := filepath.Join(f.gitDir, "worktrees")
	mustWrite(t, file, "x")
	raw, calls := f.create(t, nil)
	wantFrame(t, raw, frame(`{"success":false,"error":"git worktree add failed: cannot prepare the repository's worktrees directory: \"`+
		file+`\" is not a directory","errorCode":"worktree_add_failed"}`))
	if addRan(calls) {
		t.Error("a worktree add call ran")
	}
	if got := mustRead(t, file); got != "x" {
		t.Errorf("the file at the registrations folder = %q, want x", got)
	}
	wantPaths(t, true, filepath.Join(f.repo, ".claude", "worktrees"))
	wantPaths(t, false, f.leaf(), filepath.Join(f.gitDir, "refs", "heads", "b1"))
}

// TestCreateWithARegistrationsFolderThatCannotBeListed: cell C5. The
// registrations folder has mode 0300. 5fd08069 refuses before `worktree add`.
// No branch and no registration is made, and only R/.claude/worktrees stays.
// Without worktreesOpenRefusal the add runs, and the create answers
// unsafe_path with the leaf, the registration and b1 left behind.
func TestCreateWithARegistrationsFolderThatCannotBeListed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user lists a folder of mode 0300")
	}
	f := newCellFixture(t)
	dir := filepath.Join(f.gitDir, "worktrees")
	if err := os.Mkdir(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	raw, calls := f.create(t, nil)
	wantFrame(t, raw, frame(`{"success":false,"error":"git worktree add failed: cannot prepare the repository's worktrees directory: `+
		`openat worktrees: permission denied","errorCode":"worktree_add_failed"}`))
	if addRan(calls) {
		t.Error("a worktree add call ran")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	wantPaths(t, true, filepath.Join(f.repo, ".claude", "worktrees"))
	wantPaths(t, false, f.leaf(), filepath.Join(dir, "w1"), filepath.Join(f.gitDir, "refs", "heads", "b1"))
}

// TestCreateWithAGitDirCopiedDuringTheAdd: cell C6, by the decision of
// the maintainer of 2026-10-14. The git stub stands in for the wrapper of the
// round: right before the real `git worktree add` runs, it renames the git
// directory of R (the old folder) and puts a real copy of it at its path.
// 5fd08069 answers success.
// The copy has the registration with its index and b1, the leaf is checked
// out, and the old folder is unchanged. Without replacedAtPath the create
// answers `was not populated by git worktree add` with unsafe_path.
func TestCreateWithAGitDirCopiedDuringTheAdd(t *testing.T) {
	f := newCellFixture(t)
	copied := f.gitDir + ".copy"
	if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	// The create makes the empty folder `worktrees` before the add, so the copy
	// of the round, made at the add, holds it too.
	if err := os.Mkdir(filepath.Join(copied, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := f.gitDir + ".old"
	before := fileDigests(t, f.gitDir)
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", strings.Join([]string{
		"rename\x1f" + f.gitDir + "\x1f" + old,
		"rename\x1f" + copied + "\x1f" + f.gitDir,
	}, "\x1e"))
	slowGit(t, "worktree,add", "pre", 0, "", "")
	raw, calls := f.create(t, nil)
	wantFrame(t, raw, frame(`{"success":true,"path":"`+f.leaf()+`","sourceBranch":"main","branch":"b1"}`))
	if _, err := os.Lstat(old); err != nil {
		t.Fatalf("the stub did not swap the git directory: %v", err)
	}
	if got := mustRead(t, filepath.Join(f.leaf(), ".git")); got != "gitdir: "+filepath.Join(f.gitDir, "worktrees", "w1")+"\n" {
		t.Errorf("the .git file of the leaf = %q", got)
	}
	reg := filepath.Join(f.gitDir, "worktrees", "w1")
	wantPaths(t, true, filepath.Join(f.leaf(), "README"), filepath.Join(reg, "HEAD"), filepath.Join(reg, "commondir"),
		filepath.Join(reg, "gitdir"), filepath.Join(reg, "index"), filepath.Join(f.gitDir, "refs", "heads", "b1"))
	wantPaths(t, false, filepath.Join(old, "worktrees", "w1"), filepath.Join(old, "refs", "heads", "b1"))
	if after := fileDigests(t, old); !slices.Equal(after, before) {
		t.Errorf("the old git directory changed:\n%q\nwant\n%q", after, before)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") }) {
		t.Error("no read-tree call ran after the add")
	}
}

// TestCreateFromALinkedWorktreeWithALinkedEntry: cell C7. baseRepo is the
// linked worktree H/B, and its entry under the registrations folder is a
// symbolic link to H/eB, with extensions.worktreeConfig set and a
// `config.worktree` file in the entry. 5fd08069 answers the trust refusal of
// H/eB after the words of a git directory that cannot be located, and
// B/.claude/worktrees exists after the reply. The four files of the cell stay.
// Without entryIsLink the reply has no such words and nothing is made.
func TestCreateFromALinkedWorktreeWithALinkedEntry(t *testing.T) {
	f := newCellFixture(t)
	b := filepath.Join(f.home, "B")
	eb := filepath.Join(f.home, "eB")
	runGit(t, f.repo, "config", "extensions.worktreeConfig", "true")
	runGit(t, f.repo, "worktree", "add", "-q", b, "-b", "bb")
	runGit(t, b, "config", "--worktree", "core.x", "1")
	entry := filepath.Join(f.gitDir, "worktrees", "B")
	if err := os.Rename(entry, eb); err != nil {
		t.Fatal(err)
	}
	linkAt(t, entry, eb)
	leaf := filepath.Join(b, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": b, "worktreePath": leaf, "branchName": "b1", "sourceBranch": "main"})
	wantFrame(t, raw, frame(`{"success":false,"error":"git worktree add failed: cannot locate the repository's git directory: `+
		`the repository's git directory could not be trusted; git not run: commondir not as git writes it: \"`+eb+
		`/commondir\" holds something other than \".\" or \"./\"; git writes that file only inside .git/worktrees/\u003cname\u003e/, and Claude Code's sandbox `+
		`may leave one containing just \".\"; anything else sends git's configuration and hooks elsewhere, so treat it as tampering: `+
		`delete it, and if you did not create it, find out what did","errorCode":"worktree_add_failed"}`))
	if addRan(calls) {
		t.Error("a worktree add call ran")
	}
	wantPaths(t, true, filepath.Join(b, ".claude", "worktrees"), filepath.Join(eb, "config.worktree"),
		filepath.Join(eb, "gitdir"), entry, filepath.Join(b, "README"))
	wantPaths(t, false, leaf, filepath.Join(f.gitDir, "refs", "heads", "b1"))
}

// TestCreateWithAnUnreadableParentOfTheGitDir: cell C8. The git directory is
// H/p/R.git, R/.git is a file that names it, and H/p has mode 0300. The create
// succeeds, as on 5fd08069.
func TestCreateWithAnUnreadableParentOfTheGitDir(t *testing.T) {
	f := newCellFixture(t)
	p := filepath.Join(f.home, "p")
	gitDir := filepath.Join(p, "R.git")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.gitDir, gitDir); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, f.gitDir, "gitdir: "+gitDir+"\n")
	if err := os.Chmod(p, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o700) })
	raw, _ := f.create(t, nil)
	wantFrame(t, raw, frame(`{"success":true,"path":"`+f.leaf()+`","sourceBranch":"main","branch":"b1"}`))
	if got := mustRead(t, filepath.Join(f.leaf(), ".git")); got != "gitdir: "+filepath.Join(gitDir, "worktrees", "w1")+"\n" {
		t.Errorf("the .git file of the leaf = %q", got)
	}
	reg := filepath.Join(gitDir, "worktrees", "w1")
	wantPaths(t, true, filepath.Join(f.leaf(), "README"), filepath.Join(reg, "HEAD"), filepath.Join(reg, "commondir"),
		filepath.Join(reg, "gitdir"), filepath.Join(reg, "index"), filepath.Join(gitDir, "refs", "heads", "b1"))
	// be1c1ca5 makes no `logs` folder in the new entry (measured on Linux and
	// macOS).
	wantPaths(t, false, filepath.Join(reg, "logs"))
}

// lockedLinkedWorktree makes the worktree leaf of branch b1 with git, locks it,
// moves the registrations folder to H/wt2 and puts a symbolic link to it in its
// place. It returns H/wt2.
func (f cellFixture) lockedLinkedWorktree(t *testing.T, leaf string) string {
	t.Helper()
	runGit(t, f.repo, "worktree", "add", "-q", leaf, "-b", "b1")
	runGit(t, f.repo, "worktree", "lock", leaf)
	wt2 := filepath.Join(f.home, "wt2")
	if err := os.Rename(filepath.Join(f.gitDir, "worktrees"), wt2); err != nil {
		t.Fatal(err)
	}
	f.linkRegistrationsTo(t, wt2)
	return wt2
}

// TestRemoveOfALockedWorktreeBehindALink: cells L1 to L3 of round tier0-26
// (Linux VM). The registration of w1 is locked and behind a symbolic link at
// the registrations folder.
//
//   - L1: the worktree folder is gone. 5fd08069 answers success and deletes
//     b1. The locked registration behind the link stays.
//   - L2: the worktree folder exists. The lock-check refusal, and nothing is
//     deleted.
//   - L3: a worktreeRoot. The same refusal, and nothing is deleted. The cell
//     has no lock. The test adds one, and the answer is the same.
//
// Without the link refusal of openRegistrationsFolder, L1 answers the locked
// text and keeps b1. L2 and L3 then answer the locked text too.
func TestRemoveOfALockedWorktreeBehindALink(t *testing.T) {
	lockCheck := func(p string) string {
		return frame(`{"success":false,"error":"failed to remove worktree: could not check whether ` + p +
			` is locked (its registrations could not be examined); retry"}`)
	}
	t.Run("L1 gone", func(t *testing.T) {
		f := newCellFixture(t)
		wt2 := f.lockedLinkedWorktree(t, f.leaf())
		if err := os.RemoveAll(f.leaf()); err != nil {
			t.Fatal(err)
		}
		raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf(), "branchName": "b1"})
		wantFrame(t, raw, frame(`{"success":true}`))
		wantPaths(t, false, filepath.Join(f.gitDir, "refs", "heads", "b1"))
		wantPaths(t, true, filepath.Join(wt2, "w1", "locked"), filepath.Join(wt2, "w1", "gitdir"), filepath.Join(wt2, "MARK"))
	})
	t.Run("L2 present", func(t *testing.T) {
		f := newCellFixture(t)
		wt2 := f.lockedLinkedWorktree(t, f.leaf())
		raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf(), "branchName": "b1"})
		wantFrame(t, raw, lockCheck(f.leaf()))
		wantPaths(t, true, filepath.Join(f.leaf(), "README"), filepath.Join(wt2, "w1", "locked"),
			filepath.Join(f.gitDir, "refs", "heads", "b1"))
	})
	t.Run("L3 worktreeRoot", func(t *testing.T) {
		f := newCellFixture(t)
		root := filepath.Join(f.home, "ext")
		leaf := filepath.Join(root, "p", "w1")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "b1", "worktreeRoot": root})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("the create of the worktree = %s", raw)
		}
		runGit(t, f.repo, "worktree", "lock", leaf)
		wt2 := filepath.Join(f.home, "wt2")
		if err := os.Rename(filepath.Join(f.gitDir, "worktrees"), wt2); err != nil {
			t.Fatal(err)
		}
		f.linkRegistrationsTo(t, wt2)
		raw, _ = f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "b1", "worktreeRoot": root})
		wantFrame(t, raw, lockCheck(leaf))
		wantPaths(t, true, filepath.Join(leaf, "README"), filepath.Join(root, "p", ".claude-managed-worktrees"),
			filepath.Join(wt2, "w1", "locked"), filepath.Join(f.gitDir, "refs", "heads", "b1"))
	})
}

// TestCreateAfterACopiedGitDir: the git directory of R is renamed and a real
// copy is put at its path. The staging step takes the hold on the copy and
// moves the registration into it (acceptedGitDir.replacedAtPath). be1c1ca5
// moves it into a copy made right before the add or right after it (measured
// on Linux).
//
//   - L4: the copy goes in right before the real `worktree add`, and the
//     read-tree call fails. The leaf goes. The rollback deletes nothing in the
//     copy and says so (claustrum's own), and the old folder is unchanged.
//   - L5: a copy made before the add goes in right after it. The create
//     succeeds in the copy, and the old folder keeps b1 and no registration.
func TestCreateAfterACopiedGitDir(t *testing.T) {
	t.Run("L4 a failed read-tree after the move", func(t *testing.T) {
		f := newCellFixture(t)
		copied := f.gitDir + ".copy"
		if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(copied, "worktrees"), 0o755); err != nil {
			t.Fatal(err)
		}
		old := f.gitDir + ".old"
		before := fileDigests(t, f.gitDir)
		t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
		t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rename\x1f"+f.gitDir+"\x1f"+old+"\x1erename\x1f"+copied+"\x1f"+f.gitDir)
		slowGit(t, "worktree,add", "pre", 0, "", "")
		t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "read-tree")
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
		t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "fatal: forced failure by the wrapper\n")
		raw, _ := f.create(t, nil)
		wantFrame(t, raw, frame(`{"success":false,"error":"git worktree add failed (checkout): fatal: forced failure by the wrapper; `+
			`and the undo could not finish for `+f.leaf()+`: the worktree registration and the branch remain; remove them by hand before retrying (`+
			replacedGitDirText+`)","errorCode":"worktree_add_failed"}`))
		wantPaths(t, false, f.leaf(), filepath.Join(old, "worktrees", "w1"), filepath.Join(old, "refs", "heads", "b1"))
		wantPaths(t, true, filepath.Join(f.gitDir, "worktrees", "w1", "HEAD"), filepath.Join(f.gitDir, "refs", "heads", "b1"))
		if after := fileDigests(t, old); !slices.Equal(after, before) {
			t.Errorf("the old git directory changed:\n%q\nwant\n%q", after, before)
		}
	})
	t.Run("L5 a copy made before the add", func(t *testing.T) {
		f := newCellFixture(t)
		copied := f.gitDir + ".copy"
		if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(copied, "worktrees"), 0o755); err != nil {
			t.Fatal(err)
		}
		old := f.gitDir + ".old"
		t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
		t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rename\x1f"+f.gitDir+"\x1f"+old+"\x1erename\x1f"+copied+"\x1f"+f.gitDir)
		slowGit(t, "worktree,add", "post", 0, "", "")
		raw, _ := f.create(t, nil)
		wantFrame(t, raw, frame(`{"success":true,"path":"`+f.leaf()+`","sourceBranch":"main","branch":"b1"}`))
		if got, want := mustRead(t, filepath.Join(f.leaf(), ".git")), worktreeGitFileText(f.gitDir, "w1"); got != want {
			t.Errorf("the .git file of the leaf = %q, want %q", got, want)
		}
		wantPaths(t, true, filepath.Join(f.gitDir, "worktrees", "w1", "HEAD"), filepath.Join(old, "refs", "heads", "b1"))
		wantPaths(t, false, filepath.Join(old, "worktrees", "w1"))
	})
}
