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

// TestInstallWorktreeIndexNewFile pins the file that the placement leaves: a new
// inode with the bytes of the temporary index, mode 0666 less the umask (rows A12a,
// A12b and the 0022 rows), and the mtime of the temporary index rounded up to a
// whole microsecond. The temporary file stays for its owner to remove. The umask is
// process-wide, so this test does not run in parallel.
func TestInstallWorktreeIndexNewFile(t *testing.T) {
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
			err := installWorktreeIndex(src, adminDir)
			syscall.Umask(old)
			if err != nil {
				t.Fatalf("installWorktreeIndex: %v", err)
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

// TestInstallWorktreeIndexReplacesFile pins cell X8 (Linux and macOS VMs). A file of
// mode 0600 exists at the index before the placement. The index is then a new
// inode, not that file and not the temporary file, with mode 0644 under umask 0022.
// The umask is process-wide, so this test does not run in parallel.
func TestInstallWorktreeIndexReplacesFile(t *testing.T) {
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
	err = installWorktreeIndex(src, adminDir)
	syscall.Umask(old)
	if err != nil {
		t.Fatalf("installWorktreeIndex: %v", err)
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

// TestInstallWorktreeIndexResolvedPath pins that the install acts on the path that
// the kernel resolves, not on a lexical clean of it. L is a symlink to a/b, so
// worktrees/L/../../w1 is worktrees/w1 for the kernel and w1 beside worktrees after
// a clean. The index lands in worktrees/w1. claustrum's own rule, not a measured row.
func TestInstallWorktreeIndexResolvedPath(t *testing.T) {
	src, _ := indexSource(t)
	base := resolveTestRoot(t, t.TempDir())
	reg := filepath.Join(base, "worktrees")
	mkdirForTest(t, filepath.Join(reg, "a", "b"))
	mkdirForTest(t, filepath.Join(reg, "w1"))
	mkdirForTest(t, filepath.Join(base, "w1"))
	if err := os.Symlink(filepath.Join("a", "b"), filepath.Join(reg, "L")); err != nil {
		t.Fatal(err)
	}
	if err := installWorktreeIndex(src, reg+"/L/../../w1"); err != nil {
		t.Fatalf("installWorktreeIndex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reg, "w1", "index")); err != nil {
		t.Errorf("the registration that the kernel names holds no index: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "w1", "index")); err == nil {
		t.Errorf("the index landed in the folder that a lexical clean names")
	}
}

// TestRemoveCreatedRegistrationResolvedPath pins that the rollback deletes the
// registration that its check verified, not the folder that a lexical clean of the
// raw path names. The .git file of the leaf names worktrees/L/../../objects, and L
// is a symlink to a/b. For the kernel that is worktrees/objects, a forged folder
// whose gitdir record names the leaf. After a clean it is the object store beside
// worktrees, which holds a forged gitdir record too. The object store must stay.
// Everything is under the test's own folder.
func TestRemoveCreatedRegistrationResolvedPath(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "repo")
	gitDir := filepath.Join(repo, ".git")
	reg := filepath.Join(gitDir, "worktrees")
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	kept := filepath.Join(gitDir, "objects", "keep")
	writeFile(t, kept, "object store\n", 0o644)
	mkdirForTest(t, filepath.Join(reg, "a", "b"))
	if err := os.Symlink(filepath.Join("a", "b"), filepath.Join(reg, "L")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(reg, "objects", "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(gitDir, "objects", "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+reg+"/L/../../objects\n", 0o644)

	registration := createdWorktreeAdminDir(repo, leaf)
	if registration.resolved == "" {
		t.Fatal("the check refused the forged registration, so the test stages nothing")
	}
	if err := removeCreatedRegistration(registration); err != nil {
		t.Errorf("removeCreatedRegistration: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the object store lost its file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(reg, "objects")); !os.IsNotExist(err) {
		t.Errorf("the verified registration still exists (Lstat err %v), want it deleted", err)
	}

	// A registration below a direct child is refused, and nothing is deleted.
	regInfo, err := os.Stat(reg)
	if err != nil {
		t.Fatal(err)
	}
	deep := createdRegistration{resolved: filepath.Join(reg, "a", "b"), registry: reg, registryInfo: regInfo}
	if err := removeCreatedRegistration(deep); err == nil {
		t.Errorf("removeCreatedRegistration of a nested path = nil, want a refusal")
	}
	if _, err := os.Stat(filepath.Join(reg, "a", "b")); err != nil {
		t.Errorf("the nested folder was deleted: %v", err)
	}

	// A registrations directory with another identity than at the check is refused,
	// and nothing is deleted. That is the state after a swap of the directory.
	otherInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	swapped := createdRegistration{resolved: filepath.Join(reg, "a"), registry: reg, registryInfo: otherInfo}
	if err := removeCreatedRegistration(swapped); err == nil {
		t.Errorf("removeCreatedRegistration with another directory identity = nil, want a refusal")
	}
	if _, err := os.Stat(filepath.Join(reg, "a")); err != nil {
		t.Errorf("the registration was deleted in a directory that was not checked: %v", err)
	}
	// The control: with the identity of the check, the same registration goes.
	swapped.registryInfo = regInfo
	if err := removeCreatedRegistration(swapped); err != nil {
		t.Errorf("removeCreatedRegistration with the checked identity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reg, "a")); !os.IsNotExist(err) {
		t.Errorf("the registration stays with the checked identity (Stat err %v)", err)
	}
}

// TestRelativeAdminDirThroughSymlink pins that a relative gitdir value counts from
// the resolved leaf. git writes such a value with worktree.useRelativePaths. The
// leaf is sent through a symlink that sits deeper than its target, so the ".."
// parts of the value lead nowhere from the path as sent. The index lands in the
// real registration, and the rollback check finds it. claustrum's own rule.
func TestRelativeAdminDirThroughSymlink(t *testing.T) {
	src, _ := indexSource(t)
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "repo")
	reg := filepath.Join(repo, ".git", "worktrees", "w1")
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	writeFile(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: ../../../.git/worktrees/w1\n", 0o644)
	deep := filepath.Join(base, "x", "y", "z")
	mkdirForTest(t, deep)
	if err := os.Symlink(filepath.Dir(leaf), filepath.Join(deep, "link")); err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(deep, "link", "w1")

	if err := installWorktreeIndex(src, absoluteAdminDir(sent, worktreeAdminDir(sent))); err != nil {
		t.Fatalf("installWorktreeIndex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reg, "index")); err != nil {
		t.Errorf("the real registration holds no index: %v", err)
	}
	if got := createdWorktreeAdminDir(repo, sent); got.resolved != reg {
		t.Errorf("createdWorktreeAdminDir = %q, want the real registration %s", got.resolved, reg)
	}
}

// TestInstallWorktreeIndexCleanPathTexts pins that a clean admin dir, as git writes
// it, is not resolved first. The errors then name the registration and the file,
// relative to the registrations directory. 89cb6289 gives the first text in cell
// Z10 and the second in cell Z11a (macOS VM). The third state is not measured.
func TestInstallWorktreeIndexCleanPathTexts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a mode without the search bit does not stop root")
	}
	src, _ := indexSource(t)
	for _, tc := range []struct {
		name, lock string
		make       bool
		want       string
	}{
		{"Z10 registration gone", "", false, "openat w1/index: no such file or directory"},
		{"Z11a registrations directory without search", "repo/.git/worktrees", true, "openat w1/index: permission denied"},
		{"git dir without search", "repo/.git", true, "open <registrations>: permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := resolveTestRoot(t, t.TempDir())
			adminDir := filepath.Join(base, "repo", ".git", "worktrees", "w1")
			mkdirForTest(t, filepath.Dir(adminDir))
			if tc.make {
				mkdirForTest(t, adminDir)
			}
			if tc.lock != "" {
				chmodForTest(t, filepath.Join(base, tc.lock), 0o600)
			}
			want := strings.Replace(tc.want, "<registrations>", filepath.Dir(adminDir), 1)
			if err := installWorktreeIndex(src, adminDir); err == nil || err.Error() != want {
				t.Errorf("installWorktreeIndex = %v, want %s", err, want)
			}
		})
	}
}

// TestInstallWorktreeIndexErrors pins the errors of a placement. A registration
// without its write bit names the registration and the file, relative to the
// registrations directory (rows A14, A14f and A14b). A temporary index that is gone
// names its path (cell X1). A registrations directory that is gone fails too.
func TestInstallWorktreeIndexErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	src, _ := indexSource(t)
	adminDir := filepath.Join(t.TempDir(), "worktrees", "w1")
	mkdirForTest(t, adminDir)
	chmodForTest(t, adminDir, 0o500)
	err := installWorktreeIndex(src, adminDir)
	if err == nil || err.Error() != "openat w1/index: permission denied" {
		t.Errorf("installWorktreeIndex = %v, want openat w1/index: permission denied", err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := installWorktreeIndex(gone, adminDir); err == nil || err.Error() != "open "+gone+": no such file or directory" {
		t.Errorf("installWorktreeIndex of a missing temporary index = %v, want open %s: no such file or directory", err, gone)
	}
	if err := installWorktreeIndex(src, filepath.Join(t.TempDir(), "gone", "w1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("installWorktreeIndex into a missing registrations directory = %v, want a not-exist error", err)
	}
}

// TestIndexInstallText pins the text of a failed placement: the stderr of the
// checkout and the error with nothing between them, cut at 512 bytes as one text.
// The cells are those of 89cb6289 on a Linux VM.
//
//   - Rows A14 and A14f: a stderr that ends with one newline gives one space.
//   - Cell Y2a: no final newline gives no space. Cell Y2b: two give two spaces.
//   - Cell X2: no stderr gives the error alone.
//   - Cells Y1b, Y1a and Y1c: a stderr of 478 bytes keeps the whole error, 500
//     bytes keep 12 bytes of it, and 512 bytes keep none.
//   - Cell X3: a stderr over 512 bytes gives its first 512 bytes and no error.
func TestIndexInstallText(t *testing.T) {
	const text = "openat w1/index: permission denied"
	err := errors.New(text)
	long := strings.Repeat("s5err-xx\n", 151)
	x := func(n int) string { return strings.Repeat("x", n) }
	for _, tc := range []struct{ name, stderr, want string }{
		{"A14 hints", "hint: a\nhint: b\n", "hint: a hint: b " + text},
		{"Y2a no final newline", "0035_0040_", "0035_0040_" + text},
		{"Y2b two final newlines", "0035_0040_\n\n", "0035_0040_  " + text},
		{"X2 no stderr", "", text},
		{"Y1b 478 bytes", x(477) + "\n", x(477) + " " + text},
		{"Y1a 500 bytes", x(499) + "\n", x(499) + " openat w1/in"},
		{"Y1c 512 bytes", x(511) + "\n", x(511)},
		{"X3 over the cap", long, strings.ReplaceAll(long[:512], "\n", " ")},
	} {
		if got := indexInstallText(tc.stderr, err); got != tc.want {
			t.Errorf("%s: indexInstallText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWorktreeCreateIndexIgnoresSharedRepository pins row A13. With
// core.sharedRepository group and umask 0022, git writes its temporary index with
// mode 0664. The index of the new worktree is 0644 all the same. The umask is
// process-wide, so this test does not run in parallel.
//
// The git stub records the mode of the temporary index. If that mode is not 0664,
// the fixture does not stage row A13, and the test fails with that reason.
func TestWorktreeCreateIndexIgnoresSharedRepository(t *testing.T) {
	_, repo := createModesRepo(t)
	runGit(t, repo, "config", "core.sharedRepository", "group")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	modeLog := filepath.Join(t.TempDir(), "idxmode.txt")
	t.Setenv("CLAUSTRUM_GITSTUB_IDXMODE", modeLog)
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	wp := filepath.Join(repo, ".claude", "worktrees", "wt")
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "s1", "worktreePath": wp}))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s, want success", raw)
	}
	if b, _ := os.ReadFile(modeLog); strings.TrimSpace(string(b)) != "0664" {
		t.Fatalf("git wrote its temporary index with mode %q, want 0664: this fixture does not stage row A13", strings.TrimSpace(string(b)))
	}
	if got := permOf(t, filepath.Join(repo, ".git", "worktrees", "wt", "index")); got != 0o644 {
		t.Errorf("index mode = %#o, want 0644", got)
	}
}

// indexInstallFixture starts the git stub for a create whose index placement fails,
// and returns the fixture, the server and the TMPDIR of the daemon. D5 is off.
func indexInstallFixture(t *testing.T) (wtFixture, *server, string) {
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

// requireNoIndexTemp fails the test if tmp holds a temporary index folder of the
// daemon. Other names do not count: a git of the host can leave its own files there.
func requireNoIndexTemp(t *testing.T, tmp string) {
	t.Helper()
	ents, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), checkoutIndexTempPrefix) {
			t.Errorf("TMPDIR holds %s after the create, want the temporary index folder removed", e.Name())
		}
	}
}

// checkoutFailedHead starts every frame of a failed placement here. The stub prints
// "hint: synthetic" first. The real git can add hint lines after it, so the tests
// pin the frame before and after them only.
const checkoutFailedHead = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): hint: synthetic`

// TestWorktreeCreateIndexInstallFails pins the answer to a registration that
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
func TestWorktreeCreateIndexInstallFails(t *testing.T) {
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
			f, s, tmp := indexInstallFixture(t)
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
			requireNoIndexTemp(t, tmp)
		})
	}
}

// TestWorktreeCreateTempIndexGone pins cell X1 (Linux and macOS VMs). The git stub
// runs the real read-tree and then deletes the temporary index. The request answers
// worktree_add_failed "(checkout)" with the stderr of the checkout and the open
// text, and the whole rollback runs: the leaf, the registration and branch w1 go.
// The temporary folder in the text has claustrum's own prefix.
func TestWorktreeCreateTempIndexGone(t *testing.T) {
	f, s, tmp := indexInstallFixture(t)
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "rmindex")
	slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

	raw, _ := f.create(t, s, "w1", "", 0)
	tail := regexp.MustCompile(regexp.QuoteMeta(strings.Trim(jsonString(t, " open "+tmp+"/"+checkoutIndexTempPrefix), `"`)) +
		`[0-9]+/index: no such file or directory","errorCode":"worktree_add_failed"\}\}$`)
	if !strings.HasPrefix(raw, checkoutFailedHead) || !tail.MatchString(raw) {
		t.Fatalf("reply = %s\nwant %s … %s", raw, checkoutFailedHead, tail)
	}
	f.assertRolledBack(t, nil, false)
	requireNoIndexTemp(t, tmp)
}

// TestInstallWorktreeIndexOrder pins the order of the install against the rows of
// 89cb6289 that hold an entry at the index: the exclusive create first, then one
// plain remove on "file exists", then the create again.
//
//   - Cell Y3 (Linux VM): a file, registration 0500: the remove fails.
//   - Cell Y7 (Linux VM): a folder that holds a file: the remove fails.
//   - Cell Z11b (macOS VM): a file, registrations directory 0600: the create fails
//     first, so the text is the openat one.
//   - Cell Z7a (macOS VM): an empty folder is replaced by the index.
//   - Cell Z7b (macOS VM): a symlink goes, its target stays, and the index is a file.
//
// Rows A14, X8, Z10 and Z11a have their own tests above.
func TestInstallWorktreeIndexOrder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a mode without the write or search bit does not stop root")
	}
	for _, tc := range []struct {
		name, entry, lock, want string
	}{
		{"Y3", "file", "w1", "removeat w1/index: permission denied"},
		{"Y7", "full folder", "", "removeat w1/index: directory not empty"},
		{"Z11b", "file", "registrations", "openat w1/index: permission denied"},
		{"Z7a", "empty folder", "", ""},
		{"Z7b", "symlink", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := indexSource(t)
			base := resolveTestRoot(t, t.TempDir())
			reg := filepath.Join(base, "worktrees")
			adminDir := filepath.Join(reg, "w1")
			dst := filepath.Join(adminDir, "index")
			target := filepath.Join(base, "target")
			writeFile(t, target, "target\n", 0o644)
			switch tc.entry {
			case "file":
				writeFile(t, dst, "OTHER-CONTENT\n", 0o600)
			case "full folder":
				writeFile(t, filepath.Join(dst, "f"), "f\n", 0o644)
			case "empty folder":
				mkdirForTest(t, dst)
			case "symlink":
				mkdirForTest(t, adminDir)
				if err := os.Symlink(target, dst); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.lock {
			case "w1":
				chmodForTest(t, adminDir, 0o500)
			case "registrations":
				chmodForTest(t, reg, 0o600)
			}
			err := installWorktreeIndex(src, adminDir)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Errorf("installWorktreeIndex = %v, want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("installWorktreeIndex: %v", err)
			}
			fi, err := os.Lstat(dst)
			if err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("the index is not a regular file: %v, %v", fi, err)
			}
			if b, _ := os.ReadFile(dst); string(b) != "0123456789" {
				t.Errorf("index = %q, want the bytes of the temporary index", b)
			}
			if b, _ := os.ReadFile(target); string(b) != "target\n" {
				t.Errorf("the target of the symlink = %q, want it untouched", b)
			}
		})
	}
}

