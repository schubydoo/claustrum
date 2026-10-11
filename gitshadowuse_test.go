package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// useOf is the gitUse of a request that accepted the repository in dir, on the
// fixture f.
func (f repoFolderFixture) useOf(t *testing.T, dir string) gitUse {
	t.Helper()
	trust := gitDirTrustFor(dir)
	v := repoReadVerdict(trust)
	if trust.verdict != gitDirTrusted || v == nil || v.refusal != "" || gitDirFilesRefusal(trust.pinCommonDir) != "" {
		t.Fatalf("the fixture in %s is not accepted: %+v", dir, v)
	}
	u, err := f.h.syncRepo(trust.pinCommonDir, v)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// clearFolderEnv takes the folder variables of git out of the environment of
// the test.
func clearFolderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGitUseFolderVariables pins the folder variables of a call in the state
// shadow, per kind of folder. The values and the order are those of the call
// logs of 5fd08069 on a Linux VM. A plain repository is in s3p2-linux cell B3a,
// a linked worktree in cell B3b and a bare repository in cell B3c. A folder
// inside the work tree is in s3p2-linux2 cell D7b. A call with --git-dir= in its argv
// has the first two variables only (val-s3p2b1-linux cell A1). A request with
// no copy, a folder of another repository and a daemon GIT_COMMON_DIR keep the
// form of before.
func TestGitUseFolderVariables(t *testing.T) {
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	u := f.useOf(t, f.repo)
	repoDir := filepath.Join(f.h.dir, shadowRepoName(f.gitDir))
	if u.repoDir != repoDir || u.folder != f.h.dir || u.gitDir != f.gitDir || !u.shadow() {
		t.Fatalf("use = %+v, want the folder %s of the git directory %s", u, repoDir, f.gitDir)
	}
	common := []string{"GIT_COMMON_DIR=" + repoDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(f.gitDir, "objects")}
	sub := filepath.Join(f.repo, "mnt", "in")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, dir string
		want      []string
	}{
		{"plain repository", f.repo, append(slices.Clone(common), "GIT_DIR="+f.gitDir, "GIT_WORK_TREE="+f.repo)},
		{"linked worktree", f.wt, append(slices.Clone(common),
			"GIT_DIR="+filepath.Join(f.gitDir, "worktrees", "wr"), "GIT_WORK_TREE="+f.wt)},
		{"folder inside the work tree", sub, append(slices.Clone(common), "GIT_DIR="+f.gitDir, "GIT_WORK_TREE="+f.repo)},
	} {
		if got := u.pin(row.dir); !slices.Equal(got, row.want) {
			t.Errorf("%s: pin = %q\nwant %q", row.name, got, row.want)
		}
		if got := u.gitDirPin(row.dir); !slices.Equal(got, common) {
			t.Errorf("%s: gitDirPin = %q\nwant %q", row.name, got, common)
		}
	}

	// The calls that keep the git directory itself: the same four names, with
	// GIT_COMMON_DIR=<git directory>.
	own := []string{"GIT_COMMON_DIR=" + f.gitDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(f.gitDir, "objects"),
		"GIT_DIR=" + f.gitDir, "GIT_WORK_TREE=" + f.repo}
	if got := u.ownConfigCalls().pin(f.repo); !slices.Equal(got, own) {
		t.Errorf("ownConfigCalls: pin = %q\nwant %q", got, own)
	}

	// A folder of another repository keeps the pin of that repository.
	other, otherGit := configRepo(t)
	if got, want := u.pin(other), []string{"GIT_COMMON_DIR=" + otherGit}; !slices.Equal(got, want) {
		t.Errorf("another repository: pin = %q, want %q", got, want)
	}
	if got, want := u.gitDirPin(other), []string{"GIT_COMMON_DIR=" + otherGit}; !slices.Equal(got, want) {
		t.Errorf("another repository: gitDirPin = %q, want %q", got, want)
	}
	// A request with no copy keeps the one variable.
	if got, want := (gitUse{}).pin(f.repo), []string{"GIT_COMMON_DIR=" + f.gitDir}; !slices.Equal(got, want) {
		t.Errorf("no copy: pin = %q, want %q", got, want)
	}

	t.Run("bare repository", func(t *testing.T) {
		bare := filepath.Join(resolveTestRoot(t, t.TempDir()), "bare.git")
		runGit(t, f.repo, "clone", "-q", "--bare", f.repo, bare)
		ub := f.useOf(t, bare)
		want := []string{"GIT_COMMON_DIR=" + filepath.Join(f.h.dir, shadowRepoName(bare)),
			"GIT_OBJECT_DIRECTORY=" + filepath.Join(bare, "objects"), "GIT_DIR=" + bare}
		if got := ub.pin(bare); !slices.Equal(got, want) {
			t.Errorf("pin = %q\nwant %q", got, want)
		}
	})
	t.Run("daemon GIT_COMMON_DIR", func(t *testing.T) {
		t.Setenv("GIT_COMMON_DIR", f.gitDir)
		if got := u.pin(f.repo); got != nil {
			t.Errorf("pin = %q, want none", got)
		}
		if got := u.gitDirPin(f.repo); got != nil {
			t.Errorf("gitDirPin = %q, want none", got)
		}
	})
	t.Run("daemon GIT_DIR", func(t *testing.T) {
		t.Setenv("GIT_DIR", f.gitDir)
		want := append(slices.Clone(common), "GIT_DIR="+f.gitDir)
		if got := u.pin(f.repo); !slices.Equal(got, want) {
			t.Errorf("pin = %q\nwant %q", got, want)
		}
	})
}

