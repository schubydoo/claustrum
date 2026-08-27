//go:build unix

package main

import (
	"path/filepath"
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
