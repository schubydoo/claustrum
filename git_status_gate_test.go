package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The gate of git.status, row by row. Each row is from the side-by-side probe of
// 89cb6289 on Linux, macOS and Windows VMs. docs/PROTOCOL.md → git.status gives the
// rules.

const (
	statusNotRepo = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`
	statusClean   = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":true}}`
)

// statusFixture is a repository T with one linked worktree W beside it, as in the
// control row K0.
type statusFixture struct {
	root  string // holds T and W
	T, W  string
	entry string // T/.git/worktrees/W
}

func newStatusFixture(t *testing.T) statusFixture {
	t.Helper()
	return newStatusFixtureInit(t, "status-base")
}

// newStatusFixtureInit is newStatusFixture with extra `git init` options. key names
// the fixture template. The test is skipped when this git does not take the options.
func newStatusFixtureInit(t *testing.T, key string, initArgs ...string) statusFixture {
	t.Helper()
	requireGit(t)
	if len(initArgs) > 0 {
		probe := exec.Command("git", append(append([]string{"init", "-q"}, initArgs...), t.TempDir())...)
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("this git does not take `git init %s`: %v: %s", strings.Join(initArgs, " "), err, out)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := statusFixture{root: root, T: filepath.Join(root, "T"), W: filepath.Join(root, "W")}
	copyFixtureTemplate(t, key, f.T, func(t *testing.T, top string) {
		runGit(t, top, append([]string{"init", "-q", "-b", "main"}, initArgs...)...)
		writeFile(t, filepath.Join(top, "t.txt"), "t\n", 0o644)
		writeFile(t, filepath.Join(top, ".gitignore"), ".claude/worktrees/\n", 0o644)
		runGit(t, top, "add", ".")
		runGit(t, top, "commit", "-q", "-m", "c0")
	})
	runGit(t, f.T, "worktree", "add", "-q", "-b", "w", f.W)
	f.entry = filepath.Join(f.T, ".git", "worktrees", "W")
	return f
}

// statusGitOut runs the real git in dir with stdin and returns its trimmed stdout.
func statusGitOut(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func statusFrame(t *testing.T, path, baseRepo string) string {
	t.Helper()
	return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.status", map[string]any{"path": path, "baseRepo": baseRepo}))
}

func (f statusFixture) status(t *testing.T) string {
	t.Helper()
	return statusFrame(t, f.W, f.T)
}

func (f statusFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.entry, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The entry files decide the gate. One row breaks one file.
func TestGitStatusEntryGate(t *testing.T) {
	mib := strings.Repeat("\n", statusFileMaxBytes)
	cases := []struct {
		name string
		edit func(t *testing.T, f statusFixture)
		want string
	}{
		// Row K0.
		{"K0 control", func(*testing.T, statusFixture) {}, statusClean},
		// Rows G01, G03 to G10 and G12, n26c and n26d: the .git inside path plays no part.
		{"G01 no .git in path", func(t *testing.T, f statusFixture) {
			if err := os.Remove(filepath.Join(f.W, ".git")); err != nil {
				t.Fatal(err)
			}
		}, statusClean},
		{"n26d .git in path names another place", func(t *testing.T, f statusFixture) {
			writeFile(t, filepath.Join(f.W, ".git"), "gitdir: "+filepath.Join(f.root, "nowhere")+"\n", 0o644)
		}, statusClean},
		// Row n03: a relative gitdir counts from the entry folder.
		{"n03 relative gitdir", func(t *testing.T, f statusFixture) {
			f.write(t, "gitdir", "../../../../W/.git\n")
		}, statusClean},
		// Row n04: the last component must be .git.
		{"n04 gitdir ends in dotgit", func(t *testing.T, f statusFixture) {
			f.write(t, "gitdir", filepath.Join(f.W, "dotgit")+"\n")
		}, statusNotRepo},
		// Rows n05c and n05b: exactly 1 MiB passes, one byte more fails.
		{"n05c gitdir of 1 MiB", func(t *testing.T, f statusFixture) {
			v := filepath.Join(f.W, ".git")
			f.write(t, "gitdir", v+mib[len(v):])
		}, statusClean},
		{"n05b gitdir of 1 MiB + 1", func(t *testing.T, f statusFixture) {
			f.write(t, "gitdir", filepath.Join(f.W, ".git")+mib[len(filepath.Join(f.W, ".git")):]+"\n")
		}, statusNotRepo},
		// No row: an empty gitdir names no folder.
		{"empty gitdir", func(t *testing.T, f statusFixture) { f.write(t, "gitdir", "\n") }, statusNotRepo},
		// Row v2 (Linux VM): a second entry whose gitdir is a directory is passed over.
		{"v2 second entry with a gitdir folder", func(t *testing.T, f statusFixture) {
			if err := os.MkdirAll(filepath.Join(filepath.Dir(f.entry), "other", "gitdir"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, statusClean},
		// Row n01: two entries name path.
		{"n01 two matching entries", func(t *testing.T, f statusFixture) {
			twin := filepath.Join(filepath.Dir(f.entry), "W2")
			if err := os.CopyFS(twin, os.DirFS(f.entry)); err != nil {
				t.Fatal(err)
			}
		}, statusNotRepo},
		// Row C10 (macOS VM): another letter case in the gitdir file does not match.
		// On Windows it matches (rows x6 and z1, Windows VM).
		{"C10 gitdir in another case", func(t *testing.T, f statusFixture) {
			f.write(t, "gitdir", filepath.Join(f.root, "w", ".git")+"\n")
		}, map[bool]string{false: statusNotRepo, true: statusClean}[runtime.GOOS == "windows"]},
		// Row n19a.
		{"n19a no commondir", func(t *testing.T, f statusFixture) {
			if err := os.Remove(filepath.Join(f.entry, "commondir")); err != nil {
				t.Fatal(err)
			}
		}, statusNotRepo},
		// Rows n19b, n19c and n19d.
		{"n19b absolute commondir", func(t *testing.T, f statusFixture) {
			f.write(t, "commondir", filepath.Join(f.T, ".git")+"\n")
		}, statusClean},
		{"n19c commondir with spaces", func(t *testing.T, f statusFixture) {
			f.write(t, "commondir", "../..\n  \n")
		}, statusClean},
		{"n19d commondir ../../.", func(t *testing.T, f statusFixture) {
			f.write(t, "commondir", "../../.\n")
		}, statusClean},
		// Row n19g.
		{"n19g commondir of 1 MiB + 1", func(t *testing.T, f statusFixture) {
			f.write(t, "commondir", "../.."+mib[len("../.."):]+"\n")
		}, statusNotRepo},
		// Rows C11 (macOS VM) and D10 (Windows VM): another letter case fails.
		{"C11 commondir in another case", func(t *testing.T, f statusFixture) {
			f.write(t, "commondir", filepath.Join(f.root, "t", ".git")+"\n")
		}, statusNotRepo},
		// Rows n15, o14a and o14e.
		{"n15 HEAD garbage", func(t *testing.T, f statusFixture) { f.write(t, "HEAD", "garbage\n") }, statusNotRepo},
		{"o14a HEAD of 39 hex", func(t *testing.T, f statusFixture) {
			f.write(t, "HEAD", strings.Repeat("a", 39)+"\n")
		}, statusNotRepo},
		{"o14e HEAD ref: x", func(t *testing.T, f statusFixture) { f.write(t, "HEAD", "ref: x\n") }, statusNotRepo},
		// Rows o14b and o14f.
		{"o14b HEAD of 40 hex", func(t *testing.T, f statusFixture) {
			f.write(t, "HEAD", statusGitOut(t, f.T, "", "rev-parse", "HEAD")+"\n")
		}, statusClean},
		{"o14f HEAD with trailing spaces", func(t *testing.T, f statusFixture) {
			f.write(t, "HEAD", "ref: refs/heads/main   \n")
		}, statusClean},
		// Rows n12 and n12b.
		{"n12 config.worktree core.sparseCheckout", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[core]\n\tsparseCheckout = true\n")
		}, statusClean},
		{"n12b config.worktree index.sparse", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[index]\n\tsparse = true\n")
		}, statusClean},
		// Rows v3a and v3b (Linux VM).
		{"v3a config.worktree core.sparseCheckoutCone", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[core]\n\tsparseCheckoutCone = true\n")
		}, statusClean},
		{"v3b config.worktree core.bare", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[core]\n\tbare = false\n")
		}, statusNotRepo},
		// Row n13.
		{"n13 config.worktree user.name", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[user]\n\tname = x\n")
		}, statusNotRepo},
		// Row n14c.
		{"n14c config.worktree syntax error", func(t *testing.T, f statusFixture) {
			f.write(t, "config.worktree", "[core\n")
		}, statusNotRepo},
		// Row n14.
		{"n14 config.worktree is a folder", func(t *testing.T, f statusFixture) {
			if err := os.Mkdir(filepath.Join(f.entry, "config.worktree"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, statusNotRepo},
		// Row n26: a locked worktree passes.
		{"n26 locked", func(t *testing.T, f statusFixture) { f.write(t, "locked", "") }, statusClean},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusFixture(t)
			c.edit(t, f)
			if got := f.status(t); got != c.want {
				t.Errorf("git.status = %s\nwant %s", got, c.want)
			}
		})
	}
}

// The tests on path and baseRepo that need no link.
func TestGitStatusPathGate(t *testing.T) {
	f := newStatusFixture(t)
	writeFile(t, filepath.Join(f.root, "F"), "x\n", 0o644)
	if err := os.Mkdir(filepath.Join(f.W, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(f.T, ".claude", "worktrees", "s0")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "s0", inside)
	deep := filepath.Join(f.T, "sub", "wt")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "deep", deep)
	if err := os.MkdirAll(filepath.Join(f.T, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, path, base, want string }{
		// Row n09: path is a regular file. Row n26b: path is gone.
		{"n09 path is a file", filepath.Join(f.root, "F"), f.T, statusNotRepo},
		{"n26b path is gone", filepath.Join(f.root, "gone"), f.T, statusNotRepo},
		// Rows n18b and n25: no entry names baseRepo or a subfolder of the worktree.
		{"n18b path is baseRepo", f.T, f.T, statusNotRepo},
		{"n25 subfolder of the worktree", filepath.Join(f.W, "sub"), f.T, statusNotRepo},
		// Row n25b: a trailing separator.
		{"n25b trailing separator", f.W + string(os.PathSeparator), f.T, statusClean},
		// Rows n07b and n08: a worktree inside baseRepo with no link.
		{"n07b worktree under .claude/worktrees", inside, f.T, statusClean},
		{"n08 worktree at T/sub/wt", deep, f.T, statusClean},
		// Row n11c2: baseRepo is the cwd as sent. Row n11c3: a trailing separator.
		{"n11c2 baseRepo T/sub/..", f.W, filepath.Join(f.T, "sub") + string(os.PathSeparator) + "..", statusClean},
		{"n11c3 baseRepo T/", f.W, f.T + string(os.PathSeparator), statusClean},
		// Row n11c1: baseRepo does not resolve.
		{"n11c1 baseRepo T/nosub/..", f.W, filepath.Join(f.T, "nosub") + string(os.PathSeparator) + "..", statusNotRepo},
		// Row A05b: baseRepo is a subfolder of the repository.
		{"A05b baseRepo is a subfolder", inside, filepath.Join(f.T, "a", "b"), statusClean},
		// Rows n25d1 and KN.
		{"n25d1 empty path", "", f.T, statusNotRepo},
		{"KN plain folder", f.root, f.T, statusNotRepo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusFrame(t, c.path, c.base); got != c.want {
				t.Errorf("git.status(%s, %s) = %s\nwant %s", c.path, c.base, got, c.want)
			}
		})
	}
}

// Rows n18 and n18c: a common directory with no worktrees folder.
func TestGitStatusNoWorktreesFolder(t *testing.T) {
	f := newStatusFixture(t)
	if err := os.RemoveAll(filepath.Join(f.T, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != statusNotRepo {
		t.Errorf("git.status = %s\nwant %s", got, statusNotRepo)
	}
}

// Rows n10, n10b1 and n10c1: a baseRepo with .claude/worktrees in it answers
// isRepo:false. Rows n10b2 and n10c2: X/.claude alone passes the test.
func TestGitStatusBaseInWorktreesTree(t *testing.T) {
	requireGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(root, "X")
	xw := filepath.Join(root, "XW")
	runGit(t, root, "init", "-q", "-b", "main", x)
	runGit(t, x, "commit", "-q", "--allow-empty", "-m", "c0")
	runGit(t, x, "worktree", "add", "-q", "-b", "xw", xw)
	r := filepath.Join(x, ".claude", "worktrees", "r")
	if err := os.MkdirAll(r, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, base, want string }{
		{"n10 below .claude/worktrees", r, statusNotRepo},
		{"n10c1 .claude/worktrees itself", filepath.Dir(r), statusNotRepo},
		{"n10c2 .claude", filepath.Join(x, ".claude"), statusClean},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := statusFrame(t, xw, c.base); got != c.want {
				t.Errorf("git.status(XW, %s) = %s\nwant %s", c.base, got, c.want)
			}
		})
	}
	if !statusBaseInManagedTree(r) || statusBaseInManagedTree(filepath.Join(x, ".claude")) {
		t.Errorf("statusBaseInManagedTree: want true for %s and false for its .claude parent", r)
	}
}

// Rows o23, o23c and o23d: a folder above baseRepo holds the marker and no .git.
// Row o23b: with a .git in that folder the request passes.
func TestGitStatusMarkerAboveBaseRepo(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(t *testing.T, m string) (base string)
		want  string
	}{
		{"o23 marker file", func(t *testing.T, m string) string {
			writeFile(t, filepath.Join(m, managedWorktreesMarker), "", 0o644)
			return filepath.Join(m, "T")
		}, statusNotRepo},
		{"o23c marker two levels up", func(t *testing.T, m string) string {
			writeFile(t, filepath.Join(m, managedWorktreesMarker), "", 0o644)
			return filepath.Join(m, "a", "T")
		}, statusNotRepo},
		{"o23d marker is a folder", func(t *testing.T, m string) string {
			if err := os.MkdirAll(filepath.Join(m, managedWorktreesMarker), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(m, "T")
		}, statusNotRepo},
		{"o23b marker beside a .git", func(t *testing.T, m string) string {
			writeFile(t, filepath.Join(m, managedWorktreesMarker), "", 0o644)
			runGit(t, filepath.Dir(m), "init", "-q", "-b", "main", m)
			return filepath.Join(m, "T")
		}, statusClean},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireGit(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			m := filepath.Join(root, "M")
			if err := os.MkdirAll(m, 0o755); err != nil {
				t.Fatal(err)
			}
			base := c.build(t, m)
			if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
				t.Fatal(err)
			}
			w := filepath.Join(root, "W")
			runGit(t, root, "init", "-q", "-b", "main", base)
			runGit(t, base, "commit", "-q", "--allow-empty", "-m", "c0")
			runGit(t, base, "worktree", "add", "-q", "-b", "w", w)
			if got := statusFrame(t, w, base); got != c.want {
				t.Errorf("git.status = %s\nwant %s", got, c.want)
			}
		})
	}
}

