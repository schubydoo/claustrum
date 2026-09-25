//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// An external worktreeRoot writable by every user on the host is refused (mode is
// echoed). The uid-ownership and shared-group refusals need a foreign owner / a
// shared group and are VM-verified rather than exercised here. Measured against
// 7d193f89. The asserted substring is the suffix common to the world-only and
// group+world spellings, so a temp dir whose gid differs from the process egid
// (which would add "its group and ") does not make the test flaky.
func TestWorktreeCreateExternalWorldWritable(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "wld")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "w",
			"worktreePath": filepath.Join(root, "d", "wt"), "worktreeRoot": root}))
	if !strings.Contains(raw, `"errorCode":"unsafe_path"`) ||
		!strings.Contains(raw, "every user on this host (mode 0777)") {
		t.Errorf("create with a world-writable root = %s, want the writable refusal", raw)
	}
}

// The uid-ownership refusal is the first check in the reference's order and needs a
// root the daemon user does not own — the filesystem root is the one such directory
// present on every unix host. worktreeRootShareRefusal only stats, so this touches
// nothing. The ownership is asserted first so a host where "/" is somehow ours skips
// rather than reporting a false negative.
func TestWorktreeRootShareRefusalForeignOwner(t *testing.T) {
	skipIfRoot(t)
	root := string(os.PathSeparator)
	fi, err := os.Stat(root)
	if err != nil {
		t.Skipf("cannot stat %s: %v", root, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Geteuid() {
		t.Skipf("%s is owned by the test user; the foreign-owner arm is unreachable here", root)
	}
	want := fmt.Sprintf("is owned by uid %d, not by you (uid %d)", st.Uid, os.Geteuid())
	if got := worktreeRootShareRefusal(root); !strings.Contains(got, want) {
		t.Errorf("worktreeRootShareRefusal(%s) = %q, want it to contain %q", root, got, want)
	}
}

// The symlinked-<directory> refusal gates REMOVE as well as create (the create arm
// is covered by TestWorktreeCreateExternalSpellingAndSymlink): a planted link at the
// <directory> level must not carry the removal out of the worktree location.
func TestWorktreeRemoveExternalDirSymlink(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	root := filepath.Join(base, "mine")
	outside := filepath.Join(base, "outside", "wt")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(root, "dlink")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": filepath.Join(root, "dlink", "wt"), "worktreeRoot": root}))
	if !strings.Contains(worktreeErrorField(t, raw), "is a symbolic link; the directory under the worktree location must be a real directory") {
		t.Errorf("remove through a symlinked <dir> = %s, want the symlink refusal", raw)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the removal followed the symlink out of the root (%v)", err)
	}
}

// A worktreePath that ends in a slash, with a worktreeRoot. The <directory> tests
// read the cleaned path, so "R/proj/w1/" is judged at "R/proj", not at "R/proj/w1".
// The frames were measured against f6010b97 and 90fca6e6 on Linux and macOS VMs
// (rows XS_c_slash, XS_c_slash_exists, XS2_c_slash_twice). Without the clean, the
// first case created the worktree in R/proj and wrote the marker there, and the
// others named the wrong directory.
func TestWorktreeExternalTrailingSlash(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "T")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "R")
	proj := filepath.Join(root, "proj")
	for _, d := range []string{filepath.Join(proj, "e0"), filepath.Join(proj, "p0")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(proj, "p0", "keep.txt"), "keep\n", 0o644)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	create := func(wp string) string {
		t.Helper()
		return dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": wp, "worktreeRoot": root}))
	}
	notEmpty := "refusing to create worktree: " + proj + " already exists, is not marked as a " +
		"worktree directory, and holds other files (for example \"e0\"); the per-repository " +
		"directory under a worktree location must start out empty — remove it, restore " +
		"its .claude-managed-worktrees file if you deleted it, or choose another location"

	t.Run("new leaf in a non-empty directory", func(t *testing.T) {
		wantError(t, create(proj+"/w1/"), notEmpty, "unsafe_path")
		if _, err := os.Lstat(filepath.Join(proj, "w1")); !os.IsNotExist(err) {
			t.Errorf("w1 was created (err=%v)", err)
		}
		if _, err := os.Lstat(filepath.Join(proj, managedWorktreesMarker)); !os.IsNotExist(err) {
			t.Errorf("the marker was written into %s (err=%v)", proj, err)
		}
	})
	t.Run("existing plain folder", func(t *testing.T) {
		wantError(t, create(proj+"/p0/"), notEmpty, "unsafe_path")
	})
	t.Run("second create in a fresh directory", func(t *testing.T) {
		wp := filepath.Join(root, "cp", "w1")
		raw := create(wp + "/")
		want := `{"success":true,"path":` + jsonString(t, wp+"/") + `,`
		if !strings.Contains(raw, want) {
			t.Fatalf("first create = %s, want %s", raw, want)
		}
		wantError(t, create(wp+"/"), "refusing to create worktree: "+wp+
			" already exists, and a new worktree is only ever created in a fresh directory", "unsafe_path")
	})
	t.Run("symlinked directory", func(t *testing.T) {
		outside := filepath.Join(base, "outside")
		if err := os.MkdirAll(filepath.Join(outside, "wt"), 0o755); err != nil {
			t.Fatal(err)
		}
		dlink := filepath.Join(root, "dlink")
		if err := os.Symlink(outside, dlink); err != nil {
			t.Fatal(err)
		}
		const tail = " is a symbolic link; the directory under the worktree location must be a real directory"
		wantError(t, create(dlink+"/w1/"), "refusing to create worktree: "+dlink+tail, "unsafe_path")
		if _, err := os.Lstat(filepath.Join(outside, "w1")); !os.IsNotExist(err) {
			t.Errorf("the create followed the symlink out of the root (err=%v)", err)
		}
		rm := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": repo, "worktreePath": dlink + "/wt/", "worktreeRoot": root}))
		if got := worktreeErrorField(t, rm); got != "refusing to remove worktree: "+dlink+tail {
			t.Errorf("remove through a symlinked <dir> = %s, want the symlink refusal", rm)
		}
	})
}

