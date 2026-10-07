//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A FIFO where the daemon reads a `.git` file, a `commondir` file or a `gitdir`
// record. The rows are B-C0 to B-L1, side by side against 89cb6289 on Linux and macOS
// VMs. No test here opens a FIFO for writing: the rows have no writer either. A read
// that waits for a writer therefore fails its test at fifoAnswerLimit and stays
// parked until the test binary ends.

// fifoAnswerLimit bounds each call that meets a FIFO. 89cb6289 answers in under 2 s.
const fifoAnswerLimit = 20 * time.Second

// within runs fn on another goroutine and fails the test when fn is not done at
// fifoAnswerLimit. fn must not touch t.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(fifoAnswerLimit):
		t.Fatalf("%s gave no answer in %s: a read waits on the FIFO", what, fifoAnswerLimit)
	}
}

// frameWithin sends one request and returns its frame. It fails the test when the
// daemon gives no answer in fifoAnswerLimit.
func frameWithin(t *testing.T, method string, params map[string]any) string {
	t.Helper()
	s := newTestServer(t)
	line := []byte(rpcLine(t, method, params))
	var resp *response
	within(t, method, func() { resp = s.dispatch(nil, line) })
	if resp == nil {
		t.Fatalf("%s: no reply", method)
	}
	raw, err := json.Marshal(*resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(raw)
}

// newFifoRepo makes dir a repository on branch main with one commit. It holds
// .gitignore, t.txt, a/f.txt and T/t.txt.
func newFifoRepo(t *testing.T, dir string) {
	t.Helper()
	requireGit(t)
	copyFixtureTemplate(t, "fifo-main", dir, func(t *testing.T, dir string) {
		runGit(t, dir, "init", "-q", "-b", "main")
		writeFile(t, filepath.Join(dir, ".gitignore"), ".claude/worktrees/\n", 0o644)
		writeFile(t, filepath.Join(dir, "t.txt"), "t\n", 0o644)
		writeFile(t, filepath.Join(dir, "a", "f.txt"), "f\n", 0o644)
		writeFile(t, filepath.Join(dir, "T", "t.txt"), "inner\n", 0o644)
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-q", "-m", "init")
	})
}

// skipUnlessGitSkipsFifoDotGit skips the test when the git of this host does not
// walk past a FIFO named `.git` in dir. The rows ran with a git that walks past it.
// A newer git refuses the FIFO itself, and the daemon then answers the text of git.
func skipUnlessGitSkipsFifoDotGit(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fifoAnswerLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("this git does not walk past a FIFO named .git: %v: %s", err, out)
	}
}

func wantFifoFrame(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func wantFileContent(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Errorf("%s = %q (%v), want %q", p, b, err, want)
	}
}

func wantFifo(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("%s is no longer a FIFO (%v)", p, err)
	}
}

func createOK(wt, branch string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":"` + wt +
		`","sourceBranch":"main","branch":"` + branch + `"}}`
}

