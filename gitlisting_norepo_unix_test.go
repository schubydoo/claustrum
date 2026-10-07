//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// Rule C2. A plain folder of mode 0000 keeps its frame with a broken pair and with no
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

// Cell C-c (Linux and macOS VMs). GIT_CONFIG_GLOBAL names a file that git cannot parse, and no
// GIT_CONFIG_COUNT is set. The listing fails, and the `git version` child gets no
// GIT_CONFIG_GLOBAL, so it passes. The answer is the hooks refusal with the detail of
// the listing, not the "cannot run" text. The text after "exit status 128: " is that
// of the git on this host. Mutation: pass GIT_CONFIG_GLOBAL on to `git version`.
func TestGitVersionChildGetsNoGlobalConfig(t *testing.T) {
	f := newNoRepoFixture(t)
	broken := filepath.Join(filepath.Dir(f.n), "broken.cfg")
	writeFile(t, broken, "[broken\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", broken)
	// The listing of the real git in N gives the expected detail.
	cmd := exec.Command(f.realGit, "config", "-z", "--list")
	cmd.Dir = f.n
	cmd.Env = append(os.Environ(), "GIT_DIR="+os.DevNull, "LC_ALL=C", "LANGUAGE=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || err.Error() != "exit status 128" {
		t.Fatalf("real listing with the broken file: err = %v, want exit status 128", err)
	}
	text := hooksRefusalPrefix + "exit status 128: " + worktreeGitText(stderr.String(), nil)
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.n})
	if want := errFrame(t, text); raw != want {
		t.Errorf("C-c info N = %s\nwant %s", raw, want)
	}
	i := slices.IndexFunc(calls, envCall.version)
	if i < 1 || !calls[i-1].listing() {
		t.Fatalf("calls = %v, want git version right after the listing", calls)
	}
	if !slices.Contains(calls[i-1].env, "GIT_CONFIG_GLOBAL="+broken) {
		t.Errorf("listing env has no GIT_CONFIG_GLOBAL=%s", broken)
	}
	for _, kv := range calls[i].env {
		if envName(kv) == "GIT_CONFIG_GLOBAL" {
			t.Errorf("git version got %q, want no GIT_CONFIG_GLOBAL", kv)
		}
	}
	if got, want := f.frame(t, "git.info", map[string]any{"path": f.top}), errFrame(t, text); got != want {
		t.Errorf("C-c info T = %s\nwant %s", got, want)
	}
	create := resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	if got := f.frame(t, "git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()}); got != create {
		t.Errorf("C-c create N = %s\nwant %s", got, create)
	}
}

// Cells CfP0 and CfP3 (Linux VM). The daemon's GIT_DIR names a regular file. git.info
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

// Cells CbP0 and CbP3 (Linux VM). A `.git` file names a git dir that does not exist.
// git.info answers the "no repository" frame, with and without an empty
// GIT_CONFIG_KEY_0.
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