// Rows o15c, o15e and o16: the size bound of index and of each sharedindex file.
// The bound is shrunk, so no test writes a file of 1 GiB.
func TestGitStatusIndexSizeBound(t *testing.T) {
	f := newStatusFixture(t)
	index, err := os.ReadFile(filepath.Join(f.entry, "index"))
	if err != nil {
		t.Fatal(err)
	}
	old := statusIndexMaxBytes
	t.Cleanup(func() { statusIndexMaxBytes = old })

	statusIndexMaxBytes = int64(len(index))
	if got := f.status(t); got != statusClean {
		t.Errorf("o15e index at the bound = %s\nwant %s", got, statusClean)
	}
	f.write(t, "sharedindex.x", string(bytes.Repeat([]byte{0}, len(index)+1)))
	if got := f.status(t); got != statusNotRepo {
		t.Errorf("o16 sharedindex.x over the bound = %s\nwant %s", got, statusNotRepo)
	}
	if err := os.Remove(filepath.Join(f.entry, "sharedindex.x")); err != nil {
		t.Fatal(err)
	}
	statusIndexMaxBytes = int64(len(index)) - 1
	if got := f.status(t); got != statusNotRepo {
		t.Errorf("o15c index over the bound = %s\nwant %s", got, statusNotRepo)
	}
}

