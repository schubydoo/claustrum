//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestPlaceWorktreeIndexReplacesFile pins cell X8 (Linux and macOS VMs). A file of
// mode 0600 exists at the index before the placement. The index is then a new
// inode, not that file and not the temporary file, with mode 0644 under umask 0022.
// The umask is process-wide, so this test does not run in parallel.
func TestPlaceWorktreeIndexReplacesFile(t *testing.T) {
	src, srcInfo := indexSource(t)
	adminDir := filepath.Join(t.TempDir(), "worktrees", "w1")
	mkdirForTest(t, adminDir)
	dst := filepath.Join(adminDir, "index")
	// A second link keeps the inode of the old file in use, so the new file cannot
	// get the same number.
	writeFile(t, dst, "OTHER-CONTENT\n", 0o600)
	if err := os.Link(dst, filepath.Join(adminDir, "old")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o022)
	err = placeWorktreeIndex(src, adminDir)
	syscall.Umask(old)
	if err != nil {
		t.Fatalf("placeWorktreeIndex: %v", err)
	}
	got, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, got) || os.SameFile(srcInfo, got) {
		t.Errorf("the index is the file that existed or the temporary file, want a new inode")
	}
	if got.Mode().Perm() != 0o644 {
		t.Errorf("index mode = %#o, want 0644", got.Mode().Perm())
	}
	if b, _ := os.ReadFile(dst); string(b) != "0123456789" {
		t.Errorf("index = %q, want the bytes of the temporary index", b)
	}
}

// TestPlaceWorktreeIndexErrors pins the errors of a placement. A registration
// without its write bit names the registration and the file, relative to the
// registrations directory (rows A14, A14f and A14b). A temporary index that is gone
// names its path (cell X1). A registrations directory that is gone fails too.
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
	gone := filepath.Join(t.TempDir(), "gone")
	if err := placeWorktreeIndex(gone, adminDir); err == nil || err.Error() != "open "+gone+": no such file or directory" {
		t.Errorf("placeWorktreeIndex of a missing temporary index = %v, want open %s: no such file or directory", err, gone)
	}
	if err := placeWorktreeIndex(src, filepath.Join(t.TempDir(), "gone", "w1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("placeWorktreeIndex into a missing registrations directory = %v, want a not-exist error", err)
	}
}

