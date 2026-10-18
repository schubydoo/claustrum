//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These tests pin the cells of round tier0-20 (5fd08069 on a Linux VM) for a
// git.worktree_create or git.worktree_remove with a worktreeRoot. Each test stages
// the cell as the round did, in a temp dir: <B> is that dir with its links
// resolved, <T> the repository and <X> the folder of the cell (mode 0700). A link
// that the round put in /tmp is a link in <B>. The texts are the replies of
// 5fd08069, and so are the folders that a cell made, kept or removed.

type locCell struct {
	B, T, X string
}

func newLocCell(t *testing.T) *locCell {
	t.Helper()
	requireGit(t)
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requireTempOutsideCheckout(t, b)
	c := &locCell{B: b, T: filepath.Join(b, "T"), X: filepath.Join(b, "X")}
	r0Repo(t, c.T)
	if err := os.Mkdir(c.X, 0o700); err != nil {
		t.Fatal(err)
	}
	return c
}

// p expands <B>, <T> and <X> in s.
func (c *locCell) p(s string) string {
	return strings.NewReplacer("<B>", c.B, "<T>", c.T, "<X>", c.X).Replace(s)
}

func (c *locCell) dir(t *testing.T, rel string) {
	t.Helper()
	if err := os.MkdirAll(c.p(rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (c *locCell) file(t *testing.T, rel string) {
	t.Helper()
	writeFile(t, c.p(rel), "", 0o644)
}

func (c *locCell) link(t *testing.T, target, rel string) {
	t.Helper()
	if err := os.Symlink(c.p(target), c.p(rel)); err != nil {
		t.Fatal(err)
	}
}

func (c *locCell) marker(t *testing.T, rel string) {
	t.Helper()
	writeFile(t, filepath.Join(c.p(rel), managedWorktreesMarker), managedWorktreesMarkerBody, 0o644)
}

// tree lists every entry below the folders of the cell with its kind and its mode,
// and the entries and branches of <T>. A refusal must leave it as it was.
func (c *locCell) tree(t *testing.T, extra ...string) string {
	t.Helper()
	var out []string
	for _, root := range append([]string{c.X}, extra...) {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				out = append(out, path+" "+err.Error())
				return nil
			}
			fi, err := os.Lstat(path)
			if err != nil {
				return nil
			}
			out = append(out, fmt.Sprintf("%s %v", path, fi.Mode()))
			return nil
		})
	}
	sort.Strings(out)
	return strings.Join(out, "\n") + "\n" + r0cState(t, &r0Fixture{T: c.T})
}

func (c *locCell) create(t *testing.T, base, root, path string) string {
	t.Helper()
	return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": c.p(base), "worktreePath": c.p(path), "worktreeRoot": c.p(root), "branchName": "b20"}))
}

func (c *locCell) remove(t *testing.T, base, root, path string) string {
	t.Helper()
	return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": c.p(base), "worktreePath": c.p(path), "worktreeRoot": c.p(root)}))
}

// stays fails the test when an entry is gone.
func (c *locCell) stays(t *testing.T, rels ...string) {
	t.Helper()
	for _, r := range rels {
		if _, err := os.Lstat(c.p(r)); err != nil {
			t.Errorf("%s is gone (%v); 5fd08069 keeps it", c.p(r), err)
		}
	}
}

func wantLocFrame(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("frame\n got %s\nwant %s", got, want)
	}
}

const locRootTail = `; choose the worktree location by its absolute path, without "..", beneath the filesystem root`

// locCreateRow is a create cell that is refused with nothing made.
type locCreateRow struct {
	name             string
	setup            func(t *testing.T, c *locCell)
	base, root, path string
	text, code       string
	extra            []string
}

func (r locCreateRow) run(t *testing.T) {
	c := newLocCell(t)
	if r.setup != nil {
		r.setup(t, c)
	}
	base := r.base
	if base == "" {
		base = "<T>"
	}
	var extra []string
	for _, e := range r.extra {
		extra = append(extra, c.p(e))
	}
	before := c.tree(t, extra...)
	code := r.code
	if code == "" {
		code = "unsafe_path"
	}
	wantLocFrame(t, c.create(t, base, r.root, r.path), failFrame(t, c.p(r.text), code))
	if after := c.tree(t, extra...); after != before {
		t.Errorf("the tree changed; 5fd08069 makes nothing\nbefore\n%s\nafter\n%s", before, after)
	}
}

