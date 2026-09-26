//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows forward slashes and backslashes are equal, and letter case is
// ignored. A `\\?\` prefix and a path without a drive letter stay different. Measured
// side by side against f6010b97 on a Windows 11 VM.
func TestGitDirTrustCommondirWindowsSpelling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(r trustRepo) string
		pass    bool
	}{
		{"forward slashes", func(r trustRepo) string { return filepath.ToSlash(filepath.Join(r.T, ".git")) }, true},
		{"backslashes", func(r trustRepo) string { return filepath.Join(r.T, ".git") }, true},
		{"relative with backslashes", func(trustRepo) string { return `..\..` }, true},
		{"lower-case drive letter", func(r trustRepo) string {
			p := filepath.Join(r.T, ".git")
			return strings.ToLower(p[:1]) + p[1:]
		}, true},
		{"all upper case", func(r trustRepo) string { return strings.ToUpper(filepath.Join(r.T, ".git")) }, true},
		{`\\?\ prefix`, func(r trustRepo) string { return `\\?\` + filepath.Join(r.T, ".git") }, false},
		{"no drive letter", func(r trustRepo) string { return filepath.Join(r.T, ".git")[len(filepath.VolumeName(r.T)):] }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.entry(), "commondir")
			writeFile(t, cd, tc.content(r)+"\n", 0o644)
			rep := info(t, r.TW)
			if tc.pass {
				wantResultPrefix(t, "info(TW)", rep, `{"isRepo":true`)
				return
			}
			wantRPCError(t, "info(TW)", rep, wantM2(cd, filepath.Join(r.T, ".git")))
		})
	}
}

// On Windows, with GIT_DIR=.git in the daemon's environment, a linked worktree's
// `.git` file is trusted as a git directory that is not an entry. Git runs with the
// common directory pinned to it. Git then fails on the entry the file
// names, and the method answers the hooks refusal with git's own error. Without the
// pin, git reads the worktree normally and answers isRepo:true. Measured against
// f6010b97 on a Windows 11 VM (E02).
func TestGitDirTrustRelativeGitDirOnLinkedWorktreeWindows(t *testing.T) {
	r := newTrustRepo(t)
	gitFile, err := os.ReadFile(filepath.Join(r.TW, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	entry := strings.TrimPrefix(strings.TrimSpace(string(gitFile)), "gitdir: ")
	t.Setenv("GIT_DIR", ".git")
	want := "config-defined hooks could not be pinned off; git not run: listing the configuration " +
		"in force: exit status 128: fatal: not a git repository: " + entry
	wantRPCError(t, "info(TW)", info(t, r.TW), want)
	wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), want)
	wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true`)
}

// On Windows an entry replaced by a regular file is refused as an entry
// without commondir (M3), where Linux and macOS answer no repository.
func TestGitDirTrustEntryIsAFileWindows(t *testing.T) {
	r := newTrustRepo(t)
	if err := os.RemoveAll(r.entry()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.entry(), "x", 0o644)
	wantRPCError(t, "info(TW)", info(t, r.TW), wantM3(r.entry()))
	wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), wantM3(r.entry()))
}

// shortPathName is the 8.3 spelling of p. The test skips when the volume makes no
// short names.
func shortPathName(t *testing.T, p string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("no short name for %s: %v", p, err)
	}
	s := windows.UTF16ToString(buf[:n])
	if !strings.Contains(s, "~") {
		t.Skipf("the volume makes no 8.3 names: %s", s)
	}
	return s
}

// A `.git` file or a daemon GIT_DIR can name the entry through 8.3 short names. That
// path is not an entry, because its parent is WORKTR~1 and not "worktrees". The short
// name is not expanded, so the entry's commondir gets M1 with the short path. Measured side by
// side against f6010b97 on a Windows 11 VM (rows K04 and K10).
func TestGitDirTrustShortNameEntryWindows(t *testing.T) {
	t.Run("gitfile", func(t *testing.T) {
		r := newTrustRepo(t)
		short := shortPathName(t, r.entry())
		writeFile(t, filepath.Join(r.TW, ".git"), "gitdir: "+short+"\n", 0o644)
		want := wantM1(filepath.Join(short, "commondir"))
		wantRPCError(t, "info(TW)", info(t, r.TW), want)
		wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), want)
	})
	t.Run("GIT_DIR", func(t *testing.T) {
		r := newTrustRepo(t)
		short := shortPathName(t, r.entry())
		t.Setenv("GIT_DIR", short)
		want := wantM1(filepath.Join(short, "commondir"))
		wantRPCError(t, "info(T)", info(t, r.T), want)
		wantRPCError(t, "info(TW)", info(t, r.TW), want)
	})
}

// resolveGitDirLinks keeps a path with no link in it exactly as spelled, short names
// included. It needs a real Windows volume, so it runs only on Windows.
func TestResolveGitDirLinksKeepsShortNamesWindows(t *testing.T) {
	r := newTrustRepo(t)
	short := shortPathName(t, r.entry())
	if got := resolveGitDirLinks(short); got != short {
		t.Errorf("resolveGitDirLinks(%q) = %q, want it unchanged", short, got)
	}
}

// The GIT_COMMON_DIR pin keeps a daemon GIT_DIR as given, short names included (row
// K09). A request directory named through short names is resolved, so the pin of the
// git directory the walk finds is the long path (row K02). Measured side by side
// against f6010b97 on a Windows 11 VM.
func TestGitDirTrustShortNamePinWindows(t *testing.T) {
	r := newTrustRepo(t)
	shortGit := shortPathName(t, filepath.Join(r.T, ".git"))
	t.Run("K09_GIT_DIR_short", func(t *testing.T) {
		t.Setenv("GIT_DIR", shortGit)
		got := commonDirPinEnv(r.T)
		if want := "GIT_COMMON_DIR=" + shortGit; len(got) != 1 || got[0] != want {
			t.Errorf("commonDirPinEnv = %q, want [%q]", got, want)
		}
	})
	t.Run("K02_request_dir_short", func(t *testing.T) {
		shortT := filepath.Dir(shortGit)
		got := commonDirPinEnv(shortT)
		if want := "GIT_COMMON_DIR=" + filepath.Join(r.T, ".git"); len(got) != 1 || !strings.EqualFold(got[0], want) {
			t.Errorf("commonDirPinEnv(%s) = %q, want [%q]", shortT, got, want)
		}
	})
}
