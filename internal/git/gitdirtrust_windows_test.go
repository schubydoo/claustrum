//go:build windows

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows forward slashes and backslashes are equal, and letter case is
// ignored. A `\\?\` prefix and a path without a drive letter stay different. Measured
// side by side against f6010b97 on Windows 11.
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

// replyLines are the replies of rs, one line each.
func replyLines(rs ...rpcReply) []string {
	var out []string
	for _, r := range rs {
		line := string(r.Result)
		if r.Error != nil {
			line = fmt.Sprintf("error %d %s", r.Error.Code, r.Error.Message)
		}
		out = append(out, line)
	}
	return out
}

// On Windows the requests ignore a relative daemon GIT_DIR=.git with no
// "shadowDirOff":true (daemonVarsKept). git.info, git.status and
// git.list_branches on a linked worktree and on its repository answer as with no
// GIT_DIR. 87375cc7 answers git.info on a linked worktree so (measured on
// Windows). Mutation: in gitDirTrustFor drop the walk for a verdict that is not
// trusted.
func TestGitDirTrustRelativeGitDirOnLinkedWorktreeWindows(t *testing.T) {
	r := newTrustRepo(t)
	replies := func() []string {
		return replyLines(info(t, r.TW), status(t, r.TW, r.TW), listBranches(t, r.TW), info(t, r.T), listBranches(t, r.T))
	}
	want := replies()
	if !strings.HasPrefix(want[0], `{"isRepo":true`) {
		t.Fatalf("info(TW) with no GIT_DIR = %s", want[0])
	}
	t.Setenv("GIT_DIR", ".git")
	if got := replies(); !slices.Equal(got, want) {
		t.Errorf("replies = %q\nwant %q", got, want)
	}
}

// The state of the test above on git.worktree_remove from baseRepo TW, in the state
// shadow. The remove deletes what it deletes with no GIT_DIR: the worktree folder
// and its branch. 87375cc7 removes a worktree under a relative GIT_DIR as with
// none (measured on Windows). Mutation: in gitDirTrustFor drop the walk for a
// verdict that is not trusted.
func TestGitDirTrustRelativeGitDirRemoveWindows(t *testing.T) {
	run := func(t *testing.T, withGitDir bool) string {
		r := newTrustRepo(t)
		if withGitDir {
			t.Setenv("GIT_DIR", ".git")
		}
		d, _ := newSeededShadowDeps(t)
		got := trustCallOn(t, d, "git.worktree_remove", map[string]any{"baseRepo": r.TW, "worktreePath": r.SWW, "branchName": "s1"})
		return fmt.Sprintf("%s folder %v branch %v", replyLines(got)[0], exists(r.SWW), branchExists(t, r.T, "s1"))
	}
	var want, got string
	t.Run("no GIT_DIR", func(t *testing.T) { want = run(t, false) })
	t.Run("GIT_DIR", func(t *testing.T) { got = run(t, true) })
	if !strings.HasPrefix(want, `{"success":true`) {
		t.Fatalf("remove with no GIT_DIR = %s", want)
	}
	if got != want {
		t.Errorf("remove = %s\nwant %s", got, want)
	}
}

// A daemon GIT_DIR that names a regular file is ignored in the state shadow.
// git.info and git.list_branches in T and git.info in TW answer as with no
// GIT_DIR. 87375cc7 serves the repository so (measured on Windows). Mutation: in
// gitDirTrustFor drop the walk for a verdict that is not trusted.
func TestGitDirTrustDaemonGitDirPlainFileWindows(t *testing.T) {
	r := newTrustRepo(t)
	f := filepath.Join(r.base, "gitdir-file")
	writeFile(t, f, "x", 0o644)
	replies := func() []string {
		d, _ := newSeededShadowDeps(t)
		return replyLines(trustCallOn(t, d, "git.info", map[string]any{"path": r.T}),
			trustCallOn(t, d, "git.list_branches", map[string]any{"path": r.T}),
			trustCallOn(t, d, "git.info", map[string]any{"path": r.TW}))
	}
	want := replies()
	t.Setenv("GIT_DIR", f)
	if got := replies(); !slices.Equal(got, want) {
		t.Errorf("replies = %q\nwant %q", got, want)
	}
}

