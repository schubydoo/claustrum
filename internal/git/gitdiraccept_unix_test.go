//go:build unix

package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// linkAt makes a symbolic link at link to target.
func linkAt(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Errorf("the link %s: %v", link, err)
	}
}

// TestStagedAddWithAFolderThatTakesNoEntry: the folder of the daemon takes no
// new entry. On Linux be1c1ca5 answers privateCopyText and `mkdir
// <S>/.s<n>: permission denied` (a folder git-shadow of mode 555). Windows
// keeps the text of 5fd08069, with prepareWorktreesPrefix. Without
// requestDirError the text starts with prepareWorktreesPrefix.
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
	want := regexp.MustCompile(`^` + regexp.QuoteMeta(privateCopyText) + `mkdir ` +
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
// of 5fd08069 on Windows (measured). On Linux and macOS no create runs these
// steps: the test sets the switch.
//
//   - Before `rev-parse --absolute-git-dir`: the call exits 128, and the create
//     stops with `cannot locate the repository's git directory: exit status
//     128` (measured). No `worktree add` runs.
//   - After that call: `cannot list the repository's worktree entries: ".git"
//     is a link or a file …` (measured).
//   - Before the read-tree call: the failed checkout with the refusal of the
//     config, and a rollback that keeps the registration and the branch and
//     removes the leaf (measured).
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
// One row is that of 5fd08069 on Linux and macOS (measured). A symbolic link
// to a folder T that is no git directory is put at the path after `rev-parse
// --absolute-git-dir`. One listing with GIT_DIR=/dev/null follows, and the text
// is `cannot list the repository's worktree entries: ".git" is a link or a file
// …`. A link to a git directory there is in
// TestCreateGoesOnAfterASwapRightAfterTheAnswer.
//
// One more row is measured on Linux and macOS. After the call, a file with
// `gitdir: <X>` gives the `lstat … commondir: not a directory` text with no
// call after it. Without the lstat the file row gets the link text.
//
// A link to the copy after the listing of the add starts the text with `cannot
// add a worktree: `. A new read of the config and the pair come before the
// refusal (4c111e86, measured on Linux). Without the read and the pair in that
// refusal the calls end at the listing.
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
		{"swap-marker: a link to no repository after the gitdir call", "answered", "none", list, 1},
		{"a file that names X", "leaf", "file", "git worktree add failed: cannot locate the repository's git directory: exit status 128", -1},
		{"swc-file: a file that names X after the gitdir call", "answered", "file", "git worktree add failed: cannot locate the repository's git directory: lstat <gitdir>/commondir: not a directory", 0},
		{"a link to X before the add", "add", "link", prepare, -1},
		{"another real directory", "gitdir", "copy", prepare, -1},
		{"a link to the copy after the listing of the add", "plain add", "link to the copy", "cannot add a worktree: cannot prepare the repository's worktrees directory: " + realDir, -1},
		{"no repository before the add", "add", "none", list, -1},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			// The rows do not show a registrations folder in the repository.
			// Without one 5fd08069 runs no pair before the add. b3081b51 and
			// c9125eb8 run it in the first create of a repository, right before
			// the add (pairBeforeAdd). A refusal before the add runs no pair
			// (claustrum's own, not measured).
			if err := os.RemoveAll(filepath.Join(f.gitDir, worktreesSubdir)); err != nil {
				t.Fatal(err)
			}
			_, other := configRepo(t)
			copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			target := newSwapTarget(t)
			records := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
			if err := os.Mkdir(records, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sessiondirs.UseForTest(records))
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
			// The refusal of the lstat of `commondir` keeps the record of the
			// git directory (from the code: the record comes before the test).
			if row.entry == "file" && row.step == "answered" {
				if got, want := recordedPaths(t, records), canonicalPath(f.gitDir); len(got) != 1 || got[0] != want {
					t.Errorf("after the refusal the records name %q, want %q", got, want)
				}
			}
			// After the listing of the add 4c111e86 reads the config again and
			// runs the pair, then refuses (measured on Linux).
			if row.step == "plain add" {
				want := []string{
					"config -z --list | " + f.repo,
					"config --no-includes --file - --list -z | /",
					"--git-dir=" + f.gitDir + " config -z --list | " + copied,
					"--git-dir=" + f.gitDir + " --work-tree=" + f.repo + " rev-parse --show-toplevel | " + copied,
				}
				if got := callsAfterAnswer(calls); !slices.Equal(got, want) {
					t.Errorf("the calls after the gitdir call:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
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
				if !slices.Contains(last.env, "GIT_DIR="+os.DevNull) {
					t.Errorf("the listing has no GIT_DIR=%s: %q", os.DevNull, last.env)
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
// pins that one step of the create goes through the held folder. A link to a
// folder T is put at the path of the git directory right before the step.
//
// The step before the leaf asks the registrations folder of the held git
// directory for the delete of an old registration whose record names the
// leaf. T has an old registration with the same record. The removal is a
// recorder. With a step by the path the recorder gets the folder of T.
//
// The index has no row here: a create with a link at the path after the
// read-tree call places none (TestCreateWithASwapAfterTheReadTree). The
// placement through the held folder is in TestHeldIndexPlacement.
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
}

// TestCreateWithASwapAfterTheReadTree: a link takes the place of `.git` after
// the read-tree call of a create ended. 5fd08069 refuses the create with the
// text below, removes the folder of the new worktree, and leaves the
// registration with no index and the branch in the real git directory. It
// makes no git call after the read-tree call, and the folder behind the link
// is unchanged (measured on Linux, 6 of 6 runs, and on macOS, 11 of 11 runs).
// Without the test after the read-tree call the create answers success and
// the registration gets the index.
func TestCreateWithASwapAfterTheReadTree(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	for _, form := range []string{"a copy of the git directory", "a folder that is no git directory"} {
		t.Run(form, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
			if form == "a copy of the git directory" {
				if err := os.CopyFS(other, os.DirFS(f.gitDir)); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite(t, filepath.Join(other, "marker-T.txt"), "a file of the other folder\n")
			before, err := os.ReadDir(other)
			if err != nil {
				t.Fatal(err)
			}
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swapAt(t, "index", f.gitDir, other)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			want := "git worktree add failed (checkout): " + realDir + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
			if got := createError(t, raw); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
				t.Errorf("the reply = %s, want the code worktree_add_failed", raw)
			}
			if last := calls[len(calls)-1]; !slices.Contains(last.argv, "read-tree") {
				t.Errorf("the last call = %q, want the read-tree", last.argv)
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			kept := f.gitDir + "-real"
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("worktrees", "w1", "gitdir"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(kept, name)); err != nil {
					t.Errorf("%s in the real git directory: %v, want it kept", name, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(kept, "worktrees", "w1", "index")); !os.IsNotExist(err) {
				t.Errorf("the index of the registration: %v, want none", err)
			}
			after, err := os.ReadDir(other)
			if err != nil || len(after) != len(before) {
				t.Errorf("the other folder holds %d entries (%v), want %d", len(after), err, len(before))
			}
			for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(other, name)); !os.IsNotExist(err) {
					t.Errorf("%s in the other folder: %v, want none", name, err)
				}
			}
		})
	}
}

// TestCreateGoesOnInTheGitDirThatGitAnswers: a symbolic link to another git
// directory is put at `.git` before `rev-parse --absolute-git-dir`. Git then
// answers that directory, and the create goes on in it, as on 5fd08069
// (measured on Linux and macOS). The copy T gets the registration, the
// branch and the index. The `.git` file of the leaf names `T/worktrees/w1`.
// The `worktree add` keeps GIT_DIR=<repo>/.git, and the read-tree names T
// and its objects. The real git directory gets nothing. Without the move of
// the hold the create is refused before its add.
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

// TestCreateGoesOnAfterASwapRightAfterTheAnswer: right after `rev-parse
// --absolute-git-dir` answered the held folder, `.git` becomes a symbolic link
// to a copy of the git directory. 4c111e86 then asks git again and answers
// success (measured on Linux). The calls after the first answer are those of
// the row. The copy gets the registration, and the record of the session
// folder names the copy. The marker of a filter in the config of the copy
// shows no run (claustrum's own: no file asks for the filter). The row with
// the git directory X of a second repository is tested, not measured. Without
// the call after the swap (askedAgain) the create is refused before its add.
func TestCreateGoesOnAfterASwapRightAfterTheAnswer(t *testing.T) {
	for _, row := range []struct{ name, entry string }{
		{"a link to a copy", "copy"},
		{"a link to X", "X"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			// The row has no registrations folder in the repository, so the
			// listing of the add comes before the pair.
			if err := os.RemoveAll(filepath.Join(f.gitDir, worktreesSubdir)); err != nil {
				t.Fatal(err)
			}
			records := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
			if err := os.Mkdir(records, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sessiondirs.UseForTest(records))
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			if err := os.CopyFS(target, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			if row.entry == "X" {
				_, target = configRepo(t)
			}
			marker := filepath.Join(t.TempDir(), "ran")
			config := filepath.Join(target, "config")
			mustWrite(t, config, mustRead(t, config)+"[filter \"x\"]\n\tsmudge = sh -c 'touch "+marker+"; cat'\n")
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swapAt(t, "answered", f.gitDir, target)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want a good create", raw)
			}
			want := []string{
				"rev-parse --git-dir | " + f.repo,
				"config --no-includes --file - --list -z | /",
				"config -z --list | " + f.repo,
				"rev-parse --absolute-git-dir | " + f.repo,
				"config -z --list | " + f.repo,
				"config --no-includes --file - --list -z | /",
				"--git-dir=" + target + " config -z --list | " + target,
				"--git-dir=" + target + " --work-tree=" + f.repo + " rev-parse --show-toplevel | " + target,
				"worktree add --no-track --no-checkout -b w1 <staged leaf> | " + f.repo,
			}
			if got := callsAfterAnswer(calls); !slices.Equal(got, want) {
				t.Errorf("the calls after the first answer:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("worktrees", "w1", "index"), filepath.Join("refs", "heads", "w1")} {
				if _, err := os.Lstat(filepath.Join(target, name)); err != nil {
					t.Errorf("%s in the directory behind the link: %v", name, err)
				}
				if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", name)); !os.IsNotExist(err) {
					t.Errorf("%s in the real git directory: %v, want none", name, err)
				}
			}
			if got, want := mustRead(t, filepath.Join(leaf, ".git")), "gitdir: "+filepath.Join(target, "worktrees", "w1")+"\n"; got != want {
				t.Errorf("the .git file of the leaf = %q, want %q", got, want)
			}
			entries, err := os.ReadDir(records)
			if err != nil || len(entries) != 1 {
				t.Fatalf("the record folder holds %v (%v), want one record", entries, err)
			}
			_, path, _ := strings.Cut(mustRead(t, filepath.Join(records, entries[0].Name())), "\n")
			// The text counts: a path through the link resolves to the target too.
			if path != target {
				t.Errorf("the record names %q, want %q", path, target)
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Errorf("the filter ran: %v", err)
			}
		})
	}
}

// TestCreateNamesTheCopyOfTheFolderOfTheSecondAnswer: right after `rev-parse
// --absolute-git-dir` answered the held folder, `.git` becomes a symbolic link
// to a copy T of the git directory. After the second answer 4c111e86 gives
// each call with --git-dir=T the folder of T in the folder of the daemon as
// its GIT_COMMON_DIR. Its other calls that name a folder of a repository keep
// the folder of the first gate (measured on Linux). The row with the git directory X of a
// second repository is tested, not measured. Without the copy of the folder
// of T (takeGitDirCopy) the calls with --git-dir=T keep the folder of the
// first gate.
func TestCreateNamesTheCopyOfTheFolderOfTheSecondAnswer(t *testing.T) {
	for _, row := range []struct{ name, entry string }{
		{"a link to a copy", "copy"},
		{"a link to X", "X"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if err := os.RemoveAll(filepath.Join(f.gitDir, worktreesSubdir)); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
			if err := os.CopyFS(target, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			if row.entry == "X" {
				_, target = configRepo(t)
			}
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swapAt(t, "answered", f.gitDir, target)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want a good create", raw)
			}
			answer := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--absolute-git-dir") })
			if answer < 0 {
				t.Fatal("the calls have no rev-parse --absolute-git-dir")
			}
			first := childCommonDir(calls[answer].env)
			if !isShadowRepoName(filepath.Base(first)) {
				t.Fatalf("the first answer has GIT_COMMON_DIR=%s, want a folder of a repository", first)
			}
			moved := filepath.Join(filepath.Dir(first), shadowRepoName(target))
			if fi, err := os.Lstat(filepath.Join(moved, "config")); err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("the config of the folder of the copy: %v", err)
			}
			named, kept := 0, 0
			for _, c := range calls[answer+1:] {
				got := childCommonDir(c.env)
				switch {
				case slices.Contains(c.argv, "--git-dir="+target):
					named++
					if got != moved {
						t.Errorf("%q has GIT_COMMON_DIR=%s, want %s", c.argv, got, moved)
					}
				case isShadowRepoName(filepath.Base(got)):
					kept++
					if got != first {
						t.Errorf("%q has GIT_COMMON_DIR=%s, want %s", c.argv, got, first)
					}
				}
			}
			if named < 2 || kept == 0 {
				t.Errorf("%d calls with --git-dir=%s and %d other calls with a folder of a repository, want 2 or more and 1 or more", named, target, kept)
			}
		})
	}
}

