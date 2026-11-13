package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// unusableRow stages one `.git` below tmp. It returns baseRepo and the folder whose
// `.git` the refusal names, or "" for the not_a_repo answer.
type unusableRow struct {
	name  string
	stage func(t *testing.T, tmp string) (base, holder string)
}

// plainFolder makes the folder tmp/B with one file and returns it.
func plainFolder(t *testing.T, tmp string) string {
	t.Helper()
	b := filepath.Join(tmp, "B")
	writeFile(t, filepath.Join(b, "README"), "x\n", 0o644)
	return b
}

// unusableRows are the states of 4c111e86, each with the systems of its rows. They
// end with a `.git` file that names a regular file (claustrum's own) and a plain
// folder, which keeps not_a_repo.
var unusableRows = []unusableRow{
	// Linux, macOS and Windows.
	{"empty .git folder", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		makeDir(t, filepath.Join(b, ".git"))
		return b, b
	}},
	// Linux, macOS and Windows.
	{"no HEAD", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		makeDir(t, filepath.Join(b, ".git", "objects"))
		makeDir(t, filepath.Join(b, ".git", "refs"))
		return b, b
	}},
	// Linux and Windows.
	{"garbage HEAD", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		makeDir(t, filepath.Join(b, ".git", "objects"))
		makeDir(t, filepath.Join(b, ".git", "refs"))
		writeFile(t, filepath.Join(b, ".git", "HEAD"), "garbage\n", 0o644)
		return b, b
	}},
	// Linux.
	{"junk only", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		writeFile(t, filepath.Join(b, ".git", "junk", "x"), "x\n", 0o644)
		return b, b
	}},
	// Linux, macOS and Windows.
	{"nested empty .git below a good repository", func(t *testing.T, tmp string) (string, string) {
		r := filepath.Join(tmp, "R")
		initTrustMain(t, r)
		sub := filepath.Join(r, "sub")
		makeDir(t, filepath.Join(sub, ".git"))
		return sub, sub
	}},
	// Linux, macOS and Windows.
	{".git file with no gitdir line", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		writeFile(t, filepath.Join(b, ".git"), "junk\n", 0o644)
		return b, b
	}},
	// Windows only, as the next two rows (gitFileNowhereRows).
	{".git file with an empty gitdir", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		writeFile(t, filepath.Join(b, ".git"), "gitdir: \n", 0o644)
		return b, b
	}},
	{".git file to a missing path", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		writeFile(t, filepath.Join(b, ".git"), "gitdir: nowhere-dir\n", 0o644)
		return b, b
	}},
	{".git file to an empty folder", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		makeDir(t, filepath.Join(b, "notgit"))
		writeFile(t, filepath.Join(b, ".git"), "gitdir: notgit\n", 0o644)
		return b, b
	}},
	// claustrum's own, not measured. Linux and macOS only (TestCreateRefusesAnUnusableGit).
	{".git file to a regular file", func(t *testing.T, tmp string) (string, string) {
		b := plainFolder(t, tmp)
		writeFile(t, filepath.Join(b, "notgit"), "x\n", 0o644)
		writeFile(t, filepath.Join(b, ".git"), "gitdir: notgit\n", 0o644)
		return b, b
	}},
	{"plain folder", func(t *testing.T, tmp string) (string, string) {
		return plainFolder(t, tmp), ""
	}},
}

// gitFileNowhereRows are the rows whose frame depends on the text of git. 4c111e86 is
// measured there with git 2.55 on Windows only. With the text of git 2.56
// (saysGitFileNowhere) git runs first. A missing path then follows the 5fd08069 rows of
// TestGitFileNowhereFourMethodsStub. The frame of an empty folder there is from the
// code, not measured. TestCreateUnusableGitFileStub pins both texts.
var gitFileNowhereRows = map[string]bool{
	".git file to a missing path":  true,
	".git file to an empty folder": true,
}

// hostGitSaysGitFileNowhere reports whether the git of this host answers a `.git`
// file to a missing path with the text of git 2.56.
func hostGitSaysGitFileNowhere(t *testing.T) bool {
	t.Helper()
	d := t.TempDir()
	writeFile(t, filepath.Join(d, ".git"), "gitdir: nowhere-dir\n", 0o644)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C", "GIT_CEILING_DIRECTORIES="+filepath.Dir(d))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return strings.HasPrefix(stderr.String(), gitFileNowhereHead)
}

func makeDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// A create on a folder whose `.git` git cannot use answers worktree_add_failed with
// the text that names that `.git`, where older builds answered not_a_repo. Nothing is
// made (4c111e86, with the systems of each row in unusableRows). Mutations: answer
// not_a_repo in notARepoResult, or pass a `.git` file that names a regular file in
// gitFileUnusable.
//
// The rows of gitFileNowhereRows run here only with a git that has the older text. The
// regular-file row runs on Linux and macOS only: on Windows the daemon's own config
// read refuses first (from the code).
func TestCreateRefusesAnUnusableGit(t *testing.T) {
	requireGit(t)
	newGitText := hostGitSaysGitFileNowhere(t)
	for _, row := range unusableRows {
		t.Run(row.name, func(t *testing.T) {
			if gitFileNowhereRows[row.name] && newGitText {
				t.Skip("this git has the text of git 2.56, and TestCreateUnusableGitFileStub pins the frame")
			}
			if row.name == ".git file to a regular file" && runtime.GOOS == "windows" {
				t.Skip("on Windows the daemon's own config read refuses first")
			}
			tmp := realTempDir(t)
			requireTempOutsideCheckout(t, tmp)
			base, holder := row.stage(t, tmp)
			want := notRepoCreate
			if holder != "" {
				want = unusableCreate(t, holder)
			}
			r, _ := create(t, base)
			wantResult(t, "create", r, want)
			if exists(filepath.Join(base, ".claude")) {
				t.Errorf("the create made %s", filepath.Join(base, ".claude"))
			}
		})
	}
}

// A daemon GIT_DIR or GIT_COMMON_DIR turns the unusable-git test off, so the create
// keeps not_a_repo (claustrum's own, not measured). The test calls unusableDotGit, so
// no git call runs with the variable. Mutations: drop the GIT_DIR test or
// daemonCommonDirSet in unusableDotGit.
func TestUnusableGitOffWithDaemonGitDir(t *testing.T) {
	tmp := realTempDir(t)
	b := plainFolder(t, tmp)
	makeDir(t, filepath.Join(b, ".git"))
	if got, want := unusableDotGit(b), filepath.Join(b, ".git"); got != want {
		t.Fatalf("with no variable: unusableDotGit = %q, want %q", got, want)
	}
	for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, filepath.Join(tmp, "elsewhere"))
			if got := unusableDotGit(b); got != "" {
				t.Errorf("with a daemon %s: unusableDotGit = %q, want no name", name, got)
			}
		})
	}
}

// The rows of gitFileNowhereRows with a stub in place of git that fails each listing
// with one of the two texts, so no git 2.56 is needed. With the older text the create
// answers the 4c111e86 text (measured with git 2.55 on Windows). With the text of git
// 2.56 git runs first: a missing path gets the error of the call about the repository,
// and an empty folder gets the listing refusal (from the code, not measured). Mutation:
// answer not_a_repo in
// notARepoResult.
func TestCreateUnusableGitFileStub(t *testing.T) {
	for _, row := range unusableRows {
		if !gitFileNowhereRows[row.name] {
			continue
		}
		for _, newText := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, git 2.56 %v", row.name, newText), func(t *testing.T) {
				// The git stub of the listing fixture, with the deps of trustCall.
				f := newListingFixture(t)
				f.d = newTestGitDeps()
				tmp := realTempDir(t)
				requireTempOutsideCheckout(t, tmp)
				base, holder := row.stage(t, tmp)
				rb, err := filepath.EvalSymlinks(holder)
				if err != nil {
					t.Fatal(err)
				}
				text := "fatal: gitfile does not point to a valid repository: " + filepath.ToSlash(filepath.Join(rb, ".git"))
				want := resultOf(unusableCreate(t, holder))
				if !newText {
					text = "fatal: not a git repository: " + filepath.ToSlash(filepath.Join(rb, "nowhere-dir"))
				} else if row.name == ".git file to a missing path" {
					want = resultFrame(t, worktreeResult{Success: false, ErrorCode: "worktree_add_failed",
						Error: fmt.Sprintf("git could not check this repository on the remote machine: at %q: %s", rb, text)})
				} else {
					want = resultFrame(t, worktreeResult{Success: false, ErrorCode: "worktree_add_failed",
						Error: "config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: " + text})
				}
				stubListing(t, 128, "")
				stubStderr(t, text+"\n", false)
				params := map[string]any{"baseRepo": base, "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1"), "branchName": "w1"}
				if got, _ := f.call(t, "git.worktree_create", params); got != want {
					t.Errorf("create = %s\nwant %s", got, want)
				}
				if exists(filepath.Join(base, ".claude")) {
					t.Errorf("the create made %s", filepath.Join(base, ".claude"))
				}
			})
		}
	}
}
