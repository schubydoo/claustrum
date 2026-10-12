//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// linkAt makes a symbolic link at link to target.
func linkAt(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Errorf("the link %s: %v", link, err)
	}
}

// TestStagedAddWithAFolderThatTakesNoEntry: the folder of the daemon takes no
// new entry. On 5fd08069 the create answers `git worktree add failed: cannot
// prepare the repository's worktrees directory: mkdir
// C:\o\w\run\git-shadow\<S>\.s<n>: Access is denied.` (tables W3 and F1 of
// val-s3p2b2-7-windows, Windows VM, with the two privileges off). The staged
// add runs on Windows only. This test calls it on Linux and macOS, where the
// error of the system reads `permission denied`. Without the words of the step
// the text starts with claustrum's own words.
func TestStagedAddWithAFolderThatTakesNoEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	if err := os.Chmod(u.folder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.folder, 0o700) })
	_, stderr, err := stagedAdd(t, f, u)
	want := regexp.MustCompile(`^cannot prepare the repository's worktrees directory: mkdir ` +
		regexp.QuoteMeta(filepath.Join(u.folder, ".s")) + `[0-9]{1,10}: permission denied$`)
	if err == nil || !want.MatchString(err.Error()) || stderr != "" {
		t.Errorf("the add = %v, %q\nwant %s", err, stderr, want)
	}
}

// createError is the `error` member of the result of a create reply.
func createError(t *testing.T, raw string) string {
	t.Helper()
	var reply struct {
		Result struct {
			Success bool
			Error   string
		}
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil || reply.Result.Success {
		t.Fatalf("the reply %s: %v, want a failed create", raw, err)
	}
	return reply.Result.Error
}

// TestCreateWithASwappedGitDir runs git.worktree_create through the server
// with the steps of a held git directory switched on, and puts a link to a
// folder T that is no git directory at `.git` at one step. The texts are those
// of 5fd08069 on a Windows VM (val-s3p2b2-7-windows). On Linux and macOS no
// create runs these steps: the test sets the switch.
//
//   - Before `rev-parse --absolute-git-dir`: the call exits 128, and the create
//     stops with `cannot locate the repository's git directory: exit status
//     128` (cells W1c and X1p-02 to X1p-05). No `worktree add` runs.
//   - After that call: `cannot list the repository's worktree entries: ".git"
//     is a link or a file …` (cell X1p-06).
//   - Before the read-tree call: the failed checkout with the refusal of the
//     config, and a rollback that keeps the registration and the branch and
//     removes the leaf (cell X1p-11).
//
// In each row T keeps its two entries and gets no other. Without the stop
// after the failed call the reply is the error of `worktree add`. Without the
// test after the call the create goes on to that call. Without the test before
// the read-tree call the reply is the error of git, and without the guard of
// the rollback the text has no registration part.
func TestCreateWithASwappedGitDir(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	link := func(gitDir string) string {
		return "this repository's git config cannot be used: " + quoteOperand(filepath.Join(gitDir, "config")) +
			": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
	}
	for _, row := range []struct {
		step string
		want func(gitDir, leaf string) string
		// added is true when the `worktree add` call ran before the swap.
		added bool
	}{
		{step: "gitdir", want: func(string, string) string {
			return "git worktree add failed: cannot locate the repository's git directory: exit status 128"
		}},
		{step: "answered", want: func(string, string) string {
			return "git worktree add failed: cannot list the repository's worktree entries: " + realDir
		}},
		{step: "checkout", added: true, want: func(gitDir, leaf string) string {
			return "git worktree add failed (checkout): " + link(gitDir) + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
	} {
		t.Run(row.step, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			target := newSwapTarget(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swapAt(t, row.step, f.gitDir, target.dir)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if got, want := createError(t, raw), row.want(f.gitDir, leaf); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
				t.Errorf("the reply = %s, want the code worktree_add_failed", raw)
			}
			target.untouched(t)
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			ran := slices.ContainsFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add")
			})
			if ran != row.added {
				t.Errorf("a worktree add call ran: %v, want %v", ran, row.added)
			}
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("refs", "heads", "w1")} {
				_, err := os.Lstat(filepath.Join(f.gitDir+"-real", name))
				if row.added && err != nil {
					t.Errorf("%s in the real git directory: %v, want it kept", name, err)
				}
				if !row.added && !os.IsNotExist(err) {
					t.Errorf("%s in the real git directory: %v, want none", name, err)
				}
			}
		})
	}
	t.Run("no swap", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
		raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
		if !strings.Contains(raw, `"success":true`) {
			t.Errorf("git.worktree_create = %s, want a good create", raw)
		}
	})
}