// TestRelativeBackPointer pins cell Z9b (macOS VM). git wrote both records as
// relative paths, and the worktree root is a symlink three levels deeper than its
// target. The rollback check must find the registration, so that the rollback
// takes the same path as in row A14. The two values are those of the cell.
func TestRelativeBackPointer(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "T")
	reg := filepath.Join(repo, ".git", "worktrees", "w1")
	leaf := filepath.Join(base, "real", "cp", "w1")
	writeFile(t, filepath.Join(reg, "gitdir"), "../../../../real/cp/w1/.git\n", 0o644)
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: ../../../T/.git/worktrees/w1\n", 0o644)
	deep := filepath.Join(base, "a", "b", "c")
	mkdirForTest(t, deep)
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(deep, "R")); err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(deep, "R", "cp", "w1")
	if got := createdWorktreeAdminDir(repo, sent); got.resolved != reg || got.unreadable != nil {
		t.Errorf("createdWorktreeAdminDir = %+v, want the registration %s", got, reg)
	}
	// A record that names another worktree is still refused.
	writeFile(t, filepath.Join(reg, "gitdir"), "../../../../real/cp/w2/.git\n", 0o644)
	if got := createdWorktreeAdminDir(repo, sent); got.resolved != "" || got.unreadable != nil {
		t.Errorf("createdWorktreeAdminDir = %+v for the record of another worktree, want the zero value", got)
	}
}

