//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A plain folder of mode 0000 keeps its frame with a broken pair and with no
// GIT_CONFIG_* entry (measured). git cannot start there, so no listing answers.
func TestBrokenConfigPairMode0000KeepsFrame(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can enter a folder of mode 0000")
	}
	f := newNoRepoFixture(t)
	n0 := filepath.Join(filepath.Dir(f.n), "N0")
	if err := os.Mkdir(n0, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(n0, 0o755) })
	want := `{"jsonrpc":"2.0","id":1,"result":` + notRepoInfo + `}`
	if got := f.frame(t, "git.info", map[string]any{"path": n0}); got != want {
		t.Errorf("in P0 = %s\nwant %s", got, want)
	}
	plantEnv(t, brokenPairPhases[0].env...)
	if got := f.frame(t, "git.info", map[string]any{"path": n0}); got != want {
		t.Errorf("in P3 = %s\nwant %s", got, want)
	}
}

// Measured on Linux and macOS. With a `git version` that fails, git.info on a regular file and
// on a folder of mode 0000 answers the "cannot run" text with the detail of the version call. A
// path that does not exist keeps its frames (measured). Mutation: do not read the answer of `git
// version` after a listing that cannot start (failedListingCheck).
func TestGitVersionFailsWhereGitCannotStart(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can enter a folder of mode 0000")
	}
	f := newNoRepoFixture(t)
	nFile := filepath.Join(f.n, "n.txt")
	writeFile(t, nFile, "n\n", 0o644)
	n0 := filepath.Join(filepath.Dir(f.n), "N0")
	if err := os.Mkdir(n0, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(n0, 0o755) })
	versionFails(t)
	plantEnv(t, brokenPairPhases[0].env...)
	leaf := filepath.Join(f.m, ".claude", "worktrees", "w1")
	for _, row := range []struct {
		name, method string
		params       map[string]any
		want         string
	}{
		{"file in the plain folder", "git.info", map[string]any{"path": nFile}, errFrame(t, versionFailsText)},
		{"folder of mode 0000", "git.info", map[string]any{"path": n0}, errFrame(t, versionFailsText)},
		{"missing path", "git.info", map[string]any{"path": f.m}, resultOf(notRepoInfo)},
		{"missing path", "git.list_branches", map[string]any{"path": f.m}, resultOf(notRepoList)},
		{"missing path", "git.worktree_create", map[string]any{"baseRepo": f.m, "branchName": "w1", "worktreePath": leaf}, resultOf(notRepoCreate)},
		{"missing path", "git.worktree_remove", map[string]any{"baseRepo": f.m, "worktreePath": leaf}, resultOf(`{"success":true}`)},
	} {
		if got := f.frame(t, row.method, row.params); got != row.want {
			t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, row.want)
		}
	}
	if exists(f.m) {
		t.Errorf("a request made %s", f.m)
	}
}

// Measured on Linux and macOS. The daemon's GIT_DIR names a regular file. git.info answers
// the "no repository" frame on a plain folder and on a repository, with and without an empty
// GIT_CONFIG_KEY_0, and no listing runs. Mutation: run the listing in that state
// (noRepoCalls).
func TestDaemonGitDirFileRunsNoListing(t *testing.T) {
	for _, ph := range []struct {
		name string
		env  []string
	}{
		{"CfP0 nothing set", nil},
		{"CfP3 empty key", brokenPairPhases[0].env},
	} {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			gd := filepath.Join(filepath.Dir(f.n), "gitdir.file")
			writeFile(t, gd, "not a git dir\n", 0o644)
			t.Setenv("GIT_DIR", gd)
			plantEnv(t, ph.env...)
			for _, dir := range []string{f.n, f.top} {
				raw, calls := f.call(t, "git.info", map[string]any{"path": dir})
				if want := resultOf(notRepoInfo); raw != want {
					t.Errorf("git.info(%s) = %s\nwant %s", dir, raw, want)
				}
				if slices.ContainsFunc(calls, envCall.listing) {
					t.Errorf("git.info(%s) ran a listing: %v", dir, calls)
				}
			}
		})
	}
}