// Rows o14b to o14f, n15, o14a and o14e, on the judge alone.
func TestStatusHeadValid(t *testing.T) {
	for in, want := range map[string]bool{
		"ref: refs/heads/w\n":            true,
		"ref: refs/heads/main   \n":      true,
		strings.Repeat("a", 40) + "\n":   true,
		strings.Repeat("a", 64) + "\n":   true,
		strings.Repeat("a", 39) + "\n":   false,
		strings.Repeat("g", 40) + "\n":   false,
		"ref: x\n":                       false,
		"garbage\n":                      false,
		"":                               false,
		strings.Repeat("a", 41) + "\n":   false, // not measured: claustrum takes 40 or 64 only
		"ref:refs/heads/w":               true,  // not measured
		"ref: \trefs/heads/w":            true,  // not measured
		"  ref: refs/heads/w  \n\n":      true,
		strings.Repeat("A", 40) + "\r\n": true, // not measured
	} {
		if got := statusHeadValid([]byte(in)); got != want {
			t.Errorf("statusHeadValid(%q) = %v, want %v", in, got, want)
		}
	}
}

// Row v5 (Linux VM): a marker inside baseRepo itself is ignored.
func TestGitStatusMarkerInsideBaseRepo(t *testing.T) {
	f := newStatusFixture(t)
	writeFile(t, filepath.Join(f.T, managedWorktreesMarker), "", 0o644)
	if got := f.status(t); got != statusClean {
		t.Errorf("git.status = %s\nwant %s", got, statusClean)
	}
}