// The create cells of round tier0-20 that 5fd08069 refuses with nothing made.
func TestLocationCellsCreateRefused(t *testing.T) {
	requireWorktreeListZ(t)
	setG := func(t *testing.T, c *locCell) {
		c.dir(t, "<X>/proj/w1")
		r0Repo(t, c.p("<X>/proj/src"))
		c.file(t, "<X>/proj/keep")
		c.link(t, "<X>/proj", "<B>/cwS")
	}
	setM := func(t *testing.T, c *locCell) {
		c.dir(t, "<X>/proj/loc")
		c.marker(t, "<X>/proj")
	}
	const session = "; beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees " +
		"marker); a worktree location must be outside every session's worktree"
	for _, r := range []locCreateRow{
		{name: "C-W2", root: "<X>/", path: "<X>/./a/b/c",
			text: "refusing to create worktree: <X>/./a/b/c is not <worktree location>/<directory>/<name> beneath <X>/"},
		{name: "C-W3", root: "<X>", path: "<X>/.Claude-Managed-Worktrees/w1",
			text: "refusing to create worktree: <X>/.Claude-Managed-Worktrees/w1 uses the reserved name " +
				".Claude-Managed-Worktrees for a directory under the worktree location"},
		{name: "C-W4", root: "<X>/.GIT/loc", path: "<X>/.GIT/loc/p/w1",
			text: "refusing to create worktree: <X>/.GIT/loc has a component named .GIT, which is reserved" + locRootTail},
		{name: "C-W4e", setup: func(t *testing.T, c *locCell) { c.dir(t, "<X>/x/.git") },
			root: "<X>/x/.git/loc", path: "<X>/x/.git/loc/p/w1",
			text: "refusing to create worktree: <X>/x/.git/loc has a component named .git, which is reserved" + locRootTail},
		{name: "C-W5", setup: setG, base: "<X>/proj/src", root: "<X>", path: "<X>/proj/w2",
			text: "refusing to create worktree: <X> would place worktrees in <X>/proj, which contains the " +
				"repository <X>/proj/src; choose a location the repository is not inside"},
		{name: "C-W6", setup: setG, base: "<B>/cwS/src", root: "<X>", path: "<X>/proj/w2",
			text: "refusing to create worktree: <X> would place worktrees in <X>/proj, which contains the " +
				"repository <B>/cwS/src; choose a location the repository is not inside"},
		{name: "C-W7", setup: func(t *testing.T, c *locCell) {
			runGit(t, c.B, "init", "-q", "-b", "main", "--separate-git-dir="+c.p("<B>/cwG"), c.p("<B>/cwT"))
			writeFile(t, c.p("<B>/cwT/keep"), "keep\n", 0o644)
			runGit(t, c.p("<B>/cwT"), "add", "keep")
			runGit(t, c.p("<B>/cwT"), "commit", "-q", "-m", "one")
			c.dir(t, "<B>/cwG/loc/p/w1")
			c.file(t, "<B>/cwG/loc/p/keep")
		}, base: "<B>/cwT", root: "<B>/cwG/loc", path: "<B>/cwG/loc/q/w2", extra: []string{"<B>/cwG", "<B>/cwT"},
			text: "refusing to create worktree: <B>/cwG/loc leads into the repository <B>/cwT (at <B>/cwG); " +
				"a worktree location must be outside the repository"},
		{name: "C-W19", setup: func(t *testing.T, c *locCell) {
			runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/proj"))
			c.dir(t, "<X>/proj/w1")
			c.file(t, "<X>/proj/keep")
		}, root: "<X>", path: "<X>/proj/w2",
			text: "refusing to create worktree: <X> would place worktrees in <X>/proj, which is (or is inside) " +
				"<X>/proj, a worktree of the repository <T>; a worktree location must be outside the repository's checkouts"},
		{name: "C-W21", setup: func(t *testing.T, c *locCell) {
			r0Repo(t, c.p("<X>/T0"))
			runGit(t, c.p("<X>/T0"), "worktree", "add", "-q", c.p("<X>/W"))
			if err := os.Rename(c.p("<X>/T0"), c.p("<X>/W/T")); err != nil {
				t.Fatal(err)
			}
			runGit(t, c.p("<X>/W/T"), "worktree", "repair")
			c.link(t, "<X>/W/T", "<B>/cwS21")
		}, base: "<B>/cwS21", root: "<X>/W/T/x", path: "<X>/W/T/x/p/w1",
			text: "refusing to create worktree: <X>/W/T/x leads into <X>/W, a worktree of the repository <B>/cwS21 " +
				"(at <X>/W); a worktree location must be outside the repository's checkouts"},
		{name: "C-W9", setup: setM, root: "<X>/proj/loc", path: "<X>/proj/loc/p/w1",
			text: "refusing to create worktree: <X>/proj/loc leads into a session worktree (at <X>/proj/loc" + session},
		{name: "C-W10", setup: setM, root: "<X>/proj/new", path: "<X>/proj/new/p/w1",
			text: "refusing to create worktree: <X>/proj/new would be made inside a session worktree or its " +
				"per-repository directory (at <X>/proj/new" + session},
		{name: "C-W11", setup: setM, root: "<X>/proj", path: "<X>/proj/p/w1",
			text: "refusing to create worktree: <X>/proj is itself a per-repository worktree directory (it holds a " +
				".claude-managed-worktrees marker); choose the directory above it, or another location"},
		// Equal on both builds: claustrum keeps its text.
		{name: "C-W12", setup: func(t *testing.T, c *locCell) {
			c.dir(t, "<X>/C/.claude/worktrees/w0")
			c.file(t, "<X>/C/.claude/worktrees/w0/.git")
		}, root: "<X>/C/.claude/worktrees/w0/loc", path: "<X>/C/.claude/worktrees/w0/loc/p/w1",
			text: "refusing to create worktree: <X>/C/.claude/worktrees/w0/loc is inside a git checkout " +
				"(<X>/C/.claude/worktrees/w0 has a .git entry); a worktree location must be outside every checkout, " +
				"so that no session working in one can reach it — choose a directory that is not part of any repository"},
		{name: "C-W13", setup: locChain, root: "<B>/cwl/l1/loc", path: "<B>/cwl/l1/loc/q/w2",
			text: "refusing to create worktree: <B>/cwl/l1/loc passes through too many symbolic links (a loop?)"},
		{name: "C-W14", setup: func(t *testing.T, c *locCell) {
			c.file(t, "<X>/F")
			chmodForTest(t, c.p("<X>/F"), 0o666)
		}, root: "<X>/F/x", path: "<X>/F/x/p/w1", code: "mkdir_failed",
			text: "failed to create parent directory: <X>/F: not a directory"},
		{name: "C-W15", setup: func(t *testing.T, c *locCell) {
			c.dir(t, "<X>/a/b/c")
			c.file(t, "<X>/a/.git")
			chmodForTest(t, c.p("<X>/a/b"), 0o777)
		}, root: "<X>/a/b/c", path: "<X>/a/b/c/p/w1",
			text: "refusing to create worktree: <X>/a/b/c is inside a git checkout (<X>/a has a .git entry); " +
				"a worktree location must be outside every checkout, so that no session working in one can " +
				"reach it — choose a directory that is not part of any repository"},
	} {
		t.Run(r.name, r.run)
	}
}

