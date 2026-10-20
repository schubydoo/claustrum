package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The tests of the hold of a repository folder by one git child. The measured
// rules are those of E2L cells 36 and 37, E3L cells 54 and 55 and EL2 cell 47
// (Linux VM). The mechanism is from the code.

// textA and textB are the two refusals of a folder that went (E3L cell 54).
const (
	textA = "Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again"
	textB = "listing the configuration in force: " + textA
)

// acceptedRepo is the trust and the read of the repository in dir, as the gate
// of a request has them.
func acceptedRepo(t *testing.T, dir string) (string, *repoVerdict) {
	t.Helper()
	trust := gitDirTrustFor(dir)
	v := repoReadVerdict(trust, nil)
	if trust.verdict != gitDirTrusted || v == nil || v.refusal != "" {
		t.Fatalf("the repository in %s is not accepted: %+v", dir, v)
	}
	return trust.pinCommonDir, v
}

// copyCmd is a git command whose GIT_COMMON_DIR is the copy of u. It never runs.
func copyCmd(u gitUse, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = u.commonVars()
	return cmd
}

// The hold spans one child only. During the child the only folder cannot go,
// and a new repository gets the text of `repoLimitError` (E2L cells 36 and
// 37). Between two children of one request it goes (E3L cell 54).
func TestRepoHoldSpansOneChild(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	_, gitB := configRepo(t)
	gitB, readB := acceptedRepo(t, filepath.Dir(gitB))
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	const textC = "Claude could not prepare its own files on the remote machine: git is running in 1 repositories at once; try again"
	err = runHeld(copyCmd(u, "status"), func() error {
		_, err := f.h.syncRepo(gitB, readB)
		if got := shadowRepoFailureText(err); got != textC {
			t.Errorf("a new repository during the child = %q\nwant %q", got, textC)
		}
		sweptThere(t, u.repoDir)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Between two children the folder is free.
	if _, err := f.h.syncRepo(gitB, readB); err != nil {
		t.Fatal(err)
	}
	sweptGone(t, u.repoDir)
}

// A folder that went is made again at the build of the next call, with no text
// and no read of the config (E3L cell 54). The child then holds it.
func TestRepoHoldRenewsAFolderThatWent(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	_, gitB := configRepo(t)
	gitB, readB := acceptedRepo(t, filepath.Dir(gitB))
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.h.syncRepo(gitB, readB)
	if err != nil {
		t.Fatal(err)
	}
	sweptGone(t, u.repoDir)
	// The build needs no git: a PATH with no git still makes the folder.
	t.Setenv("PATH", t.TempDir())
	cmd := copyCmd(u, "status")
	sweptThere(t, filepath.Join(u.repoDir, "config"))
	sweptGone(t, b.repoDir)
	if got, _ := os.ReadFile(filepath.Join(u.repoDir, "config")); string(got) != readA.shadowText {
		t.Errorf("the config copy = %q, want the kept read %q", got, readA.shadowText)
	}
	ran := false
	if err := runHeld(cmd, func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("the child after the build: ran %v, error %v", ran, err)
	}
}

// At the build, with the only folder held, the folder that went is not made,
// and the start of that call answers the text of `repoLimitError`. The
// request keeps nothing: the build of the next call makes the folder, and that
// call runs.
func TestRepoHoldBuildFailureRefusesThatCall(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	_, gitB := configRepo(t)
	gitB, readB := acceptedRepo(t, filepath.Dir(gitB))
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.h.syncRepo(gitB, readB)
	if err != nil {
		t.Fatal(err)
	}
	release := holdFolder(t, f.h, b.repoDir)
	cmd := copyCmd(u, "status")
	const textC = "Claude could not prepare its own files on the remote machine: git is running in 1 repositories at once; try again"
	sweptGone(t, u.repoDir)
	ran := false
	if err := runHeld(cmd, func() error { ran = true; return nil }); err == nil || err.Error() != textC || ran {
		t.Errorf("the call = %v, ran %v, want %q and no start", err, ran, textC)
	}
	release()
	if err := runHeld(copyCmd(u, "status"), func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("the next call: ran %v, error %v", ran, err)
	}
	sweptThere(t, u.repoDir)
	// The good build ends the failure: a record that goes after it gives
	// `repoCopyGoneText`.
	cmd = copyCmd(u, "status")
	f.h.repoMu.Lock()
	delete(f.h.repos, filepath.Base(u.repoDir))
	f.h.repoMu.Unlock()
	if err := runHeld(cmd, func() error { return nil }); err == nil || err.Error() != textA {
		t.Errorf("a call after the good build = %v, want text A", err)
	}
}

// blockCopy makes the next mkdir of a repository folder find the folder with
// a folder file in it, so the copy of file fails after the record is there.
// path gives the folder of the repository at that moment.
func blockCopy(t *testing.T, path func() string, file string) {
	t.Helper()
	old := shadowRepoBeforeMkdir
	t.Cleanup(func() { shadowRepoBeforeMkdir = old })
	shadowRepoBeforeMkdir = func() {
		shadowRepoBeforeMkdir = old
		if err := os.MkdirAll(filepath.Join(path(), file, "x"), 0o700); err != nil {
			t.Error(err)
		}
	}
}

// blockHeadCopy is blockCopy for the copy of HEAD.
func blockHeadCopy(t *testing.T, path func() string) {
	t.Helper()
	blockCopy(t, path, "HEAD")
}

// recordIncomplete reports whether h has a record of the repository folder dir
// that a failed build marked incomplete. A folder with no record fails t.
func recordIncomplete(t *testing.T, h *shadowHost, dir string) bool {
	t.Helper()
	h.repoMu.Lock()
	defer h.repoMu.Unlock()
	st := h.repos[filepath.Base(dir)]
	if st == nil {
		t.Errorf("no record of %s", dir)
		return false
	}
	return st.incomplete
}

// A build that fails after it made the record marks it incomplete. That call
// is refused with the text of the failure and starts no child. The next call
// makes the folder whole and runs.
func TestRepoHoldFailedRenewMarksTheRecord(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	_, gitB := configRepo(t)
	gitB, readB := acceptedRepo(t, filepath.Dir(gitB))
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.syncRepo(gitB, readB); err != nil {
		t.Fatal(err)
	}
	sweptGone(t, u.repoDir)
	blockHeadCopy(t, func() string { return u.repoDir })
	cmd := copyCmd(u, "status")
	if !recordIncomplete(t, f.h, u.repoDir) {
		t.Error("the failed build left a record that is not marked incomplete")
	}
	const prefix = "Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: "
	ran := false
	if err := runHeld(cmd, func() error { ran = true; return nil }); err == nil || !strings.HasPrefix(err.Error(), prefix) || ran {
		t.Errorf("the call = %v, ran %v, want the write failure and no start", err, ran)
	}
	if err := os.RemoveAll(filepath.Join(u.repoDir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := runHeld(copyCmd(u, "status"), func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("the next call: ran %v, error %v", ran, err)
	}
	sweptThere(t, filepath.Join(u.repoDir, "config"))
	if recordIncomplete(t, f.h, u.repoDir) {
		t.Error("the good build left the mark")
	}
}

// A first build at the gate that fails after it made the record marks it
// incomplete too. A git call of that use starts no child, and the next request
// makes the folder whole.
func TestRepoHoldFailedFirstBuildMarksTheRecord(t *testing.T) {
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	path := func() string {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return filepath.Join(f.h.dir, shadowRepoName(gitA))
	}
	blockHeadCopy(t, path)
	u, err := f.h.syncRepo(gitA, readA)
	if err == nil {
		t.Fatal("the first build passed with a folder HEAD in the way")
	}
	if !recordIncomplete(t, f.h, u.repoDir) {
		t.Error("the failed first build left a record that is not marked incomplete")
	}
	ran := false
	if err := runHeld(copyCmd(u, "status"), func() error { ran = true; return nil }); !copyGone(err) || ran {
		t.Errorf("a call of the failed use = %v, ran %v, want a refusal and no start", err, ran)
	}
	if err := os.RemoveAll(filepath.Join(u.repoDir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if u, err = f.h.syncRepo(gitA, readA); err != nil {
		t.Fatal(err)
	}
	if err := runHeld(copyCmd(u, "status"), func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("a call of the next request: ran %v, error %v", ran, err)
	}
}

// A failed build keeps the copies that it wrote. The build copies info/exclude,
// and the copy of BISECT_START fails. The user then removes info/exclude, and
// the next build removes its copy (dropCopy).
func TestRepoHoldRetryDropsACopyOfTheFailedBuild(t *testing.T) {
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	exclude := filepath.Join(gitA, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitA, "BISECT_START"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := func() string {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return filepath.Join(f.h.dir, shadowRepoName(gitA))
	}
	blockCopy(t, path, "BISECT_START")
	u, err := f.h.syncRepo(gitA, readA)
	if err == nil {
		t.Fatal("the build passed with a folder BISECT_START in the way")
	}
	copied := filepath.Join(u.repoDir, "info", "exclude")
	sweptThere(t, copied)
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(u.repoDir, "BISECT_START")); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := runHeld(copyCmd(u, "status"), func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("the retry: ran %v, error %v", ran, err)
	}
	sweptGone(t, copied)
}

// A child whose record is incomplete starts no process. It gets the text of
// the last build of renewRepo that failed, with no listing prefix, and its
// own build is not part of the test: the child carries the variables only.
func TestRepoHoldRefusesAChildOfAnIncompleteRecord(t *testing.T) {
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitA, "BISECT_START"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(u.repoDir, "BISECT_START", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.syncRepo(gitA, readA); err == nil {
		t.Fatal("the build passed with a folder BISECT_START in the way")
	}
	renewErr := f.h.renewRepo(u.folder, gitA, readA)
	if renewErr == nil {
		t.Fatal("the renew passed with a folder BISECT_START in the way")
	}
	want := shadowRepoFailureText(renewErr)
	for _, listing := range []bool{false, true} {
		args := []string{"status"}
		if listing {
			args = []string{"config", "--list"}
		}
		cmd := exec.Command("git", args...)
		cmd.Env = []string{"GIT_COMMON_DIR=" + u.repoDir}
		ran := false
		if err := runHeld(cmd, func() error { ran = true; return nil }); err == nil || err.Error() != want || ran {
			t.Errorf("listing %v: the child = %v, ran %v, want %q and no start", listing, err, ran, want)
		}
	}
}

// A child whose folder has no record starts no process. A listing child gets
// the listing form (`listingInForcePrefix` + `repoCopyGoneText`), any other
// child `repoCopyGoneText`.
func TestRepoHoldRefusesAChildWithNoRecord(t *testing.T) {
	f := newRepoFolderFixture(t)
	gitA, readA := acceptedRepo(t, f.repo)
	u, err := f.h.syncRepo(gitA, readA)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, textA},
		{[]string{"--git-dir=x", "config", "-z", "--list"}, textB},
	} {
		cmd := exec.Command("git", c.args...)
		cmd.Env = append(u.commonVars(), "X=1")
		f.h.repoMu.Lock()
		delete(f.h.repos, filepath.Base(u.repoDir))
		f.h.repoMu.Unlock()
		ran := false
		err := runHeld(cmd, func() error { ran = true; return nil })
		if err == nil || err.Error() != c.want || ran {
			t.Errorf("%v: error %v, ran %v, want %q and no start", c.args, err, ran, c.want)
		}
		if _, err := f.h.syncRepo(gitA, readA); err != nil {
			t.Fatal(err)
		}
	}
	// A folder that is no longer the folder of the daemon is refused too.
	cmd := exec.Command("git", "status")
	cmd.Env = u.commonVars()
	f.h.mu.Lock()
	dir := f.h.dir
	f.h.dir = filepath.Join(filepath.Dir(dir), "other")
	f.h.mu.Unlock()
	_, err = holdRepoCopy(cmd)
	f.h.mu.Lock()
	f.h.dir = dir
	f.h.mu.Unlock()
	if !errors.Is(err, repoCopyGone{}) {
		t.Errorf("a child in an old folder = %v, want text A", err)
	}
	// After the shutdown of the settings control a child is refused too.
	f.h.mu.Lock()
	f.h.closed = true
	f.h.mu.Unlock()
	_, err = holdRepoCopy(cmd)
	f.h.mu.Lock()
	f.h.closed = false
	f.h.mu.Unlock()
	if !errors.Is(err, repoCopyGone{}) {
		t.Errorf("a child after the shutdown = %v, want text A", err)
	}
	// A call that names no repository folder of a daemon holds nothing.
	for _, env := range [][]string{nil, {"GIT_COMMON_DIR=" + filepath.Join(dir, ".probe")},
		{"GIT_COMMON_DIR=" + filepath.Join(t.TempDir(), strings.Repeat("a", 32))}} {
		cmd := exec.Command("git", "status")
		cmd.Env = env
		drop, err := holdRepoCopy(cmd)
		if err != nil {
			t.Errorf("%v: %v", env, err)
			continue
		}
		drop()
	}
}

// holdTrial is a shadow fixture whose first git child of the repository
// folder with an argv that match accepts makes a second repository first, with
// a limit of 1. So the folder of the request goes right before that child.
type holdTrial struct {
	shadowFixture
	mu    sync.Mutex
	fired bool
}

func newHoldTrial(t *testing.T, match func(args []string) bool) *holdTrial {
	t.Helper()
	lowerRepoLimit(t, 1)
	f := &holdTrial{shadowFixture: newShadowFixture(t)}
	other, _ := configRepo(t)
	gitB, readB := acceptedRepo(t, other)
	old := beforeRepoHold
	beforeRepoHold = func(args []string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fired || !match(args) {
			return
		}
		f.fired = true
		if _, err := f.s.gitShadow.syncRepo(gitB, readB); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeRepoHold = old })
	return f
}

// warm runs a git.info that makes the state shadow and the folder of the
// repository, with no refusal. The next request gets the refusal.
func (f *holdTrial) warm(t *testing.T) {
	t.Helper()
	f.setFired(true)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("the warm-up git.info = %s", raw)
	}
	f.setFired(false)
}

func (f *holdTrial) setFired(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = v
}

func (f *holdTrial) didFire() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fired
}

// subIs matches the first git child of the copy whose subcommand is sub.
func subIs(sub string) func(args []string) bool {
	return func(args []string) bool { return gitSubcommand(args) == sub }
}

// argsHave matches the first git child of the copy whose argv has words.
func argsHave(words ...string) func(args []string) bool {
	return func(args []string) bool { return argvHas(args, words...) }
}

// gitDirListing matches a config listing with a --git-dir option.
func gitDirListing(args []string) bool {
	return gitSubcommand(args) == "config" && slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
}

// nthMatch matches the n-th git child of the copy that match accepts. The
// trial calls it once for each child, under its lock.
func nthMatch(n int, match func(args []string) bool) func(args []string) bool {
	seen := 0
	return func(args []string) bool {
		if !match(args) {
			return false
		}
		seen++
		return seen == n
	}
}

// Each refused call of git.status answers its text in the error object, byte
// for byte, and no later call of the answer runs. A refused config listing
// gives the listing form (`listingInForcePrefix` + `repoCopyGoneText`). The
// refused child starts no process (E3L cell 54).
func TestRepoHoldStatusFrames(t *testing.T) {
	tempListing := func(args []string) bool {
		return gitSubcommand(args) == "config" && slices.ContainsFunc(args, func(a string) bool {
			return strings.HasPrefix(a, "--git-dir=") && strings.Contains(a, shadowStatusDirPrefix)
		})
	}
	for _, c := range []struct {
		name  string
		match func(args []string) bool
		want  string
		gone  [][]string
	}{
		{"first listing", subIs("config"), textB, [][]string{{"rev-parse"}}},
		{"probe listing", nthMatch(1, gitDirListing), textB, [][]string{{"--show-toplevel"}}},
		{"absolute-git-dir", argsHave("rev-parse", "--absolute-git-dir"), textA, [][]string{{"--show-toplevel"}}},
		{"show-toplevel", argsHave("rev-parse", "--show-toplevel"), textA, [][]string{{"hash-object"}}},
		{"temp listing", tempListing, textB, [][]string{{"status"}, {"ls-files"}}},
		{"status", subIs("status"), textA, [][]string{{"status"}, {"ls-files"}, {"diff-index"}}},
		{"verify", argsHave("rev-parse", "--verify"), textA, [][]string{{"diff-index"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoldTrial(t, c.match)
			f.warm(t)
			raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
			if want := errorFrame(t, c.want); raw != want {
				t.Errorf("git.status = %s\nwant %s", raw, want)
			}
			if !f.didFire() {
				t.Fatal("no child was refused")
			}
			for _, call := range calls {
				for _, words := range c.gone {
					if argvHas(call.argv, words...) {
						t.Errorf("a %v call ran after the refusal: %q", words, call.argv)
					}
				}
			}
		})
	}
}

// git.info and git.list_branches answer a refused call in the error object.
// The second call of git.list_branches answers it too (from the code).
func TestRepoHoldReadFrames(t *testing.T) {
	for _, c := range []struct {
		method string
		match  func(args []string) bool
		want   string
	}{
		{"git.info", subIs("config"), textB},
		{"git.info", argsHave("rev-parse", "--git-dir"), textA},
		{"git.list_branches", argsHave("rev-parse", "--git-dir"), textA},
		{"git.list_branches", subIs("for-each-ref"), textA},
	} {
		t.Run(c.method, func(t *testing.T) {
			f := newHoldTrial(t, c.match)
			f.warm(t)
			raw, _ := f.call(t, c.method, map[string]any{"path": f.repo})
			if want := errorFrame(t, c.want); raw != want {
				t.Errorf("%s = %s\nwant %s", c.method, raw, want)
			}
			if !f.didFire() {
				t.Fatal("no child was refused")
			}
		})
	}
}

// branchThere reports whether repo has the branch name.
func branchThere(t *testing.T, repo, name string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run() == nil
}

// A refused call of git.worktree_create before the add answers its text in
// the result object, byte for byte (the frame of the text of `repoLimitError`
// in E3L cell 55). Nothing of the worktree stays. On Windows the add child
// names the staging folder, so the row "add" drops the folder at the staging
// hold (holdCopy), before the staging folder is made (from the code, not
// measured).
func TestRepoHoldCreateFrames(t *testing.T) {
	for _, c := range []struct {
		name  string
		match func(args []string) bool
		want  string
	}{
		{"listing", subIs("config"), textB},
		{"repository test", argsHave("rev-parse", "--is-inside-work-tree"), textA},
		{"add", argsHave("worktree", "add"), textA},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoldTrial(t, c.match)
			f.warm(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + c.want + `","errorCode":"worktree_add_failed"}}`
			if raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if !f.didFire() {
				t.Fatal("no child was refused")
			}
			for _, call := range calls {
				if argvHas(call.argv, "worktree", "add") {
					t.Errorf("a worktree add ran after the refusal: %q", call.argv)
				}
			}
			sweptGone(t, leaf)
			if branchThere(t, f.repo, "w1") {
				t.Error("the branch w1 stays")
			}
		})
	}
}

// A refused read-tree call answers its text after the words of the checkout.
// The calls of the undo make the folder again and run, so the leaf, the
// registration and the branch go, as after a failed checkout (from the code).
func TestRepoHoldCreateCheckoutUndoes(t *testing.T) {
	f := newHoldTrial(t, subIs("read-tree"))
	f.warm(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): ` + textA + `","errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if !f.didFire() {
		t.Fatal("no child was refused")
	}
	sweptGone(t, leaf)
	sweptGone(t, filepath.Join(f.gitDir, "worktrees", "w1"))
	if branchThere(t, f.repo, "w1") {
		t.Error("the branch w1 stays")
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return argvHas(c.argv, "update-ref", "-d") }) {
		t.Error("the branch step ran no update-ref after the refusal")
	}
}

// A refused listing of the checkout answers the listing form
// (`listingInForcePrefix` + `repoCopyGoneText`) after the words of the
// checkout, with no other prefix, and the undo runs (from the code). The first
// such listing is that of the pair before the add, whose failure changes
// nothing (cell R-Q4e).
func TestRepoHoldCreateCheckoutListings(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int
	}{{"first listing", 2}, {"second listing", 3}} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoldTrial(t, nthMatch(c.n, gitDirListing))
			f.warm(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): ` + textB + `","errorCode":"worktree_add_failed"}}`
			if raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			for _, call := range calls {
				if gitSubcommand(append([]string{"git"}, call.argv...)) == "read-tree" {
					t.Errorf("a read-tree ran after the refusal: %q", call.argv)
				}
			}
			sweptGone(t, leaf)
			if branchThere(t, f.repo, "w1") {
				t.Error("the branch w1 stays")
			}
		})
	}
}

