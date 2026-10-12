package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The decisions of the staged `worktree add` (worktreestaging.go). These tests
// run on every system. The values are those of the Windows VM cells that each
// test names. Each test says which change of the code turns it red.

// TestIsStagingName pins the name test of the cleanup with the seven names of
// cell 48 of s3p2b-edges3-windows. Without the dash in the prefix the two names
// `.claude-new-worktree` and `.claude-new-worktreeX` go.
func TestIsStagingName(t *testing.T) {
	for name, want := range map[string]bool{
		".claude-new-worktree-0123456789abcdef": true,
		".claude-new-worktree-old1":             true,
		".claude-new-worktree-":                 true,
		".claude-new-worktree-0123456789ABCDEF": true,
		".claude-new-worktree":                  false,
		".claude-new-worktreeX":                 false,
		".claude-old-worktree-0123456789abcdef": false,
		// Not in the cell: a name with the text after its start, and other entries
		// of a git directory.
		"x.claude-new-worktree-1": false,
		"worktrees":               false,
		"HEAD":                    false,
	} {
		if got := isStagingName(name); got != want {
			t.Errorf("isStagingName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestStagingEntryIsOld pins the age test with the ages of cell 48: the four
// oldest entries that stay and the youngest that goes. The bound itself is not
// in the cell: an age of one hour goes here. A test for `>` in place of `>=`
// fails at that row, and another bound fails at the measured rows.
func TestStagingEntryIsOld(t *testing.T) {
	now := time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		age  time.Duration
		want bool
	}{
		{5 * time.Minute, false},
		{3595240 * time.Millisecond, false},
		{3595370 * time.Millisecond, false},
		{3595710 * time.Millisecond, false},
		{3596420 * time.Millisecond, false},
		{time.Hour - time.Nanosecond, false},
		{time.Hour, true},
		{3601280 * time.Millisecond, true},
		{3602750 * time.Millisecond, true},
		{121 * time.Minute, true},
	} {
		if got := stagingEntryIsOld(now.Add(-row.age), now); got != row.want {
			t.Errorf("an entry of %s: old = %v, want %v", row.age, got, row.want)
		}
	}
}

// TestGoesAsTree pins which kind of entry goes with its content: a real folder
// only. A junction is an irregular file for Go on Windows. Without the mode
// test a link or a junction is removed as a tree.
func TestGoesAsTree(t *testing.T) {
	for _, row := range []struct {
		name string
		mode fs.FileMode
		want bool
	}{
		{"a folder", fs.ModeDir | 0o755, true},
		{"a plain file", 0o644, false},
		{"a symbolic link", fs.ModeSymlink | 0o777, false},
		{"a junction", fs.ModeIrregular, false},
		{"a link with the folder bit", fs.ModeDir | fs.ModeSymlink, false},
		{"an irregular entry with the folder bit", fs.ModeDir | fs.ModeIrregular, false},
	} {
		if got := goesAsTree(row.mode); got != row.want {
			t.Errorf("%s: goesAsTree = %v, want %v", row.name, got, row.want)
		}
	}
}

// The texts of cell 49 of s3p2b-edges3-windows.
const (
	cell49Head   = "ref: refs/heads/main\n"
	cell49WsHead = "ref: refs/heads/ws\n"
	cell49Common = "../..\n"
	cell49WsDir  = "C:/o/w/repo/.claude/worktrees/ws/.git\n"
	cell49Ws2Dir = "C:/o/w/repo/.claude/worktrees/ws2/.git\n"
)

func cell49Registration(name, head, gitdir string, locked *string) registrationCopy {
	reg := registrationCopy{name: name, files: []copyEntry{
		{path: "HEAD", data: []byte(head)},
		{path: "commondir", data: []byte(cell49Common)},
		{path: "gitdir", data: []byte(gitdir)},
	}}
	if locked != nil {
		reg.files = append(reg.files, copyEntry{path: "locked", data: []byte(*locked)})
	}
	return reg
}

// TestAddCopyEntries pins the entries of `.s<n>` per state of the repository,
// with the six states of cell 49: one registration, none, two, a locked one, a
// locked one with a reason, and one whose worktree folder is gone. Each entry
// is a line `<path> <size>`, and a folder has the size `dir`. The sizes are
// those of the cell. A `worktrees` folder that is always made fails the state
// 49b, and a missing `locked` fails 49d and 49e.
func TestAddCopyEntries(t *testing.T) {
	config := []byte(strings.Repeat("c", 212))
	exclude := []byte(strings.Repeat("e", 240))
	top := []string{"HEAD 21", "config 212", "info dir", "info/exclude 240", "objects dir", "refs dir"}
	ws := []string{"worktrees dir", "worktrees/ws dir", "worktrees/ws/HEAD 19", "worktrees/ws/commondir 6", "worktrees/ws/gitdir 38"}
	empty, reason := "", "kept for the test\n"
	for _, row := range []struct {
		cell string
		regs []registrationCopy
		want []string
	}{
		{"49a, one registration", []registrationCopy{cell49Registration("ws", cell49WsHead, cell49WsDir, nil)}, ws},
		{"49b, no registration", nil, nil},
		{"49c, two registrations", []registrationCopy{
			cell49Registration("ws", cell49WsHead, cell49WsDir, nil),
			cell49Registration("ws2", "ref: refs/heads/ws2\n", cell49Ws2Dir, nil),
		}, append(slices.Clone(ws), "worktrees/ws2 dir", "worktrees/ws2/HEAD 20", "worktrees/ws2/commondir 6", "worktrees/ws2/gitdir 39")},
		{"49d, locked", []registrationCopy{cell49Registration("ws", cell49WsHead, cell49WsDir, &empty)},
			append(slices.Clone(ws), "worktrees/ws/locked 0")},
		{"49e, locked with a reason", []registrationCopy{cell49Registration("ws", cell49WsHead, cell49WsDir, &reason)},
			append(slices.Clone(ws), "worktrees/ws/locked 18")},
		{"49f, the worktree folder is gone", []registrationCopy{cell49Registration("ws", cell49WsHead, cell49WsDir, nil)}, ws},
	} {
		var got []string
		for _, e := range addCopyEntries([]byte(cell49Head), config, exclude, true, row.regs) {
			if e.dir {
				got = append(got, e.path+" dir")
			} else {
				got = append(got, fmt.Sprintf("%s %d", e.path, len(e.data)))
			}
		}
		if want := append(slices.Clone(top), row.want...); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", row.cell, got, want)
		}
	}
	// No cell has a repository with no info/exclude: the folder `info` is empty.
	for _, e := range addCopyEntries([]byte(cell49Head), config, nil, false, nil) {
		if e.path == "info/exclude" {
			t.Error("a repository with no info/exclude got that entry")
		}
	}
	// The texts go in as they are: the locked text of cell 49e.
	last := addCopyEntries([]byte(cell49Head), config, exclude, true,
		[]registrationCopy{cell49Registration("ws", cell49WsHead, cell49WsDir, &reason)})
	if e := last[len(last)-1]; e.path != "worktrees/ws/locked" || string(e.data) != reason {
		t.Errorf("the last entry = %q with %q, want worktrees/ws/locked with %q", e.path, e.data, reason)
	}
}

// TestRegistrationFiles pins the files of a registration that are copied. Cell
// 49 shows these four, and no `index`, `ORIG_HEAD`, `logs` or `refs`.
func TestRegistrationFiles(t *testing.T) {
	if want := []string{"HEAD", "commondir", "gitdir", "locked"}; !slices.Equal(registrationFiles, want) {
		t.Errorf("registrationFiles = %q, want %q", registrationFiles, want)
	}
}

// TestWorktreeGitFileText pins the text of the `.git` file of a new worktree
// after the staging step: the 38 bytes of cells 44a and 49a to 49f. A text with
// backslashes, or with no newline, fails here.
func TestWorktreeGitFileText(t *testing.T) {
	const want = "gitdir: C:/o/w/repo/.git/worktrees/w1\n"
	got := worktreeGitFileText(`C:\o\w\repo\.git`, "w1")
	if got != want || len(got) != 38 {
		t.Errorf("worktreeGitFileText = %q (%d bytes), want %q (38 bytes)", got, len(got), want)
	}
}

// TestStagedGitFileText pins which old text of the `.git` file is replaced. The
// first row has the 94 bytes of cell 44a: the path of the registration in
// `.s<n>` with forward slashes. Without the test of the end, the text of
// another folder or of another name is replaced.
func TestStagedGitFileText(t *testing.T) {
	const gitDir = `C:\o\w\repo\.git`
	const want = "gitdir: C:/o/w/repo/.git/worktrees/w1\n"
	viaCopy := "gitdir: C:/o/w/run/git-shadow/WIN_ABCDEFGHIJK-1234-0123456789abcdef/.s3621190347/worktrees/w1\n"
	if len(viaCopy) != 94 {
		t.Fatalf("the fixture text has %d bytes, want the 94 of cell 44a", len(viaCopy))
	}
	for _, row := range []struct {
		name, old string
		ok        bool
	}{
		{"the path in .s<n>, forward slashes", viaCopy, true},
		{"the path in .s<n>, backslashes", strings.ReplaceAll(viaCopy, "/", `\`), true},
		{"another .s folder", strings.Replace(viaCopy, ".s3621190347", ".s1", 1), false},
		{"another name", strings.Replace(viaCopy, "/w1\n", "/w2\n", 1), false},
		{"no newline", strings.TrimSuffix(viaCopy, "\n"), false},
		{"a second line", viaCopy + "x\n", false},
		{"no gitdir start", strings.TrimPrefix(viaCopy, "gitdir: "), false},
		{"in the repository already", want, false},
	} {
		got, ok := stagedGitFileText(row.old, ".s3621190347", "w1", gitDir)
		if ok != row.ok || (ok && got != want) {
			t.Errorf("%s: %q, %v, want %q, %v", row.name, got, ok, want, row.ok)
		}
	}
}

// TestNewRegistrationName pins the name of the registration that git added:
// the one name that the daemon did not copy. No new name, two new names and a
// name that is no single name give no move.
func TestNewRegistrationName(t *testing.T) {
	for _, row := range []struct {
		before, after []string
		want          string
	}{
		{[]string{"ws"}, []string{"w1", "ws"}, "w1"},
		{nil, []string{"w1"}, "w1"},
		{[]string{"ws", "ws2"}, []string{"w1", "ws", "ws2"}, "w1"},
		{[]string{"ws"}, []string{"ws"}, ""},
		{[]string{"ws"}, []string{"a", "b", "ws"}, ""},
		{nil, []string{".."}, ""},
		{nil, []string{`a\b`}, ""},
	} {
		got, ok := newRegistrationName(row.before, row.after)
		if got != row.want || ok != (row.want != "") {
			t.Errorf("newRegistrationName(%q, %q) = %q, %v, want %q", row.before, row.after, got, ok, row.want)
		}
	}
}

// TestWithAddCopy pins the folder variable of the `worktree add` call: the
// folder `.s<n>` in the place of the copy of the repository, and the three
// other folder variables as they are (cell 49b, the call log).
func TestWithAddCopy(t *testing.T) {
	env := []string{"A=1", `GIT_COMMON_DIR=C:\S\0a83`, `GIT_OBJECT_DIRECTORY=C:\repo\.git\objects`, `GIT_DIR=C:\repo\.git`, `GIT_WORK_TREE=C:\repo`}
	got := withAddCopy(env, `C:\S\0a83`, `C:\S\.s1314002893`)
	want := []string{"A=1", `GIT_COMMON_DIR=C:\S\.s1314002893`, `GIT_OBJECT_DIRECTORY=C:\repo\.git\objects`, `GIT_DIR=C:\repo\.git`, `GIT_WORK_TREE=C:\repo`}
	if !slices.Equal(got, want) {
		t.Errorf("withAddCopy = %q\nwant %q", got, want)
	}
	if env[1] != `GIT_COMMON_DIR=C:\S\0a83` {
		t.Error("withAddCopy changed its input")
	}
}

// stagingSweepFixture is a folder that stands for a git directory, with the
// entries of cell 48. Every entry is new. The cleanup of a test runs with a
// clock that is two hours ahead, so an entry is old unless the test gave it a
// write time that is 90 minutes ahead.
type stagingSweepFixture struct {
	dir    string
	target string
	now    time.Time
}

func newStagingSweepFixture(t *testing.T) stagingSweepFixture {
	t.Helper()
	base := resolveTestRoot(t, t.TempDir())
	f := stagingSweepFixture{dir: filepath.Join(base, "gitdir"), target: filepath.Join(base, "target"), now: time.Now().Add(2 * time.Hour)}
	young := f.now.Add(-30 * time.Minute)
	mkdir := func(name string) string {
		p := filepath.Join(f.dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	age := func(p string, at time.Time) {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("worktrees")
	// Old entries with the name form: a folder with one file, an empty folder
	// and a plain file. The file in the folder is young: only the folder counts.
	mustWrite(t, filepath.Join(mkdir(".claude-new-worktree-0123456789abcdef"), "f.txt"), "f\n")
	age(filepath.Join(f.dir, ".claude-new-worktree-0123456789abcdef", "f.txt"), young)
	mkdir(".claude-new-worktree-old1")
	mustWrite(t, filepath.Join(f.dir, ".claude-new-worktree-"), "plain\n")
	// Young entries with the name form: a folder whose file is old, and a file.
	mustWrite(t, filepath.Join(mkdir(".claude-new-worktree-young"), "f.txt"), "f\n")
	age(filepath.Join(f.dir, ".claude-new-worktree-young"), young)
	mustWrite(t, filepath.Join(f.dir, ".claude-new-worktree-youngfile"), "plain\n")
	age(filepath.Join(f.dir, ".claude-new-worktree-youngfile"), young)
	// Old entries with another name.
	mustWrite(t, filepath.Join(mkdir(".claude-new-worktree"), "f.txt"), "f\n")
	mkdir(".claude-new-worktreeX")
	mkdir(".claude-old-worktree-0123456789abcdef")
	// An old entry with the name form below another folder: no direct entry.
	mkdir(filepath.Join("worktrees", ".claude-new-worktree-inner"))
	// The target of a link, with a marker. The target is young.
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.target, "marker.txt"), "marker of the junction target\n")
	age(f.target, young)
	return f
}

// link makes the old link entry of the fixture. It reports false where the
// system gives the test no symbolic link.
func (f stagingSweepFixture) link(t *testing.T) bool {
	t.Helper()
	return os.Symlink(f.target, filepath.Join(f.dir, ".claude-new-worktree-link")) == nil
}

func (f stagingSweepFixture) sweep(t *testing.T) {
	t.Helper()
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	sweepOldStaging(root, f.dir, f.now)
}

// recordStagingRemovals replaces the removal of the cleanup with a recorder
// that removes nothing. It returns the recorded calls as `<name> tree` or
// `<name> entry`.
func recordStagingRemovals(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := removeStagingEntry
	t.Cleanup(func() { removeStagingEntry = old })
	removeStagingEntry = func(_ *os.Root, name string, tree bool) error {
		kind := "entry"
		if tree {
			kind = "tree"
		}
		calls = append(calls, name+" "+kind)
		return nil
	}
	return &calls
}

// TestSweepOldStagingGuards pins what the cleanup asks to remove, with the
// removal replaced: the test removes nothing, also when a guard is gone. The
// rows are those of cell 48. The names that go are the three old entries with
// the name form and the link. A real folder goes as a tree, and a file and a
// link go as one entry.
//
// Without the name test the names with no dash and `worktrees` are asked for.
// Without the age test the two young entries are. With Stat in place of Lstat
// the link is asked for as a tree. Without the kind test the file and the link
// are asked for as trees.
func TestSweepOldStagingGuards(t *testing.T) {
	f := newStagingSweepFixture(t)
	want := []string{
		".claude-new-worktree- entry",
		".claude-new-worktree-0123456789abcdef tree",
		".claude-new-worktree-old1 tree",
	}
	if f.link(t) {
		want = []string{want[0], want[1], ".claude-new-worktree-link entry", want[2]}
	}
	calls := recordStagingRemovals(t)
	f.sweep(t)
	if !slices.Equal(*calls, want) {
		t.Errorf("the cleanup asked to remove %q\nwant %q", *calls, want)
	}
}

// TestSweepOldStagingRefusesHome pins the home guard of the cleanup: an old
// entry that holds the home folder is not asked for. The removal is replaced,
// so the test removes nothing without the guard. Without the wipesHomeDir call
// the entry is asked for as a tree.
func TestSweepOldStagingRefusesHome(t *testing.T) {
	f := newStagingSweepFixture(t)
	home := filepath.Join(f.dir, ".claude-new-worktree-home", "user")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	logs := captureLogBuf(t)
	calls := recordStagingRemovals(t)
	f.sweep(t)
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.HasPrefix(c, ".claude-new-worktree-home ") }) {
		t.Errorf("the cleanup asked to remove the entry that holds the home folder: %q", *calls)
	}
	if len(*calls) != 3 {
		t.Errorf("the cleanup asked to remove %q, want the three other old entries", *calls)
	}
	if !strings.Contains(logs.String(), "refusing to remove the old entry .claude-new-worktree-home") {
		t.Errorf("no log line for the refused entry: %q", logs.String())
	}
}

// TestSweepOldStagingRemoves runs the cleanup with its real removal, on the
// fixture of cell 48. The old entries with the name form are gone, the folder
// with its file. The target of the link and its marker stay (30 of 30 junction
// cells on 5fd08069). Every other entry stays. A removal that fails is logged
// and does not stop the cleanup.
func TestSweepOldStagingRemoves(t *testing.T) {
	f := newStagingSweepFixture(t)
	linked := f.link(t)
	f.sweep(t)
	var got []string
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		got = append(got, e.Name())
	}
	want := []string{".claude-new-worktree", ".claude-new-worktree-young", ".claude-new-worktree-youngfile",
		".claude-new-worktreeX", ".claude-old-worktree-0123456789abcdef", "worktrees"}
	if !slices.Equal(got, want) {
		t.Errorf("the folder holds %q\nwant %q", got, want)
	}
	if linked {
		if text := mustRead(t, filepath.Join(f.target, "marker.txt")); text != "marker of the junction target\n" {
			t.Errorf("the marker in the target of the link = %q", text)
		}
	}
	for _, kept := range []string{
		filepath.Join(f.dir, ".claude-new-worktree", "f.txt"),
		filepath.Join(f.dir, ".claude-new-worktree-young", "f.txt"),
		filepath.Join(f.dir, "worktrees", ".claude-new-worktree-inner"),
	} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s: %v, want it kept", kept, err)
		}
	}
}

// TestSweepOldStagingGoesOnAfterAnError pins that a removal that fails does
// not stop the cleanup: each of the three old entries is asked for, and each
// failure gets one log line.
func TestSweepOldStagingGoesOnAfterAnError(t *testing.T) {
	f := newStagingSweepFixture(t)
	logs := captureLogBuf(t)
	n := 0
	old := removeStagingEntry
	t.Cleanup(func() { removeStagingEntry = old })
	removeStagingEntry = func(*os.Root, string, bool) error {
		n++
		return os.ErrPermission
	}
	f.sweep(t)
	if n != 3 {
		t.Errorf("the cleanup asked for %d entries, want 3", n)
	}
	if got := strings.Count(logs.String(), " of the git folder was not removed: "); got != 3 {
		t.Errorf("%d log lines for the failed removals, want 3: %q", got, logs.String())
	}
}

// listTree lists the tree below dir as lines `<path>` for a folder and `<path>
// = <text>` for a file, with forward slashes, in sorted order.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out = append(out, rel)
			return nil
		}
		b, err := os.ReadFile(p)
		out = append(out, rel+" = "+string(b))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOpenAddStage pins the folder `.s<n>` before git runs, on a repository
// with one registration (`wr`), which is the state of cells 44a and 49a. The
// name is `.s` and a decimal number. HEAD, config and info/exclude have the
// texts of the copy of the repository, `objects` and `refs` are empty, and the
// registration has its three files and nothing else of the real one. A locked
// registration gets its `locked` file with its text (cell 49e), and a link in
// `worktrees` is no registration. After close the folder is gone (cells 44b and
// 44d).
//
// Without the removal in close the folder stays. A copy of the whole
// registration fails at `index`, `ORIG_HEAD` or `logs`.
func TestOpenAddStage(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	reg := filepath.Join(f.gitDir, "worktrees", "wr")
	mustWrite(t, filepath.Join(reg, "locked"), "kept for the test\n")
	if err := os.Symlink(reg, filepath.Join(f.gitDir, "worktrees", "alias")); err != nil && !os.IsPermission(err) {
		t.Logf("no link in the fixture: %v", err)
	}
	for _, extra := range []string{"index", "ORIG_HEAD"} {
		if _, err := os.Lstat(filepath.Join(reg, extra)); err != nil {
			mustWrite(t, filepath.Join(reg, extra), "x\n")
		}
	}

	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	if name := filepath.Base(st.dir); !regexp.MustCompile(`^\.s[0-9]{1,10}$`).MatchString(name) || filepath.Dir(st.dir) != u.folder {
		t.Errorf("the folder is %s, want .s<n> in %s", st.dir, u.folder)
	}
	want := []string{
		"HEAD = " + mustRead(t, filepath.Join(u.repoDir, "HEAD")),
		"config = " + mustRead(t, filepath.Join(u.repoDir, "config")),
		"info",
		"info/exclude = " + mustRead(t, filepath.Join(f.gitDir, "info", "exclude")),
		"objects",
		"refs",
		"worktrees",
		"worktrees/wr",
		"worktrees/wr/HEAD = " + mustRead(t, filepath.Join(reg, "HEAD")),
		"worktrees/wr/commondir = " + mustRead(t, filepath.Join(reg, "commondir")),
		"worktrees/wr/gitdir = " + mustRead(t, filepath.Join(reg, "gitdir")),
		"worktrees/wr/locked = kept for the test\n",
	}
	if got := listTree(t, st.dir); !slices.Equal(got, want) {
		t.Errorf("the folder holds\n %q\nwant\n %q", got, want)
	}
	if !slices.Equal(st.copied, []string{"wr"}) {
		t.Errorf("the copied registrations = %q, want wr", st.copied)
	}
	dir := st.dir
	st.close()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder after close: %v, want it gone", err)
	}
}

// TestOpenAddStageWithNoRegistration pins cell 49b: a repository with no
// registration gets no `worktrees` folder in `.s<n>`, and the folder
// `worktrees` of its git directory is made before the call. The repository of
// the test has no info/exclude either, which no cell has: `info` is then
// empty.
func TestOpenAddStageWithNoRegistration(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	if err := os.Remove(filepath.Join(f.gitDir, "worktrees")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.gitDir, "info", "exclude")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	u := f.useOf(t, f.repo)
	st, err := openAddStage(heldUse(t, f.gitDir, u))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	if fi, err := os.Lstat(filepath.Join(f.gitDir, "worktrees")); err != nil || !fi.IsDir() {
		t.Errorf("the worktrees folder of the git directory: %v, want a folder", err)
	}
	if _, err := os.Lstat(filepath.Join(st.dir, "worktrees")); !os.IsNotExist(err) {
		t.Errorf("worktrees in the folder of the add: %v, want none", err)
	}
	if len(st.copied) != 0 {
		t.Errorf("the copied registrations = %q, want none", st.copied)
	}
	if got, want := listTree(t, st.dir)[2:], []string{"info", "objects", "refs"}; !slices.Equal(got, want) {
		t.Errorf("the folder holds %q after HEAD and config, want %q", got, want)
	}
}

// heldUse is u with the hold of a create on gitDir, where the switch is set
// and u holds no git directory yet.
func heldUse(t *testing.T, gitDir string, u gitUse) gitUse {
	t.Helper()
	if u.accepted != nil || !gitDirMustBeReal {
		return u
	}
	u.accepted = acceptGitDir(gitDir, gitDirIdentity(gitDir))
	if u.accepted == nil {
		t.Fatal("the git directory of the fixture is not accepted")
	}
	t.Cleanup(u.accepted.release)
	return u
}

// stagedAdd runs one staged add of the branch w1 on the fixture and returns
// the leaf, the stderr text and the error of the call. A use that holds no
// git directory gets the hold of a create first, where the switch is set.
func stagedAdd(t *testing.T, f repoFolderFixture, u gitUse, args ...string) (leaf, stderr string, err error) {
	t.Helper()
	u = heldUse(t, f.gitDir, u)
	leaf = filepath.Join(f.repo, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := checkpointCreatedWorktree(leaf)
	t.Cleanup(cp.release)
	if args == nil {
		args = []string{"worktree", "add", "--no-track", "--no-checkout", "-b", "w1", leaf}
	}
	stderr, err = worktreeAddStaged(context.Background(), u, f.repo, leaf, cp, args...)
	return leaf, stderr, err
}

// stagingEntries are the names in dir that start with `.claude-new-worktree`.
func stagingEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".claude-new-worktree") {
			names = append(names, e.Name())
		}
	}
	return names
}

// addFolders are the `.s<n>` folders in the folder of the daemon.
func addFolders(t *testing.T, folder string) []string {
	t.Helper()
	ents, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), addCopyPrefix) {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestWorktreeAddStaged runs the staged add with the git of the host, and pins
// the end state of cells 44a, 47 and 49a:
//
//   - The registration is in `<git directory>/worktrees/w1` with HEAD,
//     commondir, gitdir and the folder refs.
//   - The `.git` file of the new worktree has the text of worktreeGitFileText.
//   - `.s<n>` is gone, and no staging folder is left in the git directory.
//   - The branch is in the repository itself, and git lists the new worktree.
//   - An old staging entry is gone and a young one stays (cell 48, control M1).
//
// Without the move the registration is missing. Without the write of the
// `.git` file its text names the folder that is gone. Without the cleanup the
// old entry stays.
func TestWorktreeAddStaged(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	old := filepath.Join(f.gitDir, ".claude-new-worktree-old1")
	young := filepath.Join(f.gitDir, ".claude-new-worktree-old2")
	for _, p := range []string{old, young} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(p, "f.txt"), "f\n")
	}
	at := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, at, at); err != nil {
		t.Fatal(err)
	}

	leaf, stderr, err := stagedAdd(t, f, u)
	if err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	reg := filepath.Join(f.gitDir, "worktrees", "w1")
	want := []string{
		"HEAD = ref: refs/heads/w1\n",
		"commondir = ../..\n",
		"gitdir = " + filepath.ToSlash(leaf) + "/.git\n",
		"refs",
	}
	if got := listTree(t, reg); !slices.Equal(got, want) {
		t.Errorf("the registration holds %q\nwant %q", got, want)
	}
	if got, want := mustRead(t, filepath.Join(leaf, ".git")), worktreeGitFileText(f.gitDir, "w1"); got != want {
		t.Errorf("the .git file of the new worktree = %q, want %q", got, want)
	}
	if got := addFolders(t, u.folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
	if got, want := stagingEntries(t, f.gitDir), []string{".claude-new-worktree-old2"}; !slices.Equal(got, want) {
		t.Errorf("the staging entries of the git directory = %q, want %q", got, want)
	}
	if _, err := os.Lstat(filepath.Join(young, "f.txt")); err != nil {
		t.Errorf("the file of the young entry: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch in the repository: %v", err)
	}
	list := gitOut(t, f.repo, "worktree", "list", "--porcelain")
	if !strings.Contains(list, "worktree "+filepath.ToSlash(leaf)+"\n") || !strings.Contains(list, "branch refs/heads/w1\n") {
		t.Errorf("git does not list the new worktree:\n%s", list)
	}
}