// TestCreateWithAnotherGitDirAtThePath runs git.worktree_create through the
// server, with the switch as the system has it. At one step the git directory
// goes to `.git-real`, and the path gets another entry.
//
// Two rows are those of 5fd08069 on Linux and macOS VMs (val-s3p2b2-9-linux2
// and val-s3p2b2-9-macos). The swap comes after `rev-parse
// --absolute-git-dir`:
//
//   - A symbolic link to a copy of the git directory. Three calls follow: the
//     call about the repository, the read of the config, and the listing of
//     the add with the variables of the calls before the swap. The create is
//     refused with `cannot prepare the repository's worktrees directory: ".git"
//     is a link or a file …`. The copy gets no entry. A build with no held git
//     directory answers success there and makes the registration in the copy.
//   - A symbolic link to a folder T that is no git directory. One listing with
//     GIT_DIR=/dev/null follows, and the text is `cannot list the repository's
//     worktree entries: ".git" is a link or a file …`.
//
// Two more rows are cells swc-file and swc-link of val-s3p2b2-10-linux2 (Linux
// VM, and cells c-cfile and c-clink of val-s3p2b2-10-macos). After the call,
// a file with `gitdir: <X>` gives the `lstat … commondir: not a directory`
// text with no call after it, and a link to X gives the text of the copy
// after three calls. Without the lstat the file row gets the link text.
//
// The other rows are not measured. A copy of the git directory as a real
// directory and a swap before the add get the rule of the measured rows. A
// file with `gitdir: <X>` before the call fails the `rev-parse
// --absolute-git-dir` call, and the create stops there.
//
// No `worktree add` runs, and no folder gets a registration or a branch.
// Without the test before the plain add each row runs that call.
func TestCreateWithAnotherGitDirAtThePath(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	const prepare = "git worktree add failed: cannot prepare the repository's worktrees directory: " + realDir
	const list = "git worktree add failed: cannot list the repository's worktree entries: " + realDir
	for _, row := range []struct {
		name, step, entry, want string
		// after is the number of git calls after `rev-parse --absolute-git-dir`,
		// or -1 for a row with no record.
		after int
	}{
		{"swap-copy: a link to a copy after the gitdir call", "answered", "link to the copy", prepare, 3},
		{"swap-marker: a link to no repository after the gitdir call", "answered", "none", list, 1},
		{"a file that names X", "leaf", "file", "git worktree add failed: cannot locate the repository's git directory: exit status 128", -1},
		{"swc-file: a file that names X after the gitdir call", "answered", "file", "git worktree add failed: cannot locate the repository's git directory: lstat <gitdir>/commondir: not a directory", 0},
		{"swc-link: a link to X after the gitdir call", "answered", "link", prepare, 3},
		{"a link to X before the add", "add", "link", prepare, -1},
		{"another real directory", "gitdir", "copy", prepare, -1},
		{"no repository before the add", "add", "none", list, -1},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			_, other := configRepo(t)
			copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			target := newSwapTarget(t)
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
					mustWrite(t, f.gitDir, "gitdir: "+other+"\n")
				case "link":
					linkAt(t, f.gitDir, other)
				case "link to the copy":
					linkAt(t, f.gitDir, copied)
				case "copy":
					if err := os.Rename(copied, f.gitDir); err != nil {
						t.Errorf("the rename of the copy: %v", err)
					}
				default:
					linkAt(t, f.gitDir, target.dir)
				}
			})
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if got, want := createError(t, raw), strings.ReplaceAll(row.want, "<gitdir>", f.gitDir); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if slices.ContainsFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add")
			}) {
				t.Error("a worktree add call ran")
			}
			if row.after >= 0 {
				held := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--absolute-git-dir") })
				rest := calls[held+1:]
				if held < 0 || len(rest) != row.after {
					t.Fatalf("%d calls after the gitdir call, want %d", len(rest), row.after)
				}
			}
			if row.after > 0 {
				held := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--absolute-git-dir") })
				rest := calls[held+1:]
				last := rest[len(rest)-1]
				if !slices.Contains(last.argv, "--list") || last.cwd != f.repo {
					t.Errorf("the last call = %q in %s, want the listing of the add in the repository", last.argv, last.cwd)
				}
				if row.after == 1 {
					if !slices.Contains(last.env, "GIT_DIR="+os.DevNull) {
						t.Errorf("the listing has no GIT_DIR=%s: %q", os.DevNull, last.env)
					}
				} else {
					if !slices.Contains(rest[0].argv, "--git-dir") || !slices.Contains(rest[1].argv, "--no-includes") {
						t.Errorf("the two calls before the listing = %q and %q, want the call about the repository and the read of the config", rest[0].argv, rest[1].argv)
					}
					for _, kv := range []string{"GIT_DIR=" + f.gitDir, "GIT_WORK_TREE=" + f.repo,
						"GIT_COMMON_DIR=" + filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir))} {
						if !slices.Contains(last.env, kv) {
							t.Errorf("the listing has no %s: %q", kv, last.env)
						}
					}
				}
			}
			target.untouched(t)
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			for _, dir := range []string{other, f.gitDir + "-real", copied} {
				for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
					if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
						t.Errorf("%s in %s: %v, want none", name, dir, err)
					}
				}
			}
		})
	}
}