// callsAfterAnswer is each call after the first `rev-parse --absolute-git-dir`
// up to the first `worktree add`, as its words with no `-c` pair, then " | "
// and its working folder. The leaf of a staged add is "<staged leaf>".
func callsAfterAnswer(calls []envCall) []string {
	var out []string
	seen := false
	for _, c := range calls {
		var words []string
		for i := 0; i < len(c.argv); i++ {
			if c.argv[i] == "-c" {
				i++
				continue
			}
			words = append(words, c.argv[i])
		}
		add := slices.Contains(words, "worktree") && slices.Contains(words, "add")
		if add {
			words[len(words)-1] = "<staged leaf>"
		}
		if seen {
			out = append(out, strings.Join(words, " ")+" | "+c.cwd)
		}
		if add && seen {
			break
		}
		seen = seen || slices.Contains(words, "--absolute-git-dir")
	}
	return out
}

// TestCreateWithAMovedGitDirWhoseConfigChanges: a symbolic link to a copy T
// of the git directory is put at `.git` before `rev-parse --absolute-git-dir`.
// The test of that step passes on T. Right after the call the config of T
// becomes a directory. The hold does not move to T, and the step of the add
// refuses the create with the refusal of the config of T. No worktree add
// runs, and no folder gets a registration or a branch. This order is from the
// code, not measured. Without the config gate of takeOver (through movedTo)
// the hold moves to T, and the create succeeds there.
func TestCreateWithAMovedGitDirWhoseConfigChanges(t *testing.T) {
	f := newSeededShadowFixture(t)
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	old := createStepHook
	t.Cleanup(func() { createStepHook = old })
	var seen []string
	createStepHook = func(at string) {
		if slices.Contains(seen, at) {
			return
		}
		seen = append(seen, at)
		switch at {
		case "gitdir":
			if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
				t.Errorf("the rename of the git directory: %v", err)
				return
			}
			linkAt(t, f.gitDir, copied)
		case "answered":
			config := filepath.Join(copied, "config")
			if err := os.Rename(config, config+"-old"); err != nil {
				t.Errorf("the rename of the config: %v", err)
				return
			}
			if err := os.Mkdir(config, 0o755); err != nil {
				t.Errorf("the folder at the config: %v", err)
			}
		}
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !slices.Contains(seen, "answered") {
		t.Fatalf("the create did not reach the step after the call, steps %q", seen)
	}
	want := "git worktree add failed: " + repoConfigPrefix + `"` + filepath.Join(copied, "config") + `"` + notRegularTail
	if got := createError(t, raw); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") }) {
		t.Error("a worktree add call ran")
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	for _, dir := range []string{f.gitDir + "-real", copied} {
		for _, name := range []string{filepath.Join("worktrees", "w1"), filepath.Join("refs", "heads", "w1")} {
			if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("%s in %s: %v, want none", name, dir, err)
			}
		}
	}
}