// writableWho names who beyond the owner can write. The three spellings are the
// reference's; the combined case is not reachable in the RPC test above without a
// shared group AND world-write, so it is pinned directly.
func TestWritableWho(t *testing.T) {
	cases := []struct {
		group, world bool
		want         string
	}{
		{true, true, "its group and every user on this host"},
		{false, true, "every user on this host"},
		{true, false, "its group"},
		{false, false, ""},
	}
	for _, c := range cases {
		if got := writableWho(c.group, c.world); got != c.want {
			t.Errorf("writableWho(group=%v,world=%v) = %q, want %q", c.group, c.world, got, c.want)
		}
	}
}

// worktreeRootShareRefusal returns "" for a missing root — the create fails later at
// the parent-creation step, not here.
func TestWorktreeRootShareRefusalMissingRoot(t *testing.T) {
	if got := worktreeRootShareRefusal(filepath.Join(t.TempDir(), "nonexistent")); got != "" {
		t.Errorf("missing root = %q, want no refusal", got)
	}
}

// A `.git` file that stats as regular but cannot be read is refused with the same
// "does not name a git dir" reason as garbage content — the fallback never deletes
// a path whose gitdir pointer it could not examine.
func TestExternalWorktreeVerifyUnreadableGitFile(t *testing.T) {
	skipIfRoot(t)
	base := t.TempDir()
	wp := filepath.Join(base, "wt")
	if err := os.Mkdir(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(wp, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /nope\n"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitFile, 0o600) })
	reason, transient := externalWorktreeVerify(filepath.Join(base, "repo"), wp)
	if transient || !strings.Contains(reason, "does not name a git dir") {
		t.Errorf("externalWorktreeVerify(unreadable .git) = (%q, %v), want a non-transient \"does not name a git dir\" refusal", reason, transient)
	}
	if _, err := os.Stat(wp); err != nil {
		t.Errorf("verify must not touch the path: %v", err)
	}
}
