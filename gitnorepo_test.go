package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the "no repository" verdict of a failed listing (claustrum's own, not
// measured). A stub stands in for git and fails the listing with a given
// exit code and text, so the verdict comes from the folders alone.

const enGitText = "fatal: not a git repository (or any of the parent directories): .git"

// noRepoTree is a folder tree with a ceiling above it. The ceiling keeps the search
// away from any repository above the temporary folder. base is the ceiling.
func noRepoTree(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	installGitSlowStub(t, realGit)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", base)
	t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", "")
	return base
}

// mkGitDir makes the shape of a git dir in d with the given HEAD text.
func mkGitDir(t *testing.T, d, head string) {
	t.Helper()
	for _, n := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(d, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(d, "HEAD"), head, 0o644)
}

// mkdir makes d and returns it.
func mkdir(t *testing.T, d string) string {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// verdictOf runs the stub listing in dir and returns saysNoRepository. env is the
// environment of the call: the process environment without GIT_DIR, then extra.
func verdictOf(t *testing.T, code int, stderr, dir, gitDir string, extra ...string) bool {
	t.Helper()
	stubListing(t, code, stderr)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") {
			env = append(env, kv)
		}
	}
	r := runListing(t.Context(), dir, gitDir, append(env, extra...))
	if r.err == nil {
		t.Fatalf("the stub listing in %s did not fail", dir)
	}
	return r.saysNoRepository()
}

// The verdict runs only for exit code 128 and a call that names no git dir. The
// text plays no part. Mutations: drop the exit code test, drop the GIT_DIR= test, drop
// the --git-dir= test, or test the text of git again.
func TestNoRepoVerdictWhen(t *testing.T) {
	base := noRepoTree(t)
	plain := mkdir(t, filepath.Join(base, "plain"))
	for _, tc := range []struct {
		name, stderr string
		code         int
		gitDir       string
		extra        []string
		want         bool
	}{
		{"exit 128, English text", enGitText, 128, "", nil, true},
		{"exit 128, another language", "fatal: kein Git-Repository", 128, "", nil, true},
		{"exit 128, no text", "", 128, "", nil, true},
		{"exit 1", enGitText, 1, "", nil, false},
		{"exit 2", enGitText, 2, "", nil, false},
		{"GIT_DIR entry", enGitText, 128, "", []string{"GIT_DIR=" + os.DevNull}, false},
		{"empty GIT_DIR entry", enGitText, 128, "", []string{"GIT_DIR="}, false},
		// A byte compare with exact case, also on Windows.
		{"Git_Dir entry", enGitText, 128, "", []string{"Git_Dir=" + os.DevNull}, true},
		{"GIT_DIRS entry", enGitText, 128, "", []string{"GIT_DIRS=x"}, true},
		{"--git-dir= option", enGitText, 128, filepath.Join(base, "x"), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdictOf(t, tc.code, tc.stderr, plain, tc.gitDir, tc.extra...); got != tc.want {
				t.Errorf("saysNoRepository = %v, want %v", got, tc.want)
			}
		})
	}
}