// TestWorktreeAddStagedFailedAdd pins a failed add (cells 44b and 44d): the
// call returns the error of git, `.s<n>` is gone, the git directory has no new
// registration and no staging folder, and an old staging entry stays, because
// the cleanup runs after a good call only. git refuses here because the branch
// exists. Without the removal after a failed call `.s<n>` stays.
func TestWorktreeAddStagedFailedAdd(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	old := filepath.Join(f.gitDir, ".claude-new-worktree-old1")
	if err := os.Mkdir(old, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, at, at); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	_, stderr, err := stagedAdd(t, f, u, "worktree", "add", "--no-track", "--no-checkout", "-b", "wr", leaf)
	if err == nil || !strings.Contains(stderr, "wr") {
		t.Fatalf("the add = %v, %q, want the refusal of git", err, stderr)
	}
	if got := addFolders(t, u.folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
	if got, want := stagingEntries(t, f.gitDir), []string{".claude-new-worktree-old1"}; !slices.Equal(got, want) {
		t.Errorf("the staging entries of the git directory = %q, want %q", got, want)
	}
	if got := listTree(t, filepath.Join(f.gitDir, "worktrees")); slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "w1") }) {
		t.Errorf("the git directory has a registration of the failed add: %q", got)
	}
}

// TestWorktreeAddStagedKeepsAnEntryInItsWay pins that the move replaces
// nothing: with a plain file at the place of the registration, the file stays,
// one line is logged, and `.s<n>` is gone. That case is not measured.
func TestWorktreeAddStagedKeepsAnEntryInItsWay(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	inWay := filepath.Join(f.gitDir, "worktrees", "w1")
	mustWrite(t, inWay, "not a registration\n")
	logs := captureLogBuf(t)
	if _, stderr, err := stagedAdd(t, f, u); err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	if got := mustRead(t, inWay); got != "not a registration\n" {
		t.Errorf("the file at the place of the registration = %q", got)
	}
	if !strings.Contains(logs.String(), "the registration of the new worktree did not move") {
		t.Errorf("no log line for the move: %q", logs.String())
	}
	if got := addFolders(t, u.folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
}

// TestWorktreeAddStagedWithNoCheckpoint pins that the `.git` file of the new
// worktree is written only into the folder that the create made and holds: with
// no checkpoint of that folder the file keeps the text of git, and one line is
// logged. That case is not measured.
func TestWorktreeAddStagedWithNoCheckpoint(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	u = heldUse(t, f.gitDir, u)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := captureLogBuf(t)
	if stderr, err := worktreeAddStaged(context.Background(), u, f.repo, leaf, worktreeCheckpoint{},
		"worktree", "add", "--no-track", "--no-checkout", "-b", "w1", leaf); err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	if got := mustRead(t, filepath.Join(leaf, ".git")); got == worktreeGitFileText(f.gitDir, "w1") {
		t.Errorf("the .git file was written with no checkpoint: %q", got)
	}
	if !strings.Contains(logs.String(), "the .git file of the new worktree was not written") {
		t.Errorf("no log line for the .git file: %q", logs.String())
	}
}
