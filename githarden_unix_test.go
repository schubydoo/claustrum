//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// resolveUserExcludesFile resolves the user's global excludes as 7d193f89 does
// (observed via a git-argv trace): a configured absolute core.excludesFile wins, else
// the XDG or HOME default when it exists, else /dev/null. Each branch is exercised with
// a controlled environment. GIT_CONFIG_SYSTEM is neutralised so the host's own config
// cannot leak in. Unix-gated deliberately: the fixtures assert POSIX-absolute paths
// (/abs/ignore, /dev/null) that the resolver's filepath.IsAbs check only treats as
// absolute on POSIX — on Windows they are relative and the selected branch differs.
func TestResolveUserExcludesFile(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	t.Run("configured_absolute_wins", func(t *testing.T) {
		gc := filepath.Join(t.TempDir(), "gitconfig")
		writeFile(t, gc, "[core]\n\texcludesFile = /abs/ignore\n", 0o644)
		t.Setenv("GIT_CONFIG_GLOBAL", gc)
		if got := resolveUserExcludesFile(); got != "/abs/ignore" {
			t.Errorf("configured = %q, want /abs/ignore", got)
		}
	})

	t.Run("xdg_default_when_present", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		xdg := t.TempDir()
		writeFile(t, filepath.Join(xdg, "git", "ignore"), "*.x\n", 0o644)
		t.Setenv("XDG_CONFIG_HOME", xdg)
		if got, want := resolveUserExcludesFile(), filepath.Join(xdg, "git", "ignore"); got != want {
			t.Errorf("xdg = %q, want %q", got, want)
		}
	})

	t.Run("home_default_when_present", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("XDG_CONFIG_HOME", "")
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".config", "git", "ignore"), "*.y\n", 0o644)
		t.Setenv("HOME", home)
		if got, want := resolveUserExcludesFile(), filepath.Join(home, ".config", "git", "ignore"); got != want {
			t.Errorf("home = %q, want %q", got, want)
		}
	})

	t.Run("dev_null_when_none", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", t.TempDir()) // no .config/git/ignore inside
		if got := resolveUserExcludesFile(); got != "/dev/null" {
			t.Errorf("none = %q, want /dev/null", got)
		}
	})
}

// buildStatusGitDir makes its folder with os.MkdirTemp before anything else, so an
// unusable temp root fails the whole call. TMPDIR is what os.TempDir reads on unix.
func TestStatusGitDirTempDirFails(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	if _, err := buildStatusGitDir(statusEntry{dir: t.TempDir()}, t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("buildStatusGitDir with no temp root = %v, want %v", err, fs.ErrNotExist)
	}
}

// The copy leaves out a file that is absent, and returns any other failure. A
// symlink that names itself fails the open with ELOOP, not ENOENT.
func TestStatusGitDirIndexOpenFails(t *testing.T) {
	entry := t.TempDir()
	if err := os.Symlink("index", filepath.Join(entry, "index")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	tmp, err := buildStatusGitDir(statusEntry{dir: entry}, t.TempDir())
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("buildStatusGitDir with a looping index = %v, want %v", err, syscall.ELOOP)
	}
}

// The same rule one step later: the index opens and is not a regular file.
func TestStatusGitDirIndexNotRegular(t *testing.T) {
	entry := t.TempDir()
	if err := os.Mkdir(filepath.Join(entry, "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := buildStatusGitDir(statusEntry{dir: entry}, t.TempDir())
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("buildStatusGitDir with a folder named index = %v, want %v", err, errNotRegularFile)
	}
}

// A directory that exists but cannot be entered (mode 0000) passes the os.Stat
// pre-check, so git cannot start in it: the start fails with a *fs.PathError. That
// is still not a hostile config — git never reached a repo — so no refusal is raised.
func TestHostileConfigRefusalCannotChangeTo(t *testing.T) {
	requireGit(t)
	skipIfRoot(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if c := hostileConfigRefusal(dir, false); c.refused() {
		t.Errorf("hostileConfigRefusal(unenterable dir) = %+v, want no refusal", c)
	}
}

// A git that cannot start in any directory is a different failure from an
// unenterable directory. The start also fails with a *fs.PathError, but dir can be
// entered, so the refusal stays. The fixture is an executable git with no valid
// format, so exec fails with ENOEXEC after the chdir. `git version` then fails to
// start too, so the text says git cannot run (89cb6289, row L12 on Linux and macOS
// VMs). Mutation: treating the start error as "no repository" gives no refusal.
func TestHostileConfigRefusalGitCannotStart(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("not a program\x00\x01"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	c := hostileConfigRefusal(t.TempDir(), false)
	want := "git cannot run on this host; git not run: fork/exec " + filepath.Join(bin, "git") + ": "
	if !strings.HasPrefix(c.refusal, want) {
		t.Errorf("hostileConfigRefusal(git cannot start) = %+v, want a refusal starting %q", c, want)
	}
}