// TestCreateStepsOfAHeldGitDir runs git.worktree_create through the server and
// pins that two steps of the create go through the held folder. A link to a
// folder T is put at the path of the git directory right before the step.
//
//   - The step before the leaf asks the registrations folder of the held git
//     directory for the delete of an old registration whose record names the
//     leaf. T has an old registration with the same record. The removal is a
//     recorder. With a step by the path the recorder gets the folder of T.
//   - The index of the checkout goes into the registration of the held folder.
//     The index of T keeps its bytes. With a placement by the path the held
//     folder gets no index.
func TestCreateStepsOfAHeldGitDir(t *testing.T) {
	t.Run("the old registration", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		worktrees, err := os.Stat(filepath.Join(f.gitDir, "worktrees"))
		if err != nil {
			t.Fatal(err)
		}
		removals := recordHeldRemovals(t, worktrees)
		leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
		record := filepath.Join(leaf, ".git") + "\n"
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old8", "gitdir"), record)
		other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
		mustWrite(t, filepath.Join(other, "worktrees", "old8", "gitdir"), record)
		swapAt(t, "stale", f.gitDir, other)
		_, _ = f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
		if got := strings.Join(*removals, ","); got != "old8 held" {
			t.Errorf("the create asked to remove %q, want the old registration of the held folder", got)
		}
	})
	t.Run("the index", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
		other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
		otherIndex := filepath.Join(other, "worktrees", "w1", "index")
		mustWrite(t, otherIndex, "the index of the other folder\n")
		mustWrite(t, filepath.Join(other, "worktrees", "w1", "gitdir"), filepath.Join(leaf, ".git")+"\n")
		swapAt(t, "index", f.gitDir, other)
		_, _ = f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
		if got := mustRead(t, otherIndex); got != "the index of the other folder\n" {
			t.Errorf("the index of the other folder = %q", got)
		}
		if ents, err := os.ReadDir(filepath.Dir(otherIndex)); err != nil || len(ents) != 2 {
			t.Errorf("the registration of the other folder holds %d entries (%v), want 2", len(ents), err)
		}
		if fi, err := os.Lstat(filepath.Join(f.gitDir+"-real", "worktrees", "w1", "index")); err != nil || fi.Size() == 0 {
			t.Errorf("the index of the held folder: %v, want the index of the checkout", err)
		}
	})
}

