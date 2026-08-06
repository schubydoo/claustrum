package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// homeEnvVar is the variable os.UserHomeDir actually reads on this platform.
// Setting the wrong one leaves the REAL home in place, which would make every
// assertion below vacuous — and, in the unfixed tree these tests are written to
// fail against, would point a live os.RemoveAll at the developer's home
// directory. That is the exact accident this file exists to prevent, so the
// switch is not a portability nicety.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

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
// git.worktree_remove is where this bites: it has no IsAbs gate, and PROTOCOL.md
// measures that its fallback delete resolves worktreePath against the daemon's
// working directory. Raised in review on #231.
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

// files.extract_tar must refuse a destDir that is (or contains) the home
// directory, BEFORE any filesystem effect.
//
// Safe to run against the unfixed tree, twice over: the wipe is stubbed, so the
// os.RemoveAll cannot fire, and the home the rows name is a t.TempDir() rather
// than the developer's real one. That is this repo's rule for guard tests — a
// test whose failure mode is destroying the machine is not a test — and it is
// also how the incident this guards against would have been caught: the fuzzer
// that fired it ran unstubbed, against a live daemon, with the real home
// reachable.
//
// The archive is DELIBERATELY nonexistent on every refused row. The guard runs
// before the archive is opened, so with it holding the row is unaffected; if it
// regresses, extraction fails on the missing archive instead of unpacking into a
// home directory. Same construction as the "root destDir" row in
// TestFilesExtractTarErrors, for the same reason.
func TestFilesExtractTarRefusesHomeDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(path string) error { wiped = path; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	archive := filepath.Join(t.TempDir(), "no-such.tar.gz")

	for _, tc := range []struct {
		name, destDir string
	}{
		// The incident's literal input. It also proves the wiring end to end:
		// nothing in filesExtractTar mentions "~", so this row only refuses if
		// bindParams' expansion and the guard meet.
		{"bare tilde", "~"},
		{"the home directory spelled out", home},
		{"the parent of home", filepath.Dir(home)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wiped = ""
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
				map[string]any{"archivePath": archive, "destDir": tc.destDir}))
			if wiped != "" {
				t.Errorf("destDir %q was refused but the wipe still ran on %q — the guard "+
					"must reject before any filesystem effect", tc.destDir, wiped)
			}
			if !strings.Contains(got, "destDir must not be or contain the home directory") {
				t.Errorf("extract into %q = %s, want the home-directory refusal", tc.destDir, got)
			}
		})
	}

	// The control. A directory UNDER home must still be accepted, or the guard has
	// broken the daemon's own install path instead of protecting it. It gets past
	// the guard and fails on the missing archive, which is the proof it was not
	// refused.
	t.Run("a directory under home is accepted", func(t *testing.T) {
		wiped = ""
		got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
			map[string]any{"archivePath": archive, "destDir": filepath.Join(home, ".claude", "1.0.0")}))
		if strings.Contains(got, "home directory") {
			t.Errorf("extract under home = %s, want it accepted — ~/... is the primary real use", got)
		}
		if !strings.Contains(got, "open archive") {
			t.Errorf("extract under home = %s, want it to reach the archive open", got)
		}
	})
}

// git.worktree_remove must refuse a worktreePath that is (or contains) the home
// directory.
//
// This is the SECOND caller-supplied path handed to os.RemoveAll, found while
// fixing the first: when `git worktree remove --force` fails — and it fails on a
// home directory, which is not a worktree — the daemon deletes worktreePath
// itself. `~` expands there too.
//
// Unlike the extract_tar wipe there is no seam on that delete, so safety here
// comes from the home being a t.TempDir(): against the unfixed tree this test
// fails by deleting a temporary directory, never a real one. KEEP.txt is the
// assertion, because it is the thing that would actually be gone.
// A TABLE, not a single row, and the ancestor row is the load-bearing one: on
// this method wipesHomeDir is the ONLY gate, where filesExtractTar still has
// IsAbs and isFilesystemRoot behind it. If the predicate ever narrows to "the
// home directory itself", extract_tar's ancestor case is caught by its root
// check and this one is not. Raised in review on #231.
func TestWorktreeRemoveRefusesHomeDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	s := newTestServer(t)

	for _, tc := range []struct {
		name, worktreePath string
	}{
		// The bare tilde the fuzzer sent — also the end-to-end proof, since
		// nothing in gitWorktreeRemove mentions "~".
		{"bare tilde", "~"},
		{"the home directory spelled out", home},
		{"the parent of home", filepath.Dir(home)},
		// Relative, resolved against the daemon's working directory — the spelling
		// the guard missed entirely before filepath.Abs was added.
		//
		// ".." is the one that actually destroys: os.RemoveAll special-cases a
		// trailing "." and returns EINVAL, so "." could not have deleted anything
		// even unguarded, while ".." from a daemon sitting in the home directory
		// resolves to home's PARENT and takes home with it. Both rows are kept —
		// "." pins that the refusal is the guard's doing and not an os.RemoveAll
		// accident.
		{"dot, with the daemon's cwd in home", "."},
		{"dotdot, with the daemon's cwd in home", ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh marker per row: each row must independently prove nothing was
			// deleted, and the previous row must not be able to satisfy it.
			keep := filepath.Join(home, "KEEP.txt")
			if err := os.WriteFile(keep, []byte("must survive"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(tc.worktreePath) && tc.worktreePath != "~" {
				t.Chdir(home)
			}
			// baseRepo is a real directory but not a repository, so git fails and the
			// destructive fallback is the arm under test.
			got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
				map[string]any{"baseRepo": t.TempDir(), "worktreePath": tc.worktreePath}))

			if _, err := os.Stat(keep); err != nil {
				t.Errorf("worktreePath %q deleted the home directory: %v", tc.worktreePath, err)
			}
			if !strings.Contains(got, "worktreePath must not be or contain the home directory") {
				t.Errorf("worktree_remove %q = %s, want the home-directory refusal", tc.worktreePath, got)
			}
			if strings.Contains(got, `"success":true`) {
				t.Errorf("worktree_remove %q = %s, want success:false — nothing was removed", tc.worktreePath, got)
			}
		})
	}
}

// An OMITTED worktreePath must keep its pre-guard reply, and this row exists
// because the guard got it wrong once.
//
// gitWorktreeRemove has no required-param check — unlike filesExtractTar, which
// answers "archivePath and destDir are required" long before the guard runs. So
// an empty worktreePath reaches wipesHomeDir, where filepath.Abs("") resolves to
// the daemon's working directory. On a daemon started in the user's home — which
// is what an SSH-launched daemon inherits, and the same premise the ".." rows
// above rest on — that equals home and the guard refused.
//
// Refusing it is wrong on both counts: os.RemoveAll("") is a documented no-op
// returning nil, so nothing was ever going to be deleted, and the refusal also
// skipped the branchName delete that the reference still performs. The frame it
// produced varied with the daemon's cwd, which no golden can observe because the
// harness runs from a temp dir. Raised in review on #232.
func TestWorktreeRemoveEmptyPathIsNotRefused(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	// The cwd is what makes this bite: an empty path resolves to it.
	t.Chdir(home)

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": t.TempDir(), "branchName": "b1"}))

	if strings.Contains(got, "home directory") {
		t.Errorf("worktree_remove with no worktreePath = %s, want the pre-guard reply — "+
			"os.RemoveAll(\"\") is a no-op, so there is nothing here to guard", got)
	}
	if !strings.Contains(got, `"success":true`) {
		t.Errorf("worktree_remove with no worktreePath = %s, want the bare success:true", got)
	}
}