// A refused call whose failure git.worktree_create does not answer keeps
// nothing: the calls after it make the folder again and run, and the create
// succeeds (from the code).
func TestRepoHoldCreateGoesOnAfterAnIgnoredCall(t *testing.T) {
	f := newHoldTrial(t, argsHave("rev-parse", "--abbrev-ref"))
	f.warm(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":"` + jsonInner(t, leaf) + `","branch":"w1"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if !f.didFire() {
		t.Fatal("no child was refused")
	}
	sweptThere(t, filepath.Join(leaf, ".git"))
}

// A refused call of the lock check of git.worktree_remove answers its text
// after the words of that check, with no errorCode (the frame of the text of
// `repoLimitError` in E3L cell 55). Nothing is deleted.
func TestRepoHoldRemoveFrames(t *testing.T) {
	for _, c := range []struct {
		name  string
		match func(args []string) bool
		want  string
	}{
		{"listing", subIs("config"), textB},
		{"absolute-git-dir", argsHave("rev-parse", "--absolute-git-dir"), textA},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoldTrial(t, c.match)
			f.warm(t)
			raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not check whether ` +
				jsonInner(t, f.wt) + ` is locked: ` + c.want + `"}}`
			if raw != want {
				t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
			}
			if !f.didFire() {
				t.Fatal("no child was refused")
			}
			sweptThere(t, filepath.Join(f.wt, ".git"))
			sweptThere(t, filepath.Join(f.gitDir, "worktrees", "wr"))
			if !branchThere(t, f.repo, "wr") {
				t.Error("the branch wr went")
			}
		})
	}
}

