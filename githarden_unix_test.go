//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// wantHardenedStatusOSError asserts that hardenedGitStatus failed on ITS OWN
// pre-git step with the given errno, not on git. The fixtures here are empty
// directories, so git itself would also fail ("not a git repository", exit 128);
// a swallowed pre-git error therefore still yields SOME error, and only the errno
// tells the two apart.
func wantHardenedStatusOSError(t *testing.T, out string, err error, want error) {
	t.Helper()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Fatalf("hardenedGitStatus reached git (%v, stdout %q); want it to fail before git with %v", err, out, want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("hardenedGitStatus error = %v (stdout %q), want %v", err, out, want)
	}
}

// hardenedGitStatus builds its isolated gitdir with os.MkdirTemp before it touches
// git at all, so an unusable temp root fails the whole call. TMPDIR is the knob
// os.TempDir reads on unix, which is why this is not a cross-platform test.
func TestHardenedGitStatusTempDirFails(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	out, err := hardenedGitStatus(t.TempDir(), t.TempDir(), t.TempDir(), "status")
	wantHardenedStatusOSError(t, out, err, fs.ErrNotExist)
}

// The HEAD/index copy loop skips a file that is ABSENT (git rebuilds it) but
// propagates any other stat failure rather than running status with incomplete
// metadata. A self-referential symlink gives a stat that fails with ELOOP, not
// ENOENT — the non-skippable arm. Unix-only: it needs a symlink and the loop.
func TestHardenedGitStatusIndexStatFails(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.Symlink("index", filepath.Join(gitDir, "index")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	out, err := hardenedGitStatus(t.TempDir(), gitDir, t.TempDir(), "status")
	wantHardenedStatusOSError(t, out, err, syscall.ELOOP)
}

// Same rule one step later: the index STATS fine but cannot be read. A directory
// named `index` reads as EISDIR, which is not ENOENT, so it is propagated too.
func TestHardenedGitStatusIndexReadFails(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(gitDir, "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := hardenedGitStatus(t.TempDir(), gitDir, t.TempDir(), "status")
	wantHardenedStatusOSError(t, out, err, syscall.EISDIR)
}

// A directory that exists but cannot be entered (mode 0000) passes the os.Stat
// pre-check, so git itself fails to chdir with "cannot change to". That is still
// not a hostile config — git never reached a repo — so no refusal is raised.
func TestHostileConfigRefusalCannotChangeTo(t *testing.T) {
	requireGit(t)
	skipIfRoot(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if msg, bad := hostileConfigRefusal(dir); bad || msg != "" {
		t.Errorf("hostileConfigRefusal(unenterable dir) = (%q, %v), want (\"\", false)", msg, bad)
	}
}
