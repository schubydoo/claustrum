//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests pin the index file of a new worktree and the answer to a failed
// placement. The rows are those of 89cb6289 on Linux and macOS VMs: A1 to A13 for the
// file, and A14, A14f and A14b for the failure.

// indexSource writes a temporary index of mode 0640 with an mtime that is not a
// whole microsecond, and returns its path and its file info as read back.
func indexSource(t *testing.T) (string, os.FileInfo) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(src, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod, because the umask of the test does not apply to it.
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	// The mtime of the temporary index of row A8, run 1 (Linux VM).
	when := time.Unix(1791348054, 843264033)
	if err := os.Chtimes(src, when, when); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	return src, fi
}

// TestPlaceWorktreeIndexNewFile pins the file that the placement leaves: a new
// inode with the bytes of the temporary index, mode 0666 less the umask (rows A12a,
// A12b and the 0022 rows), and the mtime of the temporary index rounded up to a
// whole microsecond. The temporary file stays for its owner to remove. The umask is
// process-wide, so this test does not run in parallel.
func TestPlaceWorktreeIndexNewFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		umask int
		want  os.FileMode
	}{{"umask 0022", 0o022, 0o644}, {"umask 0077", 0o077, 0o600}, {"umask 0002", 0o002, 0o664}} {
		t.Run(tc.name, func(t *testing.T) {
			src, srcInfo := indexSource(t)
			adminDir := filepath.Join(t.TempDir(), "worktrees", "w1")
			mkdirForTest(t, adminDir)
			old := syscall.Umask(tc.umask)
			err := placeWorktreeIndex(src, adminDir)
			syscall.Umask(old)
			if err != nil {
				t.Fatalf("placeWorktreeIndex: %v", err)
			}
			dst := filepath.Join(adminDir, "index")
			got, err := os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(dst); string(b) != "0123456789" {
				t.Errorf("index = %q, want the bytes of the temporary index", b)
			}
			if os.SameFile(srcInfo, got) {
				t.Errorf("the index is the temporary file itself, want a new inode")
			}
			if _, err := os.Stat(src); err != nil {
				t.Errorf("the temporary index after the placement: %v, want it kept", err)
			}
			if got.Mode().Perm() != tc.want {
				t.Errorf("index mode = %#o, want %#o", got.Mode().Perm(), tc.want)
			}
			want := roundUpToMicrosecond(srcInfo.ModTime())
			if want.Equal(srcInfo.ModTime()) {
				t.Logf("this file system keeps no time below the microsecond, so the mtime check tells nothing here")
			}
			if !got.ModTime().Equal(want) {
				t.Errorf("index mtime = %v, want %v (temporary index %v)", got.ModTime().UnixNano(), want.UnixNano(), srcInfo.ModTime().UnixNano())
			}
		})
	}
}

// TestRoundUpToMicrosecond pins the rounding of the index mtime. The first pair is
// row A8, run 1 (Linux VM). A whole microsecond stays: claustrum's choice (not
// measured).
func TestRoundUpToMicrosecond(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{843264033, 843265000},
		{843264001, 843265000},
		{843264999, 843265000},
		{999999001, 1000000000},
		{843264000, 843264000},
	} {
		got := roundUpToMicrosecond(time.Unix(1791348054, tc.in))
		if want := time.Unix(1791348054, tc.want); !got.Equal(want) {
			t.Errorf("roundUpToMicrosecond(.%09d) = %v, want %v", tc.in, got.UnixNano(), want.UnixNano())
		}
	}
}

// TestPlaceWorktreeIndexErrors pins the error of a placement that cannot make the
// file: it names the registration and the file, relative to the registrations
// directory (rows A14, A14f and A14b). A registrations directory that is gone fails
// too. A temporary index that is gone places nothing and fails nothing: claustrum's
// choice (not measured).
func TestPlaceWorktreeIndexErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	src, _ := indexSource(t)
	adminDir := filepath.Join(t.TempDir(), "worktrees", "w1")
	mkdirForTest(t, adminDir)
	chmodForTest(t, adminDir, 0o500)
	err := placeWorktreeIndex(src, adminDir)
	if err == nil || err.Error() != "openat w1/index: permission denied" {
		t.Errorf("placeWorktreeIndex = %v, want openat w1/index: permission denied", err)
	}
	if err := placeWorktreeIndex(filepath.Join(t.TempDir(), "gone"), adminDir); err != nil {
		t.Errorf("placeWorktreeIndex of a missing temporary index = %v, want nil", err)
	}
	if err := placeWorktreeIndex(src, filepath.Join(t.TempDir(), "gone", "w1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("placeWorktreeIndex into a missing registrations directory = %v, want a not-exist error", err)
	}
}