// jsonInner is s as it is inside a JSON string.
func jsonInner(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[1 : len(b)-1])
}

// A folder that goes between two git calls of git.status is made again at the
// build of the next call. The reply is normal, and the request reads the
// config no more often than without the loss (E3L cell 54).
func TestRepoHoldStatusRenewsBetweenCalls(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newShadowFixture(t)
	other, _ := configRepo(t)
	gitB, readB := acceptedRepo(t, other)
	fileReads := func(calls []envCall) int {
		n := 0
		for _, c := range calls {
			if argvHas(c.argv, "--file", "-") {
				n++
			}
		}
		return n
	}
	params := map[string]any{"path": f.wt, "baseRepo": f.repo}
	if raw, _ := f.call(t, "git.status", params); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("the warm-up git.status = %s", raw)
	}
	control, calls := f.call(t, "git.status", params)
	reads := fileReads(calls)
	var mu sync.Mutex
	fired := false
	old := afterRepoDrop
	afterRepoDrop = func(args []string) {
		mu.Lock()
		defer mu.Unlock()
		if fired || !argvHas(args, "rev-parse", "--absolute-git-dir") {
			return
		}
		fired = true
		if _, err := f.s.gitShadow.syncRepo(gitB, readB); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterRepoDrop = old })
	raw, calls := f.call(t, "git.status", params)
	if !fired {
		t.Fatal("the folder did not go")
	}
	if raw != control {
		t.Errorf("git.status after the loss = %s\nwant %s", raw, control)
	}
	if got := fileReads(calls); got != reads {
		t.Errorf("%d reads of the config, want %d as without the loss", got, reads)
	}
	sweptThere(t, filepath.Join(daemonFolder(t, f.run), shadowRepoName(filepath.Join(f.repo, ".git"))))
	sweptGone(t, filepath.Join(daemonFolder(t, f.run), shadowRepoName(filepath.Join(other, ".git"))))
}