// On Windows an entry replaced by a regular file is refused as an entry
// without commondir (M3), where Linux and macOS answer no repository. 5fd08069
// answers the same text (measured on Windows).
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
// name is not expanded, so the entry's commondir counts as stray, and its content "../.."
// is refused with the short path. That answer follows from the stray-commondir rule of
// 89cb6289 and is not measured on it. Measured side by
// side against f6010b97 on Windows 11.
func TestGitDirTrustShortNameEntryWindows(t *testing.T) {
	t.Run("gitfile", func(t *testing.T) {
		r := newTrustRepo(t)
		short := shortPathName(t, r.entry())
		writeFile(t, filepath.Join(r.TW, ".git"), "gitdir: "+short+"\n", 0o644)
		want := wantTR(filepath.Join(short, "commondir"), "../..")
		wantRPCError(t, "info(TW)", info(t, r.TW), want)
		wantRPCError(t, "list_branches(TW)", listBranches(t, r.TW), want)
	})
	t.Run("GIT_DIR", func(t *testing.T) {
		r := newTrustRepo(t)
		short := shortPathName(t, r.entry())
		t.Setenv("GIT_DIR", short)
		want := wantTR(filepath.Join(short, "commondir"), "../..")
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

// The GIT_COMMON_DIR pin keeps a daemon GIT_DIR as given, short names included
// (measured). A request directory named through short names is resolved, so the pin
// of the git directory the walk finds is the long path (measured). Measured side by
// side against f6010b97 on Windows 11.
func TestGitDirTrustShortNamePinWindows(t *testing.T) {
	r := newTrustRepo(t)
	shortGit := shortPathName(t, filepath.Join(r.T, ".git"))
	t.Run("GIT_DIR_short", func(t *testing.T) {
		t.Setenv("GIT_DIR", shortGit)
		got := commonDirPinEnv(r.T)
		if want := "GIT_COMMON_DIR=" + shortGit; len(got) != 1 || got[0] != want {
			t.Errorf("commonDirPinEnv = %q, want [%q]", got, want)
		}
	})
	t.Run("request_dir_short", func(t *testing.T) {
		shortT := filepath.Dir(shortGit)
		got := commonDirPinEnv(shortT)
		if want := "GIT_COMMON_DIR=" + filepath.Join(r.T, ".git"); len(got) != 1 || !strings.EqualFold(got[0], want) {
			t.Errorf("commonDirPinEnv(%s) = %q, want [%q]", shortT, got, want)
		}
	})
}

// TestShortDaemonGitDirRootPairWindows: git.info with "shadowDirOff":true and
// a daemon GIT_DIR at the 8.3 spelling of the repository's own `.git`. The two
// calls with --git-dir= run in the long spelling and carry it in --git-dir= and
// GIT_COMMON_DIR. GIT_DIR keeps the short one. 87375cc7 does so for short folder
// names above .git (measured on Windows). A short name of .git itself is
// claustrum's own, not measured. Mutation: in gitInfo pass u.gitDirPin to
// gitInfoRoot.
func TestShortDaemonGitDirRootPairWindows(t *testing.T) {
	f := newShadowFixture(t)
	repo, gitDir := configRepo(t)
	short := shortPathName(t, gitDir)
	t.Setenv("GIT_DIR", short)
	raw, calls := f.info(t, repo, map[string]any{"shadowDirOff": true})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	pair := 0
	for _, c := range calls {
		named := ""
		for _, a := range c.argv {
			if v, ok := strings.CutPrefix(a, "--git-dir="); ok {
				named = v
			}
		}
		if named == "" {
			continue
		}
		pair++
		common, _ := lookupExact(c.env, "GIT_COMMON_DIR")
		daemon, _ := lookupExact(c.env, "GIT_DIR")
		for name, got := range map[string]string{"--git-dir=": named, "the folder": c.cwd, "GIT_COMMON_DIR": common} {
			if strings.Contains(got, "~") || !strings.EqualFold(filepath.Clean(got), gitDir) {
				t.Errorf("call %q: %s is %q, want %q", c.argv, name, got, gitDir)
			}
		}
		if daemon != short {
			t.Errorf("call %q: GIT_DIR is %q, want %q", c.argv, daemon, short)
		}
	}
	if pair != 2 {
		t.Errorf("%d calls name --git-dir=, want 2", pair)
	}
}
