//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A plain folder of mode 0000 keeps its frame with a broken pair and with no
// GIT_CONFIG_* entry (row A-X1). git cannot start there, so no listing answers.
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
		t.Errorf("A-X1 in P0 = %s\nwant %s", got, want)
	}
	plantEnv(t, brokenPairPhases[0].env...)
	if got := f.frame(t, "git.info", map[string]any{"path": n0}); got != want {
		t.Errorf("A-X1 in P3 = %s\nwant %s", got, want)
	}
}

// Phase P11, rows A-M2 and A-X1 (Linux and macOS VMs). With a `git version` that fails, git.info
// on a regular file and on a folder of mode 0000 answers the "cannot run" text with
// the detail of the version call. A path that does not exist keeps its frames (rows
// A-M1 and A-M3 to A-M5). Mutation: do not read the answer of `git version` after a
// listing that cannot start (failedListingCheck).
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
		{"A-M2", "git.info", map[string]any{"path": nFile}, errFrame(t, versionFailsText)},
		{"A-X1", "git.info", map[string]any{"path": n0}, errFrame(t, versionFailsText)},
		{"A-M1", "git.info", map[string]any{"path": f.m}, resultOf(notRepoInfo)},
		{"A-M3", "git.list_branches", map[string]any{"path": f.m}, resultOf(notRepoList)},
		{"A-M4", "git.worktree_create", map[string]any{"baseRepo": f.m, "branchName": "w1", "worktreePath": leaf}, resultOf(notRepoCreate)},
		{"A-M5", "git.worktree_remove", map[string]any{"baseRepo": f.m, "worktreePath": leaf}, resultOf(`{"success":true}`)},
	} {
		if got := f.frame(t, row.method, row.params); got != row.want {
			t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, row.want)
		}
	}
	if exists(f.m) {
		t.Errorf("a request made %s", f.m)
	}
}

// Cells CfP0 and CfP3 (Linux and macOS VMs). The daemon's GIT_DIR names a regular file. git.info
// answers the "no repository" frame on a plain folder and on a repository, with and
// without an empty GIT_CONFIG_KEY_0, and no listing runs. Mutation: run the listing
// in that state (noRepoListingRefusal).
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

// Cells CbP0 and CbP3 (Linux and macOS VMs). A `.git` file names a git dir that does not exist.
// git.info answers the "no repository" frame, with and without an empty
// GIT_CONFIG_KEY_0. No Windows cell measures that state, so the test does not run on
// Windows.
func TestGitFileToNowhereKeepsFrame(t *testing.T) {
	for _, ph := range []struct {
		name string
		env  []string
	}{
		{"CbP0 nothing set", nil},
		{"CbP3 empty key", brokenPairPhases[0].env},
	} {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			g := filepath.Join(filepath.Dir(f.n), "G")
			writeFile(t, filepath.Join(g, ".git"), "gitdir: "+filepath.Join(filepath.Dir(f.n), "nowhere", "gitdir")+"\n", 0o644)
			plantEnv(t, ph.env...)
			if got, want := f.frame(t, "git.info", map[string]any{"path": g}), resultOf(notRepoInfo); got != want {
				t.Errorf("git.info = %s\nwant %s", got, want)
			}
		})
	}
}