// A folder that goes between a listing and its call is not made again there:
// the call is refused with `repoCopyGoneText`. On 5fd08069 the step of
// `repoCopyGoneText` is a listing in 44 of 44 error lines (E3L cell 54).
func TestRepoHoldNoRenewAfterAListing(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newShadowFixture(t)
	other, _ := configRepo(t)
	gitB, readB := acceptedRepo(t, other)
	params := map[string]any{"path": f.wt, "baseRepo": f.repo}
	if raw, _ := f.call(t, "git.status", params); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("the warm-up git.status = %s", raw)
	}
	var mu sync.Mutex
	fired := false
	old := afterRepoDrop
	afterRepoDrop = func(args []string) {
		mu.Lock()
		defer mu.Unlock()
		if fired || gitSubcommand(args) != "config" {
			return
		}
		fired = true
		if _, err := f.s.gitShadow.syncRepo(gitB, readB); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterRepoDrop = old })
	raw, _ := f.call(t, "git.status", params)
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + textA + `"}}`
	if raw != want {
		t.Errorf("git.status = %s\nwant %s", raw, want)
	}
}

// The branch step follows the rule of hardenedGitCmd: a folder that goes
// between its listing and its call is not made again, and the call gets text
// A (from the code).
func TestRepoHoldBranchStepNoRenewAfterItsListing(t *testing.T) {
	lowerRepoLimit(t, 1)
	f := newShadowFixture(t)
	other, _ := configRepo(t)
	gitB, readB := acceptedRepo(t, other)
	params := map[string]any{"path": f.wt, "baseRepo": f.repo}
	if raw, _ := f.call(t, "git.status", params); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("the warm-up git.status = %s", raw)
	}
	gitA, readA := acceptedRepo(t, f.repo)
	var mu sync.Mutex
	fired := false
	old := afterRepoDrop
	afterRepoDrop = func(args []string) {
		mu.Lock()
		defer mu.Unlock()
		if fired || gitSubcommand(args) != "config" {
			return
		}
		fired = true
		if _, err := f.s.gitShadow.syncRepo(gitB, readB); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterRepoDrop = old })
	steps := map[string]func(u gitUse) error{
		"rev-parse": func(u gitUse) error {
			_, _, err := branchCheckGit(context.Background(), u, f.repo, nil, "rev-parse", "HEAD")
			return err
		},
		"update-ref": func(u gitUse) error {
			return deleteBranchRef(u, f.repo, "refs/heads/nothing", strings.Repeat("0", 40))
		},
	}
	for name, step := range steps {
		u, err := f.s.gitShadow.syncRepo(gitA, readA)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		fired = false
		mu.Unlock()
		err = step(u)
		mu.Lock()
		went := fired
		mu.Unlock()
		if !went {
			t.Fatalf("%s: the folder did not go", name)
		}
		if err == nil || err.Error() != textA {
			t.Errorf("%s after its listing = %v, want text A", name, err)
		}
	}
}
