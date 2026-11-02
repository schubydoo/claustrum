package homeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWipesHomeDir pins the predicate: the home directory and its ancestors are
// refused, everything else is allowed.
//
// The allowed rows matter as much as the refused ones. A guard that rejected the
// whole home subtree would be "safe" and useless — extracting into ~/.claude is
// the daemon's own install path — so "under home" and "shares a name prefix with
// home" are asserted to pass, not merely left untested.
func TestWipesHomeDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "users", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"the home directory itself", home, true},
		{"home with a trailing separator", home + string(os.PathSeparator), true},
		{"home reached through a dot segment", filepath.Join(home, "."), true},
		{"home reached through a parent segment", filepath.Join(home, "x", ".."), true},
		{"the parent of home (/home, /Users)", filepath.Dir(home), true},
		{"a grandparent of home", base, true},
		// Allowed — refusing these would break real use.
		{"a directory under home", filepath.Join(home, ".claude", "1.0.0"), false},
		{"a sibling of home", filepath.Join(filepath.Dir(home), "other"), false},
		// The name-prefix trap: "someonex" contains "someone" as a string prefix
		// but holds nothing. A containment check written with plain HasPrefix and
		// no separator would refuse this.
		{"a sibling whose name extends home's", home + "x", false},
		{"an unrelated absolute path", filepath.Join(base, "tmp", "out"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wipesHomeDir(tc.path); got != tc.want {
				t.Errorf("wipesHomeDir(%q) = %v, want %v (home = %q)", tc.path, got, tc.want, home)
			}
		})
	}
}

// A daemon whose home cannot be resolved keeps its pre-guard behaviour rather
// than refusing everything: with nothing to compare against, an empty home would
// otherwise be a prefix of every path on the system and both methods would
// reject all input.
//
// The assertion is on a path the guard WOULD refuse with a home resolved, and
// the precondition proves it. An earlier version asserted on an unrelated
// t.TempDir() path, which passes whether or not the fail-open branch exists —
// delete that branch and `home` becomes "", Clean("") is ".", and "." is not a
// prefix of the path either, so the test answered true by a different route and
// could not fail. Raised in review on #231.
func TestWipesHomeDirWithNoResolvableHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "users", "someone")
	t.Setenv(homeEnvVar(), home)
	if !wipesHomeDir(home) {
		t.Fatal("precondition: the guard must refuse the home directory itself")
	}

	t.Setenv(homeEnvVar(), "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this platform resolves a home directory without the environment variable")
	}
	if wipesHomeDir(home) {
		t.Error("wipesHomeDir refused a path with no home to protect")
	}
}

// A RELATIVE path must be judged by what it resolves to, not by its spelling.
// filepath.Clean leaves it relative, so before the filepath.Abs call in
// wipesHomeDir every relative input compared unequal to an absolute home and the
// guard answered false — including ".." from a daemon whose working directory is
// the home directory, which is os.RemoveAll on home's parent.
//
// Before 7d193f89, git.worktree_remove was where this bit: it had no IsAbs gate,
// and its old fallback delete resolved worktreePath against the daemon's working
// directory. Raised in review on #231.
func TestWipesHomeDirResolvesRelativePaths(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "users", "someone")
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	for _, tc := range []struct {
		name, cwd, path string
		want            bool
	}{
		// The daemon's cwd IS the home directory — the shape a daemon started
		// from a user's home has.
		{"dot from home", home, ".", true},
		{"dotdot from home", home, "..", true},
		{"dotdot from a subdirectory of home", filepath.Join(home, "sub"), "..", true},
		{"a named child of home", home, "sub", false},
		// An ancestor reached relatively is still an ancestor.
		{"dotdot twice, landing on home's grandparent", filepath.Join(home, "sub"), "../../..", true},
		// From elsewhere the same spellings resolve to unrelated directories and
		// must NOT be refused — otherwise the guard rejects ordinary relative use.
		// These rows are what stops "resolve it" from becoming "refuse everything".
		{"dot in an unrelated directory", filepath.Join(base, "elsewhere"), ".", false},
		{"dotdot from an unrelated subdirectory", filepath.Join(base, "elsewhere", "deep"), "..", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(tc.cwd)
			if got := wipesHomeDir(tc.path); got != tc.want {
				t.Errorf("wipesHomeDir(%q) with cwd %q = %v, want %v (home = %q)",
					tc.path, tc.cwd, got, tc.want, home)
			}
		})
	}
}

// The case-insensitive comparison is Windows' semantics, but the branch is dead
// code on every other leg — so it is exercised through the pathsAreCaseInsensitive
// seam rather than left unasserted until a Windows run.
func TestWipesHomeDirCaseFolding(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Users", "Someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	shouted := strings.ToUpper(home)

	if wipesHomeDir(shouted) && !pathsAreCaseInsensitive {
		t.Errorf("case-sensitive platform refused %q, a genuinely different directory", shouted)
	}

	old := pathsAreCaseInsensitive
	pathsAreCaseInsensitive = true
	t.Cleanup(func() { pathsAreCaseInsensitive = old })

	if !wipesHomeDir(shouted) {
		t.Errorf("wipesHomeDir(%q) = false with case-insensitive paths, want true — "+
			"a caller must not walk around the guard by shouting (home = %q)", shouted, home)
	}
	if !wipesHomeDir(strings.ToUpper(filepath.Dir(home))) {
		t.Error("an ancestor of home spelled in another case was not refused")
	}
	if wipesHomeDir(filepath.Join(shouted, "SUB")) {
		t.Error("a directory under home was refused; descendants must stay allowed")
	}
}
