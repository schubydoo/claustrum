//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSearchDaemonFolder gives the folder of the daemon of f mode 0300 (write
// and search, no read) until the end of the test. It returns that folder.
func writeSearchDaemonFolder(t *testing.T, f shadowFixture) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0300")
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// readableAgain lists the `.s<n>` folders of dir, a folder of mode 0300, with
// the read bit back for the listing only.
func readableAgain(t *testing.T, dir string) []string {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o300) }()
	return addFolders(t, dir)
}

// TestCreateInAWriteSearchDaemonFolder: with the folder of the daemon at mode
// 0300, 4c111e86 makes `.s<n>`, the leaf and the branch, and the create
// succeeds. `.s<n>` and `.checkout-<n>` stay, and the emptied
// `.readback-scratch-<n>` goes (measured on Linux). Without the mkdir of
// requestDir by path, or without probeFolderBySearch, the create fails.
func TestCreateInAWriteSearchDaemonFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	first := filepath.Join(f.repo, ".claude", "worktrees", "w0")
	if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w0", "worktreePath": first}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("the first git.worktree_create = %s", raw)
	}
	dir := writeSearchDaemonFolder(t, f)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"main","branch":"w1"}}`
	if raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if !branchExists(t, f.repo, "w1") {
		t.Error("branch w1 is missing")
	}
	if fi, err := os.Lstat(filepath.Join(f.repo, ".git", "worktrees", "w1")); err != nil || !fi.IsDir() {
		t.Errorf("the registration of w1: %v", err)
	}
	if got := readableAgain(t, dir); len(got) != 1 {
		t.Errorf("the folder of the daemon holds %q, want the one .s<n> that stays", got)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	names := entryNames(t, dir)
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, checkoutDirPrefix) }) {
		t.Errorf("the folder of the daemon holds %q, want the .checkout-<n> that stays", names)
	}
	if slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, shadowScratchDirPrefix) }) {
		t.Errorf("the folder of the daemon holds %q, want no %s<n>", names, shadowScratchDirPrefix)
	}
}

// TestFailedCreateInAWriteSearchDaemonFolder: a create that fails after the
// add with the folder of the daemon at mode 0300 still runs the branch step of
// its rollback, so branch w1 goes (claustrum's own, not measured). Without the
// mkdir of requestDir by path the create fails before the add.
func TestFailedCreateInAWriteSearchDaemonFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := writeSearchDaemonFolder(t, f)
	atStep(t, "staged", func() {
		names := readableAgain(t, dir)
		if len(names) != 1 {
			t.Errorf("the folder of the daemon holds %q, want one .s<n>", names)
			return
		}
		mustWrite(t, filepath.Join(dir, names[0], worktreesSubdir, "w9", "HEAD"), "ref: refs/heads/main\n")
	})
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, "git registered 2 worktrees where one was expected; try again") {
		t.Fatalf("git.worktree_create = %s, want the failure of the move", raw)
	}
	if branchExists(t, f.repo, "w1") {
		t.Errorf("branch w1 stayed after the rollback: %s", raw)
	}
}

// TestProbeFolderInAWriteSearchDaemonFolder: with the folder of the daemon at
// mode 0300, the preparation of `.probe` makes and opens the folder by its
// path, deletes its leftovers and writes its files. Without
// probeFolderBySearch it fails with the error of the open of the folder of the
// daemon.
func TestProbeFolderInAWriteSearchDaemonFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0300")
	}
	run := resolveTestRoot(t, t.TempDir())
	dir := filepath.Join(run, shadowRootName, "d")
	probe := filepath.Join(dir, shadowProbeName)
	mustWrite(t, filepath.Join(probe, "info", "exclude"), "x\n")
	mustWrite(t, filepath.Join(probe, "worktrees", "e1", "HEAD"), "x\n")
	mustWrite(t, filepath.Join(probe, "keepme"), "kept\n")
	if err := os.Mkdir(filepath.Join(probe, "rebase-merge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := NewShadowHost(run).prepareProbeFolder(dir); err != nil {
		t.Fatalf("prepareProbeFolder: %v", err)
	}
	for _, gone := range []string{filepath.Join("info", "exclude"), "worktrees", "rebase-merge"} {
		if _, err := os.Lstat(filepath.Join(probe, gone)); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it gone", gone, err)
		}
	}
	if got := mustRead(t, filepath.Join(probe, "keepme")); got != "kept\n" {
		t.Errorf("keepme = %q, want it kept", got)
	}
	if got := mustRead(t, filepath.Join(probe, "config")); got != probeConfigText {
		t.Errorf("config = %q, want %q", got, probeConfigText)
	}
}

// TestProbeFolderBySearchRefusals: only a permission error passes, and
// `.probe` must be a real folder. Mode 0100 with no `.probe` gets the error of
// its mkdir. A link at `.probe` gets the open error, and nothing is made in
// its target.
func TestProbeFolderBySearchRefusals(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of any mode")
	}
	openErr := &os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}
	dir := t.TempDir()
	if _, err := probeFolderBySearch(dir, os.ErrNotExist); err != os.ErrNotExist {
		t.Errorf("another error: %v, want it as it is", err)
	}
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	want := "mkdir " + filepath.Join(dir, shadowProbeName) + ": permission denied"
	if _, err := probeFolderBySearch(dir, openErr); err == nil || err.Error() != want {
		t.Errorf("mode 0100: %v, want %s", err, want)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, shadowProbeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	if _, err := probeFolderBySearch(dir, openErr); err != openErr {
		t.Errorf("a link at .probe: %v, want the open error", err)
	}
	if names := entryNames(t, target); len(names) != 0 {
		t.Errorf("the target of the link holds %q, want nothing", names)
	}
}

// TestWriteSearchStepsKeepOtherOpenErrors: a folder of the daemon whose owner
// bits allow no write and search gets the error of its open at `.s<n>`, as
// before (claustrum's own, not measured). Without the mode test of
// writeSearchFolder that step runs by path. At `.probe` the mkdir by its path
// fails with its own error.
func TestWriteSearchStepsKeepOtherOpenErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of any mode")
	}
	run := resolveTestRoot(t, t.TempDir())
	dir := filepath.Join(run, shadowRootName, "d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probeWant := "mkdir " + filepath.Join(dir, shadowProbeName) + ": permission denied"
	if err := NewShadowHost(run).prepareProbeFolder(dir); err == nil || err.Error() != probeWant {
		t.Errorf("prepareProbeFolder = %v, want %s", err, probeWant)
	}
	want := "open " + dir + ": permission denied"
	u := gitUse{host: NewShadowHost(run), folder: dir}
	if _, _, err := u.requestDir(addCopyPrefix, ""); err == nil || err.Error() != want {
		t.Errorf("requestDir = %v, want %s", err, want)
	}
}

// TestProbeFolderBySearchOpenError: a `.probe` that cannot be opened in a
// folder of mode 0300 gets the open error of that folder (claustrum's own,
// not measured).
func TestProbeFolderBySearchOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of any mode")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, shadowProbeName)
	if err := os.Mkdir(probe, 0o300); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.Chmod(probe, 0o700) })
	openErr := &os.PathError{Op: "open", Path: dir, Err: os.ErrPermission}
	if root, err := probeFolderBySearch(dir, openErr); err != openErr {
		if root != nil {
			_ = root.Close()
		}
		t.Errorf("probeFolderBySearch = %v, want the open error of the folder", err)
	}
}

// TestProbeLeftoverDeleteErrorNamesItsPath: a leftover of `.probe` that cannot
// be deleted stops the step with an error that names its full path
// (probePathError, claustrum's own, not measured).
func TestProbeLeftoverDeleteErrorNamesItsPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user deletes in a folder of mode 0500")
	}
	run := resolveTestRoot(t, t.TempDir())
	dir := filepath.Join(run, shadowRootName, "d")
	info := filepath.Join(dir, shadowProbeName, "info")
	mustWrite(t, filepath.Join(info, "attributes"), "x\n")
	if err := os.Chmod(info, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
	err := NewShadowHost(run).prepareProbeFolder(dir)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(info, "attributes")+": permission denied") {
		t.Errorf("prepareProbeFolder = %v, want an error that names %s", err, filepath.Join(info, "attributes"))
	}
}

// TestRequestDirRemoveInAWriteSearchDaemonFolder: the folder of the daemon has
// mode 0300. 4c111e86 removes an empty request folder by one rmdir and keeps a
// folder with content (measured on Linux). claustrum does the same for a
// folder that requestDir made. A file at the
// name stays (claustrum's own, not measured). Without the rmdir of
// removeEmptyRequestDir the empty folder stays. With a tree remove in its
// place the file goes.
func TestRequestDirRemoveInAWriteSearchDaemonFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0300")
	}
	run := resolveTestRoot(t, t.TempDir())
	dir := filepath.Join(run, shadowRootName, "d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	u := gitUse{host: NewShadowHost(run), folder: dir}
	var calls []string
	old := rmdirRequestDir
	rmdirRequestDir = func(folder, name string, parent os.FileInfo) error {
		calls = append(calls, filepath.Join(folder, name))
		return old(folder, name, parent)
	}
	t.Cleanup(func() { rmdirRequestDir = old })
	for _, tc := range []struct {
		name  string
		fill  func(t *testing.T, path string)
		stays bool
	}{
		{"empty", func(*testing.T, string) {}, false},
		{"with content", func(t *testing.T, path string) { mustWrite(t, filepath.Join(path, "x", "f"), "x\n") }, true},
		{"a file at the name", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, path, "x\n")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = nil
			path, remove, err := u.requestDir(shadowStatusDirPrefix, "")
			if err != nil {
				t.Fatalf("requestDir: %v", err)
			}
			tc.fill(t, path)
			remove()
			if !slices.Equal(calls, []string{path}) {
				t.Errorf("rmdir calls = %q, want one of %s", calls, path)
			}
			_, err = os.Lstat(path)
			if tc.stays && err != nil {
				t.Errorf("%s: %v, want it kept", path, err)
			}
			if !tc.stays && !os.IsNotExist(err) {
				t.Errorf("%s: %v, want it gone", path, err)
			}
		})
	}
}

// TestEmptyRequestDirRemoveKeepsHome: homeguard.WipesHomeDir refuses a request
// folder that is home or holds home, so no rmdir runs (claustrum's own). The
// seam records the call and deletes nothing. Without the guard the rmdir
// runs on home.
func TestEmptyRequestDirRemoveKeepsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var calls []string
	old := rmdirRequestDir
	rmdirRequestDir = func(folder, name string, _ os.FileInfo) error {
		calls = append(calls, filepath.Join(folder, name))
		return nil
	}
	t.Cleanup(func() { rmdirRequestDir = old })
	removeEmptyRequestDir(filepath.Dir(home), filepath.Base(home), nil)
	removeEmptyRequestDir(filepath.Dir(filepath.Dir(home)), filepath.Base(filepath.Dir(home)), nil)
	if len(calls) != 0 {
		t.Errorf("rmdir calls = %q, want none", calls)
	}
	inside := filepath.Join(home, "x")
	removeEmptyRequestDir(home, "x", nil)
	if !slices.Equal(calls, []string{inside}) {
		t.Errorf("rmdir calls = %q, want one of %s", calls, inside)
	}
}

// TestRequestDirRemoveKeepsAFolderOfASwappedParent: the folder of the daemon
// has mode 0300, and after requestDir made an empty request folder, the folder
// git-shadow becomes a link to another folder. That folder holds a folder of
// mode 0300 with the name of the folder of the daemon, and in it an empty
// folder with the name of the request folder. The rmdir refuses the other
// parent, so both empty folders stay (claustrum's own). Without the identity
// test of rmdirInFolder the empty folder of the other parent goes.
func TestRequestDirRemoveKeepsAFolderOfASwappedParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0300")
	}
	run := resolveTestRoot(t, t.TempDir())
	shadow := filepath.Join(run, shadowRootName)
	dir := filepath.Join(shadow, "d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(run, "moved")
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(moved, "d"), 0o700) })
	u := gitUse{host: NewShadowHost(run), folder: dir}
	path, remove, err := u.requestDir(shadowStatusDirPrefix, "")
	if err != nil {
		t.Fatalf("requestDir: %v", err)
	}
	other := filepath.Join(run, "other")
	twin := filepath.Join(other, "d", filepath.Base(path))
	if err := os.MkdirAll(twin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(twin), 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(twin), 0o700) })
	if err := os.Rename(shadow, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, shadow); err != nil {
		t.Fatal(err)
	}
	remove()
	for _, kept := range []string{twin, filepath.Join(moved, "d", filepath.Base(path))} {
		if fi, err := os.Lstat(kept); err != nil || !fi.IsDir() {
			t.Errorf("%s: %v, want the empty folder kept", kept, err)
		}
	}
}

// TestRequestDirRemoveRmdirOnlyAfterAPermissionError: the folder of the daemon
// has mode 0300, and the folder git-shadow went before the remove. removeEntry
// then fails with no permission error, and no rmdir runs (claustrum's own).
// Without the test of the error the rmdir runs.
func TestRequestDirRemoveRmdirOnlyAfterAPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0300")
	}
	run := resolveTestRoot(t, t.TempDir())
	shadow := filepath.Join(run, shadowRootName)
	dir := filepath.Join(shadow, "d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(run, "moved")
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(moved, "d"), 0o700) })
	var calls []string
	old := rmdirRequestDir
	rmdirRequestDir = func(folder, name string, parent os.FileInfo) error {
		calls = append(calls, filepath.Join(folder, name))
		return old(folder, name, parent)
	}
	t.Cleanup(func() { rmdirRequestDir = old })
	u := gitUse{host: NewShadowHost(run), folder: dir}
	_, remove, err := u.requestDir(shadowStatusDirPrefix, "")
	if err != nil {
		t.Fatalf("requestDir: %v", err)
	}
	if err := os.Rename(shadow, moved); err != nil {
		t.Fatal(err)
	}
	remove()
	if len(calls) != 0 {
		t.Errorf("rmdir calls = %q, want none", calls)
	}
}