// TestWorktreeCreateRegistrationsUnreadable pins the frame and the disk of cell
// Z11a (macOS VM). The git stub runs the real read-tree
// and then sets the registrations directory to mode 0600. The index cannot be
// placed, and the rollback cannot read the back-pointer of the registration. The
// frame carries the openat text and the registration clause, the leaf goes, and
// the registration and branch w1 stay: no branch step runs. claustrum attempts no
// delete there, and the text in the parentheses has the shape of the cell.
func TestWorktreeCreateRegistrationsUnreadable(t *testing.T) {
	f, s, tmp := indexInstallFixture(t)
	t.Cleanup(func() { _ = os.Chmod(f.regDir, 0o755) })
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "nosearch")
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF", f.regDir)
	slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

	raw, _ := f.create(t, s, "w1", "", 0)
	if err := os.Chmod(f.regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tail := jsonString(t, " openat w1/index: permission denied; and the undo could not finish for "+f.leaf()+
		": the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll w1: permission denied)")
	tail = tail[1:] + `,"errorCode":"worktree_add_failed"}}`
	if !strings.HasPrefix(raw, checkoutFailedHead) || !strings.HasSuffix(raw, tail) {
		t.Fatalf("reply = %s\nwant %s … %s", raw, checkoutFailedHead, tail)
	}
	if got := f.leafEntries(t); got != nil {
		t.Errorf("leaf entries = %q, want the leaf removed", got)
	}
	if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
		t.Errorf("registrations = %q, want w1 kept", got)
	}
	if !f.hasRef(t, "w1") {
		t.Errorf("refs/heads/w1 was deleted, want it kept beside a registration that stays")
	}
	requireNoIndexTemp(t, tmp)
}