// locChain is the chain of the cells C-W13 and C-S5: <B>/cwl/l1 to l45 are 45 links,
// and l45 leads to <X>/R13. The kernel follows at most 40 of them on Linux and 32
// on macOS.
func locChain(t *testing.T, c *locCell) {
	c.dir(t, "<X>/R13/loc/p/w1")
	c.file(t, "<X>/R13/loc/p/keep")
	c.dir(t, "<B>/cwl")
	c.link(t, "<X>/R13", "<B>/cwl/l45")
	for i := 44; i >= 1; i-- {
		if err := os.Symlink(fmt.Sprintf("l%d", i+1), c.p(fmt.Sprintf("<B>/cwl/l%d", i))); err != nil {
			t.Fatal(err)
		}
	}
}

// C-W1: a relative worktreePath wins over the filesystem-root text of the root "/".
func TestLocationCellRelativePathFirst(t *testing.T) {
	c := newLocCell(t)
	before := c.tree(t)
	wantLocFrame(t, c.create(t, "<T>", "/", "x/y"), failFrame(t, `refusing to create worktree: x/y is a relative `+
		`path; choose the session folder by its absolute path, without ".."`, "unsafe_path"))
	if after := c.tree(t); after != before {
		t.Errorf("the tree changed; 5fd08069 makes nothing\n%s", after)
	}
	if _, err := os.Lstat("x"); err == nil {
		t.Errorf("the create made x in the working folder of the test")
	}
}

