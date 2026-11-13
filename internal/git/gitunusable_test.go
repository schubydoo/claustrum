package git

import (
	"os"
	"path/filepath"
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
	// Windows only, as the next two rows.
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
	// claustrum's own, not measured.
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
func TestCreateRefusesAnUnusableGit(t *testing.T) {
	requireGit(t)
	for _, row := range unusableRows {
		t.Run(row.name, func(t *testing.T) {
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