// TestIndexPlacementText pins the text of a failed placement: the stderr of the
// checkout, one space and the error, cut at 512 bytes as one text.
//
//   - Rows A14, A14f and A14b: stderr on one line, one space, the error.
//   - Cell X2: no stderr gives the error alone, with no space before it.
//   - Cell X3: a stderr over 512 bytes gives its first 512 bytes and no error.
//   - A stderr of 500 bytes with its newline leaves 12 bytes of the error. No cell
//     measures that: it follows from the cut over the joined text.
func TestIndexPlacementText(t *testing.T) {
	err := errors.New("openat w1/index: permission denied")
	long := strings.Repeat("s5err-xx\n", 151)
	near := strings.Repeat("x", 499) + "\n"
	for _, tc := range []struct{ name, stderr, want string }{
		{"hints", "hint: a\nhint: b\n", "hint: a hint: b openat w1/index: permission denied"},
		{"no stderr", "", "openat w1/index: permission denied"},
		{"newline only", "\n", "openat w1/index: permission denied"},
		{"over the cap", long, strings.ReplaceAll(long[:512], "\n", " ")},
		{"near the cap", near, near[:499] + " openat w1/in"},
	} {
		if got := indexPlacementText(tc.stderr, err); got != tc.want {
			t.Errorf("%s: indexPlacementText = %q, want %q", tc.name, got, tc.want)
		}
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

// placementFixture starts the git stub for a create whose index placement fails,
// and returns the fixture, the server and the TMPDIR of the daemon. D5 is off.
func placementFixture(t *testing.T) (wtFixture, *server, string) {
	t.Helper()
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
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return f, s, tmp
}

// checkoutFailedHead starts every frame of a failed placement here. The stub prints
// "hint: synthetic" first. The real git can add hint lines after it, so the tests
// pin the frame before and after them only.
const checkoutFailedHead = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): hint: synthetic`

// TestWorktreeCreateIndexPlacementFails pins the answer to a registration that
// loses its write bit during the checkout. The git stub runs the real read-tree and
// then makes the registration read-only, so the index cannot be placed and the
// rollback cannot delete the registration. The request answers worktree_add_failed
// "(checkout)" with the stderr of the checkout, the openat text and one undo text.
// The registration stays, and no branch step runs. The temporary index folder is
// removed.
//
//   - Rows A14 and A14f (Linux VM), A14 and A14b (macOS VM): the leaf is deleted.
//     Branch w1 stays, although main reaches its tip.
//   - Cell X5 (Linux and macOS VMs): in attach mode the text names the
//     registration alone. The attached branch stays.
//   - Cell X11 (Linux VM): the parent of the leaf is read-only too, so the empty
//     leaf stays. The frame carries the registration text alone.
func TestWorktreeCreateIndexPlacementFails(t *testing.T) {
	const both = "the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll w1: permission denied)"
	const attach = "the worktree registration remains; remove it by hand before retrying (RemoveAll w1: permission denied)"
	for _, tc := range []struct {
		name, existing, undo, branch string
		lockLeafParent               bool
	}{
		{"A14", "", both, "w1", false},
		{"X5 attach", "ex", attach, "ex", false},
		{"X11 leaf parent locked", "", both, "w1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, tmp := placementFixture(t)
			reg := filepath.Join(f.regDir, "w1")
			t.Cleanup(func() {
				_ = os.Chmod(reg, 0o755)
				_ = os.Chmod(filepath.Dir(f.leaf()), 0o755)
			})
			// "lockparents" makes the parent of each path read-only.
			locked := filepath.Join(reg, "index")
			if tc.lockLeafParent {
				locked += string(os.PathListSeparator) + f.leaf()
			}
			t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "lockparents")
			t.Setenv("CLAUSTRUM_GITSTUB_LEAF", locked)
			slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

			raw, _ := f.create(t, s, "w1", tc.existing, 0)
			tail := jsonString(t, " openat w1/index: permission denied; and the undo could not finish for "+f.leaf()+": "+tc.undo)
			tail = tail[1:] + `,"errorCode":"worktree_add_failed"}}`
			if !strings.HasPrefix(raw, checkoutFailedHead) || !strings.HasSuffix(raw, tail) {
				t.Fatalf("reply = %s\nwant %s … %s", raw, checkoutFailedHead, tail)
			}
			if strings.Contains(raw, "the worktree directory remains") {
				t.Errorf("reply = %s, want the registration text alone", raw)
			}
			wantLeaf := []string(nil)
			if tc.lockLeafParent {
				wantLeaf = []string{}
			}
			if got := f.leafEntries(t); (got == nil) != (wantLeaf == nil) || len(got) != 0 {
				t.Errorf("leaf entries = %q, want %q (nil means the leaf is removed)", got, wantLeaf)
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
			if !f.hasRef(t, tc.branch) {
				t.Errorf("refs/heads/%s was deleted, want it kept beside a registration that stays", tc.branch)
			}
			if ents, err := os.ReadDir(tmp); err != nil || len(ents) != 0 {
				t.Errorf("TMPDIR holds %d entries after the create (%v), want the temporary index folder removed", len(ents), err)
			}
		})
	}
}

// TestWorktreeCreateTempIndexGone pins cell X1 (Linux and macOS VMs). The git stub
// runs the real read-tree and then deletes the temporary index. The request answers
// worktree_add_failed "(checkout)" with the stderr of the checkout and the open
// text, and the whole rollback runs: the leaf, the registration and branch w1 go.
// The temporary folder in the text has claustrum's own prefix.
func TestWorktreeCreateTempIndexGone(t *testing.T) {
	f, s, tmp := placementFixture(t)
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "rmindex")
	slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

	raw, _ := f.create(t, s, "w1", "", 0)
	tail := regexp.MustCompile(regexp.QuoteMeta(strings.Trim(jsonString(t, " open "+tmp+"/"+checkoutIndexTempPrefix), `"`)) +
		`[0-9]+/index: no such file or directory","errorCode":"worktree_add_failed"\}\}$`)
	if !strings.HasPrefix(raw, checkoutFailedHead) || !tail.MatchString(raw) {
		t.Fatalf("reply = %s\nwant %s … %s", raw, checkoutFailedHead, tail)
	}
	f.assertRolledBack(t, nil, false)
	if ents, err := os.ReadDir(tmp); err != nil || len(ents) != 0 {
		t.Errorf("TMPDIR holds %d entries after the create (%v), want the temporary index folder removed", len(ents), err)
	}
}