// TestCreateGoesOnInTheGitDirThatGitAnswers: a symbolic link to another git
// directory is put at `.git` before `rev-parse --absolute-git-dir`. Git then
// answers that directory, and the create goes on in it, as on 5fd08069 (cells
// swb-first and swb-abbrev of val-s3p2b2-10-linux2, Linux VM, and cells
// b-first and b-abbrev of val-s3p2b2-10-macos, macOS VM). The copy T gets the
// registration, the branch and the index. The `.git` file of the leaf names
// `T/worktrees/w1`. The `worktree add` keeps GIT_DIR=<repo>/.git, and the
// read-tree names T and its objects. The real git directory gets nothing.
// Without the move of the hold the create is refused before its add.
//
// The row with the git directory X of a second repository is not measured: it
// gets the same rule.
func TestCreateGoesOnInTheGitDirThatGitAnswers(t *testing.T) {
	for _, row := range []struct{ name, step, entry string }{
		{"swb-first: a link to a copy after the first calls", "leaf", "copy"},
		{"swb-abbrev: a link to a copy before the gitdir call", "gitdir", "copy"},
		{"a link to X before the gitdir call", "gitdir", "X"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			_, other := configRepo(t)
			copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			target := copied
			if row.entry == "X" {
				target = other
			}
			swapAt(t, row.step, f.gitDir, target)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want a good create", raw)
			}
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("worktrees", "w1", "index"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(target, name)); err != nil {
					t.Errorf("%s in the directory that git answered: %v", name, err)
				}
				if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", name)); !os.IsNotExist(err) {
					t.Errorf("%s in the real git directory: %v, want none", name, err)
				}
			}
			if got, want := mustRead(t, filepath.Join(leaf, ".git")), "gitdir: "+filepath.Join(target, "worktrees", "w1")+"\n"; got != want {
				t.Errorf("the .git file of the leaf = %q, want %q", got, want)
			}
			add := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") })
			tree := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") })
			if add < 0 || tree < 0 {
				t.Fatalf("the calls have no worktree add or no read-tree")
			}
			if !slices.Contains(calls[add].env, "GIT_DIR="+f.gitDir) {
				t.Errorf("the worktree add has no GIT_DIR=%s: %q", f.gitDir, calls[add].env)
			}
			if !slices.Contains(calls[tree].argv, "--git-dir="+target) || !slices.Contains(calls[tree].env, "GIT_OBJECT_DIRECTORY="+filepath.Join(target, "objects")) {
				t.Errorf("the read-tree = %q with %q, want the directory that git answered and its objects", calls[tree].argv, calls[tree].env)
			}
		})
	}
}

// TestCreateWithAGitDirThatChangedAtTheAdd: right after a good `worktree add`
// the path of the git directory no longer names the held folder. 5fd08069
// answers `refusing to create worktree: <leaf> was not populated by git
// worktree add` with `unsafe_path`, makes no other call and rolls nothing back
// (cell swb-wtadd of val-s3p2b2-10-linux2, Linux VM, and cells b-postadd and
// b-precfg of val-s3p2b2-10-macos, macOS VM). The real git directory keeps
// the registration and the branch, and T gets nothing. Without the test after
// the add the text is that of a `.git` file that names no registration.
func TestCreateWithAGitDirThatChangedAtTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	swapAt(t, "added", f.gitDir, copied)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got, want := createError(t, raw), "refusing to create worktree: "+leaf+" was not populated by git worktree add"; got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if !strings.Contains(raw, `"errorCode":"unsafe_path"`) {
		t.Errorf("the reply = %s, want the code unsafe_path", raw)
	}
	if last := calls[len(calls)-1]; !slices.Contains(last.argv, "worktree") || !slices.Contains(last.argv, "add") {
		t.Errorf("the last call = %q, want the worktree add", last.argv)
	}
	for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("refs", "heads", "w1")} {
		if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", name)); err != nil {
			t.Errorf("%s in the real git directory: %v, want it kept", name, err)
		}
		if _, err := os.Lstat(filepath.Join(copied, name)); !os.IsNotExist(err) {
			t.Errorf("%s in the copy: %v, want none", name, err)
		}
	}
}