// The search and the judgment of the git dir. Mutations: stop the search at
// no folder, skip the shape test, skip the HEAD test, skip the commondir test, follow
// a symlink at commondir, let a bad `.git` directory go on upward.
func TestNoRepoVerdictSearch(t *testing.T) {
	base := noRepoTree(t)
	good := "ref: refs/heads/main\n"
	// dir returns a fresh folder name below base.
	n := 0
	fresh := func() string {
		n++
		return mkdir(t, filepath.Join(base, "c"+string(rune('a'+n))))
	}
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, top string) string // returns the folder to list in
		want  bool                                  // true: "no repository"
	}{
		{"plain folder", func(t *testing.T, top string) string { return top }, true},
		{"repository above", func(t *testing.T, top string) string {
			mkGitDir(t, filepath.Join(top, ".git"), good)
			return mkdir(t, filepath.Join(top, "a", "b"))
		}, false},
		{"bare repository", func(t *testing.T, top string) string {
			mkGitDir(t, top, good)
			return top
		}, false},
		{"bad HEAD, no commondir, stops the search", func(t *testing.T, top string) string {
			mkGitDir(t, filepath.Join(top, "in", ".git"), "garbage\n")
			mkGitDir(t, filepath.Join(top, ".git"), good)
			return filepath.Join(mkdir(t, filepath.Join(top, "in")), ".")
		}, true},
		{"git dir without objects, no commondir, stops the search", func(t *testing.T, top string) string {
			mkdir(t, filepath.Join(top, ".git"))
			writeFile(t, filepath.Join(top, ".git", "HEAD"), good, 0o644)
			return top
		}, true},
		{"linked entry: good HEAD and commondir", func(t *testing.T, top string) string {
			mkdir(t, filepath.Join(top, ".git"))
			writeFile(t, filepath.Join(top, ".git", "HEAD"), good, 0o644)
			writeFile(t, filepath.Join(top, ".git", "commondir"), "../..\n", 0o644)
			return top
		}, false},
		{"linked entry: bad HEAD and commondir", func(t *testing.T, top string) string {
			mkdir(t, filepath.Join(top, ".git"))
			writeFile(t, filepath.Join(top, ".git", "HEAD"), "garbage\n", 0o644)
			writeFile(t, filepath.Join(top, ".git", "commondir"), "../..\n", 0o644)
			return top
		}, true},
		{"gitfile to a git dir", func(t *testing.T, top string) string {
			g := filepath.Join(top, "real")
			mkGitDir(t, g, good)
			writeFile(t, filepath.Join(top, "w", ".git"), "gitdir: "+g+"\n", 0o644)
			return filepath.Join(top, "w")
		}, false},
		{"gitfile to a folder with a HEAD only", func(t *testing.T, top string) string {
			g := mkdir(t, filepath.Join(top, "real"))
			writeFile(t, filepath.Join(g, "HEAD"), good, 0o644)
			writeFile(t, filepath.Join(top, "w", ".git"), "gitdir: "+g+"\n", 0o644)
			return filepath.Join(top, "w")
		}, true},
		{"gitfile to a folder with a HEAD and a commondir", func(t *testing.T, top string) string {
			g := mkdir(t, filepath.Join(top, "real"))
			writeFile(t, filepath.Join(g, "HEAD"), good, 0o644)
			writeFile(t, filepath.Join(g, "commondir"), "../x\n", 0o644)
			writeFile(t, filepath.Join(top, "w", ".git"), "gitdir: "+g+"\n", 0o644)
			return filepath.Join(top, "w")
		}, false},
		{"gitfile to nowhere", func(t *testing.T, top string) string {
			mkGitDir(t, filepath.Join(top, ".git"), good)
			writeFile(t, filepath.Join(top, "w", ".git"), "gitdir: "+filepath.Join(top, "gone")+"\n", 0o644)
			return filepath.Join(top, "w")
		}, true},
		{"gitfile without a target stops the search", func(t *testing.T, top string) string {
			mkGitDir(t, filepath.Join(top, ".git"), good)
			writeFile(t, filepath.Join(top, "w", ".git"), "text\n", 0o644)
			return filepath.Join(top, "w")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := fresh()
			dir := tc.build(t, top)
			if got := verdictOf(t, 128, enGitText, dir, ""); got != tc.want {
				t.Errorf("saysNoRepository = %v, want %v", got, tc.want)
			}
		})
	}
}