// TestCreateWithAGitDirThatChangedAtTheAdd: right after a good `worktree add`
// the path of the git directory is a symbolic link to a copy T. be1c1ca5
// answers `git worktree add failed (completing the new worktree's git files):
// ".git" is a link or a file where Claude needs a real directory; put the
// directory itself in its place` with worktree_add_failed, removes the leaf
// and keeps the branch in the old folder (measured on Linux). claustrum gives
// the same reply, and no folder gets a registration. Without the test after
// the add the registration goes into the held folder and the create succeeds
// there.
func TestCreateWithAGitDirThatChangedAtTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
			t.Errorf("the rename of the git directory: %v", err)
			return
		}
		linkAt(t, f.gitDir, copied)
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got, want := createError(t, raw), completingAddPrefix+realDirText(f.gitDir); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Errorf("the reply = %s, want the code worktree_add_failed", raw)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch in the real git directory: %v, want it kept", err)
	}
	for _, dir := range []string{f.gitDir + "-real", copied} {
		if _, err := os.Lstat(filepath.Join(dir, "worktrees", "w1")); !os.IsNotExist(err) {
			t.Errorf("the registration in %s: %v, want none", dir, err)
		}
	}
}

// TestCreateWithALinkToTheRenamedGitDirAtTheAdd: right after a good `worktree
// add` the git directory is renamed, and a symbolic link to it
// is put at `.git`. 4c111e86 answers the text of TestCreateWithAGitDirThatChangedAtTheAdd,
// removes the leaf and deletes the new branch through the link (measured on
// Linux and macOS). No registration stays. Mutation: drop the linksToHeld test
// in runBranchStep, and the branch stays.
func TestCreateWithALinkToTheRenamedGitDirAtTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	real := f.gitDir + ".real"
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.Rename(f.gitDir, real); err != nil {
			t.Errorf("the rename of the git directory: %v", err)
			return
		}
		linkAt(t, f.gitDir, filepath.Base(real))
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		jsonString(t, completingAddPrefix+realDirText(f.gitDir)) + `,"errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("reply = %s\nwant %s", raw, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "refs", "heads", "w1")); !os.IsNotExist(err) {
		t.Errorf("the branch in the renamed git directory: %v, want it deleted", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "refs", "heads", "main")); err != nil {
		t.Errorf("the branch main: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the registration: %v, want none", err)
	}
}

// TestCreateWithALinkToACopyMadeAtTheAdd: right after a good `worktree add` the
// git directory is copied, with the new branch w1 at the tip of the add, and
// renamed away, and `.git` becomes a link to the copy. The branch step keeps
// w1 in both folders, as for any link that does not name the held git
// directory (claustrum's own, not measured with a copy made at this step).
// Mutation: drop the identity test of linksToHeld and keep only its Lstat, and
// w1 of the copy goes.
func TestCreateWithALinkToACopyMadeAtTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
			t.Errorf("the copy of the git directory: %v", err)
			return
		}
		if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
			t.Errorf("the rename of the git directory: %v", err)
			return
		}
		linkAt(t, f.gitDir, copied)
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got, want := createError(t, raw), completingAddPrefix+realDirText(f.gitDir); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	for _, dir := range []string{f.gitDir + "-real", copied} {
		if _, err := os.Lstat(filepath.Join(dir, "refs", "heads", "w1")); err != nil {
			t.Errorf("w1 in %s: %v, want it kept", dir, err)
		}
	}
}

// TestCreateWithADaemonCommonDirKeepsTheBranchOfTheClone: the daemon's
// environment has GIT_COMMON_DIR of a clone whose own branch w1 is at the tip
// of the add. A `.git` is planted in the leaf right after the staged add, and
// the rollback runs the branch step. git reads the branches of the git
// directory of baseRepo there, not those of the clone, so w1 of the clone stays
// and the new w1 of the repository goes (from the code and git 2.47, not
// measured on a reference). Mutations: give the calls of the branch step
// GIT_DIR=<the clone> (w1 of the clone goes, and w1 of the repository stays),
// or drop the daemon GIT_COMMON_DIR arm of named in worktreeAdd (the text
// differs, and w1 of the repository stays).
func TestCreateWithADaemonCommonDirKeepsTheBranchOfTheClone(t *testing.T) {
	f := newSeededShadowFixture(t)
	clone := filepath.Join(resolveTestRoot(t, t.TempDir()), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", f.repo, clone)
	runGit(t, clone, "branch", "w1", "origin/main")
	t.Setenv("GIT_COMMON_DIR", filepath.Join(clone, ".git"))
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() { mustWrite(t, filepath.Join(leaf, ".git"), "planted\n") })
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "sourceBranch": "main"})
	if got, want := createError(t, raw), plantedGitFileText(leaf); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if _, err := os.Lstat(filepath.Join(clone, ".git", "refs", "heads", "w1")); err != nil {
		t.Errorf("w1 of the clone: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
		t.Errorf("w1 of the repository: %v, want it gone", err)
	}
}

// TestCreateWithTheRegistrationsFolderGoneAfterTheAdd: right after a good
// `worktree add` the folder `worktrees` of the git directory is renamed away.
// 4c111e86 makes `worktrees` again, moves the registration into it and
// succeeds (measured on Linux and macOS). The renamed folder stays. Mutation:
// drop the mkdir of `worktrees` in addStage.move, and the rename fails.
func TestCreateWithTheRegistrationsFolderGoneAfterTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	wt := filepath.Join(f.gitDir, worktreesSubdir)
	gone := wt + ".gone"
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.Rename(wt, gone); err != nil {
			t.Errorf("the rename of the registrations folder: %v", err)
		}
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want a good create", raw)
	}
	if got, want := mustRead(t, filepath.Join(leaf, ".git")), "gitdir: "+filepath.Join(wt, "w1")+"\n"; got != want {
		t.Errorf("the .git file of the leaf = %q, want %q", got, want)
	}
	for _, name := range []string{"HEAD", "commondir", "gitdir", "index"} {
		if _, err := os.Lstat(filepath.Join(wt, "w1", name)); err != nil {
			t.Errorf("%s of the registration: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch: %v, want it kept", err)
	}
	if fi, err := os.Lstat(gone); err != nil || !fi.IsDir() {
		t.Errorf("the renamed registrations folder: %v, want it kept", err)
	}
}

// TestCreateWithTheLeafReplacedAfterTheAdd: right after a good staged `worktree
// add` the leaf is renamed to w1.old, and a new folder is put at its path.
// 4c111e86 answers the text of a replaced leaf with unsafe_path (measured on
// Linux and macOS). The registration w1 is moved into place with no `locked`
// file, the renamed leaf holds the new `.git` file, and branch w1 stays. An
// empty folder at the path goes, as on 4c111e86. A folder with a file there
// stays with the file, and a link there stays with its target: the rollback is
// one rmdir, never a tree delete (claustrum's own, not measured). Mutations:
// keep the old error of leafHoldsNoGitFile (the frame differs, and the
// registration and the branch go), write the `.git` file by the path (the frame
// differs), return no leafMovedAfterAdd (the frame differs), drop the
// undoMovedAwayLeaf call (the empty folder stays), call undoFailedAdd in its
// place (the link goes), or remove the folder with os.RemoveAll (the file goes).
// A leaf that is deleted and replaced by an empty folder, not renamed, gets
// the text of the failed write of `.git` with worktree_add_failed, and the
// moved registration, the branch and the empty folder go (4c111e86, measured
// on Linux and macOS). Mutations: restore the
// early return for a deleted leaf (the frame differs), or drop the
// undoMovedAwayLeaf call of the refusal (the empty folder stays). A leaf that is
// deleted and not made again gets the same text, and then the leaf text of a
// failed rmdir with the error of a remove of w1 (4c111e86, measured on Linux).
// Mutations: restore the early return for a gone leaf (the frame differs), or
// drop the goneLeafUndoPart text (the frame differs). A dangling link put in
// place of the deleted leaf keeps the raw error of the open of the leaf (from
// the code, not measured). Mutation: drop the leafNameIsFree test (the frame
// differs). An attach add of main
// fails, since main is checked out, and its fallback add gets the same reply
// (from the code, not measured). Mutation: drop the movedAwayReply call after
// the fallback add (the frame differs).
func TestCreateWithTheLeafReplacedAfterTheAdd(t *testing.T) {
	for _, row := range []struct {
		name, kind, existing string
	}{{"an empty folder", "empty", ""}, {"a folder with a file", "file", ""}, {"a link to a folder", "link", ""}, {"a deleted and replaced leaf", "deleted", ""}, {"a deleted leaf", "gone", ""}, {"a dangling link", "dangling", ""}, {"an empty folder after the fallback add", "empty", "main"}} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			old := leaf + ".old"
			target := filepath.Join(resolveTestRoot(t, t.TempDir()), "target")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			atStep(t, "staged", func() {
				if row.kind == "deleted" || row.kind == "gone" {
					if err := os.Remove(leaf); err != nil {
						t.Errorf("the delete of the leaf: %v", err)
					}
					if row.kind == "gone" {
						return
					}
					if err := os.Mkdir(leaf, 0o755); err != nil {
						t.Errorf("the new folder: %v", err)
					}
					return
				}
				if row.kind == "dangling" {
					if err := os.Remove(leaf); err != nil {
						t.Errorf("the delete of the leaf: %v", err)
					}
					linkAt(t, leaf, filepath.Join(target, "none"))
					return
				}
				if err := os.Rename(leaf, old); err != nil {
					t.Errorf("the rename of the leaf: %v", err)
					return
				}
				if row.kind == "link" {
					linkAt(t, leaf, target)
					return
				}
				if err := os.Mkdir(leaf, 0o755); err != nil {
					t.Errorf("the new folder: %v", err)
				}
				if row.kind == "file" {
					mustWrite(t, filepath.Join(leaf, "keep.txt"), "kept\n")
				}
			})
			params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
			if row.existing != "" {
				params["existingBranch"] = row.existing
			}
			raw, _ := f.call(t, "git.worktree_create", params)
			if row.kind == "dangling" {
				want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
					jsonString(t, completingAddPrefix+"open "+leaf+": no such file or directory") +
					`,"errorCode":"worktree_add_failed"}}`
				if raw != want {
					t.Errorf("reply = %s\nwant %s", raw, want)
				}
				return
			}
			if row.kind == "deleted" || row.kind == "gone" {
				text := completingAddPrefix + "openat .git: no such file or directory"
				if row.kind == "gone" {
					text += "; and the undo could not finish for " + leaf + ": the worktree directory remains (re-populated while undoing?); " +
						"remove it by hand before retrying (removeat w1: no such file or directory)"
				}
				want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
					jsonString(t, text) +
					`,"errorCode":"worktree_add_failed"}}`
				if raw != want {
					t.Errorf("reply = %s\nwant %s", raw, want)
				}
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the path of the leaf: %v, want nothing there", err)
				}
				if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
					t.Errorf("the branch: %v, want it gone", err)
				}
				if _, err := os.Lstat(filepath.Join(f.gitDir, worktreesSubdir, "w1")); !os.IsNotExist(err) {
					t.Errorf("the registration w1: %v, want none", err)
				}
				entries, err := os.ReadDir(f.gitDir)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), stagingNamePrefix) {
						t.Errorf("the staging folder %s: want none", e.Name())
					}
				}
				return
			}
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonString(t, "refusing to create worktree: `"+leaf+"` is no longer the directory that was created for the worktree") +
				`,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
			reg := filepath.Join(f.gitDir, worktreesSubdir, "w1")
			if got, want := mustRead(t, filepath.Join(old, ".git")), "gitdir: "+reg+"\n"; got != want {
				t.Errorf("the .git file of the renamed leaf = %q, want %q", got, want)
			}
			for _, name := range []string{"HEAD", "commondir", "gitdir"} {
				if _, err := os.Lstat(filepath.Join(reg, name)); err != nil {
					t.Errorf("%s of the registration: %v, want it kept", name, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(reg, "locked")); !os.IsNotExist(err) {
				t.Errorf("locked of the registration: %v, want none", err)
			}
			if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
				t.Errorf("the branch: %v, want it kept", err)
			}
			switch row.kind {
			case "empty":
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the empty folder at the path: %v, want it gone", err)
				}
			case "file":
				if got := mustRead(t, filepath.Join(leaf, "keep.txt")); got != "kept\n" {
					t.Errorf("the file in the folder at the path = %q, want it kept", got)
				}
			case "link":
				if fi, err := os.Lstat(leaf); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("the link at the path: %v, want it kept", err)
				}
				if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
					t.Errorf("the target of the link: %v, want it kept", err)
				}
			}
		})
	}
}

// TestCreateWithADaemonCommonDir: the daemon's environment has GIT_COMMON_DIR,
// and the add is staged anyway. 4c111e86 does so (measured on Linux and macOS):
//
//   - A clone of the repository there: the create succeeds, and the repository
//     has the registration. The clone gets none.
//   - Another repository there: the add fails, and git's staged path is "the
//     new folder" in the text (measured on Linux). The words of git there
//     change with its version, so the test does not read them.
//
// Mutation: leave the add unstaged with that variable (worktreeAdd), and the
// clone gets the registration and the text keeps the path.
func TestCreateWithADaemonCommonDir(t *testing.T) {
	for _, row := range []struct {
		name  string
		clone bool
	}{{"a clone", true}, {"another repository", false}} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			other := filepath.Join(resolveTestRoot(t, t.TempDir()), "other")
			if row.clone {
				runGit(t, filepath.Dir(other), "clone", "-q", f.repo, other)
			} else {
				_, gd := configRepo(t)
				other = filepath.Dir(gd)
			}
			t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "sourceBranch": "main"})
			if _, err := os.Lstat(filepath.Join(other, ".git", worktreesSubdir, "w1")); !os.IsNotExist(err) {
				t.Errorf("the registration in the other folder: %v, want none", err)
			}
			if !row.clone {
				if got := createError(t, raw); !strings.HasPrefix(got, "git worktree add failed: ") || !strings.HasSuffix(got, "(the new folder is `"+leaf+"`)") {
					t.Errorf("the error = %s, want git's text with the new folder", got)
				}
				return
			}
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want a good create", raw)
			}
			reg := filepath.Join(f.gitDir, worktreesSubdir, "w1")
			if got, want := mustRead(t, filepath.Join(leaf, ".git")), "gitdir: "+reg+"\n"; got != want {
				t.Errorf("the .git file of the leaf = %q, want %q", got, want)
			}
			if _, err := os.Lstat(filepath.Join(reg, "index")); err != nil {
				t.Errorf("the index of the registration: %v", err)
			}
		})
	}
}

// TestCreateWithALinkedGitDirThatChanges: `.git` is a symbolic link to a real
// git directory before the first request, and the daemon serves the repository
// (measured on Linux). After `rev-parse --absolute-git-dir` the link is
// replaced by a link to a copy T. The create is refused before its `worktree
// add`, and no folder gets a branch, a registration or a leaf. That end state
// is the one of 5fd08069 (measured on Linux and macOS). The text is that of
// the nearest measured step: 5fd08069 answers another text there, after a
// `worktree add` and a rollback. Without the test of the spelling of the
// request the create answers success in the first git directory.
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
// succeeds, as on 5fd08069 in two measured cases (on macOS, 2 of 2 runs).
//
//   - i-ctl: baseRepo, worktreePath and the record go through a symbolic link
//     above the repository. The reply has the path as sent. The `.git` file of
//     the leaf and the new record have the real path.
//   - h-ctl, the control: all three have the real path.
//
// Without the compare against the path as sent, the registration of that measured case
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