// TestGitUseProfile pins the variables of the light profile per state. In the
// state shadow they are the six values in the order of the Linux call log of
// 5fd08069 (val-s3p2b1-linux row N1). With no copy they are the four of the
// light profile. The heavy profile is the same in both.
func TestGitUseProfile(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "")
	if err := os.Unsetenv("GIT_ALLOW_PROTOCOL"); err != nil {
		t.Fatal(err)
	}
	shadow := gitUse{folder: "s", repoDir: "r", gitDir: "g"}
	six := []string{"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_ASKPASS="}
	if got := shadow.profileEnv(false); !slices.Equal(got, six) {
		t.Errorf("light profile in the state shadow = %q\nwant %q", got, six)
	}
	four := []string{"GIT_ALLOW_PROTOCOL=https:ssh", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull}
	// The state fallback-git has the folder of the daemon and no copy: six values.
	if got := (gitUse{folder: "s"}).profileEnv(false); !slices.Equal(got, six) {
		t.Errorf("light profile in the state fallback-git = %q\nwant %q", got, six)
	}
	if got := (gitUse{}).profileEnv(false); !slices.Equal(got, four) {
		t.Errorf("light profile with no copy = %q\nwant %q", got, four)
	}
	if a, b := shadow.profileEnv(true), (gitUse{}).profileEnv(true); !slices.Equal(a, b) || !slices.Equal(a, profileEnv(true)) {
		t.Errorf("heavy profile = %q and %q, want %q in both", a, b, profileEnv(true))
	}
}