// Rows v6a and v6b (Linux VM): a SHA-256 repository. The --attr-source value is the
// empty tree id of that object format, so status runs.
func TestGitStatusSHA256Repository(t *testing.T) {
	f := newStatusFixtureInit(t, "status-sha256", "--object-format=sha256")
	if got := f.status(t); got != statusClean {
		t.Fatalf("v6a git.status = %s\nwant %s", got, statusClean)
	}
	writeFile(t, filepath.Join(f.W, "t.txt"), "changed\n", 0o644)
	want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":false,"changes":[" M t.txt"]}}`
	if got := f.status(t); got != want {
		t.Errorf("v6b git.status = %s\nwant %s", got, want)
	}
}

// Rows o19a to o19d (macOS VM): a repository with the reftable format. git takes
// `--ref-format=reftable` since 2.45, and the test is skipped on an older one.
func TestGitStatusReftableRepository(t *testing.T) {
	f := newStatusFixtureInit(t, "status-reftable", "--ref-format=reftable")
	list := filepath.Join(f.entry, "reftable", "tables.list")
	names, err := os.ReadFile(list)
	if err != nil {
		t.Fatalf("the entry of a reftable worktree has no tables.list: %v", err)
	}
	// Row o19a.
	if got := f.status(t); got != statusClean {
		t.Fatalf("o19a git.status = %s\nwant %s", got, statusClean)
	}
	// Row o19b.
	writeFile(t, filepath.Join(f.W, "t.txt"), "changed\n", 0o644)
	want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":false,"changes":[" M t.txt"]}}`
	if got := f.status(t); got != want {
		t.Errorf("o19b git.status = %s\nwant %s", got, want)
	}
	// Rows o19c and o19d: the list names a table that is not there. Row r4: the list
	// is empty.
	missing := "0x000000000009-0x000000000009-deadbeef.ref\n"
	for row, body := range map[string]string{"o19c": string(names) + missing, "o19d": missing, "r4": ""} {
		writeFile(t, list, body, 0o644)
		if got := f.status(t); got != statusNotRepo {
			t.Errorf("%s git.status = %s\nwant %s", row, got, statusNotRepo)
		}
	}
}

