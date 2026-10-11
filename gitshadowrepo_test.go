package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newSeededShadowServer returns a server whose settings control has a run
// folder and the state shadow, with no start test run. The first git request
// makes the folder of the daemon. Tests of the folder of a repository and of
// the git calls in the state shadow use it.
func newSeededShadowServer(t *testing.T) (*server, string) {
	t.Helper()
	s, run := newShadowServer(t)
	h := s.gitShadow
	h.mu.Lock()
	h.state, h.tested = controlShadow, shadowClock()
	h.mu.Unlock()
	return s, run
}

// repoFolderFixture is a repository with a linked worktree and a seeded
// settings control whose folder is made.
type repoFolderFixture struct {
	refusalFixture
	h *shadowHost
}

func newRepoFolderFixture(t *testing.T) repoFolderFixture {
	t.Helper()
	f := newRefusalFixture(t)
	s, _ := newSeededShadowServer(t)
	h := s.gitShadow
	h.mu.Lock()
	err := h.makeFolder()
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return repoFolderFixture{refusalFixture: f, h: h}
}

// sync is the step of a request that accepted the repository in dir: the read
// of the config, then the folder. It returns the folder of the repository.
func (f repoFolderFixture) sync(t *testing.T, dir string) string {
	t.Helper()
	trust := gitDirTrustFor(dir)
	v := repoReadVerdict(trust)
	if trust.verdict != gitDirTrusted || v == nil || v.refusal != "" || gitDirFilesRefusal(trust.pinCommonDir) != "" {
		t.Fatalf("the fixture in %s is not accepted: %+v", dir, v)
	}
	if _, err := f.h.syncRepo(trust.pinCommonDir, v); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f.h.dir, shadowRepoName(trust.pinCommonDir))
}

