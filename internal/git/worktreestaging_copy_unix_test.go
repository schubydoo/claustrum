//go:build unix

package git

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStagedAddInAFolderWithSearchOnly: the folder of the daemon has mode 0100
// after the first request. A create answers privateCopyText with the error of
// the mkdir of `.s<n>`, as with mode 0500. 4c111e86 answers that text in both
// states (measured on Linux).
func TestStagedAddInAFolderWithSearchOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0100")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	start := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + strings.Trim(jsonQuoted(t, privateCopyText), `"`) + `mkdir ` +
		strings.Trim(jsonQuoted(t, filepath.Join(dir, addCopyPrefix)), `"`)
	rest, ok := strings.CutPrefix(raw, start)
	n, tail, _ := strings.Cut(rest, ":")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" || tail != ` permission denied","errorCode":"worktree_add_failed"}}` {
		t.Errorf("git.worktree_create = %s\nwant it to start %s<n>: permission denied", raw, start)
	}
}

// umaskModes are the modes that a new file of mode 0666 and a new folder of
// mode 0777 get in dir under the umask of the test.
func umaskModes(t *testing.T, dir string) (file, folder os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, "probe-file")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Mkdir(filepath.Join(dir, "probe-folder"), 0o777); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Join(dir, "probe-folder"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), di.Mode().Perm()
}

// TestStagedCopyModes: in `.s<n>`, HEAD, config and info/exclude are made
// with mode 0666 and info, objects and refs with 0777, less the umask. The
// folder, worktrees and each entry keep 0700, and the entry files keep 0600.
// Under umask 022 4c111e86 gives 0644 and 0755 (measured on Linux).
func TestStagedCopyModes(t *testing.T) {
	file, folder := umaskModes(t, t.TempDir())
	if file == 0o600 && folder == 0o700 {
		t.Skip("the umask of the test gives the old modes")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	want := map[string]os.FileMode{
		".": 0o700, "HEAD": file, "config": file, "info": folder, "info/exclude": file, "objects": folder, "refs": folder,
		"worktrees": 0o700, "worktrees/wr": 0o700, "worktrees/wr/HEAD": 0o600, "worktrees/wr/commondir": 0o600, "worktrees/wr/gitdir": 0o600,
	}
	for name, mode := range want {
		fi, err := os.Lstat(filepath.Join(st.dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if fi.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v", name, fi.Mode().Perm(), mode)
		}
	}
}

// TestStagedCopyOfAnUnreadableGitdir: a `gitdir` of a registration with mode
// 000 gets an empty copy in `.s<n>`. 4c111e86 writes a `gitdir` there
// (measured on Linux). The empty text is claustrum's own.
func TestStagedCopyOfAnUnreadableGitdir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	gitdir := filepath.Join(f.gitDir, "worktrees", "wr", "gitdir")
	if err := os.Chmod(gitdir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitdir, 0o644) })
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	got, err := os.ReadFile(filepath.Join(st.dir, "worktrees", "wr", "gitdir"))
	if err != nil || len(got) != 0 {
		t.Errorf("the copy of gitdir = %q, %v, want an empty file", got, err)
	}
}

// manyRegistrations adds n real folders `e<i>` to `<gitDir>/worktrees`.
func manyRegistrations(t *testing.T, gitDir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.Mkdir(filepath.Join(gitDir, "worktrees", "e"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStagedAddRegistrationLimit: a repository with more than 4096
// registrations is refused before the add with registrationLimitText. 4096
// pass. 4c111e86 refuses 4097 old entries and adds with 4096 (measured on
// Linux).
func TestStagedAddRegistrationLimit(t *testing.T) {
	for _, tc := range []struct {
		total   int
		refused bool
	}{{registrationLimit, false}, {registrationLimit + 1, true}} {
		f := newRepoFolderFixture(t)
		clearFolderEnv(t)
		u := f.useOf(t, f.repo)
		manyRegistrations(t, f.gitDir, tc.total-1) // the fixture has wr
		_, stderr, err := stagedAdd(t, f, u)
		var own *addOwnFiles
		refused := errors.As(err, &own) && own.text == registrationLimitText
		if refused != tc.refused || (tc.refused && stderr != "") {
			t.Errorf("%d registrations: the add = %v, %q, want refused %v", tc.total, err, stderr, tc.refused)
		}
	}
}

// TestCreateOverTheRegistrationLimit: the frame of a create with 4097 old
// entries, as 4c111e86 answers it on Linux (measured). The leaf goes.
func TestCreateOverTheRegistrationLimit(t *testing.T) {
	f := newSeededShadowFixture(t)
	manyRegistrations(t, filepath.Join(f.repo, ".git"), registrationLimit)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"cannot add a worktree: the repository has more than 4096 worktrees; remove some (git worktree prune, git worktree remove)","errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}

// TestCreateWritesTheHeadCopyAgain: after the first request the copy of HEAD
// of the repository has mode 000, or no bytes. A create writes it again and
// succeeds, as 4c111e86 does (measured on Linux). A folder there stays, and
// the create fails: 4c111e86 removes the folder, and claustrum builds no such
// delete.
func TestCreateWritesTheHeadCopyAgain(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	for _, tc := range []struct {
		name string
		mut  func(t *testing.T, head string)
		ok   bool
	}{
		{"mode 000", func(t *testing.T, head string) {
			if err := os.Chmod(head, 0); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"no bytes", func(t *testing.T, head string) { mustWrite(t, head, "") }, true},
		{"a folder", func(t *testing.T, head string) {
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(head, 0o700); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			head := filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "HEAD")
			want := mustRead(t, head)
			tc.mut(t, head)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if got := strings.Contains(raw, `"success":true`); got != tc.ok {
				t.Errorf("git.worktree_create = %s, want success %v", raw, tc.ok)
			}
			fi, err := os.Lstat(head)
			switch {
			case err != nil:
				t.Errorf("the copy of HEAD: %v", err)
			case tc.ok && (!fi.Mode().IsRegular() || mustRead(t, head) != want):
				t.Errorf("the copy of HEAD is %v, want a file with %q", fi.Mode(), want)
			case !tc.ok && !fi.IsDir():
				t.Errorf("the copy of HEAD is %v, want the folder kept", fi.Mode())
			}
		})
	}
}