// TestIndexPlacementText pins the text of a failed placement: the stderr of the
// checkout on one line, one space, and the error (rows A14, A14f and A14b). With no
// stderr text the error stands alone: claustrum's choice (not measured).
func TestIndexPlacementText(t *testing.T) {
	err := errors.New("openat w1/index: permission denied")
	if got, want := indexPlacementText("hint: a\nhint: b\n", err), "hint: a hint: b openat w1/index: permission denied"; got != want {
		t.Errorf("indexPlacementText = %q, want %q", got, want)
	}
	if got, want := indexPlacementText("\n", err), "openat w1/index: permission denied"; got != want {
		t.Errorf("indexPlacementText with no stderr text = %q, want %q", got, want)
	}
}

// TestWorktreeCreateIndexIgnoresSharedRepository pins row A13. With
// core.sharedRepository group and umask 0022, git writes its temporary index with
// mode 0664. The index of the new worktree is 0644 all the same. The umask is
// process-wide, so this test does not run in parallel.
func TestWorktreeCreateIndexIgnoresSharedRepository(t *testing.T) {
	_, repo := createModesRepo(t)
	runGit(t, repo, "config", "core.sharedRepository", "group")
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	wp := filepath.Join(repo, ".claude", "worktrees", "wt")
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "s1", "worktreePath": wp}))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s, want success", raw)
	}
	if got := permOf(t, filepath.Join(repo, ".git", "worktrees", "wt", "index")); got != 0o644 {
		t.Errorf("index mode = %#o, want 0644", got)
	}
}

// TestWorktreeCreateIndexPlacementFails pins rule I2 (rows A14 and A14f on a Linux
// VM, A14 and A14b on a macOS VM). The git stub runs the real read-tree and then
// takes the write bit from the registration, so the index cannot be placed. The
// request answers worktree_add_failed "(checkout)" with the stderr of the checkout,
// the openat text and the undo text of a registration that stays. The leaf is
// deleted. The registration and the branch stay, although main reaches the tip of
// the branch: the branch step does not run. The temporary index folder is removed.
//
// The real git can add hint lines to its stderr, so the test pins the frame before
// and after them only.
func TestWorktreeCreateIndexPlacementFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	f := newWTFixture(t, false)
	s := newTestServer(t)
	reg := filepath.Join(f.regDir, "w1")
	t.Cleanup(func() { _ = os.Chmod(reg, 0o755) })
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// "lockparent" makes the parent of its path read-only: the registration.
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "lockparent")
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF", filepath.Join(reg, "index"))
	slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

	raw, _ := f.create(t, s, "w1", "", 0)
	head := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): hint: synthetic`
	tail := jsonString(t, " openat w1/index: permission denied; and the undo could not finish for "+f.leaf()+
		": the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll w1: permission denied)")
	tail = tail[1:] + `,"errorCode":"worktree_add_failed"}}`
	if !strings.HasPrefix(raw, head) || !strings.HasSuffix(raw, tail) {
		t.Fatalf("reply = %s\nwant %s … %s", raw, head, tail)
	}
	if got := f.leafEntries(t); got != nil {
		t.Errorf("leaf entries = %q, want the leaf removed", got)
	}
	if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
		t.Errorf("registrations = %q, want w1 kept", got)
	}
	if _, err := os.Lstat(filepath.Join(reg, "gitdir")); err != nil {
		t.Errorf("the registration lost its gitdir record: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(reg, "index")); !os.IsNotExist(err) {
		t.Errorf("the registration holds an index (Lstat err %v), want none", err)
	}
	if !f.hasRef(t, "w1") {
		t.Errorf("refs/heads/w1 was deleted, want it kept beside a registration that stays")
	}
	if ents, err := os.ReadDir(tmp); err != nil || len(ents) != 0 {
		t.Errorf("TMPDIR holds %d entries after the create (%v), want the temporary index folder removed", len(ents), err)
	}
}