// entryTreeOf is every entry below dir, by its slash path, as Lstat sees it.
func entryTreeOf(t *testing.T, dir string) map[string]os.FileInfo {
	t.Helper()
	tree := map[string]os.FileInfo{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}
		// The identity is read now. On Windows os.SameFile reads it from the
		// path at the first compare, and a compare after a replace then saw the
		// new file two times (val-s3p2b2-3-windows part C).
		os.SameFile(fi, fi)
		rel, _ := filepath.Rel(dir, path)
		tree[filepath.ToSlash(rel)] = fi
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func treeNames(tree map[string]os.FileInfo) []string {
	names := []string{}
	for name := range tree {
		if name != "." {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// sameEntry reports whether a and b show one file in one state: the identity,
// the size, the modification time and the mode.
func sameEntry(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() &&
		a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

// changedEntries are the names of before and after that are not one file in
// one state, sorted. The folder itself is left out.
func changedEntries(before, after map[string]os.FileInfo) []string {
	changed := []string{}
	for name, fi := range after {
		if name != "." && !sameEntry(before[name], fi) {
			changed = append(changed, name)
		}
	}
	for name := range before {
		if after[name] == nil {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	return changed
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// laterTime gives path a modification time that no earlier write of the test
// has, so that a change of the same size is seen on a file system with coarse
// times.
func laterTime(t *testing.T, path string) {
	t.Helper()
	at := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestShadowRepoName: the name is the first 32 hex digits of the sha256 of the
// path of the git directory. The rows are the digits that 5fd08069 used on
// macOS and Windows VMs (s3p2-macos cell B2, s3p2-windows cells B1 and B2, and
// cell 24 c of s3p2b-edges on both).
func TestShadowRepoName(t *testing.T) {
	sep := string(filepath.Separator)
	for path, want := range map[string]string{
		"/private/tmp/o/REPO/.git": "eb0b1c7686f7daa4cedb6d069553238f",
		"/private/tmp/o/repo":      "fb7ee1d5ce36b609d2d148e045644e73",
		`C:\o\w\repo\.git`:         "0a83e704d30073e7cc965a58a66dc000",
		`C:\o\w\jrepo\.git`:        "3199f79b788536521cc03df25adfb8e9",
		`C:\o\w\repo`:              "b41cef901bc68ff9ce2cadc6d8acf8b6",
	} {
		// The row is spelled for its own system. filepath.Clean changes the
		// separators of the other system, so the row runs where its separator is.
		if strings.Contains(path, `\`) != (sep == `\`) {
			continue
		}
		if got := shadowRepoName(path); got != want {
			t.Errorf("name of %s = %s, want %s", path, got, want)
		}
		if got := shadowRepoName(path + sep); got != want {
			t.Errorf("name of %s with a trailing separator = %s, want %s", path, got, want)
		}
	}
	if a, b := shadowRepoName(filepath.Join("x", "repo", ".git")), shadowRepoName(filepath.Join("x", "REPO", ".git")); a == b {
		t.Error("another letter case gives the same name: the name must not fold the case")
	}
}

// TestShadowRepoFolderPerKind: which git directory names the folder, per kind
// of repository. A request on a linked worktree uses the folder of its
// repository (s3p2-linux cell B3b). A bare repository uses its own path (cell
// B3c). A `.git` file uses the git directory that it names (cell 20j of
// s3p2b-edges-linux).
func TestShadowRepoFolderPerKind(t *testing.T) {
	f := newRepoFolderFixture(t)
	want := filepath.Join(f.h.dir, shadowRepoName(f.gitDir))
	if got := f.sync(t, f.repo); got != want {
		t.Errorf("plain repository: folder %s, want %s", got, want)
	}
	if got := f.sync(t, f.wt); got != want {
		t.Errorf("linked worktree: folder %s, want that of its repository %s", got, want)
	}

	base := filepath.Dir(f.repo)
	bare := filepath.Join(base, "bare.git")
	runGit(t, base, "init", "-q", "--bare", "-b", "main", bare)
	if got, want := f.sync(t, bare), filepath.Join(f.h.dir, shadowRepoName(bare)); got != want {
		t.Errorf("bare repository: folder %s, want %s", got, want)
	}

	// A `.git` file that names a git directory elsewhere.
	holder := filepath.Join(base, "holder")
	realGit := filepath.Join(base, "realgit")
	if err := os.Mkdir(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, holder, "init", "-q", "-b", "main", "--separate-git-dir", realGit)
	if got, want := f.sync(t, holder), filepath.Join(f.h.dir, shadowRepoName(realGit)); got != want {
		t.Errorf("a .git file: folder %s, want %s", got, want)
	}

	names := entryNames(t, f.h.dir)
	if len(names) != 4 {
		t.Errorf("the folder of the daemon holds %v, want three repository folders and the lock file", names)
	}
}

// TestShadowRepoEntries: the entries of the folder of a plain repository, and
// their content (s3-linux cell A3 and s3p2-linux cell B3a, Linux VM. Cell 24
// of s3p2b-edges on macOS and Windows VMs). Windows has no `worktrees` entry.
func TestShadowRepoEntries(t *testing.T) {
	f := newRepoFolderFixture(t)
	setRepoConfig(t, f.gitDir, stdRepoConfig)
	dir := f.sync(t, f.repo)

	want := []string{"HEAD", "config", "info", "info/exclude", "objects", "refs"}
	if shadowLinksWorktrees {
		want = append(want, "worktrees")
	}
	tree := entryTreeOf(t, dir)
	if got := treeNames(tree); !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if got := mustRead(t, filepath.Join(dir, "HEAD")); got != "ref: refs/heads/main\n" {
		t.Errorf("HEAD = %q", got)
	}
	if got, want := mustRead(t, filepath.Join(dir, "config")), stdCopyBlocks+shadowConfigTail; got != want {
		t.Errorf("config = %q, want %q", got, want)
	}
	if got, want := mustRead(t, filepath.Join(dir, "info", "exclude")), mustRead(t, filepath.Join(f.gitDir, "info", "exclude")); got != want || got == "" {
		t.Errorf("info/exclude = %q, want the text of the repository %q", got, want)
	}
	for _, name := range []string{"info", "objects", "refs"} {
		if !tree[name].IsDir() {
			t.Errorf("%s is no folder", name)
		}
	}
	if shadowLinksWorktrees {
		if tree["worktrees"].Mode()&fs.ModeSymlink == 0 {
			t.Errorf("worktrees has the mode %v, want a symbolic link", tree["worktrees"].Mode())
		}
		if got, want := mustReadlink(t, filepath.Join(dir, "worktrees")), filepath.Join(f.gitDir, "worktrees"); got != want {
			t.Errorf("worktrees points to %s, want %s", got, want)
		}
	}
}

func mustReadlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// TestShadowRepoSecondRequestChangesNothing: a second request with nothing
// changed changes no entry (s3p2-linux cell B4, Linux VM, and cell 24 B4 of
// s3p2b-edges on macOS and Windows VMs).
func TestShadowRepoSecondRequestChangesNothing(t *testing.T) {
	f := newRepoFolderFixture(t)
	mustWrite(t, filepath.Join(f.gitDir, "shallow"), strings.Repeat("a", 40)+"\n")
	mustWrite(t, filepath.Join(f.gitDir, "rebase-merge", "head-name"), "refs/heads/side\n")
	mustWrite(t, filepath.Join(f.gitDir, "BISECT_LOG"), "git bisect start\n")
	dir := f.sync(t, f.repo)
	before := entryTreeOf(t, dir)
	if len(before) < 10 {
		t.Fatalf("entries = %v", treeNames(before))
	}
	f.sync(t, f.repo)
	f.sync(t, f.wt)
	if changed := changedEntries(before, entryTreeOf(t, dir)); len(changed) != 0 {
		t.Errorf("a request with nothing changed changed %v", changed)
	}
	if got := changedEntries(before, map[string]os.FileInfo{}); len(got) != len(before) {
		t.Errorf("the compare of two trees sees %d of %d entries", len(got), len(before))
	}
}

// TestShadowRepoConfigIsWrittenAgain: after a change of the repository's config
// the copy is a new file with the new text, and every other entry stays
// (s3p2-linux cell B4 and cell 18k of s3p2b-edges-linux, Linux VM). A change of
// an included file alone gives a new copy too, also with the same size (cell
// 21a of that round).
func TestShadowRepoConfigIsWrittenAgain(t *testing.T) {
	f := newRepoFolderFixture(t)
	setRepoConfig(t, f.gitDir, stdRepoConfig+"[include]\n\tpath = inc.cfg\n")
	setIncludeFile(t, f.gitDir, "inc.cfg", "[core]\n\tquotepath = false\n")
	dir := f.sync(t, f.repo)
	if got, want := mustRead(t, filepath.Join(dir, "config")), stdCopyBlocks+"[core]\n\tquotepath = \"false\"\n"+shadowConfigTail; got != want {
		t.Fatalf("config = %q, want %q", got, want)
	}

	// The included file gets another text of the same size.
	before := entryTreeOf(t, dir)
	setIncludeFile(t, f.gitDir, "inc.cfg", "[core]\n\tsplitindex = true\n")
	laterTime(t, filepath.Join(f.gitDir, "inc.cfg"))
	f.sync(t, f.repo)
	after := entryTreeOf(t, dir)
	if changed := changedEntries(before, after); !slices.Equal(changed, []string{"config"}) {
		t.Errorf("after a change of the included file the changed entries are %v, want [config]", changed)
	}
	if os.SameFile(before["config"], after["config"]) {
		t.Error("the config of the copy is the old file, want a new file")
	}
	if got, want := mustRead(t, filepath.Join(dir, "config")), stdCopyBlocks+"[core]\n\tsplitindex = \"true\"\n"+shadowConfigTail; got != want {
		t.Errorf("config after the change = %q, want %q", got, want)
	}

	// The config itself changes.
	before = after
	setRepoConfig(t, f.gitDir, stdRepoConfig+"[core]\n\teol = lf\n")
	f.sync(t, f.repo)
	after = entryTreeOf(t, dir)
	if changed := changedEntries(before, after); !slices.Equal(changed, []string{"config"}) {
		t.Errorf("after a change of the config the changed entries are %v, want [config]", changed)
	}
	if got, want := mustRead(t, filepath.Join(dir, "config")), stdCopyBlocks+"[core]\n\teol = \"lf\"\n"+shadowConfigTail; got != want {
		t.Errorf("config after the second change = %q, want %q", got, want)
	}
	// A new time of the config with the same text. The daemon reads the config
	// again, and claustrum writes the copy again. Whether 5fd08069 writes a new
	// file there is not measured.
	before = after
	laterTime(t, filepath.Join(f.gitDir, "config"))
	f.sync(t, f.repo)
	after = entryTreeOf(t, dir)
	if os.SameFile(before["config"], after["config"]) {
		t.Error("after a new time of the config the copy is the old file")
	}
	// No temporary file is left.
	for _, name := range treeNames(after) {
		if strings.Contains(name, ".tmp-") {
			t.Errorf("the temporary file %s is left", name)
		}
	}
}

// TestShadowRepoHead: the copy of HEAD follows the repository's HEAD as a new
// file (cell 18a of s3p2b-edges-linux, Linux VM), and it holds the first 8192
// bytes only (cells 17a to 17k of that round). The request of a linked
// worktree gets the HEAD of the repository (s3p2-linux cell B3b).
func TestShadowRepoHead(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	before := entryTreeOf(t, dir)

	mustWrite(t, filepath.Join(f.gitDir, "HEAD"), "ref: refs/heads/other\n")
	f.sync(t, f.wt)
	after := entryTreeOf(t, dir)
	if changed := changedEntries(before, after); !slices.Equal(changed, []string{"HEAD"}) {
		t.Errorf("after a change of HEAD the changed entries are %v, want [HEAD]", changed)
	}
	if os.SameFile(before["HEAD"], after["HEAD"]) {
		t.Error("the HEAD of the copy is the old file, want a new file")
	}
	if got := mustRead(t, filepath.Join(dir, "HEAD")); got != "ref: refs/heads/other\n" {
		t.Errorf("HEAD = %q, want the HEAD of the repository", got)
	}

	for _, size := range []int{8191, 8192, 8193, 1 << 20} {
		text := "ref: refs/heads/" + strings.Repeat("a", size-17) + "\n"
		mustWrite(t, filepath.Join(f.gitDir, "HEAD"), text)
		f.sync(t, f.repo)
		got := mustRead(t, filepath.Join(dir, "HEAD"))
		if want := text[:min(size, 8192)]; got != want {
			t.Errorf("HEAD of %d bytes: the copy has %d bytes, want %d", size, len(got), len(want))
		}
	}
}

// TestShadowRepoCopiesFollowTheirFiles: each copied file appears, is written
// again as a new file and goes with its file (cells 19a to 19e and 20a of
// s3p2b-edges-linux, Linux VM. Cell 15 of s3p2b-edges2 on macOS and Windows
// VMs). After info/exclude went, `info` stays empty. A rebase folder goes with
// its file. BISECT_LOG is an empty file (s3p2-linux cell B3i).
func TestShadowRepoCopiesFollowTheirFiles(t *testing.T) {
	f := newRepoFolderFixture(t)
	if err := os.Remove(filepath.Join(f.gitDir, "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	dir := f.sync(t, f.repo)
	if got := entryNames(t, filepath.Join(dir, "info")); len(got) != 0 {
		t.Fatalf("info holds %v with no file in the repository", got)
	}
	base := len(entryTreeOf(t, dir))

	for _, row := range []struct {
		file, text, next string
		// more are the entries beside the file itself: its folder.
		more int
	}{
		{"shallow", strings.Repeat("a", 40) + "\n", strings.Repeat("b", 40) + "\n", 0},
		{"info/attributes", "*.txt text\n", "*.bin binary\n", 0},
		{"info/exclude", "old-line\n", "new-exclude-line\n", 0},
		{"rebase-merge/head-name", "refs/heads/side\n", "refs/heads/main\n", 1},
		{"rebase-apply/head-name", "refs/heads/side\n", "refs/heads/main\n", 1},
		{"BISECT_START", "main\n", "side\n", 0},
	} {
		src := filepath.Join(f.gitDir, filepath.FromSlash(row.file))
		copyPath := filepath.Join(dir, filepath.FromSlash(row.file))

		mustWrite(t, src, row.text)
		f.sync(t, f.repo)
		first := entryTreeOf(t, dir)
		if got := mustRead(t, copyPath); got != row.text {
			t.Errorf("%s: the copy holds %q, want %q", row.file, got, row.text)
		}
		if len(first) != base+1+row.more {
			t.Errorf("%s: the folder holds %v", row.file, treeNames(first))
		}

		mustWrite(t, src, row.next)
		f.sync(t, f.repo)
		second := entryTreeOf(t, dir)
		if got := mustRead(t, copyPath); got != row.next {
			t.Errorf("%s after a change: the copy holds %q, want %q", row.file, got, row.next)
		}
		if os.SameFile(first[row.file], second[row.file]) {
			t.Errorf("%s after a change: the copy is the old file, want a new file", row.file)
		}

		if err := os.Remove(src); err != nil {
			t.Fatal(err)
		}
		f.sync(t, f.repo)
		if got := entryTreeOf(t, dir); len(got) != base {
			t.Errorf("%s after its file went: the folder holds %v", row.file, treeNames(got))
		}
	}

	log := filepath.Join(f.gitDir, "BISECT_LOG")
	mustWrite(t, log, strings.Repeat("x", 340))
	f.sync(t, f.repo)
	if fi, err := os.Stat(filepath.Join(dir, "BISECT_LOG")); err != nil || fi.Size() != 0 {
		t.Errorf("BISECT_LOG of the copy: %v, %v, want an empty file", fi, err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	f.sync(t, f.repo)
	if got := entryTreeOf(t, dir); len(got) != base {
		t.Errorf("after BISECT_LOG went the folder holds %v", treeNames(got))
	}
}

// TestShadowRepoFilesWithNoCopy: the files of the git directory that 5fd08069
// gives no entry (s3p2-linux cells B3f and B3h, and cells 20b to 20i of
// s3p2b-edges-linux, Linux VM).
func TestShadowRepoFilesWithNoCopy(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	before := treeNames(entryTreeOf(t, dir))
	for _, name := range []string{
		"packed-refs", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer/todo", "ORIG_HEAD",
		"FETCH_HEAD", "info/grafts", "info/sparse-checkout", "objects/info/alternates",
	} {
		mustWrite(t, filepath.Join(f.gitDir, filepath.FromSlash(name)), "")
	}
	f.sync(t, f.repo)
	if after := treeNames(entryTreeOf(t, dir)); !slices.Equal(before, after) {
		t.Errorf("entries = %v, want the entries from before %v", after, before)
	}
}

// TestShadowRepoFolderComesBack: a folder of a repository that went is made
// again from what the daemon read before, with no new read of the config (cell
// 54 of s3p2b-edges3-linux2, Linux VM). A new folder of the daemon starts with
// no folder of a repository.
func TestShadowRepoFolderComesBack(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	kept := repoReadVerdict(gitDirTrustFor(f.repo))
	want := treeNames(entryTreeOf(t, dir))
	if err := f.h.removeEntry(filepath.Join(filepath.Base(f.h.dir), filepath.Base(dir))); err != nil {
		t.Fatal(err)
	}
	f.sync(t, f.repo)
	if got := treeNames(entryTreeOf(t, dir)); !slices.Equal(got, want) {
		t.Errorf("the folder that came back holds %v, want %v", got, want)
	}
	if again := repoReadVerdict(gitDirTrustFor(f.repo)); again != kept {
		t.Error("the config was read again for a folder that came back")
	}

	// The folder of the daemon goes and is made again.
	old := f.h.dir
	f.h.mu.Lock()
	f.h.dropLock()
	f.h.mu.Unlock()
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	f.h.mu.Lock()
	f.h.remakeFolder()
	f.h.mu.Unlock()
	if f.h.dir == old {
		t.Fatal("the folder of the daemon was not made again")
	}
	dir = f.sync(t, f.repo)
	if got := treeNames(entryTreeOf(t, dir)); !slices.Equal(got, want) {
		t.Errorf("the folder in the new folder of the daemon holds %v, want %v", got, want)
	}
}

// configReads are the positions in calls of the daemon's own reads of a config
// file: `git config --no-includes --file - --list -z`.
func configReads(calls []envCall) []int {
	var at []int
	for i, c := range calls {
		if slices.Contains(c.argv, "--no-includes") {
			at = append(at, i)
		}
	}
	return at
}

// newSeededShadowFixture is newShadowFixture with the state shadow and no start
// test. A test makes one such fixture at most: the git stub of a second one
// finds the stub of the first as its git. A test with more fixtures makes each
// in its own subtest.
func newSeededShadowFixture(t *testing.T) shadowFixture {
	t.Helper()
	f := newShadowFixture(t)
	h := f.s.gitShadow
	h.mu.Lock()
	h.state, h.tested = controlShadow, shadowClock()
	h.mu.Unlock()
	return f
}

// TestShadowConfigReadCalls: how many reads of a config file a request makes,
// per fixture. The rows are the cells 1a to 1m of s3p2b-edges2-linux and 11a
// to 11f of s3p2b-edges-linux (Linux VM): one read for the config, one for each
// included file that exists, one for the file of an `includeIf` section, and
// two for the `config.worktree` file of the repository when the switch is on
// and the file exists. The reads follow each other. A second request makes
// none.
func TestShadowConfigReadCalls(t *testing.T) {
	ext := "[extensions]\n\tworktreeConfig = true\n"
	inc := "[core]\n\tfilemode = false\n"
	for _, row := range []struct {
		cell   string
		config string
		files  map[string]string
		want   int
	}{
		{"1k plain", "", nil, 1},
		{"1m one include", "[include]\n\tpath = inc.cfg\n", map[string]string{"inc.cfg": inc}, 2},
		{"8c an include of a missing file", "[include]\n\tpath = nosuch.cfg\n", nil, 1},
		{"8d an include in an included file", "[include]\n\tpath = inc.cfg\n",
			map[string]string{"inc.cfg": "[core]\n\teol = lf\n[include]\n\tpath = inc2.cfg\n", "inc2.cfg": inc}, 3},
		{"11c an includeIf section", "[includeIf \"onbranch:main\"]\n\tpath = ii.cfg\n", map[string]string{"ii.cfg": inc}, 2},
		{"1a the switch and the file", ext, map[string]string{"config.worktree": inc}, 3},
		{"1d the switch and no file", ext, nil, 1},
		{"1e the file and no switch", "", map[string]string{"config.worktree": inc}, 1},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			setRepoConfig(t, f.gitDir, stdRepoConfig+row.config)
			for name, text := range row.files {
				setIncludeFile(t, f.gitDir, name, text)
			}
			raw, calls := f.info(t, f.repo, nil)
			reads := configReads(calls)
			if len(reads) != row.want {
				t.Fatalf("%d reads, want %d (reply %s)", len(reads), row.want, raw)
			}
			if last := reads[len(reads)-1]; last-reads[0] != len(reads)-1 {
				t.Errorf("the reads are the calls %v, want calls in a row", reads)
			}
			if _, calls := f.info(t, f.repo, nil); len(configReads(calls)) != 0 {
				t.Errorf("the second request made %d reads, want none", len(configReads(calls)))
			}
		})
	}
}

// TestShadowConfigReadsOfLaterRequests: the reads of the `config.worktree`
// files in later requests (cells 1a, 1c and 1k of s3p2b-edges2-linux, Linux
// VM). A git.info on a linked worktree reads the file of that worktree one
// time in each request, and it does not read the config of the repository
// again. git.list_branches reads nothing. A create reads the file of the
// repository one time, right after its `worktree add`.
func TestShadowConfigReadsOfLaterRequests(t *testing.T) {
	f := newSeededShadowFixture(t)
	setRepoConfig(t, f.gitDir, stdRepoConfig+"[extensions]\n\tworktreeConfig = true\n")
	setIncludeFile(t, f.gitDir, "config.worktree", "[core]\n\tfilemode = false\n")
	if _, calls := f.info(t, f.repo, nil); len(configReads(calls)) != 3 {
		t.Fatalf("first request: %d reads, want 3", len(configReads(calls)))
	}

	// The linked worktree has no file yet: no read.
	if _, calls := f.info(t, f.wt, nil); len(configReads(calls)) != 0 {
		t.Errorf("git.info on a linked worktree with no config.worktree: %d reads, want none", len(configReads(calls)))
	}
	mustWrite(t, filepath.Join(f.gitDir, "worktrees", "wr", "config.worktree"), "[core]\n\teol = lf\n")
	for i := range 2 {
		raw, calls := f.info(t, f.wt, nil)
		if n := len(configReads(calls)); n != 1 || !strings.Contains(raw, `"isRepo":true`) {
			t.Errorf("git.info %d on a linked worktree with a config.worktree: %d reads, want 1 (reply %s)", i+1, n, raw)
		}
	}
	if _, calls := f.call(t, "git.list_branches", map[string]any{"path": f.wt}); len(configReads(calls)) != 0 {
		t.Errorf("git.list_branches on the linked worktree: %d reads, want none", len(configReads(calls)))
	}
	// The key of the file of the worktree is in no copy.
	copyText := mustRead(t, filepath.Join(daemonFolder(t, f.run), shadowRepoName(f.gitDir), "config"))
	if want := stdCopyBlocks + shadowConfigTail; copyText != want {
		t.Errorf("the copy = %q, want %q", copyText, want)
	}

	create := map[string]any{"baseRepo": f.repo, "branchName": "w1",
		"worktreePath": filepath.Join(f.repo, ".claude", "worktrees", "w1")}
	raw, calls := f.call(t, "git.worktree_create", create)
	reads := configReads(calls)
	if len(reads) != 1 || !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create with the switch and the file: reads %v, want one (reply %s)", reads, raw)
	}
	if prev := calls[reads[0]-1].argv; !slices.Contains(prev, "worktree") || !slices.Contains(prev, "add") {
		t.Errorf("the call before the read is %v, want the worktree add", prev)
	}

	// With the switch off again a create reads nothing, and so does a git.info
	// on the linked worktree.
	setRepoConfig(t, f.gitDir, stdRepoConfig)
	f.info(t, f.repo, nil)
	if _, calls := f.info(t, f.wt, nil); len(configReads(calls)) != 0 {
		t.Errorf("git.info on a linked worktree with no switch: %d reads, want none", len(configReads(calls)))
	}
	create["branchName"], create["worktreePath"] = "w2", filepath.Join(f.repo, ".claude", "worktrees", "w2")
	if raw, calls := f.call(t, "git.worktree_create", create); len(configReads(calls)) != 0 || !strings.Contains(raw, `"success":true`) {
		t.Errorf("create with no switch: %d reads, want none (reply %s)", len(configReads(calls)), raw)
	}
}

// TestShadowRepoFolderPerState: which requests make the folder of a
// repository. Each of the five methods makes it as the first request of a
// daemon in the state shadow (git.info, git.status and git.worktree_create in
// s3-macos section 1 and s3b-linux cell W1. The two others are claustrum's
// choice). A request with the switch makes none (s3-linux cell B1), the state
// fallback-git makes none (s3p2-oldgit section 5) and a refused config makes
// none (s3p2-linux section 4).
func TestShadowRepoFolderPerState(t *testing.T) {
	folders := func(t *testing.T, run string) []string {
		t.Helper()
		var names []string
		for _, daemon := range daemonFolders(t, run) {
			for _, name := range entryNames(t, filepath.Join(run, shadowRootName, daemon)) {
				if name != shadowLockName {
					names = append(names, name)
				}
			}
		}
		return names
	}
	fixture := func(t *testing.T) (refusalFixture, *server, string) {
		t.Helper()
		f := newRefusalFixture(t)
		s, run := newSeededShadowServer(t)
		return f, s, run
	}
	for i := range 5 {
		f, s, run := fixture(t)
		r := f.fiveRequests(nil)[i]
		raw := dispatchRaw(t, s, rpcLine(t, r.method, r.params))
		if got, want := folders(t, run), []string{shadowRepoName(f.gitDir)}; !slices.Equal(got, want) {
			t.Errorf("%s as the first request: folders %v, want %v (reply %s)", r.method, got, want, raw)
		}
	}

	f, s, run := fixture(t)
	dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo, "shadowDirOff": true}))
	if got := folders(t, run); len(got) != 0 {
		t.Errorf("with the switch: folders %v, want none", got)
	}

	f, s, run = fixture(t)
	s.gitShadow.mu.Lock()
	s.gitShadow.state = controlFallback
	s.gitShadow.mu.Unlock()
	dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo}))
	if got := folders(t, run); len(got) != 0 {
		t.Errorf("in the state fallback-git: folders %v, want none", got)
	}

	f, s, run = fixture(t)
	setRepoConfig(t, f.gitDir, versionConfig("repositoryformatversion = 2"))
	raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo}))
	if got := folders(t, run); len(got) != 0 || !strings.Contains(raw, repoConfigPrefix) {
		t.Errorf("with a refused config: folders %v, want none (reply %s)", got, raw)
	}
}