const createNotARepo = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`

// wantCreatedIn asserts the disk state of a create that succeeded: the entry name in
// the repository at repo names the leaf wt, the `.git` file of wt names that entry,
// and the branch exists there.
func wantCreatedIn(t *testing.T, repo, wt, name string) {
	t.Helper()
	entry := filepath.Join(repo, ".git", "worktrees", name)
	wantFileContent(t, filepath.Join(entry, "gitdir"), filepath.Join(wt, ".git")+"\n")
	wantFileContent(t, filepath.Join(entry, "commondir"), "../..\n")
	wantFileContent(t, filepath.Join(entry, "HEAD"), "ref: refs/heads/"+name+"\n")
	wantFileContent(t, filepath.Join(wt, ".git"), "gitdir: "+entry+"\n")
	wantFileContent(t, filepath.Join(wt, "t.txt"), "t\n")
	wantFileContent(t, filepath.Join(wt, "a", "f.txt"), "f\n")
	if !branchExists(t, repo, name) {
		t.Errorf("branch %s is not in %s", name, repo)
	}
}

func createParams(base, wt, branch string) map[string]any {
	return map[string]any{"baseRepo": base, "worktreePath": wt, "branchName": branch}
}

// Row B-F1: <baseRepo>/.git is a FIFO. The create succeeds, and the worktree is made
// in the repository above baseRepo.
func TestFifoDotGitCreateSucceeds(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	a := filepath.Join(T, "a")
	mkfifo(t, filepath.Join(a, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, a)
	wt := filepath.Join(a, ".claude", "worktrees", "w1")
	wantFifoFrame(t, "create(T/a)", frameWithin(t, "git.worktree_create", createParams(a, wt, "w1")), createOK(wt, "w1"))
	wantCreatedIn(t, T, wt, "w1")
	wantFifo(t, filepath.Join(a, ".git"))
}

// Row B-S2: <baseRepo>/.git is a symlink to a FIFO. The create succeeds as in B-F1.
func TestSymlinkToFifoDotGitCreateSucceeds(t *testing.T) {
	base := realTempDir(t)
	T := filepath.Join(base, "T")
	newFifoRepo(t, T)
	a := filepath.Join(T, "a")
	fifo := filepath.Join(base, "fifo")
	mkfifo(t, fifo)
	symlink(t, fifo, filepath.Join(a, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, a)
	wt := filepath.Join(a, ".claude", "worktrees", "w1")
	wantFifoFrame(t, "create(T/a)", frameWithin(t, "git.worktree_create", createParams(a, wt, "w1")), createOK(wt, "w1"))
	wantCreatedIn(t, T, wt, "w1")
	wantFifo(t, fifo)
}

// Row B-F4: baseRepo T lies inside an outer repository O, and T/.git is a FIFO. The
// create succeeds, and the worktree is made in O.
func TestFifoDotGitCreateUsesOuterRepository(t *testing.T) {
	O := filepath.Join(realTempDir(t), "O")
	newFifoRepo(t, O)
	T := filepath.Join(O, "T")
	mkfifo(t, filepath.Join(T, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, T)
	wt := filepath.Join(T, ".claude", "worktrees", "w1")
	wantFifoFrame(t, "create(O/T)", frameWithin(t, "git.worktree_create", createParams(T, wt, "w1")), createOK(wt, "w1"))
	wantCreatedIn(t, O, wt, "w1")
	wantFileContent(t, filepath.Join(wt, "T", "t.txt"), "inner\n")
	wantFifo(t, filepath.Join(T, ".git"))
}

// Row B-L1: a create as in B-F1, then a create in T itself with another leaf. Both
// succeed.
func TestFifoDotGitCreateThenSecondCreate(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	a := filepath.Join(T, "a")
	mkfifo(t, filepath.Join(a, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, a)
	w1 := filepath.Join(a, ".claude", "worktrees", "w1")
	w2 := filepath.Join(T, ".claude", "worktrees", "w2")
	wantFifoFrame(t, "first create(T/a)", frameWithin(t, "git.worktree_create", createParams(a, w1, "w1")), createOK(w1, "w1"))
	wantFifoFrame(t, "second create(T)", frameWithin(t, "git.worktree_create", createParams(T, w2, "w2")), createOK(w2, "w2"))
	wantCreatedIn(t, T, w1, "w1")
	wantCreatedIn(t, T, w2, "w2")
}

// Row B-G3: the `commondir` file of the registration to remove is a FIFO. The remove
// succeeds. The worktree, its registration and its branch go, and the worktrees
// folder stays.
func TestFifoCommondirRemoveSucceeds(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	wt := filepath.Join(T, ".claude", "worktrees", "w1")
	runGit(t, T, "worktree", "add", "-q", "-b", "w1", wt)
	entry := filepath.Join(T, ".git", "worktrees", "w1")
	cd := filepath.Join(entry, "commondir")
	if err := os.Remove(cd); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, cd)
	wantFifoFrame(t, "remove(w1)", frameWithin(t, "git.worktree_remove", createParams(T, wt, "w1")), removeOK)
	mustBeGone(t, wt)
	mustBeGone(t, entry)
	mustExist(t, filepath.Join(T, ".git", "worktrees"))
	if branchExists(t, T, "w1") {
		t.Error("branch w1 stays, want it deleted")
	}
	if !branchExists(t, T, "main") {
		t.Error("branch main is gone")
	}
}

// Row B-G2: a FIFO is the `gitdir` record of ANOTHER registration. The remove of a
// real worktree succeeds, and the other entry stays.
func TestFifoGitdirRecordOfOtherEntryRemoveSucceeds(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	wt := filepath.Join(T, ".claude", "worktrees", "w1")
	runGit(t, T, "worktree", "add", "-q", "-b", "w1", wt)
	old := filepath.Join(T, ".git", "worktrees", "old", "gitdir")
	mkfifo(t, old)
	wantFifoFrame(t, "remove(w1)", frameWithin(t, "git.worktree_remove", createParams(T, wt, "w1")), removeOK)
	mustBeGone(t, wt)
	mustBeGone(t, filepath.Join(T, ".git", "worktrees", "w1"))
	wantFifo(t, old)
	if branchExists(t, T, "w1") {
		t.Error("branch w1 stays, want it deleted")
	}
}

// Row B-F3: the worktree is made first, then <baseRepo>/.git becomes a FIFO. The
// remove succeeds.
func TestFifoDotGitRemoveSucceeds(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	a := filepath.Join(T, "a")
	wt := filepath.Join(a, ".claude", "worktrees", "w1")
	runGit(t, T, "worktree", "add", "-q", "-b", "w1", wt)
	mkfifo(t, filepath.Join(a, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, a)
	wantFifoFrame(t, "remove(w1)", frameWithin(t, "git.worktree_remove", createParams(a, wt, "w1")), removeOK)
	mustBeGone(t, wt)
	mustBeGone(t, filepath.Join(T, ".git", "worktrees", "w1"))
	mustExist(t, filepath.Join(T, ".git", "worktrees"))
	if branchExists(t, T, "w1") {
		t.Error("branch w1 stays, want it deleted")
	}
	wantFifo(t, filepath.Join(a, ".git"))
}

// Row B-F2: git.info, git.list_branches and git.status with a FIFO `.git`.
func TestFifoDotGitReadMethods(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	newFifoRepo(t, T)
	a := filepath.Join(T, "a")
	mkfifo(t, filepath.Join(a, ".git"))
	skipUnlessGitSkipsFifoDotGit(t, a)
	result := func(body string) string { return `{"jsonrpc":"2.0","id":1,"result":` + body + `}` }
	wantFifoFrame(t, "info(T/a)", frameWithin(t, "git.info", map[string]any{"path": a}),
		result(`{"isRepo":true,"repo":"T","branch":"main","root":"`+T+`","repoSlug":"","defaultBranch":""}`))
	wantFifoFrame(t, "list_branches(T/a)", frameWithin(t, "git.list_branches", map[string]any{"path": a}),
		result(`{"isRepo":true,"branches":["main"]}`))
	wantFifoFrame(t, "status(T/a)", frameWithin(t, "git.status", map[string]any{"path": a, "baseRepo": T}),
		result(`{"isRepo":false,"clean":false}`))
}

// Rows B-F5 and B-S1: a FIFO `.git` with no repository above it, and a `.git` file
// of 0 bytes. Both answer not_a_repo, and nothing is made.
func TestFifoDotGitWithoutRepositoryIsNotARepo(t *testing.T) {
	t.Run("B-F5 FIFO and no outer repository", func(t *testing.T) {
		T := filepath.Join(realTempDir(t), "P5", "T")
		mkfifo(t, filepath.Join(T, ".git"))
		wt := filepath.Join(T, ".claude", "worktrees", "w1")
		wantFifoFrame(t, "create(T)", frameWithin(t, "git.worktree_create", createParams(T, wt, "w1")), createNotARepo)
		mustBeGone(t, filepath.Join(T, ".claude"))
	})
	t.Run("B-S1 file of 0 bytes", func(t *testing.T) {
		T := filepath.Join(realTempDir(t), "T")
		newFifoRepo(t, T)
		a := filepath.Join(T, "a")
		writeFile(t, filepath.Join(a, ".git"), "", 0o644)
		wt := filepath.Join(a, ".claude", "worktrees", "w1")
		wantFifoFrame(t, "create(T/a)", frameWithin(t, "git.worktree_create", createParams(a, wt, "w1")), createNotARepo)
		mustBeGone(t, filepath.Join(a, ".claude"))
		mustBeGone(t, filepath.Join(T, ".git", "worktrees"))
	})
}

// The read of row B-G1: a FIFO is the `gitdir` record of another registration, and a
// create looks for a stale registration of its leaf. The look goes past the FIFO, and
// the entry stays. The create then reaches `git worktree add`, which this test does
// not run: on 89cb6289 that call waits.
func TestDropStaleRegistrationGoesPastFifoRecord(t *testing.T) {
	T := filepath.Join(realTempDir(t), "T")
	old := filepath.Join(T, ".git", "worktrees", "old", "gitdir")
	mkfifo(t, old)
	stale := filepath.Join(T, ".git", "worktrees", "zz")
	wt := filepath.Join(T, ".claude", "worktrees", "w1")
	writeFile(t, filepath.Join(stale, "gitdir"), filepath.Join(wt, ".git")+"\n", 0o644)
	within(t, "dropStaleWorktreeRegistration", func() { dropStaleWorktreeRegistration(T, wt) })
	wantFifo(t, old)
	mustBeGone(t, stale)
}

// Each read of a `.git` file, a `commondir` file or a `gitdir` record answers at once
// for a FIFO, with the answer of a file that is not usable. These reads do not need
// git, so they run on every host.
func TestGitFileReadsDoNotWaitOnFifo(t *testing.T) {
	base := realTempDir(t)
	wt := filepath.Join(base, "wt")
	mkfifo(t, filepath.Join(wt, ".git"))
	registry := filepath.Join(base, "worktrees")
	admin := filepath.Join(registry, "admin")
	mkfifo(t, filepath.Join(admin, "gitdir"))
	mkfifo(t, filepath.Join(admin, "commondir"))
	linked := filepath.Join(base, "linked")
	writeFile(t, filepath.Join(linked, ".git"), "gitdir: "+admin+"\n", 0o644)

	var adminDir, gitDir, registryDir string
	var belongs bool
	var belongsErr error
	within(t, "the path reads", func() {
		adminDir = worktreeAdminDir(wt)
		gitDir = repoGitDir(wt)
		belongs, belongsErr = worktreeAdminBelongsTo(admin, linked)
		registryDir = worktreeRegistryDir(linked)
	})
	if adminDir != "" {
		t.Errorf("worktreeAdminDir(FIFO .git) = %q, want \"\"", adminDir)
	}
	if want := filepath.Join(wt, ".git"); gitDir != want {
		t.Errorf("repoGitDir(FIFO .git) = %q, want %q", gitDir, want)
	}
	if belongs || !errors.Is(belongsErr, errNotRegularFile) {
		t.Errorf("worktreeAdminBelongsTo(FIFO gitdir) = (%v, %v), want (false, %v)", belongs, belongsErr, errNotRegularFile)
	}
	if want := filepath.Join(admin, "worktrees"); registryDir != want {
		t.Errorf("worktreeRegistryDir(FIFO commondir) = %q, want %q", registryDir, want)
	}

	root, err := os.OpenRoot(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	var recOK bool
	var entryErr error
	within(t, "the reads through a root", func() {
		_, recOK = loadEntryRecord(root, registry, "admin")
		_, entryErr = verifiedWorktreeEntry(admin, base, "", linked,
			newWorktreePathSet(linked), nil)
	})
	if recOK {
		t.Error("loadEntryRecord(FIFO gitdir) = true, want the record skipped")
	}
	if entryErr == nil {
		t.Error("verifiedWorktreeEntry = nil, want an error")
	}
}

// A regular file of 1 MiB + 1 bytes reads whole through the helper, by path and in a
// root. The reads had no size bound before the helper, and they have none now.
func TestReadGitPlainFileHasNoSizeBound(t *testing.T) {
	dir := realTempDir(t)
	want := strings.Repeat("x", 1<<20) + "y"
	writeFile(t, filepath.Join(dir, "big"), want, 0o644)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	byPath, pathErr := readGitPlainFile(filepath.Join(dir, "big"))
	if pathErr != nil || string(byPath) != want {
		t.Errorf("by path = (%d bytes, %v), want the %d bytes of the file", len(byPath), pathErr, len(want))
	}
	inRoot, rootErr := readGitPlainFileIn(root, "big")
	if rootErr != nil || string(inRoot) != want {
		t.Errorf("in a root = (%d bytes, %v), want the %d bytes of the file", len(inRoot), rootErr, len(want))
	}
}

// readGitPlainFile reads a regular file, also through a symlink. A FIFO, a symlink
// to a FIFO and a folder are errors, at once.
func TestReadGitPlainFileKinds(t *testing.T) {
	dir := realTempDir(t)
	writeFile(t, filepath.Join(dir, "plain"), "gitdir: x\n", 0o644)
	symlink(t, "plain", filepath.Join(dir, "link"))
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, "fifo", filepath.Join(dir, "fifolink"))
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, c := range []struct {
		name    string
		size    int
		notFile bool
		fails   bool
	}{
		{"plain", len("gitdir: x\n"), false, false},
		{"link", len("gitdir: x\n"), false, false},
		{"fifo", 0, true, true},
		{"fifolink", 0, true, true},
		{"folder", 0, true, true},
		{"missing", 0, false, true},
	} {
		var byPath, inRoot []byte
		var pathErr, rootErr error
		within(t, "readGitPlainFile("+c.name+")", func() {
			byPath, pathErr = readGitPlainFile(filepath.Join(dir, c.name))
			inRoot, rootErr = readGitPlainFileIn(root, c.name)
		})
		for _, r := range []struct {
			what string
			b    []byte
			err  error
		}{{"by path", byPath, pathErr}, {"in a root", inRoot, rootErr}} {
			if (r.err != nil) != c.fails || len(r.b) != c.size || errors.Is(r.err, errNotRegularFile) != c.notFile {
				t.Errorf("%s %s = (%d bytes, %v), want %d bytes, fails %v, not a regular file %v",
					c.name, r.what, len(r.b), r.err, c.size, c.fails, c.notFile)
			}
		}
	}
}