// C-W8: the root is in the listed worktree w0 whose folder is gone. 5fd08069 makes
// <X>/w0 (mode 0700, empty) and refuses. The entry of w0 stays.
func TestLocationCellGoneWorktreeFolderMade(t *testing.T) {
	requireWorktreeListZ(t)
	c := newLocCell(t)
	runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/w0"))
	if err := os.RemoveAll(c.p("<X>/w0")); err != nil {
		t.Fatal(err)
	}
	wantLocFrame(t, c.create(t, "<T>", "<X>/w0/loc", "<X>/w0/loc/p/w1"), failFrame(t, c.p(
		"refusing to create worktree: <X>/w0/loc leads into <X>/w0, a worktree of the repository <T> (at <X>/w0); "+
			"a worktree location must be outside the repository's checkouts"), "unsafe_path"))
	fi, err := os.Lstat(c.p("<X>/w0"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("<X>/w0 = %v, %v; 5fd08069 makes it with mode 0700", fi, err)
	}
	if entries, _ := os.ReadDir(c.p("<X>/w0")); len(entries) != 0 {
		t.Errorf("<X>/w0 holds %d entries; 5fd08069 leaves it empty", len(entries))
	}
	if got := r0cState(t, &r0Fixture{T: c.T}); got != "worktrees/w0 refs/heads/main refs/heads/w0" {
		t.Errorf("entries and branches of <T> = %s", got)
	}
}

// C-W16: the root passes through a link that another user owns. The round used a
// second user. Here a link in <B> gets the owner uid 1002 through the stat seam.
func TestLocationCellLinkOwner(t *testing.T) {
	if os.Geteuid() == 1002 {
		t.Skip("the test user is uid 1002, the owner of the link in this cell")
	}
	c := newLocCell(t)
	if err := os.Mkdir(c.p("<B>/cw16"), 0o700); err != nil {
		t.Fatal(err)
	}
	c.link(t, "<B>/cw16", "<B>/L2")
	foreignOwner(t, 1002, c.p("<B>/L2"))
	r := locCreateRow{root: "<B>/L2/loc", path: "<B>/L2/loc/p/w1", extra: []string{"<B>/cw16"},
		text: "refusing to create worktree: <B>/L2/loc passes through <B>/L2, which is owned by uid 1002, " +
			"neither you nor the system; choose a location you reach through your own directories"}
	before := c.tree(t, c.p("<B>/cw16"))
	wantLocFrame(t, c.create(t, "<T>", r.root, r.path), failFrame(t, c.p(r.text), "unsafe_path"))
	if after := c.tree(t, c.p("<B>/cw16")); after != before {
		t.Errorf("the tree changed; 5fd08069 makes nothing\nbefore\n%s\nafter\n%s", before, after)
	}
}

// C-W18: the <directory> level is writable by every user. 5fd08069 refuses, and the
// old registration of the leaf keeps its probe file.
func TestLocationCellWritableFolderKeepsRegistration(t *testing.T) {
	privateGroup(t)
	c := newLocCell(t)
	c.dir(t, "<X>/proj")
	c.marker(t, "<X>/proj")
	chmodForTest(t, c.p("<X>/proj"), 0o777)
	runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/proj/w1"))
	if err := os.RemoveAll(c.p("<X>/proj/w1")); err != nil {
		t.Fatal(err)
	}
	c.file(t, "<T>/.git/worktrees/w1/probe")
	head, err := os.ReadFile(c.p("<T>/.git/worktrees/w1/HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	before := c.tree(t, c.p("<T>/.git/worktrees"))
	wantLocFrame(t, c.create(t, "<T>", "<X>", "<X>/proj/w1"), failFrame(t, c.p("refusing to create worktree: "+
		"<X>/proj is writable by every user on this host (mode 0777); choose a directory only you can write to, "+
		"or remove the extra write permission (chmod go-w)"), "unsafe_path"))
	if after := c.tree(t, c.p("<T>/.git/worktrees")); after != before {
		t.Errorf("the tree changed; 5fd08069 keeps the registration\nbefore\n%s\nafter\n%s", before, after)
	}
	if b, err := os.ReadFile(c.p("<T>/.git/worktrees/w1/HEAD")); err != nil || string(b) != string(head) {
		t.Errorf("HEAD of the registration = %q, %v, want %q", b, err, head)
	}
}

// C-W20: the root is a link to "/". The round put the link at /cwL. Here it is in
// <B>, and the <directory> level is a name that does not exist below "/".
func TestLocationCellRootLinkToSlash(t *testing.T) {
	skipIfRoot(t)
	c := newLocCell(t)
	c.link(t, "/", "<B>/cwL")
	name := "claustrum-cw20-" + filepath.Base(c.B)
	if _, err := os.Lstat("/" + name); err == nil {
		t.Skipf("/%s exists", name)
	}
	wantLocFrame(t, c.create(t, "<T>", "<B>/cwL", "<B>/cwL/"+name+"/w2"), failFrame(t, c.p(
		"refusing to create worktree: <B>/cwL resolves to the filesystem root"+locRootTail), "unsafe_path"))
	if _, err := os.Lstat("/" + name); err == nil {
		t.Errorf("/%s exists after the create; 5fd08069 makes nothing", name)
	}
}

// C-W17 and C-W17b: a failed `worktree list`, with and without -z, or a failed
// `rev-parse --show-toplevel`, stops the create. Nothing is made.
func TestLocationCellFailedCallStops(t *testing.T) {
	for _, r := range []struct{ name, call, text string }{
		{"C-W17", "worktree list", "cannot list the repository's worktrees: exit status 128"},
		{"C-W17b", "rev-parse --show-toplevel", "cannot determine the repository's work tree: exit status 128"},
	} {
		t.Run(r.name, func(t *testing.T) {
			c := newLocCell(t)
			stubGitCall(t, r.call, "exit 128")
			before := c.tree(t)
			wantLocFrame(t, c.create(t, "<T>", "<X>", "<X>/p/w1"),
				failFrame(t, "failed to create parent directory: "+r.text, "mkdir_failed"))
			if after := c.tree(t); after != before {
				t.Errorf("the tree changed; 5fd08069 makes nothing\n%s", after)
			}
		})
	}
}

// P0: the positive create. Both builds make <X>/p (mode 0700) with the marker and
// the worktree.
func TestLocationCellCreateSucceeds(t *testing.T) {
	c := newLocCell(t)
	got := c.create(t, "<T>", "<X>", "<X>/p/w1")
	wantLocFrame(t, got, `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":"`+jsonEscape(t, c.p("<X>/p/w1"))+
		`","sourceBranch":"main","branch":"b20"}}`)
	if b, err := os.ReadFile(c.p("<X>/p/" + managedWorktreesMarker)); err != nil || string(b) != managedWorktreesMarkerBody {
		t.Errorf("marker = %q, %v", b, err)
	}
	c.stays(t, "<X>/p/w1/.git", "<X>/p/w1/a.txt")
}

// locRemoveRow is a remove cell. A refusal must delete nothing. stay lists the
// entries that 5fd08069 keeps, and calls is false for a cell with no git call.
type locRemoveRow struct {
	name             string
	setup            func(t *testing.T, c *locCell)
	base, root, path string
	text             string
	stay             []string
	noCalls          bool
	extra            []string
}

func (r locRemoveRow) run(t *testing.T) {
	c := newLocCell(t)
	if r.setup != nil {
		r.setup(t, c)
	}
	base := r.base
	if base == "" {
		base = "<T>"
	}
	var extra []string
	for _, e := range r.extra {
		extra = append(extra, c.p(e))
	}
	before := c.tree(t, extra...)
	calls := logGitArgv(t)
	wantLocFrame(t, c.remove(t, base, r.root, r.path), refusalFrame(t, c.p(r.text)))
	c.stays(t, r.stay...)
	if after := c.tree(t, extra...); after != before {
		t.Errorf("the tree changed; 5fd08069 removes nothing\nbefore\n%s\nafter\n%s", before, after)
	}
	if steps := gitSteps(calls()); r.noCalls && len(steps) != 0 {
		t.Errorf("git calls = %q, want none", steps)
	}
}

// The remove cells of round tier0-20. 5fd08069 refuses each and removes nothing.
// claustrum before this change removed the leaf in C-S1 to C-S8. Each row runs in
// its temp dir only, so it is safe without the change too.
func TestLocationCellsRemoveRefused(t *testing.T) {
	requireWorktreeListZ(t)
	setG := func(t *testing.T, c *locCell) {
		c.dir(t, "<X>/proj/w1")
		r0Repo(t, c.p("<X>/proj/src"))
		c.file(t, "<X>/proj/keep")
		c.link(t, "<X>/proj", "<B>/cwS")
	}
	for _, r := range []locRemoveRow{
		{name: "C-S1", setup: func(t *testing.T, c *locCell) {
			c.dir(t, "<X>/.Git/w1")
			c.file(t, "<X>/.Git/keep")
		}, root: "<X>", path: "<X>/.Git/w1", noCalls: true, stay: []string{"<X>/.Git/w1", "<X>/.Git/keep"},
			text: "refusing to remove worktree: <X>/.Git/w1 uses the reserved name .Git for a directory under the worktree location"},
		{name: "C-S2", setup: func(t *testing.T, c *locCell) {
			c.dir(t, "<X>/.GIT/loc/p/w1")
			c.file(t, "<X>/.GIT/loc/p/keep")
		}, root: "<X>/.GIT/loc", path: "<X>/.GIT/loc/p/w1", noCalls: true,
			stay: []string{"<X>/.GIT/loc/p/w1", "<X>/.GIT/loc/p/keep"},
			text: "refusing to remove worktree: <X>/.GIT/loc has a component named .GIT, which is reserved" + locRootTail},
		{name: "C-S3", setup: setG, base: "<X>/proj/src", root: "<X>", path: "<X>/proj/w1", noCalls: true,
			stay: []string{"<X>/proj/w1", "<X>/proj/keep", "<X>/proj/src/.git"},
			text: "refusing to remove worktree: <X> would place worktrees in <X>/proj, which contains the " +
				"repository <X>/proj/src; choose a location the repository is not inside"},
		{name: "C-S8", setup: setG, base: "<B>/cwS/src", root: "<X>", path: "<X>/proj/w1",
			stay: []string{"<X>/proj/w1", "<X>/proj/keep", "<X>/proj/src/.git"},
			text: "refusing to remove worktree: <X> would place worktrees in <X>/proj, which contains the " +
				"repository <B>/cwS/src; choose a location the repository is not inside"},
		{name: "C-S4", setup: func(t *testing.T, c *locCell) {
			runGit(t, c.B, "init", "-q", "-b", "main", "--separate-git-dir="+c.p("<B>/cwG"), c.p("<B>/cwT"))
			writeFile(t, c.p("<B>/cwT/keep"), "keep\n", 0o644)
			runGit(t, c.p("<B>/cwT"), "add", "keep")
			runGit(t, c.p("<B>/cwT"), "commit", "-q", "-m", "one")
			c.dir(t, "<B>/cwG/loc/p/w1")
			c.file(t, "<B>/cwG/loc/p/keep")
		}, base: "<B>/cwT", root: "<B>/cwG/loc", path: "<B>/cwG/loc/p/w1", extra: []string{"<B>/cwG", "<B>/cwT"},
			stay: []string{"<B>/cwG/loc/p/w1", "<B>/cwG/loc/p/keep"},
			text: "refusing to remove worktree: <B>/cwG/loc leads into the repository <B>/cwT (at <B>/cwG); " +
				"a worktree location must be outside the repository"},
		{name: "C-S7", setup: func(t *testing.T, c *locCell) {
			runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/proj"))
			c.dir(t, "<X>/proj/w1")
			c.file(t, "<X>/proj/keep")
		}, root: "<X>", path: "<X>/proj/w1", stay: []string{"<X>/proj/w1", "<X>/proj/keep", "<X>/proj/.git"},
			text: "refusing to remove worktree: <X> would place worktrees in <X>/proj, which is (or is inside) " +
				"<X>/proj, a worktree of the repository <T>; a worktree location must be outside the repository's checkouts"},
		{name: "C-S5", setup: locChain, root: "<B>/cwl/l1/loc", path: "<B>/cwl/l1/loc/p/w1",
			stay: []string{"<X>/R13/loc/p/w1", "<X>/R13/loc/p/keep"},
			text: "refusing to remove worktree: <B>/cwl/l1/loc passes through too many symbolic links (a loop?)"},
		{name: "C-W22", setup: func(t *testing.T, c *locCell) { c.link(t, "<X>", "<B>/LK") },
			root: "<B>/LK/missing", path: "<B>/LK/missing/p/w1",
			text: "failed to remove worktree: the worktree location <B>/LK/missing is not reachable (<X>/missing " +
				"does not exist); nothing was removed — retry once it is available, or remove the worktree by hand"},
		{name: "C-W23", setup: func(t *testing.T, c *locCell) { c.link(t, "<B>/LP", "<B>/LP") },
			root: "<B>/LP/loc", path: "<B>/LP/loc/p/w1",
			text: "refusing to remove worktree: <B>/LP/loc passes through too many symbolic links (a loop?)"},
		{name: "C-W24", setup: func(t *testing.T, c *locCell) { c.file(t, "<X>/F") },
			root: "<X>/F/x", path: "<X>/F/x/p/w1",
			text: "failed to remove worktree: the worktree location <X>/F/x is not reachable (<X>/F: not a " +
				"directory); nothing was removed — retry once it is available, or remove the worktree by hand"},
	} {
		t.Run(r.name, r.run)
	}
}

// C-S6: the root is a link to "/". The round removed /cwalk-p/w1 through /cwL. Here
// the link is in <B>, and the leaf is a new folder in /tmp with a file in it. The
// test skips where /tmp is not a folder (macOS, where /tmp is a link) or cannot be
// written.
func TestLocationCellRemoveRootLinkToSlash(t *testing.T) {
	if fi, err := os.Lstat("/tmp"); err != nil || !fi.IsDir() {
		t.Skip("/tmp is not a folder on this host")
	}
	leaf, err := os.MkdirTemp("/tmp", "claustrum-cs6-")
	if err != nil {
		t.Skipf("cannot make a folder in /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(leaf) })
	writeFile(t, filepath.Join(leaf, "keep"), "keep\n", 0o644)
	c := newLocCell(t)
	c.link(t, "/", "<B>/cwL")
	got := c.remove(t, "<T>", "<B>/cwL", "<B>/cwL/tmp/"+filepath.Base(leaf))
	wantLocFrame(t, got, refusalFrame(t, c.p("refusing to remove worktree: <B>/cwL resolves to the filesystem root"+locRootTail)))
	if _, err := os.Stat(filepath.Join(leaf, "keep")); errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is gone; 5fd08069 keeps it", leaf)
	}
}

// P1: the remove of an empty leaf in a marked <directory> level succeeds on both
// builds. The leaf goes, and keep stays.
func TestLocationCellRemoveEmptyLeaf(t *testing.T) {
	c := newLocCell(t)
	c.dir(t, "<X>/p/w1")
	c.marker(t, "<X>/p")
	c.file(t, "<X>/p/keep")
	wantLocFrame(t, c.remove(t, "<T>", "<X>", "<X>/p/w1"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`)
	if _, err := os.Lstat(c.p("<X>/p/w1")); err == nil {
		t.Errorf("<X>/p/w1 is still there; both builds remove it")
	}
	c.stays(t, "<X>/p/keep")
}

// C-W7 and C-S4 with a listing that names only the work tree <B>/cwT. git 2.47
// lists a separate git directory as the main worktree, so the rows above match it
// through the listing. This listing is constructed: the test shows that the git
// directory itself refuses the root, whatever git lists.
func TestLocationCellSeparateGitDirNotListed(t *testing.T) {
	for _, verb := range []string{"create", "remove"} {
		t.Run(verb, func(t *testing.T) {
			c := newLocCell(t)
			runGit(t, c.B, "init", "-q", "-b", "main", "--separate-git-dir="+c.p("<B>/cwG"), c.p("<B>/cwT"))
			writeFile(t, c.p("<B>/cwT/keep"), "keep\n", 0o644)
			runGit(t, c.p("<B>/cwT"), "add", "keep")
			runGit(t, c.p("<B>/cwT"), "commit", "-q", "-m", "one")
			c.dir(t, "<B>/cwG/loc/p/w1")
			c.file(t, "<B>/cwG/loc/p/keep")
			stubGitCall(t, "worktree list", `printf 'worktree %s\0branch refs/heads/main\0\0' '`+c.p("<B>/cwT")+`'; exit 0`)
			before := c.tree(t, c.p("<B>/cwG"), c.p("<B>/cwT"))
			text := "refusing to " + verb + " worktree: <B>/cwG/loc leads into the repository <B>/cwT (at <B>/cwG); " +
				"a worktree location must be outside the repository"
			if verb == "create" {
				wantLocFrame(t, c.create(t, "<B>/cwT", "<B>/cwG/loc", "<B>/cwG/loc/q/w2"), failFrame(t, c.p(text), "unsafe_path"))
			} else {
				wantLocFrame(t, c.remove(t, "<B>/cwT", "<B>/cwG/loc", "<B>/cwG/loc/p/w1"), refusalFrame(t, c.p(text)))
			}
			if after := c.tree(t, c.p("<B>/cwG"), c.p("<B>/cwT")); after != before {
				t.Errorf("the tree changed; 5fd08069 changes nothing\nbefore\n%s\nafter\n%s", before, after)
			}
		})
	}
}

// X1 and X2 of round tier0-22 (Linux VM), and M3 of that round on a macOS VM: the root goes
// through a link that leads out of a checkout or out of the repository. 5fd08069
// refuses and makes nothing in the folder behind the link.
func TestLocationCellLinkOutOfCheckout(t *testing.T) {
	requireWorktreeListZ(t)
	for _, r := range []locCreateRow{
		{name: "X1", setup: func(t *testing.T, c *locCell) {
			r0Repo(t, c.p("<X>/home"))
			c.dir(t, "<B>/out22")
			c.link(t, "<B>/out22", "<X>/home/link")
		}, root: "<X>/home/link/wt", path: "<X>/home/link/wt/p/w1", extra: []string{"<B>/out22"},
			text: "refusing to create worktree: <X>/home/link/wt is inside a git checkout (<X>/home has a .git " +
				"entry); a worktree location must be outside every checkout, so that no session working in one " +
				"can reach it — choose a directory that is not part of any repository"},
		{name: "X2", setup: func(t *testing.T, c *locCell) {
			r0Repo(t, c.p("<X>/R"))
			writeFile(t, c.p("<X>/R/sub/f"), "f\n", 0o644)
			runGit(t, c.p("<X>/R"), "add", "sub/f")
			runGit(t, c.p("<X>/R"), "commit", "-q", "-m", "sub")
			c.dir(t, "<B>/out22b")
			c.link(t, "<B>/out22b", "<X>/R/out")
		}, base: "<X>/R/sub", root: "<X>/R/out/wt", path: "<X>/R/out/wt/p/w1", extra: []string{"<B>/out22b"},
			text: "refusing to create worktree: <X>/R/out/wt leads into the repository <X>/R/sub (at <X>/R); " +
				"a worktree location must be outside the repository"},
	} {
		t.Run(r.name, r.run)
	}
}

// X3 of round tier0-22 (Linux VM): a missing root with a final slash. The reason
// names the path with the slash dropped. M1 and M2 on a macOS VM name the path with
// its links followed, as C-W22 does.
func TestLocationCellMissingRootSlash(t *testing.T) {
	r := locRemoveRow{root: "<B>/gone22/", path: "<B>/gone22/p/w1",
		text: "failed to remove worktree: the worktree location <B>/gone22/ is not reachable (<B>/gone22 does " +
			"not exist); nothing was removed — retry once it is available, or remove the worktree by hand"}
	r.run(t)
}

// The folder of a gone listed worktree (cell C-W8) is made through a root at the
// parent that the checkout test saw. Here the parent <X> becomes a link to <B>/away
// after the test. Nothing is made behind the link, and nothing in the old folder.
func TestLocationGoneFolderParentSwapped(t *testing.T) {
	requireWorktreeListZ(t)
	c := newLocCell(t)
	runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/w0"))
	if err := os.RemoveAll(c.p("<X>/w0")); err != nil {
		t.Fatal(err)
	}
	c.dir(t, "<B>/away")
	old := goneFolderStep
	goneFolderStep = func() {
		if err := os.Rename(c.p("<X>"), c.p("<B>/Xold")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(c.p("<B>/away"), c.p("<X>")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { goneFolderStep = old })
	wantLocFrame(t, c.create(t, "<T>", "<X>/w0/loc", "<X>/w0/loc/p/w1"), failFrame(t, c.p(
		"refusing to create worktree: <X>/w0/loc leads into <X>/w0, a worktree of the repository <T> (at <X>/w0); "+
			"a worktree location must be outside the repository's checkouts"), "unsafe_path"))
	for _, p := range []string{"<B>/away/w0", "<B>/Xold/w0"} {
		if _, err := os.Lstat(c.p(p)); err == nil {
			t.Errorf("%s exists; a parent that changed after the test gets no folder", c.p(p))
		}
	}
}

// The parent <X> of the gone worktree becomes a link to <B>/away after the checkout
// test and before its identity is taken. The identity comes from an lstat, so it is
// the link, and the folder is not made behind it.
func TestLocationGoneFolderParentLinkedBeforeIdentity(t *testing.T) {
	requireWorktreeListZ(t)
	c := newLocCell(t)
	runGit(t, c.T, "worktree", "add", "-q", c.p("<X>/w0"))
	if err := os.RemoveAll(c.p("<X>/w0")); err != nil {
		t.Fatal(err)
	}
	c.dir(t, "<B>/away")
	old := goneIdentityStep
	goneIdentityStep = func() {
		if err := os.Rename(c.p("<X>"), c.p("<B>/Xold")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(c.p("<B>/away"), c.p("<X>")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { goneIdentityStep = old })
	wantLocFrame(t, c.create(t, "<T>", "<X>/w0/loc", "<X>/w0/loc/p/w1"), failFrame(t, c.p(
		"refusing to create worktree: <X>/w0/loc leads into <X>/w0, a worktree of the repository <T> (at <X>/w0); "+
			"a worktree location must be outside the repository's checkouts"), "unsafe_path"))
	for _, p := range []string{"<B>/away/w0", "<B>/Xold/w0"} {
		if _, err := os.Lstat(c.p(p)); err == nil {
			t.Errorf("%s exists; a parent that became a link gets no folder", c.p(p))
		}
	}
}