// TestRequestDir pins the place, the name and the life of a short-lived folder
// of one request. In the state shadow it is <S>/<prefix><decimal number> with
// mode 0700 (the modes of val-s3p2b2-1-linux cell s3-first-create, Linux VM).
// Its remove function takes it away, and so does the shutdown of the daemon
// (s3p2-linux2 cell F3). With no copy it is a new folder of the temporary
// folder with the other prefix.
func TestRequestDir(t *testing.T) {
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	u := f.useOf(t, f.repo)
	name := regexp.MustCompile(`^\.gitdir-[0-9]+$`)

	dir, remove, err := u.requestDir(shadowStatusDirPrefix, statusGitDirTempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != f.h.dir || !name.MatchString(filepath.Base(dir)) {
		t.Errorf("folder = %s, want %s/.gitdir-<n>", dir, f.h.dir)
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the folder: %v, %v", fi, err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("the folder has mode %v, want 0700", fi.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "index"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, removeSecond, err := u.requestDir(shadowStatusDirPrefix, statusGitDirTempPrefix)
	if err != nil || second == dir {
		t.Fatalf("a second folder = %s, %v, want another name", second, err)
	}
	remove()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder after its remove: %v, want it gone", err)
	}
	if _, err := os.Lstat(second); err != nil {
		t.Errorf("the second folder after the remove of the first: %v", err)
	}

	// The shutdown takes a folder in flight with it. A remove after that changes
	// nothing, and a new folder cannot be made.
	f.h.close()
	if _, err := os.Lstat(second); !os.IsNotExist(err) {
		t.Errorf("the folder after the shutdown: %v, want it gone", err)
	}
	removeSecond()
	if got := daemonFolders(t, f.h.runDir); len(got) != 0 {
		t.Errorf("git-shadow after the shutdown holds %v", got)
	}
	if dir, _, err := u.requestDir(shadowStatusDirPrefix, statusGitDirTempPrefix); err == nil {
		t.Errorf("a folder after the shutdown = %s, want an error", dir)
	}

	// With no copy the folder is in the temporary folder.
	dir, remove, err = (gitUse{}).requestDir(shadowIndexDirPrefix, checkoutIndexTempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(resolveTestRoot(t, filepath.Dir(dir)), resolveTestRoot(t, os.TempDir())) ||
		!strings.HasPrefix(filepath.Base(dir), "claude-ssh-index-") {
		t.Errorf("folder with no copy = %s, want %s/claude-ssh-index-<n>", dir, os.TempDir())
	}
	remove()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the temporary folder after its remove: %v, want it gone", err)
	}
}

// TestGitTempNames pins the names of the short-lived entries. With
// "shadowDirOff":true 5fd08069 has them in the temporary folder
// (val-s3p2b1-linux section 1.2: rows D5 and D6, and the index name). Without
// it they are in the folder of the daemon (row N11 and cell A3-create-plain).
func TestGitTempNames(t *testing.T) {
	for got, want := range map[string]string{
		statusGitDirTempPrefix:  "claude-ssh-gitdir-",
		checkoutIndexTempPrefix: "claude-ssh-index-",
		includeTempPrefix:       "claude-ssh-worktreeinclude-",
		includePatternsName:     "patterns",
		shadowStatusDirPrefix:   ".gitdir-",
		shadowIndexDirPrefix:    ".index-",
		shadowIncludeDirPrefix:  ".worktreeinclude-",
	} {
		if got != want {
			t.Errorf("name %q, want %q", got, want)
		}
	}
}

// TestShadowRepoFailureRefusesTheRequest pins the answer of each method when
// the folder of the repository cannot be made. It is a refusal with
// claustrum's own text, in the shape that the method gives a failed start
// test. No git call runs on the repository. No row of 5fd08069 has this
// failure.
func TestShadowRepoFailureRefusesTheRequest(t *testing.T) {
	for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
		t.Run(method, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			h := f.s.gitShadow
			h.mu.Lock()
			err := h.makeFolder()
			dir := h.dir
			h.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			// A file stands where the folder of the repository goes.
			blocked := filepath.Join(dir, shadowRepoName(f.gitDir))
			if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			m := f.fiveRequests(nil)[i]
			raw, calls := f.call(t, m.method, m.params)
			// The text of 5fd08069 for a plain file with the name of the folder
			// (cells N4 of val-s3p2b2-3-linux2, val-s3p2b2-3-macos and
			// val-s3p2b2-3-windows). The cause is `mkdir <path>: not a directory`
			// on Linux and macOS, and `mkdir <path>: The system cannot find the
			// path specified.` on Windows. That is syscall.ENOTDIR on each system.
			cause := (&os.PathError{Op: "mkdir", Path: blocked, Err: syscall.ENOTDIR}).Error()
			if runtime.GOOS != "windows" && cause != "mkdir "+blocked+": not a directory" {
				t.Fatalf("the cause of the test = %q", cause)
			}
			text := "Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: " + cause
			start := strings.Trim(jsonQuoted(t, text), `"`)
			const end = ""
			wantShape := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + start
			switch method {
			case "git.worktree_create":
				wantShape = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + start
			case "git.worktree_remove":
				wantShape = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not check whether ` +
					strings.Trim(jsonQuoted(t, f.wt), `"`) + ` is locked: ` + start
			}
			if !strings.HasPrefix(raw, wantShape+`"`) || !strings.Contains(raw, end) {
				t.Errorf("%s = %s\nwant it to start %s", method, raw, wantShape)
			}
			// The same request again gets the same line with no git call, and the
			// file stays (the same cells).
			again, calls2 := f.call(t, m.method, m.params)
			if again != raw || len(calls2) != 0 {
				t.Errorf("%s again = %s with %d calls\nwant the same line and no call", method, again, len(calls2))
			}
			if b, err := os.ReadFile(blocked); err != nil || string(b) != "x" {
				t.Errorf("the planted file: %q, %v", b, err)
			}
			if method == "git.worktree_create" && !strings.HasSuffix(raw, `,"errorCode":"worktree_add_failed"}}`) {
				t.Errorf("%s = %s, want the errorCode worktree_add_failed", method, raw)
			}
			for _, c := range calls {
				if !c.excludesRead() && !c.configRead() {
					t.Errorf("%s ran %q after the folder step failed", method, c.argv)
				}
			}
			if _, err := os.Stat(f.wt); err != nil {
				t.Errorf("the worktree of the fixture: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
				t.Errorf("a worktree was made: %v", err)
			}
			// A request with the member needs no folder and works.
			if raw, _ := f.info(t, f.repo, map[string]any{"shadowDirOff": true}); !strings.Contains(raw, `"settingsControl":"off-switch"`) {
				t.Errorf("git.info with the member = %s", raw)
			}
		})
	}
}