// TestCreateWithALinkedGitDirThatChanges: `.git` is a symbolic link to a real
// git directory before the first request, and the daemon serves the
// repository (cell D part 1 of val-s3p2b2-10-linux2). After `rev-parse
// --absolute-git-dir` the link is replaced by a link to a copy T. The create
// is refused before its `worktree add`, and no folder gets a branch, a
// registration or a leaf. That end state is the one of 5fd08069 in cell
// dlink-swap (Linux VM) and cell d-dswap (macOS VM). The text is that of the
// nearest measured step, cell sw-copy: 5fd08069 answers another text there,
// after a `worktree add` and a rollback. Without the test of the spelling of
// the request the create answers success in the first git directory.
func TestCreateWithALinkedGitDirThatChanges(t *testing.T) {
	f := newSeededShadowFixture(t)
	realgd := filepath.Join(resolveTestRoot(t, t.TempDir()), "realgd")
	if err := os.Rename(f.gitDir, realgd); err != nil {
		t.Fatal(err)
	}
	linkAt(t, f.gitDir, realgd)
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	if err := os.CopyFS(copied, os.DirFS(realgd)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) || !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	atStep(t, "answered", func() {
		if err := os.Remove(f.gitDir); err != nil {
			t.Errorf("the remove of the link: %v", err)
			return
		}
		linkAt(t, f.gitDir, copied)
	})
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := "git worktree add failed: cannot prepare the repository's worktrees directory: " +
		`".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	if got := createError(t, raw); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") }) {
		t.Error("a worktree add call ran")
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	for _, dir := range []string{realgd, copied} {
		for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
			if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("%s in %s: %v, want none", name, dir, err)
			}
		}
	}
}

// TestCreateOverAnOldRegistrationInTheSpellingOfTheRequest: the git directory
// holds one old registration `w1` whose record names the `.git` of the new
// leaf, and the leaf does not exist. The create removes that registration and
// succeeds, as on 5fd08069 in two cells of val-s3p2b2-11-macos (macOS VM, 2 of
// 2 runs).
//
//   - i-ctl: baseRepo, worktreePath and the record go through a symbolic link
//     above the repository. The reply has the path as sent. The `.git` file of
//     the leaf and the new record have the real path.
//   - h-ctl, the control: all three have the real path.
//
// Without the compare against the path as sent, the registration of row i-ctl
// stays, and git refuses the add of a registered worktree.
func TestCreateOverAnOldRegistrationInTheSpellingOfTheRequest(t *testing.T) {
	for _, row := range []struct {
		name string
		link bool
	}{
		{"i-ctl: through a link above the repository", true},
		{"h-ctl: the real path", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			repo := f.repo
			if row.link {
				above := filepath.Join(resolveTestRoot(t, t.TempDir()), "above")
				linkAt(t, above, filepath.Dir(f.repo))
				repo = filepath.Join(above, filepath.Base(f.repo))
			}
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			old := filepath.Join(f.gitDir, "worktrees", "w1")
			mustWrite(t, filepath.Join(old, "gitdir"), filepath.Join(leaf, ".git")+"\n")
			mustWrite(t, filepath.Join(old, "HEAD"), "ref: refs/heads/w1\n")
			mustWrite(t, filepath.Join(old, "commondir"), "../..\n")
			mustWrite(t, filepath.Join(old, "marker-stale.txt"), "the old registration\n")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": leaf})
			if !strings.Contains(raw, `"success":true,"path":"`+leaf+`"`) {
				t.Fatalf("git.worktree_create = %s, want a good create at %s", raw, leaf)
			}
			if _, err := os.Lstat(filepath.Join(old, "marker-stale.txt")); !os.IsNotExist(err) {
				t.Errorf("the marker of the old registration: %v, want it gone", err)
			}
			realLeaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			if got, want := mustRead(t, filepath.Join(realLeaf, ".git")), "gitdir: "+old+"\n"; got != want {
				t.Errorf("the .git file of the leaf = %q, want %q", got, want)
			}
			if got, want := mustRead(t, filepath.Join(old, "gitdir")), filepath.Join(realLeaf, ".git")+"\n"; got != want {
				t.Errorf("the record of the registration = %q, want %q", got, want)
			}
		})
	}
}