// TestUnreadableBackPointerOutsideRegistry pins that only a registration inside the
// registrations directory counts as one that stays. The .git file of the leaf
// names a folder outside it whose gitdir record cannot be read. The check answers
// the zero value, as on main: no clause, and the branch step runs.
func TestUnreadableBackPointerOutsideRegistry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a mode without the search bit does not stop root")
	}
	base := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(base, "repo")
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	mkdirForTest(t, filepath.Join(repo, ".git", "worktrees"))
	for _, tc := range []struct {
		name, admin string
		want        bool
	}{
		{"outside", filepath.Join(base, "other", "w1"), false},
		{"inside", filepath.Join(repo, ".git", "worktrees", "w1"), true},
	} {
		writeFile(t, filepath.Join(tc.admin, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
		chmodForTest(t, filepath.Dir(tc.admin), 0o600)
		writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+tc.admin+"\n", 0o644)
		got := createdWorktreeAdminDir(repo, leaf)
		_ = os.Chmod(filepath.Dir(tc.admin), 0o755)
		if got.resolved != "" || (got.unreadable != nil) != tc.want {
			t.Errorf("%s: createdWorktreeAdminDir = %+v, want unreadable set: %v", tc.name, got, tc.want)
		}
		if tc.want && got.unreadable.Error() != "RemoveAll w1: permission denied" {
			t.Errorf("%s: unreadable = %v, want RemoveAll w1: permission denied", tc.name, got.unreadable)
		}
	}
}