// TestShadowRepoFailureInTheShutdown pins that a request in the shutdown of the
// daemon gets the refusal, and no git call on the repository. Not measured on
// 5fd08069.
func TestShadowRepoFailureInTheShutdown(t *testing.T) {
	f := newSeededShadowFixture(t)
	f.s.gitShadow.close()
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, "Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: the daemon is in its shutdown") {
		t.Errorf("git.info in the shutdown = %s", raw)
	}
	for _, c := range calls {
		if !c.excludesRead() && !c.configRead() {
			t.Errorf("git.info ran %q in the shutdown", c.argv)
		}
	}
}

// TestShadowAnswersWhereGitRefusesAValue pins the answers in the cells where
// plain git refuses a value of the repository's config. In the state shadow git
// reads the daemon's copy. The copy leaves the four first values out, and
// 5fd08069 answers git.info and git.status normally there. It carries
// extensions.objectformat = sha256 in a sha1 repository, and git then finds no
// repository. It carries core.bare = true, and both methods answer normally
// (s3p2-linux cells A3, Linux VM, and the A3 cells of s3p2-macos and
// s3p2-windows).
//
// With "shadowDirOff":true git reads the repository's own config, and the
// answers are those of before.
func TestShadowAnswersWhereGitRefusesAValue(t *testing.T) {
	const (
		infoShadow = `"result":{"isRepo":true,"repo":"repo","branch":"main","root":`
		noRepo     = `"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`
		status     = `"result":{"isRepo":true,"clean":true}`
		noStatus   = `"result":{"isRepo":false,"clean":false}`
	)
	for _, cell := range []struct {
		name, config     string
		info, status     string
		normalWithMember bool
	}{
		{"core.filemode = maybe", "[core]\n\trepositoryformatversion = 0\n\tfilemode = maybe\n\tbare = false\n\tlogallrefupdates = true\n", infoShadow, status, false},
		{"core.autocrlf = sometimes", version0 + "[core]\n\tautocrlf = sometimes\n", infoShadow, status, false},
		{"index.version = banana", version0 + "[index]\n\tversion = banana\n", infoShadow, status, false},
		{"extensions.objectformat = md5", version0 + "[extensions]\n\tobjectformat = md5\n", infoShadow, status, false},
		{"extensions.objectformat = sha256", version0 + "[extensions]\n\tobjectformat = sha256\n", noRepo, noStatus, false},
		{"core.bare = true", "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n\tlogallrefupdates = true\n", infoShadow, status, true},
	} {
		t.Run(cell.name, func(t *testing.T) {
			clearFolderEnv(t)
			f := newRefusalFixture(t)
			s, _ := newSeededShadowServer(t)
			setRepoConfig(t, f.gitDir, cell.config)
			info := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo}))
			if !strings.Contains(info, cell.info) || (cell.info == infoShadow && !strings.HasSuffix(info, `"settingsControl":"shadow"}}`)) {
				t.Errorf("git.info = %s\nwant %s", info, cell.info)
			}
			if got := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})); !strings.Contains(got, cell.status) {
				t.Errorf("git.status = %s\nwant %s", got, cell.status)
			}
			// With the member the answer is the one of before the copy.
			off := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo, "shadowDirOff": true}))
			if normal := strings.Contains(off, `"isRepo":true`); normal != cell.normalWithMember {
				t.Errorf("git.info with the member = %s\nwant a normal answer: %v", off, cell.normalWithMember)
			}
		})
	}
}

// TestShadowCreateWithBareInConfigWorktree pins two answers in a repository
// with the worktreeConfig switch and `core.bare = true` in its
// `config.worktree` file. In the state shadow 5fd08069 creates the worktree.
// A git.status on it then answers a clean worktree (cell 9a of
// s3p2b-edges2-linux, Linux VM, requests 2 and 4). With "shadowDirOff":true
// git reads that file, and the create answers "not a git repository" as before.
func TestShadowCreateWithBareInConfigWorktree(t *testing.T) {
	clearFolderEnv(t)
	f := newRefusalFixture(t)
	s, _ := newSeededShadowServer(t)
	runGit(t, f.repo, "config", "extensions.worktreeConfig", "true")
	setIncludeFile(t, f.gitDir, "config.worktree", "[core]\n\tbare = true\n")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	off := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": f.repo, "branchName": "w0", "worktreePath": filepath.Join(f.repo, ".claude", "worktrees", "w0"), "shadowDirOff": true}))
	if !strings.Contains(off, `"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}`) {
		t.Errorf("git.worktree_create with the member = %s", off)
	}
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}))
	if !strings.Contains(raw, `"result":{"success":true,"path":`) || !strings.HasSuffix(raw, `,"sourceBranch":"main","branch":"w1"}}`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	raw = dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": leaf, "baseRepo": f.repo}))
	if !strings.Contains(raw, `"result":{"isRepo":true,"clean":true}`) {
		t.Errorf("git.status on the new worktree = %s", raw)
	}
}