// TestGitDirCopyOnlyOfTheHeldFolder: takeGitDirCopy makes the folder of the
// copy only for a pinned git directory with the identity of the hold. Another
// git directory, and a real folder that replaced the held one at its path,
// get no folder (claustrum's own). Without the identity test both get one.
func TestGitDirCopyOnlyOfTheHeldFolder(t *testing.T) {
	_, held := configRepo(t)
	_, other := configRepo(t)
	run := resolveTestRoot(t, t.TempDir())
	u := gitUse{host: NewShadowHost(run)}
	read := &repoVerdict{shadowText: "[core]\n"}
	a := acceptGitDir(held, gitDirIdentity(held))
	if a == nil {
		t.Fatal("no hold of the git directory")
	}
	defer a.release()
	a.takeGitDirCopy(u, other, read)
	if a.gitDirCopy != nil {
		t.Errorf("another git directory got the folder %s", a.gitDirCopy.repoDir)
	}
	a.takeGitDirCopy(u, held, read)
	if a.gitDirCopy == nil || a.gitDirCopy.repoDir == "" {
		t.Fatal("the held git directory got no folder")
	}
	a.gitDirCopy = nil
	if err := os.Rename(held, held+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(held, os.DirFS(held+"-old")); err != nil {
		t.Fatal(err)
	}
	a.takeGitDirCopy(u, held, read)
	if a.gitDirCopy != nil {
		t.Errorf("a folder that replaced the held one got the folder %s", a.gitDirCopy.repoDir)
	}
}

// TestLinksToHeldArms: linksToHeld is false on nil, and it skips an empty
// spelling of the request. A link at the path to the held folder then counts,
// and no path at all does not. Mutations: drop the nil test (a panic), or
// test an empty spelling with os.Stat (the link row is false).
func TestLinksToHeldArms(t *testing.T) {
	var none *acceptedGitDir
	if none.linksToHeld() {
		t.Error("nil: want false")
	}
	dir := resolveTestRoot(t, t.TempDir())
	held := filepath.Join(dir, "held")
	if err := os.Mkdir(held, 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(held)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	linkAt(t, link, held)
	if !(&acceptedGitDir{path: link, info: fi}).linksToHeld() {
		t.Error("a link to the held folder with no spelling: want true")
	}
	if (&acceptedGitDir{sent: link, info: fi}).linksToHeld() {
		t.Error("no path: want false")
	}
}

// TestLeafMovedAfterAddText: the error of a leaf moved away after a staged
// add names the state with the text of errLeafMoved.
func TestLeafMovedAfterAddText(t *testing.T) {
	if got, want := (&leafMovedAfterAdd{}).Error(), errLeafMoved.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
