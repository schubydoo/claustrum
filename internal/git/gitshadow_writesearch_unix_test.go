//go:build unix

package git

import (
	"os"
	"path/filepath"
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
// succeeds. `.s<n>` stays (measured on Linux). Without the mkdir of
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

// TestProbeFolderBySearchRefusals: only a permission error of a folder of
// mode 0300 passes, and `.probe` must be a real folder. A link at `.probe`
// gets the open error, and nothing is made in its target.
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
	if _, err := probeFolderBySearch(dir, openErr); err != openErr {
		t.Errorf("mode 0100: %v, want the open error", err)
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
// bits allow no write and search gets the error of its open at `.probe` and
// at `.s<n>`, as before (claustrum's own, not measured). Without the mode test
// of writeSearchFolder the steps run by path.
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
	want := "open " + dir + ": permission denied"
	if err := NewShadowHost(run).prepareProbeFolder(dir); err == nil || err.Error() != want {
		t.Errorf("prepareProbeFolder = %v, want %s", err, want)
	}
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