// Rows o19a and r1 (macOS VM): the reftable files of the temporary git folder. Every
// regular file of the folder is copied, also a table that the list does not name.
func TestStatusGitDirReftableContents(t *testing.T) {
	entry, common := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"HEAD": "ref: refs/heads/.invalid\n", "index": "idx", "refs/heads": "x\n",
		"reftable/tables.list": "a.ref\n", "reftable/a.ref": "table", "reftable/b.ref": "not listed",
		"reftable/sub/c.ref": "in a folder",
	} {
		writeFile(t, filepath.Join(entry, filepath.FromSlash(name)), body, 0o644)
	}
	e := statusEntryAt(t, entry)
	files, ok := statusReftable(e.root)
	if want := []string{"a.ref", "b.ref", "tables.list"}; !ok || !slices.Equal(files, want) {
		t.Fatalf("statusReftable = %q %v, want %q", files, ok, want)
	}
	e.reftable = files
	tmp, err := buildStatusGitDir(e, common)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for name, want := range map[string]bool{"reftable/tables.list": true, "reftable/a.ref": true,
		"reftable/b.ref": true, "reftable/sub": false, "refs": false} {
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(name))); (err == nil) != want {
			t.Errorf("%s in the temporary folder: present %v, want %v", name, err == nil, want)
		}
	}
	// An entry with no reftable folder has no file to copy.
	if files, ok := statusReftable(statusEntryAt(t, t.TempDir()).root); !ok || files != nil {
		t.Errorf("statusReftable with no folder = %q %v, want none and true", files, ok)
	}
}

// The entry folder is opened once, at the match. A folder that takes its place
// after the match is never read: the answer comes from the matched folder, or it is
// isRepo:false. The swapped folder here is the entry without its index, which by
// itself answers a list of changes (row o15d).
func TestGitStatusEntrySwappedAfterMatch(t *testing.T) {
	f := newStatusFixture(t)
	old := statusEntryMatched
	t.Cleanup(func() { statusEntryMatched = old })
	swapped := false
	statusEntryMatched = func(entry string) {
		aside := entry + ".aside"
		if err := os.Rename(entry, aside); err != nil {
			// Windows does not rename a folder that the daemon holds open. The entry
			// then stays the matched one.
			t.Logf("the entry cannot be swapped here: %v", err)
			return
		}
		if err := os.CopyFS(entry, os.DirFS(aside)); err != nil {
			t.Error(err)
		}
		if err := os.Remove(filepath.Join(entry, "index")); err != nil {
			t.Error(err)
		}
		swapped = true
	}
	got := f.status(t)
	if got != statusClean && got != statusNotRepo {
		t.Errorf("git.status = %s\nwant %s or %s (swapped: %v)", got, statusClean, statusNotRepo, swapped)
	}
}