// Measured on Linux and macOS. A `.git` file names a git dir that does not exist. git.info
// answers the "no repository" frame, with and without an empty GIT_CONFIG_KEY_0. In claustrum
// the empty key makes the second run of the call about the repository fail, so git.info
// answers its error there (claustrum's own, not measured). Those cells ran a git older than
// 2.56. A measurement shows the state on Windows with git 2.56 and no such key, and
// TestGitFileNowhereFourMethodsStub covers it there. The empty key is not measured on Windows,
// so this test does not run there.
//
// git 2.56 fails the listing there with its gitfile text. The four "no repository"
// frames stay then too, also for the reply to the first git request of a
// daemon and with "shadowDirOff":true (4c111e86, measured on Linux and macOS with git 2.56.1).
// The empty key is not measured with git 2.56. The test decides by the text of the git
// on PATH.
func TestGitFileToNowhereKeepsFrame(t *testing.T) {
	newGit := gitSaysGitFileNowhere(t)
	for _, ph := range []struct {
		name string
		env  []string
	}{
		{"CbP0 nothing set", nil},
		{"CbP3 empty key", brokenPairPhases[0].env},
	} {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			f.d, _ = newSeededShadowDeps(t)
			g := filepath.Join(filepath.Dir(f.n), "G")
			writeFile(t, filepath.Join(g, ".git"), "gitdir: "+filepath.Join(filepath.Dir(f.n), "nowhere", "gitdir")+"\n", 0o644)
			plantEnv(t, ph.env...)
			want := gitFileNowhereFrames(t, g, "", false)
			if newGit && ph.env == nil {
				first, _ := newShadowDeps(t)
				if got, w := gitDispatch(t, first, rpcLine(t, "git.info", map[string]any{"path": g})), noRepoShadow; got != w {
					t.Errorf("first git.info = %s\nwant %s", got, w)
				}
				want = append(want, gitFileNowhereFrames(t, g, "", true)...)
			}
			// git.info runs the call about the repository and its second run. With the
			// empty key the second run fails, and the answer is the error with the text
			// of the first call (claustrum's own, not measured).
			if ph.env == nil {
				want[0].frame = noRepoShadow
			} else {
				got := f.frame(t, "git.info", map[string]any{"path": g})
				if prefix := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"git could not check this repository on the remote machine: at `; !strings.HasPrefix(got, prefix) {
					t.Errorf("git.info = %s\nwant the error of the call about the repository", got)
				}
				want = want[1:]
			}
			for _, m := range want {
				if m.method == "git.status" && ph.env != nil {
					// git.status refuses the empty key on its own listing (measured).
					continue
				}
				if m.method == "git.worktree_create" && ph.env != nil {
					// The create runs the same call (gitWorktreeCreateLocked) and answers
					// its error the same way. The fixture has a passed start test. With a
					// real git the start test of a daemon with this key fails first, and a
					// create or git.info as the first git request answers the failed-test
					// text on 4c111e86 and on claustrum (measured on Linux, git 2.43). So
					// this answer is claustrum's own and needs a stub git.
					got := f.frame(t, m.method, m.params)
					if !strings.Contains(got, `"error":"git could not check this repository on the remote machine: at `) || !strings.Contains(got, `"errorCode":"worktree_add_failed"`) {
						t.Errorf("%s = %s\nwant the error of the call about the repository", m.method, got)
					}
					continue
				}
				if got := f.frame(t, m.method, m.params); got != m.frame {
					t.Errorf("%s = %s\nwant %s", m.method, got, m.frame)
				}
			}
			if exists(filepath.Join(g, ".claude")) {
				t.Errorf("a request made %s", filepath.Join(g, ".claude"))
			}
		})
	}
}
