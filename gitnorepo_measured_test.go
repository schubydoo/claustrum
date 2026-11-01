package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// In a folder with no repository the listing carries GIT_DIR=<null device>. When it
// exits 128 with git's "not a git repository" text, c8e2f083 runs `git version` and
// answers the hooks refusal on git.info and git.list_branches, and the same text with
// worktree_add_failed on git.worktree_create (measured on Linux and Windows).
// git.status keeps its answer isRepo:false. Mutations: drop the GIT_DIR= test in
// listingNamesGitDir (the three refusals fail), or drop the exit128Says test in
// statusNoRepoCalls (git.status fails).
func TestNoRepoFolderListingExit128(t *testing.T) {
	f := newNoRepoFixture(t)
	stubListing(t, 128, enGitText+"\n")
	text := hooksRefusalPrefix + "exit status 128: " + enGitText
	for _, row := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.info", map[string]any{"path": f.n}, errFrame(t, text)},
		{"git.list_branches", map[string]any{"path": f.n}, errFrame(t, text)},
		{"git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()},
			resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})},
		{"git.status", map[string]any{"baseRepo": f.n, "path": f.n}, resultOf(notRepoStatus)},
	} {
		raw, calls := f.call(t, row.method, row.params)
		if raw != row.want {
			t.Errorf("%s = %s\nwant %s", row.method, raw, row.want)
		}
		if row.method == "git.status" {
			continue
		}
		i := slices.IndexFunc(calls, envCall.listing)
		if i < 0 || !slices.Contains(calls[i].env, "GIT_DIR="+os.DevNull) {
			t.Fatalf("%s: calls = %v, want a listing with GIT_DIR=%s", row.method, calls, os.DevNull)
		}
		if !slices.ContainsFunc(calls[i+1:], envCall.version) {
			t.Errorf("%s: no `git version` call after the listing in %v", row.method, calls)
		}
	}
	if got := dirNames(t, f.n); len(got) != 0 {
		t.Errorf("the plain folder holds %q, want nothing", got)
	}
}

// git.info on a folder whose `.git` file names a gone path, in the state shadow.
// c8e2f083 answers "no repository" in both cases, with these calls (measured on Linux
// with git 2.43). For `<dir>/gone/x` it runs the call about the repository, then its
// second run with GIT_DIR=<.probe>. For a gone entry under the `worktrees` folder of a
// repository it runs the listing and the heavy `rev-parse --git-dir`, both with
// GIT_DIR=<null device>. git.status answers isRepo:false in both cases. For
// `<dir>/gone/x` the stub answers both runs of the call and the listings with the
// answers of git 2.43. git 2.56 prints its gitfile text in the listing there, and
// git.status then refuses (TestGitFileNowhereFourMethodsStub). Mutations:
// drop the call about the repository in gitInfo (the first case fails), or keep
// GIT_COMMON_DIR for a gone worktree entry in judgeGitDir (the second case fails).
func TestGoneGitFileInfoCalls(t *testing.T) {
	for _, row := range []struct {
		name  string
		entry bool
	}{{"gone path", false}, {"gone worktree entry", true}} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			dir := resolveTestRoot(t, t.TempDir())
			g := filepath.Join(dir, "G")
			target := filepath.Join(dir, "gone", "x")
			if row.entry {
				target = filepath.Join(f.gitDir, "worktrees", "gone")
			}
			writeFile(t, filepath.Join(g, ".git"), "gitdir: "+filepath.ToSlash(target)+"\n", 0o644)
			older := "fatal: not a git repository: " + filepath.ToSlash(target) + "\n"
			if !row.entry {
				answerProbe(t, 128, "", older)
				answerSecondProbe(t, 0, "")
			}
			raw, calls := f.info(t, g, nil)
			if raw != noRepoShadow {
				t.Errorf("git.info = %s\n  want %s", raw, noRepoShadow)
			}
			listings := slices.ContainsFunc(calls, envCall.listing)
			if !row.entry {
				second, ok := secondProbeCall(calls)
				if n := len(probeCalls(calls)); n != exit128Runs() || !ok || listings {
					t.Fatalf("calls = %v, want the call about the repository and its second run, and no listing", calls)
				}
				probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
				if v, _ := lookupExact(second.env, "GIT_DIR"); v != probe {
					t.Errorf("the second run has GIT_DIR=%s, want %s", v, probe)
				}
			} else {
				if n := len(probeCalls(calls)); n != 0 {
					t.Errorf("%d calls about the repository, want none", n)
				}
				var steps []string
				for _, c := range calls {
					if v, _ := lookupExact(c.env, "GIT_DIR"); v != os.DevNull || canonicalPath(c.cwd) != canonicalPath(g) {
						continue
					}
					switch {
					case c.listing():
						steps = append(steps, "list")
					case argvEndsWith(c.argv, []string{"rev-parse", "--git-dir"}) && slices.Contains(c.argv, "core.hooksPath="+hooksNullDevice):
						steps = append(steps, "git-dir")
					}
				}
				// The call order is measured on Linux. On Windows git.info finds its
				// root by its own steps (gitinforoot_windows.go), so only the listing
				// with the null device is checked there (claustrum's own, not measured).
				want, ok := []string{"list", "git-dir"}, false
				if runtime.GOOS == "windows" {
					ok = len(steps) > 0 && steps[0] == "list"
				} else {
					ok = slices.Equal(steps, want)
				}
				if !ok {
					t.Errorf("calls with GIT_DIR=%s in %s: %q, want %q (Windows: the listing first). All calls: %v", os.DevNull, g, steps, want, calls)
				}
			}
			if !row.entry {
				stageGit(t, stagedCall{Match: []string{"config", "-z", "--list"}, Exit: 128, Stderr: older})
			}
			status, _ := f.call(t, "git.status", map[string]any{"path": g, "baseRepo": g})
			if want := resultOf(notRepoStatus); status != want {
				t.Errorf("git.status = %s\n  want %s", status, want)
			}
		})
	}
}