// A commondir that is a symlink counts as present, whatever it points to.
// Mutation: use os.Stat for the commondir test.
func TestNoRepoVerdictCommondirNotFollowed(t *testing.T) {
	base := noRepoTree(t)
	top := mkdir(t, filepath.Join(base, "t"))
	g := mkdir(t, filepath.Join(top, ".git"))
	writeFile(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n", 0o644)
	if err := os.Symlink(filepath.Join(base, "gone"), filepath.Join(g, "commondir")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if verdictOf(t, 128, enGitText, top, "") {
		t.Error("a dangling symlink at commondir gave \"no repository\", want the generic path")
	}
}

// The HEAD test, through the verdict. Mutations: accept `ref:` without refs/,
// accept 39 hex digits, accept a non-hex digit.
func TestNoRepoVerdictHead(t *testing.T) {
	base := noRepoTree(t)
	hex40 := strings.Repeat("aB3f", 10)
	for i, tc := range []struct {
		head string
		want bool // true: "no repository"
	}{
		{"ref: refs/heads/main\n", false},
		{"ref:\t\r\n refs/heads/main", false},
		{"ref: heads/main\n", true},
		{"ref:refs", true},
		{hex40 + "\n", false},
		{hex40[:39], true},
		{hex40[:39] + "g", true},
		{"", true},
		{"garbage\n", true},
	} {
		top := mkdir(t, filepath.Join(base, "h"+string(rune('a'+i))))
		mkGitDir(t, filepath.Join(top, ".git"), tc.head)
		if got := verdictOf(t, 128, enGitText, top, ""); got != tc.want {
			t.Errorf("HEAD %q: saysNoRepository = %v, want %v", tc.head, got, tc.want)
		}
	}
}

// A ceiling stops the search before its folder is tested. Only an absolute
// entry counts. The search reads the environment of the daemon, not that of the call.
// Mutations: ignore the ceilings, count a relative entry, test the ceiling folder, read
// the ceilings from the call.
func TestNoRepoVerdictCeilings(t *testing.T) {
	base := noRepoTree(t)
	repo := mkdir(t, filepath.Join(base, "repo"))
	mkGitDir(t, filepath.Join(repo, ".git"), "ref: refs/heads/main\n")
	sub := mkdir(t, filepath.Join(repo, "sub"))
	sep := string(os.PathListSeparator)
	for _, tc := range []struct {
		name, ceilings string
		want           bool
	}{
		{"none above the repository", base, false},
		{"the repository folder", repo, true},
		{"in a list", filepath.Join(base, "other") + sep + repo, true},
		{"after an empty entry", sep + repo, true},
		{"a relative entry", "repo", false},
		{"the start folder", sub, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_CEILING_DIRECTORIES", tc.ceilings)
			if got := verdictOf(t, 128, enGitText, sub, ""); got != tc.want {
				t.Errorf("saysNoRepository = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("ceiling only in the call", func(t *testing.T) {
		if got := verdictOf(t, 128, enGitText, sub, "", "GIT_CEILING_DIRECTORIES="+repo); got {
			t.Error("the ceiling of the call stopped the search, want the daemon's environment only")
		}
	})
}

// The values of GIT_DISCOVERY_ACROSS_FILESYSTEM that turn the device test
// off are the git true values. Mutation: take only the word "true".
func TestDiscoveryAcrossFilesystemValues(t *testing.T) {
	for v, want := range map[string]bool{
		"": false, "0": false, "no": false, "false": false, "off": false, "x": false,
		"1": true, "2": true, "true": true, "TRUE": true, "yes": true, "on": true,
	} {
		t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", v)
		if got := discoveryAcrossFilesystem(); got != want {
			t.Errorf("%q = %v, want %v", v, got, want)
		}
	}
}

// The verdict through failedListingCheck: the verdict gives noRepo with the stderr head,
// and the generic path gives the refusal with the exit status. Mutation: make the
// verdict return false.
func TestFailedListingCheckVerdict(t *testing.T) {
	base := noRepoTree(t)
	plain := mkdir(t, filepath.Join(base, "plain"))
	repo := mkdir(t, filepath.Join(base, "repo"))
	mkGitDir(t, filepath.Join(repo, ".git"), "ref: refs/heads/main\n")
	text := "  fatal: refused here\n"
	for _, tc := range []struct {
		name, dir string
		noRepo    bool
	}{{"no repository", plain, true}, {"repository", repo, false}} {
		t.Run(tc.name, func(t *testing.T) {
			stubListing(t, 128, text)
			r := runListing(t.Context(), tc.dir, "", os.Environ())
			c := failedListingCheck(gitUse{}, r, tc.dir, false)
			if c.noRepo != tc.noRepo {
				t.Fatalf("noRepo = %v, want %v (refusal %q)", c.noRepo, tc.noRepo, c.refusal)
			}
			if tc.noRepo {
				if c.noRepoText != "fatal: refused here" || c.refusal != "" {
					t.Errorf("noRepoText %q, refusal %q", c.noRepoText, c.refusal)
				}
				return
			}
			if want := hooksRefusalPrefix + "exit status 128: fatal: refused here"; c.refusal != want {
				t.Errorf("refusal = %q\nwant %q", c.refusal, want)
			}
		})
	}
}

// With the verdict "repository", `git version` runs, and a failing one
// gives the cannot-run text. Mutation: skip the version check on exit 128.
func TestFailedListingCheckGenericVersion(t *testing.T) {
	base := noRepoTree(t)
	repo := mkdir(t, filepath.Join(base, "repo"))
	mkGitDir(t, filepath.Join(repo, ".git"), "ref: refs/heads/main\n")
	stubListing(t, 128, enGitText)
	r := runListing(t.Context(), repo, "", os.Environ())
	slowGit(t, "version", "fail", 0, "no git", "")
	c := failedListingCheck(gitUse{}, r, repo, false)
	if c.noRepo || !strings.HasPrefix(c.refusal, gitCannotRunPrefix) {
		t.Errorf("noRepo %v, refusal %q, want the cannot-run text", c.noRepo, c.refusal)
	}
}

// In removeListingCheck: after exit 128 in a folder with no repository the remove
// runs the second listing and gives the "no repository" answer, and it runs no `git
// version` first. In a repository `git version` runs first, and a failing one gives
// the cannot-run text. Mutation: make the verdict return false.
func TestRemoveListingCheckNoRepository(t *testing.T) {
	base := noRepoTree(t)
	// The daemon pins GIT_DIR in a folder where its own walk finds no repository, and
	// then the verdict does not run. So the folder is below a repository that is a
	// ceiling: the walk of the daemon finds it, and the search of the verdict stops.
	repo := mkdir(t, filepath.Join(base, "repo"))
	mkGitDir(t, filepath.Join(repo, ".git"), "ref: refs/heads/main\n")
	plain := mkdir(t, filepath.Join(repo, "sub"))
	t.Setenv("GIT_CEILING_DIRECTORIES", repo)
	stubListing(t, 128, enGitText)
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "version")
	t.Setenv("CLAUSTRUM_GITSTUB_MODE2", "fail")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "128")
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "no git")
	c, relisted, final := removeListingCheck(gitUse{}, plain, false)
	if !c.noRepo || !relisted || !final {
		t.Errorf("plain folder: noRepo %v, relisted %v, final %v, want all true", c.noRepo, relisted, final)
	}
	c, _, final = removeListingCheck(gitUse{}, repo, false)
	if c.noRepo || !final || !strings.HasPrefix(c.refusal, gitCannotRunPrefix) {
		t.Errorf("repository: noRepo %v, final %v, refusal %q, want the cannot-run text", c.noRepo, final, c.refusal)
	}
}
