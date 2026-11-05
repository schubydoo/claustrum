package git

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCopyRegistrationTreeCopiesALargeFile: a file of the new registration
// over 1 MiB goes into the staging folder whole. b3081b51 copies an
// `info/sparse-checkout` of 1 MiB + 1 and of 4 MiB (measured on Linux).
func TestCopyRegistrationTreeCopiesALargeFile(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	big := bytes.Repeat([]byte("/a\n"), 4<<20/3+1)
	for _, d := range []string{filepath.Join(src, "info"), filepath.Join(dst, "s")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "info", "sparse-checkout"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyRegistrationTree(openTestRoot(t, src), openTestRoot(t, dst), "s", ""); err != nil {
		t.Fatalf("the copy = %v, want no error", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "s", "info", "sparse-checkout")); err != nil || !bytes.Equal(got, big) {
		t.Errorf("the copy has %d bytes, %v, want %d", len(got), err, len(big))
	}
}

// refsFixture is a git directory with the registration wr and its folder
// `reftable`, which holds tables.list and the tables that it names.
func refsFixture(t *testing.T, tables ...string) (git, reftable string) {
	t.Helper()
	git = t.TempDir()
	reftable = filepath.Join(git, "worktrees", "wr", "reftable")
	if err := os.MkdirAll(reftable, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range tables {
		mustWrite(t, filepath.Join(reftable, name), "table "+name+"\n")
	}
	mustWrite(t, filepath.Join(reftable, "tables.list"), strings.Join(tables, "\n")+"\n")
	return git, reftable
}

// missingCause is the error of the system for a file that is not there.
func missingCause(t *testing.T) string {
	t.Helper()
	_, err := openTestRoot(t, t.TempDir()).Lstat("gone")
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("the Lstat of a missing file = %v", err)
	}
	return pe.Err.Error()
}

// TestCopyEntryRefs pins the copy of the refs of a registration into `.s<n>`.
// The list and each table that it names go there. A folder in `reftable`, a
// table that the list does not name, a `reftable` that is a regular file and
// no `reftable` pass. A named table that is missing refuses the create with
// the text of b3081b51 (measured on Linux and Windows).
func TestCopyEntryRefs(t *testing.T) {
	t.Run("a good folder", func(t *testing.T) {
		git, reftable := refsFixture(t, "0x1-0x2-aa.ref", "0x3-0x3-bb.ref")
		mustWrite(t, filepath.Join(reftable, "0xf-0xf-cc.ref"), "extra\n")
		if err := os.Mkdir(filepath.Join(reftable, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wr"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr"); err != nil {
			t.Fatalf("the copy = %v, want no error", err)
		}
		names, err := os.ReadDir(filepath.Join(dir, "worktrees", "wr", "reftable"))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range names {
			got = append(got, n.Name())
		}
		if want := []string{"0x1-0x2-aa.ref", "0x3-0x3-bb.ref", "tables.list"}; !slices.Equal(got, want) {
			t.Errorf("the copy holds %v, want %v", got, want)
		}
		if got := mustRead(t, filepath.Join(dir, "worktrees", "wr", "reftable", "0x3-0x3-bb.ref")); got != "table 0x3-0x3-bb.ref\n" {
			t.Errorf("the copied table = %q", got)
		}
	})
	for _, row := range []struct {
		name  string
		stage func(t *testing.T, git string)
	}{
		{"no reftable", func(t *testing.T, git string) {
			if err := os.MkdirAll(filepath.Join(git, "worktrees", "wr"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a reftable that is a regular file", func(t *testing.T, git string) {
			mustWrite(t, filepath.Join(git, "worktrees", "wr", "reftable"), "x\n")
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			git, dir := t.TempDir(), t.TempDir()
			row.stage(t, git)
			if err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr"); err != nil {
				t.Errorf("the copy = %v, want no error", err)
			}
			if names, _ := os.ReadDir(dir); len(names) != 0 {
				t.Errorf("the copy made %v, want nothing", names)
			}
		})
	}
	t.Run("a named table that is missing", func(t *testing.T) {
		git, reftable := refsFixture(t, "0x1-0x2-aa.ref")
		mustWrite(t, filepath.Join(reftable, "tables.list"), "0x1-0x2-aa.ref\n0x99-0x9a-deadbeef.ref\n")
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wr"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr")
		want := `"worktrees/wr/reftable": "0x99-0x9a-deadbeef.ref": ` + missingCause(t)
		var refsErr *refsCopyError
		if !errors.As(err, &refsErr) || refsErr.text != want {
			t.Errorf("the copy = %v, want %s", err, want)
		}
	})
	// A link, a junction on Windows, to a folder outside the git directory.
	// The cause has the separators of the system (measured on Linux and
	// Windows).
	t.Run("a reftable that is a link to a folder outside", func(t *testing.T) {
		git, reftable := refsFixture(t, "0x1-0x2-aa.ref")
		out := filepath.Join(t.TempDir(), "rt-out")
		if err := os.Rename(reftable, out); err != nil {
			t.Fatal(err)
		}
		linkAt(t, reftable, out)
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wr"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr")
		want := `"worktrees/wr/reftable": openat ` + filepath.Join("worktrees", "wr", "reftable", "tables.list") + ": path escapes from parent"
		var refsErr *refsCopyError
		if !errors.As(err, &refsErr) || refsErr.text != want {
			t.Errorf("the copy = %v, want %s", err, want)
		}
		if _, err := os.Lstat(filepath.Join(dir, "worktrees", "wr", "reftable")); !os.IsNotExist(err) {
			t.Errorf("the folder reftable in the copy: %v, want none", err)
		}
	})
}

// TestCreateRefusesAMissingTableOfARegistration runs git.worktree_create on a
// repository whose registration wr lists a table that is missing. b3081b51
// answers the refusal of the copy of the refs, with no `worktree add` call
// (measured on Linux and Windows). The registration keeps its folder
// `reftable`, and the empty leaf goes.
func TestCreateRefusesAMissingTableOfARegistration(t *testing.T) {
	f := newSeededShadowFixture(t)
	reftable := filepath.Join(f.gitDir, "worktrees", "wr", "reftable")
	if err := os.Mkdir(reftable, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(reftable, "tables.list"), "0x99-0x9a-deadbeef.ref\n")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		jsonQuoted(t, refsCopyText+`"worktrees/wr/reftable": "0x99-0x9a-deadbeef.ref": `+missingCause(t)) +
		`,"errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	for _, c := range calls {
		if i := slices.Index(c.argv, "worktree"); i >= 0 && i+1 < len(c.argv) && c.argv[i+1] == "add" {
			t.Errorf("a worktree add ran: %v", c.argv)
		}
	}
	if got := mustRead(t, filepath.Join(reftable, "tables.list")); got != "0x99-0x9a-deadbeef.ref\n" {
		t.Errorf("tables.list = %q, want it kept", got)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}

// TestCopyEntryRefsRefusesABadList pins three faults of the copy of the refs
// that the list of the registration sets off (claustrum's own, not
// measured). A name that is not one path element and a name that the list
// gives twice are refused. A folder `reftable` that is already in the copy
// fails the step with the error of the system.
func TestCopyEntryRefsRefusesABadList(t *testing.T) {
	stage := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wr"), 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for _, table := range []string{"..", "sub/0x1-0x2-aa.ref", `sub\0x1-0x2-aa.ref`} {
		t.Run("the name "+table, func(t *testing.T) {
			git, reftable := refsFixture(t, "0x1-0x2-aa.ref")
			mustWrite(t, filepath.Join(reftable, "tables.list"), table+"\n")
			err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, stage(t)), "wr")
			want := `"worktrees/wr/reftable": ` + quoteOperand(table) + ": " + fs.ErrInvalid.Error()
			if err == nil || err.Error() != want {
				t.Errorf("the copy = %v, want %s", err, want)
			}
		})
	}
	t.Run("a name that the list gives twice", func(t *testing.T) {
		git, reftable := refsFixture(t, "0x1-0x2-aa.ref")
		mustWrite(t, filepath.Join(reftable, "tables.list"), "0x1-0x2-aa.ref\n0x1-0x2-aa.ref\n")
		dir := stage(t)
		err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr")
		var refsErr *refsCopyError
		if !errors.As(err, &refsErr) || !strings.HasPrefix(refsErr.text, `"worktrees/wr/reftable": "0x1-0x2-aa.ref": `) {
			t.Errorf("the copy = %v, want a refusal of the second 0x1-0x2-aa.ref", err)
		}
		if got := mustRead(t, filepath.Join(dir, "worktrees", "wr", "reftable", "0x1-0x2-aa.ref")); got != "table 0x1-0x2-aa.ref\n" {
			t.Errorf("the copied table = %q", got)
		}
	})
	t.Run("a reftable that is already in the copy", func(t *testing.T) {
		git, _ := refsFixture(t, "0x1-0x2-aa.ref")
		dir := stage(t)
		if err := os.Mkdir(filepath.Join(dir, "worktrees", "wr", "reftable"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := copyEntryRefs(openTestRoot(t, git), openTestRoot(t, dir), "wr")
		var refsErr *refsCopyError
		if !errors.Is(err, fs.ErrExist) || errors.As(err, &refsErr) {
			t.Errorf("the copy = %v, want the mkdir error", err)
		}
	})
}

// TestCreateRefusesAReftableLinkOfTheGitDirectory runs git.worktree_create with
// the step of Windows on a repository whose `.git/reftable` is a link, a
// junction on Windows, to a folder outside the git directory. b3081b51 answers
// the refusal of the copy of the refs, with no `worktree add` call (measured on
// Windows). Linux and macOS do not take this step (not measured there). No
// `reftable`, and a `reftable` that is a regular file, pass the step.
func TestCreateRefusesAReftableLinkOfTheGitDirectory(t *testing.T) {
	old := mainRefsOpenTest
	mainRefsOpenTest = true
	t.Cleanup(func() { mainRefsOpenTest = old })
	t.Run("a link to a folder outside", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		out := filepath.Join(t.TempDir(), "rt-out")
		if err := os.Mkdir(out, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(out, "tables.list"), "")
		linkAt(t, filepath.Join(f.gitDir, "reftable"), out)
		leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
		raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
		want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
			jsonQuoted(t, refsCopyText+`"reftable": openat `+filepath.Join("reftable", "tables.list")+": path escapes from parent") +
			`,"errorCode":"worktree_add_failed"}}`
		if raw != want {
			t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
		}
		for _, c := range calls {
			if i := slices.Index(c.argv, "worktree"); i >= 0 && i+1 < len(c.argv) && c.argv[i+1] == "add" {
				t.Errorf("a worktree add ran: %v", c.argv)
			}
		}
	})
	t.Run("no reftable and a regular file", func(t *testing.T) {
		git := t.TempDir()
		if err := mainRefsFault(openTestRoot(t, git)); err != nil {
			t.Errorf("no reftable: %v, want no error", err)
		}
		mustWrite(t, filepath.Join(git, "reftable"), "x\n")
		if err := mainRefsFault(openTestRoot(t, git)); err != nil {
			t.Errorf("a regular file reftable: %v, want no error", err)
		}
	})
}
