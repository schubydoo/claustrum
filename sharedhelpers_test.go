package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// homeEnvVar is the variable os.UserHomeDir actually reads on this platform.
// Setting the wrong one leaves the REAL home in place, which would make the
// assertions of TestWorktreeRemoveRefusesHomeDir vacuous. In the unfixed tree
// that test is written to fail against, it would also point a live os.RemoveAll
// at the developer's home directory. That is the accident that
// worktree_homeguard_test.go exists to prevent, so the switch is not a
// portability nicety.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// treeOf lists a directory's contents relative to root, excluding .git, so a
// copied worktree can be compared as a whole rather than file by file.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // the root itself is not an entry
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// isolateGitConfig points the git commands the DAEMON runs at an empty global and
// system config, so a fixture's ignore state is decided by its own `.gitignore`
// alone.
//
// runGit already isolates the commands the TEST runs, but hardenedGitEnv builds on
// os.Environ(), so the daemon-side `--exclude-standard` reads whatever global
// excludes the machine has. That is correct in production and reproduces the
// reference. In a test it makes the result depend on the machine: a developer
// whose global ignore names `.claude` silently turns a "not ignored" fixture into
// an ignored one, and the expected tree changes under them.
//
// GIT_CONFIG_GLOBAL alone is NOT enough, which is the trap this helper exists to
// close. The user's ignore file is not read through git config at all: when
// core.excludesFile is unset the resolver looks up $XDG_CONFIG_HOME/git/ignore when
// XDG_CONFIG_HOME is absolute, and <home>/.config/git/ignore otherwise. So both of
// those have to move as well.
//
// ⚠️ Moving HOME is a no-op on Windows: os.UserHomeDir reads USERPROFILE there, not
// HOME. With an absolute XDG dir the resolver never falls back to the home folder,
// and an empty XDG dir gives the null device. The empty ignore file that this
// helper creates under the temp XDG dir is belt and braces: it makes the value a
// real file on every OS. An older resolver fell back to the home folder, and there
// the file was needed.
//
// The environment is only half of it. userExcludesFile keeps the configured value
// for the life of the process, and hardenedProfileArgs injects it as
// `-c core.excludesFile=`. So whichever test runs FIRST decides the value for
// every later test, and the environment this one sets arrives too late.
//
// That is what made these tests pass alone and fail in the suite: a socket test
// runs a git command first, the cache holds the machine's real ignore file, and a
// `.claude` line in it then ignores `.claude/` in every later fixture. Resetting
// that cache is what makes the isolation actually hold.
func isolateGitConfig(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	writeFile(t, filepath.Join(empty, "git", "ignore"), "", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	resetUserExcludesCache(t)
}

func eqTree(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
		}
	}
}
