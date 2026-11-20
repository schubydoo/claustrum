//go:build !windows

package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// lfsFixture is a shadow create on a repository with LFS pointers, a
// stand-in git-lfs smudge filter (lfsSmudgeHelper) and a fake server folder.
type lfsFixture struct {
	checkoutRuleFixture
	remote, log string
	oids        map[string]string // by file name
	bodies      map[string][]byte
}

// newLFSFixture commits a pointer for each file of files, with the filter f.
func newLFSFixture(t *testing.T, files map[string]string) lfsFixture {
	t.Helper()
	return newNamedLFSFixture(t, "f", files)
}

// newNamedLFSFixture is newLFSFixture with the filter name.
func newNamedLFSFixture(t *testing.T, name string, files map[string]string) lfsFixture {
	t.Helper()
	f := lfsFixture{checkoutRuleFixture: newCheckoutRuleFixture(t), remote: t.TempDir(),
		log: filepath.Join(t.TempDir(), "smudge.log"), oids: map[string]string{}, bodies: map[string][]byte{}}
	mustWrite(t, filepath.Join(f.repo, ".gitattributes"), "*.bin filter="+name+"\n")
	names := []string{".gitattributes"}
	for name, body := range files {
		sum := sha256.Sum256([]byte(body))
		oid := hex.EncodeToString(sum[:])
		f.oids[name], f.bodies[name] = oid, []byte(body)
		mustWrite(t, filepath.Join(f.repo, name), fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(body)))
		names = append(names, name)
	}
	// -f: the repository ignores .claude, and a test commits a file there.
	runGit(t, f.repo, append([]string{"add", "-f"}, names...)...)
	runGit(t, f.repo, "commit", "-q", "-m", "lfs")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outside(t, "[filter \""+name+"\"]\n\tsmudge = CLAUSTRUM_TEST_HELPER=lfs-smudge '"+filepath.ToSlash(exe)+"' '"+
		filepath.ToSlash(f.remote)+"' '"+filepath.ToSlash(f.log)+"'\n\trequired = true\n")
	return f
}

// local puts the object of name into the store of the repository, with body.
func (f lfsFixture) local(t *testing.T, name, body string) {
	t.Helper()
	p := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids[name])
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p, body)
}

// onServer puts the object of name on the fake server.
func (f lfsFixture) onServer(t *testing.T, name string) {
	t.Helper()
	mustWrite(t, filepath.Join(f.remote, f.oids[name]), string(f.bodies[name]))
}

// smudges are the lines of the stand-in filter, by file name: "present=<bool>
// private=<bool>", where private says the store was in a `.checkout-` folder.
func (f lfsFixture) smudges(t *testing.T) map[string]string {
	t.Helper()
	b, _ := os.ReadFile(f.log)
	out := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		short, rest, _ := strings.Cut(line, " ")
		present, store, _ := strings.Cut(rest, " ")
		for name, oid := range f.oids {
			if strings.HasPrefix(oid, short) && short != "" {
				out[name] = present + fmt.Sprintf(" private=%v", strings.Contains(store, string(filepath.Separator)+checkoutDirPrefix))
			}
		}
	}
	return out
}

func (f lfsFixture) wantSuccess(t *testing.T) {
	t.Helper()
	raw, _ := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
}

// shard is the shard folder of the object of name in the store of the
// repository.
func (f lfsFixture) shard(name string) string {
	return filepath.Dir(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids[name]))
}

// TestLFSSeedIntoPrivateStore: the filter reads the store of the checkout
// folder. A local object with the size of its pointer is there, also with
// wrong bytes. An object of another size or a link is not, and git-lfs
// downloads it (4c111e86, measured on Linux).
func TestLFSSeedIntoPrivateStore(t *testing.T) {
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object", "c.bin": "gamma-object", "d.bin": "delta-object"})
	f.local(t, "a.bin", "alpha-object")
	f.local(t, "b.bin", "BETA-OBJECT")
	f.local(t, "c.bin", "short")
	f.onServer(t, "c.bin")
	target := filepath.Join(t.TempDir(), "delta")
	mustWrite(t, target, "delta-object")
	d := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["d.bin"])
	if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, d); err != nil {
		t.Fatal(err)
	}
	f.onServer(t, "d.bin")
	f.wantSuccess(t)
	want := map[string]string{"a.bin": "present=true private=true", "b.bin": "present=true private=true",
		"c.bin": "present=false private=true", "d.bin": "present=false private=true"}
	if got := f.smudges(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("smudge lines = %v, want %v", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(f.leaf, "b.bin")); err != nil || string(b) != "BETA-OBJECT" {
		t.Errorf("b.bin of the worktree = %q, %v, want the bytes of the local object", b, err)
	}
}

// TestLFSFileUnderClaudeSmudgesOnce: the content check compares an LFS file
// under .claude with the sha256 of its pointer, with no second smudge. A good
// object passes, and wrong bytes of the same size refuse with the LFS text
// (4c111e86, measured on macOS). The path has the filter lfs.
func TestLFSFileUnderClaudeSmudgesOnce(t *testing.T) {
	const name = ".claude/commands/x.bin"
	for _, good := range []bool{true, false} {
		t.Run(fmt.Sprint("good ", good), func(t *testing.T) {
			f := newNamedLFSFixture(t, "lfs", map[string]string{name: "claude-lfs-object"})
			body := "claude-lfs-object"
			if !good {
				body = "CLAUDE-LFS-OBJECT"
			}
			f.local(t, name, body)
			raw, _ := f.create(t, nil)
			b, _ := os.ReadFile(f.log)
			if n := strings.Count(string(b), "\n"); n != 1 {
				t.Errorf("smudge lines = %q, want one", b)
			}
			if good {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			want := `so it did not keep it. Turn off Worktree for this session, then try again: the Git LFS content checked out at \"` + name +
				`\" doesn't match its pointer in commit `
			if !strings.Contains(raw, `"errorCode":"worktree_verify_content"`) || !strings.Contains(raw, want) {
				t.Fatalf("git.worktree_create = %s, want the LFS text %s", raw, want)
			}
		})
	}
}

// TestLFSKeepDownloadedObject: an object that git-lfs downloaded into the
// private store reaches the store of the repository, also with no folder
// `lfs` there before (measured on Linux). No temp name stays.
func TestLFSKeepDownloadedObject(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	f.wantSuccess(t)
	p := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])
	if b, err := os.ReadFile(p); err != nil || string(b) != "alpha-object" {
		t.Fatalf("the object in the store of the repository = %q, %v, want it kept", b, err)
	}
	names, _ := os.ReadDir(filepath.Dir(p))
	if len(names) != 1 {
		t.Errorf("the shard holds %v, want only the object", names)
	}
	// The modes of the folders are pinned by TestLFSKeepShardModeFollowsUmask.
}

// TestLFSKeepAfterTheSettleCall: after a good checkout the keep copies the
// object only once the settle call (diff-files) has run, and the reply comes
// after the keep (4c111e86, measured on macOS). Mutation: run the keep of
// runWorktreeCheckout before settleCheckout (the copy sees no settle call).
func TestLFSKeepAfterTheSettleCall(t *testing.T) {
	seamFreeSpace(t)
	setAttrSource(t, true)
	// No tick runs, so the one copy is the keep after the checkout.
	setKeepTick(t, time.Hour)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	prev := keepCopy
	var mu sync.Mutex
	copies, settled := 0, false
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		copies++
		for _, c := range readEnvCalls(t, f.shadowFixture.log) {
			settled = settled || slices.Contains(c.argv, "diff-files")
		}
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepCopy = prev })
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "diff-files") }) {
		t.Fatal("the create ran no settle call")
	}
	mu.Lock()
	defer mu.Unlock()
	if copies != 1 || !settled {
		t.Errorf("the keep made %d copies, after the settle call: %v; want 1 copy after it", copies, settled)
	}
	if !exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])) {
		t.Error("a.bin is not in the store of the repository")
	}
}

// TestLFSKeepShardModeFollowsUmask: 4c111e86 asks for mode 0777 for the two
// shard levels (measured on Linux), so umask 0002 gives 0775. A missing `lfs`
// and `lfs/objects` get 0755 less the umask (claustrum's own, not measured).
// The test sets the umask of the process, so it does not run in parallel.
func TestLFSKeepShardModeFollowsUmask(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	lfs := filepath.Join(f.gitDir, "lfs")
	if err := os.RemoveAll(lfs); err != nil {
		t.Fatal(err)
	}
	old := unix.Umask(0o002)
	t.Cleanup(func() { unix.Umask(old) })
	f.wantSuccess(t)
	shard := f.shard("a.bin")
	for d, want := range map[string]os.FileMode{lfs: 0o755, filepath.Join(lfs, "objects"): 0o755,
		filepath.Dir(shard): 0o775, shard: 0o775} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("the folder %s has mode %#o, want %#o", d, got, want)
		}
	}
}

// TestLFSKeepRefusesLink: a link at `lfs` of the git directory gets no
// object, also when it names a folder inside that git directory (claustrum's
// own: 4c111e86 keeps nothing behind a link to a folder outside, measured on
// Linux). The create succeeds.
func TestLFSKeepRefusesLink(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	inner := filepath.Join(f.gitDir, "otherlfs")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("otherlfs", filepath.Join(f.gitDir, "lfs")); err != nil {
		t.Fatal(err)
	}
	f.wantSuccess(t)
	if names, _ := os.ReadDir(inner); len(names) != 0 {
		t.Errorf("%s holds %v, want nothing behind the link", inner, names)
	}
}

// TestLFSSweepStaleTempNames: after the keep, the shard of the kept object
// loses the temp names of that object and those of another object older than
// 10 minutes. A newer one, upper case, a short oid, another name, a link and a
// folder stay. The shard of a seeded object keeps its names (measured on
// Linux, the link and the folder are claustrum's own). A name that only holds
// the pattern stays.
func TestLFSSweepStaleTempNames(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.local(t, "a.bin", "alpha-object")
	f.onServer(t, "b.bin")
	ob, oa := f.oids["b.bin"], f.oids["a.bin"]
	sb, sa := f.shard("b.bin"), f.shard("a.bin")
	if err := os.MkdirAll(sb, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-12 * time.Minute)
	files := []struct {
		dir, name string
		at        time.Time
		goes      bool
	}{
		{sb, ".claude-aaaaaaaa-" + ob, time.Now(), true},
		{sb, ".claude-bbbbbbbb-" + strings.Repeat("0", 64), old, true},
		{sb, ".claude-cccccccc-" + strings.Repeat("1", 64), time.Now().Add(-time.Minute), false},
		{sb, ".claude-dddddddd-" + strings.ToUpper(ob), old, false},
		{sb, ".claude-eeeeeeee-" + ob[:60], old, false},
		{sb, "other-file", old, false},
		{sb, "x.claude-aaaaaaaa-" + ob, old, false},
		{sb, ".claude-aaaaaaaa-" + ob + ".tmp", old, false},
		{sb, ".claude-aaaaaaa-" + ob, old, false},
		{sb, ".claude-aaaaaaaaa-" + ob, old, false},
		{sa, ".claude-aaaaaaaa-" + oa, old, false},
	}
	for _, x := range files {
		mustWrite(t, filepath.Join(x.dir, x.name), "stale")
		if err := os.Chtimes(filepath.Join(x.dir, x.name), x.at, x.at); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(sb, ".claude-ffffffff-"+ob)
	if err := os.Symlink("other-file", link); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(sb, ".claude-11111111-"+ob)
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantSuccess(t)
	for _, x := range files {
		if gone := !exists(filepath.Join(x.dir, x.name)); gone != x.goes {
			t.Errorf("%s gone %v, want %v", x.name, gone, x.goes)
		}
	}
	for _, p := range []string{link, folder} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: %v, want it kept", p, err)
		}
	}
	if !exists(filepath.Join(sb, ob)) {
		t.Error("the downloaded object is not in the store of the repository")
	}
}

// TestLFSCopyWhenNoLink: with no hard link, as across two file systems, the
// seed copies the object (measured on Linux). The keep copies it with mode
// 0666 before the umask (measured on Linux).
func TestLFSCopyWhenNoLink(t *testing.T) {
	seamFreeSpace(t)
	prevSeed, prevKeep := seedLink, keepHardLink
	seedLink = func(string, string) error { return os.ErrPermission }
	keepHardLink = func(*os.Root, string, string) error { return os.ErrPermission }
	t.Cleanup(func() { seedLink, keepHardLink = prevSeed, prevKeep })
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.local(t, "a.bin", "alpha-object")
	f.onServer(t, "b.bin")
	f.wantSuccess(t)
	if got := f.smudges(t)["a.bin"]; got != "present=true private=true" {
		t.Errorf("smudge line of a.bin = %q, want the seeded copy", got)
	}
	p := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["b.bin"])
	fi, err := os.Stat(p)
	// A file made with mode 0666 here gets the same umask.
	ref := filepath.Join(t.TempDir(), "ref")
	if err := os.WriteFile(ref, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(ref)
	if b, rerr := os.ReadFile(p); err != nil || rerr != nil || string(b) != "beta-object" || fi.Mode().Perm() != want.Mode().Perm() {
		t.Errorf("the kept copy = %v, %q, %v, want the object with mode 0666", fi, b, err)
	}
}

// TestLFSSeedCopiesThroughLink: a link at `lfs` or at `lfs/objects` of the
// repository gives the seed as a copy, with no hard link (4c111e86, measured
// on Linux).
func TestLFSSeedCopiesThroughLink(t *testing.T) {
	seamFreeSpace(t)
	for _, at := range []string{"lfs", filepath.Join("lfs", "objects")} {
		t.Run(filepath.ToSlash(at), func(t *testing.T) {
			links := 0
			prev := seedLink
			seedLink = func(src, dst string) error { links++; return prev(src, dst) }
			t.Cleanup(func() { seedLink = prev })
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
			f.local(t, "a.bin", "alpha-object")
			moved := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(filepath.Join(f.gitDir, at), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, filepath.Join(f.gitDir, at)); err != nil {
				t.Fatal(err)
			}
			f.wantSuccess(t)
			if got := f.smudges(t)["a.bin"]; got != "present=true private=true" || links != 0 {
				t.Errorf("smudge line of a.bin = %q with %d hard links, want the seeded copy", got, links)
			}
		})
	}
}

// TestLFSKeepAfterFailedCheckout: git-lfs downloads a.bin, then b.bin is on
// no server and the checkout fails. a.bin reaches the store of the repository
// by a hard link at its name, and no temp name stays (4c111e86 keeps after a
// deadline, measured on Linux. A failed filter is claustrum's own, not
// measured).
func TestLFSKeepAfterFailedCheckout(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	if raw, _ := f.create(t, nil); strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want a failure", raw)
	}
	if got := f.smudges(t)["a.bin"]; got != "present=false private=true" {
		t.Fatalf("smudge line of a.bin = %q, want a download", got)
	}
	p := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])
	if b, err := os.ReadFile(p); err != nil || string(b) != "alpha-object" {
		t.Fatalf("the object in the store of the repository = %q, %v, want it kept after a failed checkout", b, err)
	}
	if names, _ := os.ReadDir(filepath.Dir(p)); len(names) != 1 {
		t.Errorf("the shard holds %v, want only the object", names)
	}
}

// TestLFSKeepNeedsItsBytes: an object that git-lfs wrote with bytes that do
// not give its oid stays out of the store of the repository. The folder
// `lfs/objects` is made, and no shard folder (4c111e86, measured on Linux and
// macOS for bytes and a size that differ from the pointer). The row with the
// size of the pointer is claustrum's own, not measured.
func TestLFSKeepNeedsItsBytes(t *testing.T) {
	seamFreeSpace(t)
	for _, row := range []struct{ name, body string }{
		{"another size", "other-bytes-of-another-size"},
		{"the same size", "ALPHA-OBJECT"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
			mustWrite(t, filepath.Join(f.remote, f.oids["a.bin"]), row.body)
			f.wantSuccess(t)
			objects := filepath.Join(f.gitDir, "lfs", "objects")
			if names, err := os.ReadDir(objects); err != nil || len(names) != 0 {
				t.Errorf("%s = %v, %v, want a made folder with no shard", objects, names, err)
			}
		})
	}
}

// keepOne keeps the object of body from a private store into a new git
// directory after a good checkout. It returns the oid, the downloaded copy, the
// store of the repository and the inode of the copy before the keep.
func keepOne(t *testing.T, body string) (oid, src, store string, before uint64) {
	t.Helper()
	gitDir, private := t.TempDir(), t.TempDir()
	oid = putObject(t, private, body)
	src = storeObjectPath(private, oid)
	before = inodeOf(t, src)
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownloads(gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root}}, private, keepAfterGood)
	return oid, src, filepath.Join(gitDir, "lfs"), before
}

// inodeOf is the inode of the file at p.
func inodeOf(t *testing.T, p string) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Ino) //nolint:unconvert // Ino is uint64 on Linux, not on every system
}

// wantNotKept fails t when the store holds the object oid or a temp name, or
// when the downloaded copy src is not the file of inode before.
func wantNotKept(t *testing.T, oid, src, store string, before uint64) {
	t.Helper()
	shard := filepath.Dir(storeObjectPath(store, oid))
	if names, err := os.ReadDir(shard); err != nil || len(names) != 0 {
		t.Errorf("the shard = %v, %v, want it made and empty", names, err)
	}
	if got := inodeOf(t, src); got != before {
		t.Errorf("the downloaded copy has inode %d, want %d as before the keep", got, before)
	}
	if names, _ := os.ReadDir(filepath.Dir(src)); len(names) != 1 {
		t.Errorf("the folder of the downloaded copy holds %v, want only the copy", names)
	}
}

// TestLFSKeepCopiesAndSwapsInTheKept: after a good checkout the keep copies the
// object into a new file of the store, so the kept object is not the file of
// the download. A `.kept-<oid>` link of the kept object then replaces the
// downloaded copy, and no temp name and no `.kept-` name stay (4c111e86,
// measured on Linux).
func TestLFSKeepCopiesAndSwapsInTheKept(t *testing.T) {
	seamFreeSpace(t)
	oid, src, store, before := keepOne(t, "alpha-object")
	kept := storeObjectPath(store, oid)
	if b, err := os.ReadFile(kept); err != nil || string(b) != "alpha-object" {
		t.Fatalf("the kept object = %q, %v, want the bytes of the download", b, err)
	}
	if got := inodeOf(t, kept); got == before {
		t.Errorf("the kept object has the inode %d of the download, want a copy", got)
	}
	if got, want := inodeOf(t, src), inodeOf(t, kept); got != want {
		t.Errorf("the downloaded copy has inode %d, want %d of the kept object", got, want)
	}
	if names, _ := os.ReadDir(filepath.Dir(kept)); len(names) != 1 {
		t.Errorf("the shard holds %v, want only the object", names)
	}
	if names, _ := os.ReadDir(filepath.Dir(src)); len(names) != 1 {
		t.Errorf("the folder of the downloaded copy holds %v, want only the copy", names)
	}
}

// TestLFSKeepFullDiskKeepsNothing: a write of the copy that fails with ENOSPC
// after some bytes keeps nothing. The temp file goes at once, no `.kept-` name
// is made, and the downloaded copy stays (4c111e86, measured on Linux).
func TestLFSKeepFullDiskKeepsNothing(t *testing.T) {
	seamFreeSpace(t)
	prev := keepCopy
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		n, _ := io.CopyN(w, r, 4)
		return n, unix.ENOSPC
	}
	t.Cleanup(func() { keepCopy = prev })
	oid, src, store, before := keepOne(t, "alpha-object")
	wantNotKept(t, oid, src, store, before)
}

// TestLFSKeepStopsAtItsBudget: a copy that outlasts keepBudget stops and keeps
// nothing. The temp file goes, and no `.kept-` name is made (4c111e86,
// measured on Linux. The bound is claustrum's own).
func TestLFSKeepStopsAtItsBudget(t *testing.T) {
	seamFreeSpace(t)
	prevBudget, prevCopy := keepBudget, keepCopy
	keepBudget = 50 * time.Millisecond
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		time.Sleep(2 * keepBudget)
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepBudget, keepCopy = prevBudget, prevCopy })
	oid, src, store, before := keepOne(t, "alpha-object")
	wantNotKept(t, oid, src, store, before)
}

// TestLFSKeepBudgetIsPerObject: each object gets its own keepBudget, so four
// copies that each take 0.3 of the bound are all kept (claustrum's own, not
// measured).
func TestLFSKeepBudgetIsPerObject(t *testing.T) {
	seamFreeSpace(t)
	prevBudget, prevCopy := keepBudget, keepCopy
	keepBudget = 2 * time.Second
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		time.Sleep(keepBudget * 3 / 10)
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepBudget, keepCopy = prevBudget, prevCopy })
	gitDir, private := t.TempDir(), t.TempDir()
	var oids []string
	for _, body := range []string{"alpha-object", "beta-object", "gamma-object", "delta-object"} {
		oids = append(oids, putObject(t, private, body))
	}
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownloads(gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root}}, private, keepAfterGood)
	for _, oid := range oids {
		if !exists(storeObjectPath(filepath.Join(gitDir, "lfs"), oid)) {
			t.Errorf("the object %s is not kept, want each object kept within its own budget", oid)
		}
	}
}

// TestLFSKeepAfterDeadlineCopies: after a deadline the keep copies the object
// and swaps in the `.kept-` link, as after a good checkout (4c111e86, measured
// on Linux). The stale temp name of the same oid stays, because that keep does
// not sweep (claustrum's own, not measured).
func TestLFSKeepAfterDeadlineCopies(t *testing.T) {
	seamFreeSpace(t)
	gitDir, private := t.TempDir(), t.TempDir()
	oid := putObject(t, private, "alpha-object")
	src := storeObjectPath(private, oid)
	before := inodeOf(t, src)
	kept := storeObjectPath(filepath.Join(gitDir, "lfs"), oid)
	stale := filepath.Join(filepath.Dir(kept), ".claude-aaaaaaaa-"+oid)
	mustWrite(t, stale, "stale")
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownloads(gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root}}, private, keepAfterDeadline)
	if b, err := os.ReadFile(kept); err != nil || string(b) != "alpha-object" {
		t.Fatalf("the kept object = %q, %v, want it kept after a deadline", b, err)
	}
	if got := inodeOf(t, kept); got == before {
		t.Errorf("the kept object has the inode %d of the download, want a copy", got)
	}
	if got, want := inodeOf(t, src), inodeOf(t, kept); got != want {
		t.Errorf("the downloaded copy has inode %d, want %d of the kept object", got, want)
	}
	if !exists(stale) {
		t.Error("the stale temp name is gone, want no sweep after a deadline")
	}
	if names, _ := os.ReadDir(filepath.Dir(kept)); len(names) != 2 {
		t.Errorf("the shard holds %v, want the object and the stale name", names)
	}
}

// TestFailedKeepForm: a checkout that the deadline of timeoutMs ended gets the
// copy form. One that failed by itself, or with no such deadline, gets the hard
// link.
func TestFailedKeepForm(t *testing.T) {
	fired, cancel := context.WithCancelCause(context.Background())
	cancel(errCallerTimeoutMs)
	killed := errors.New("signal: killed")
	for _, row := range []struct {
		name string
		ctx  context.Context
		err  error
		want keepForm
	}{
		{"a deadline", fired, killed, keepAfterDeadline},
		{"a failure before the deadline", fired, checkoutFailedAlone{killed}, keepAfterFailure},
		{"no deadline", context.Background(), killed, keepAfterFailure},
	} {
		if got := failedKeepForm(row.ctx, row.err); got != row.want {
			t.Errorf("%s: failedKeepForm = %d, want %d", row.name, got, row.want)
		}
	}
}

// TestLFSKeepLinkFailsKeepsNoTemp: an object that turns up at the name oid
// during the copy fails the link of the temp name. The temp name goes, the
// other object stays, and the downloaded copy is left as it is.
func TestLFSKeepLinkFailsKeepsNoTemp(t *testing.T) {
	seamFreeSpace(t)
	gitDir, private := t.TempDir(), t.TempDir()
	oid := putObject(t, private, "alpha-object")
	src := storeObjectPath(private, oid)
	before := inodeOf(t, src)
	kept := storeObjectPath(filepath.Join(gitDir, "lfs"), oid)
	prev := keepCopy
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		mustWrite(t, kept, "other")
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepCopy = prev })
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownloads(gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root}}, private, keepAfterGood)
	if names, _ := os.ReadDir(filepath.Dir(kept)); len(names) != 1 || names[0].Name() != oid {
		t.Errorf("the shard holds %v, want only the other object", names)
	}
	if b, _ := os.ReadFile(kept); string(b) != "other" {
		t.Errorf("the object at the name oid = %q, want the other object", b)
	}
	if got := inodeOf(t, src); got != before {
		t.Errorf("the downloaded copy has inode %d, want %d as before", got, before)
	}
}

// swapFixture is a shard of a repository store that holds oid and a temp name
// of the same file, and a private store with a downloaded copy of it.
type swapFixture struct {
	shard        *os.Root
	shardPath    string
	private, src string
	oid, temp    string
	before       uint64
}

func newSwapFixture(t *testing.T) swapFixture {
	t.Helper()
	store, private := t.TempDir(), t.TempDir()
	oid := putObject(t, store, "alpha-object")
	putObject(t, private, "alpha-object")
	f := swapFixture{shardPath: filepath.Dir(storeObjectPath(store, oid)), private: private,
		src: storeObjectPath(private, oid), oid: oid, temp: ".claude-aaaaaaaa-" + oid}
	if err := os.Link(filepath.Join(f.shardPath, oid), filepath.Join(f.shardPath, f.temp)); err != nil {
		t.Fatal(err)
	}
	f.before = inodeOf(t, f.src)
	shard, err := os.OpenRoot(f.shardPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shard.Close() })
	f.shard = shard
	return f
}

// unchanged fails t when the downloaded copy is not the file of before.
func (f swapFixture) unchanged(t *testing.T, why string) {
	t.Helper()
	if got := inodeOf(t, f.src); got != f.before {
		t.Errorf("%s: the downloaded copy has inode %d, want %d as before", why, got, f.before)
	}
}

// TestSwapInKept: swapInKept replaces no path that is the home folder, and no
// copy whose link or folder fails. A link that is not the temp file of the keep
// is not renamed. With another home it replaces the copy.
func TestSwapInKept(t *testing.T) {
	f := newSwapFixture(t)
	t.Setenv("HOME", f.src)
	swapInKept(f.shard, f.private, f.oid, f.temp)
	f.unchanged(t, "the home path")
	t.Setenv("HOME", t.TempDir())
	// A `.kept-` name that is there already fails the link, and a missing
	// store fails the open.
	kept := filepath.Join(filepath.Dir(f.src), keptPrefix+f.oid)
	mustWrite(t, kept, "other")
	swapInKept(f.shard, f.private, f.oid, f.temp)
	swapInKept(f.shard, filepath.Join(t.TempDir(), "gone"), f.oid, f.temp)
	f.unchanged(t, "a failed link")
	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	// A temp name of another file gets the link but no rename.
	swapInKept(f.shard, f.private, f.oid, "other-temp")
	f.unchanged(t, "a missing temp")
	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.shardPath, "other-temp"), "alpha-object")
	swapInKept(f.shard, f.private, f.oid, "other-temp")
	f.unchanged(t, "another temp file")
	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	swapInKept(f.shard, f.private, f.oid, f.temp)
	if got, want := inodeOf(t, f.src), inodeOf(t, filepath.Join(f.shardPath, f.oid)); got != want {
		t.Errorf("the downloaded copy has inode %d, want %d of the kept object", got, want)
	}
}

// TestSwapInKeptLinksNoLinkTarget: a symbolic link at the name oid of the shard,
// here to the temp file, gives a link at `.kept-<oid>` and no rename. A link
// that followed it would give the temp file and pass the test of its identity.
func TestSwapInKeptLinksNoLinkTarget(t *testing.T) {
	f := newSwapFixture(t)
	at := filepath.Join(f.shardPath, f.oid)
	if err := os.Remove(at); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.temp, at); err != nil {
		t.Fatal(err)
	}
	swapInKept(f.shard, f.private, f.oid, f.temp)
	f.unchanged(t, "a link at the name oid")
	if fi, err := os.Lstat(filepath.Join(filepath.Dir(f.src), keptPrefix+f.oid)); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the .kept- name = %v, want a symbolic link or nothing", fi.Mode())
	}
}

// TestSwapInKeptRefusesLinkedShard: a shard folder of the private store that is
// a link gets no `.kept-` name and no rename, also when the link names a
// folder of the same store with the same copy.
func TestSwapInKeptRefusesLinkedShard(t *testing.T) {
	f := newSwapFixture(t)
	other := filepath.Join(f.private, "objects", "alt")
	if err := os.Rename(filepath.Dir(f.src), other); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "alt"), filepath.Dir(f.src)); err != nil {
		t.Fatal(err)
	}
	swapInKept(f.shard, f.private, f.oid, f.temp)
	f.unchanged(t, "a linked shard folder")
	if names, _ := os.ReadDir(other); len(names) != 1 {
		t.Errorf("the linked folder holds %v, want only the copy", names)
	}
}

// TestDownloadedOIDs: only a regular file named by its oid in its own shard
// counts as a download. A link and a file in another shard do not.
func TestDownloadedOIDs(t *testing.T) {
	private := t.TempDir()
	good, link, moved := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	mustWrite(t, storeObjectPath(private, good), "x")
	target := filepath.Join(t.TempDir(), "target")
	mustWrite(t, target, "x")
	lp := storeObjectPath(private, link)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lp); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(private, "objects", "aa", "aa", moved), "x")
	if got := downloadedOIDs(private); !slices.Equal(got, []string{good}) {
		t.Errorf("downloadedOIDs = %q, want only %q", got, good)
	}
}

// TestLFSNoKeepWithoutHeldDir: a create that holds no git directory keeps
// nothing (claustrum's own).
func TestLFSNoKeepWithoutHeldDir(t *testing.T) {
	gitDir, private := t.TempDir(), t.TempDir()
	oid := strings.Repeat("d", 64)
	mustWrite(t, storeObjectPath(private, oid), "x")
	returnDownloads(gitUse{gitDir: gitDir}, private, keepAfterGood)
	if names, _ := os.ReadDir(gitDir); len(names) != 0 {
		t.Errorf("%s holds %v, want nothing", gitDir, names)
	}
}

// TestOpenObjectNoFollowFIFO: a FIFO at the object path does not block the
// open, and the open fails.
func TestOpenObjectNoFollowFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openObjectNoFollow(p)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("openObjectNoFollow of a FIFO = nil error, want a failure")
		}
	case <-time.After(10 * time.Second):
		// Free the blocked open, so the test ends.
		if w, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("openObjectNoFollow of a FIFO blocks")
	}
}

// TestRemoveFileAtKeepsFolder: the remove of the store takes a file and
// leaves an empty folder of the same kind of name.
func TestRemoveFileAtKeepsFolder(t *testing.T) {
	dir := t.TempDir()
	file, folder := ".claude-00000000-"+strings.Repeat("1", 64), ".claude-11111111-"+strings.Repeat("1", 64)
	mustWrite(t, filepath.Join(dir, file), "")
	if err := os.Mkdir(filepath.Join(dir, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := removeFileAt(root, file); err != nil || exists(filepath.Join(dir, file)) {
		t.Errorf("removeFileAt of a file = %v, want it gone", err)
	}
	if err := removeFileAt(root, folder); err == nil || !exists(filepath.Join(dir, folder)) {
		t.Errorf("removeFileAt of a folder = %v, want it kept", err)
	}
}

// TestLFSFailureNotes: with the private store, a failed download of the filter
// lfs gets the note of its text. The notes of a full disk and of a quota name
// the folder of the socket (4c111e86, measured on Linux and macOS, the quota
// on Linux).
func TestLFSFailureNotes(t *testing.T) {
	for _, row := range []struct {
		name, error, want string
	}{
		{"missing object", "", lfsMissingNote},
		{"full disk", "write x: no space left on device\n", ". A disk on the remote machine filled up during the checkout: the one that holds the new worktree, or the one that holds <run>" + lfsStoreHoldsText},
		{"quota", "write x: Disk quota exceeded\n", ". Your disk quota on the remote machine ran out during the checkout: on the disk that holds the new worktree, or on the one that holds <run>" + lfsStoreHoldsText},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newNamedLFSFixture(t, "lfs", map[string]string{"b.bin": "beta-object"})
			if row.error != "" {
				mustWrite(t, filepath.Join(f.remote, "error"), row.error)
			}
			raw, _ := f.create(t, nil)
			want := strings.Trim(jsonQuoted(t, strings.ReplaceAll(row.want, "<run>", f.run)), `"`) + `","errorCode":"worktree_add_failed"}}`
			if !strings.HasSuffix(raw, want) {
				t.Errorf("git.worktree_create = %s\nwant the end %s", raw, want)
			}
			f.wantGone(t)
		})
	}
}

// TestLFSSeedKeepsSpare: on a tiny file system the seed makes no copy, and
// the failure gets the note of the space, not the note of a missing object
// (4c111e86, measured on Linux and macOS). The bound of 1 GiB after the copy
// is claustrum's own: one byte under it gives no copy, and at it the copy is
// made. The rows write 1 GiB as a number, so they pin seedSpare.
func TestLFSSeedKeepsSpare(t *testing.T) {
	const body = "alpha-object"
	for _, row := range []struct {
		name   string
		free   uint64
		seeded bool
	}{
		{"one byte under the bound", 1<<30 + uint64(len(body)) - 1, false},
		{"at the bound", 1<<30 + uint64(len(body)), true},
	} {
		t.Run(row.name, func(t *testing.T) {
			prevSeed, prevFree := seedLink, storeFreeBytes
			seedLink = func(string, string) error { return os.ErrPermission }
			storeFreeBytes = func(string) (uint64, bool) { return row.free, true }
			t.Cleanup(func() { seedLink, storeFreeBytes = prevSeed, prevFree })
			f := newNamedLFSFixture(t, "lfs", map[string]string{"a.bin": body})
			f.local(t, "a.bin", body)
			raw, _ := f.create(t, nil)
			if row.seeded {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				if got := f.smudges(t)["a.bin"]; got != "present=true private=true" {
					t.Errorf("smudge line of a.bin = %q, want the seeded copy", got)
				}
				return
			}
			note := ". There isn't enough space on the remote machine (in " + f.run + ") for this repository's Git LFS files," +
				" and downloading them failed. Free up space there (Claude keeps 1 GiB spare), or turn off Worktree for this session, then try again."
			want := strings.Trim(jsonQuoted(t, note), `"`) + `","errorCode":"worktree_add_failed"}}`
			if !strings.HasSuffix(raw, want) {
				t.Errorf("git.worktree_create = %s\nwant the end %s", raw, want)
			}
			if got := f.smudges(t)["a.bin"]; got != "present=false private=true" {
				t.Errorf("smudge line of a.bin = %q, want no seeded copy", got)
			}
			f.wantGone(t)
		})
	}
}

// TestFreeBytes: statfs gives the free space of a folder, and a missing path
// gives none.
func TestFreeBytes(t *testing.T) {
	if n, ok := freeBytes(t.TempDir()); !ok || n == 0 {
		t.Errorf("freeBytes of a temp folder = %d, %v, want a free space", n, ok)
	}
	if _, ok := freeBytes(filepath.Join(t.TempDir(), "gone")); ok {
		t.Error("freeBytes of a missing path is ok, want false")
	}
}

// TestCommitPointersStopsAtDeadline: the scan of the seed runs under the
// context of the caller. With no deadline it gives the pointers, and after
// the deadline it gives none.
func TestCommitPointersStopsAtDeadline(t *testing.T) {
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	if got := commitPointers(context.Background(), gitUse{}, f.repo, "HEAD"); len(got) != 2 {
		t.Fatalf("commitPointers = %+v, want the two pointers", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pointerScanGit(ctx, gitUse{}, f.repo, nil, nil, io.Discard, "ls-tree", "HEAD"); err == nil {
		t.Error("the ls-tree call of the seed after the deadline = nil error, want a failure")
	}
	if got := commitPointers(ctx, gitUse{}, f.repo, "HEAD"); len(got) != 0 {
		t.Errorf("commitPointers after the deadline = %+v, want none", got)
	}
}

// seedListings is the count of the listings right before the two calls of
// the seed: the same listing twice (4c111e86, measured on Linux).
const seedListings = 2

// seedScanCalls are the listings right before the seed and its ls-tree and
// cat-file calls. The two calls run at once, so their records come in either
// order. It pins the list: seedListings plain listings, then ls-tree and
// `cat-file --batch --buffer`.
func seedScanCalls(t *testing.T, calls []envCall) []envCall {
	t.Helper()
	scan := func(c envCall) string {
		switch {
		case slices.Contains(c.argv, "ls-tree"):
			return "ls-tree"
		case argvEndsWith(c.argv, []string{"cat-file", "--batch", "--buffer"}):
			return "cat-file --batch --buffer"
		}
		return ""
	}
	i := slices.IndexFunc(calls, func(c envCall) bool { return scan(c) != "" })
	if i < 0 || i+2 > len(calls) {
		t.Fatal("no ls-tree and cat-file calls of the seed")
	}
	start := i
	for start > 0 && calls[start-1].listing() && !slices.ContainsFunc(calls[start-1].argv, func(a string) bool {
		return strings.HasPrefix(a, "--git-dir=")
	}) {
		start--
	}
	pair := []string{scan(calls[i]), scan(calls[i+1])}
	slices.Sort(pair)
	if i-start != seedListings || !slices.Equal(pair, []string{"cat-file --batch --buffer", "ls-tree"}) {
		t.Fatalf("%d listings, then %q, want %d listings, then ls-tree and cat-file --batch --buffer", i-start, pair, seedListings)
	}
	return calls[start : i+2]
}

// TestSeedScanRunsInBaseRepo: the ls-tree and cat-file calls of the seed run
// in baseRepo, with GIT_DIR=<repo>/.git and GIT_WORK_TREE=<repo>, and so does
// the two listings before them (4c111e86, measured on Linux). A linked worktree as
// baseRepo gets its `worktrees/<n>` entry as GIT_DIR, and its pointer is still
// seeded (claustrum's own, not measured).
func TestSeedScanRunsInBaseRepo(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%v", linked), func(t *testing.T) {
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
			f.local(t, "a.bin", "alpha-object")
			base, gitDir, extra := f.repo, f.gitDir, map[string]any(nil)
			if linked {
				base = filepath.Join(filepath.Dir(f.repo), "linked")
				runGit(t, f.repo, "worktree", "add", "-q", "-b", "base1", base)
				gitDir = filepath.Join(f.gitDir, worktreesSubdir, "linked")
				if raw, _ := f.call(t, "git.info", map[string]any{"path": base}); !strings.Contains(raw, `"isRepo":true`) {
					t.Fatalf("git.info = %s", raw)
				}
				extra = map[string]any{"baseRepo": base, "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1")}
			}
			raw, calls := f.create(t, extra)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if got := f.smudges(t)["a.bin"]; got != "present=true private=true" {
				t.Errorf("smudge line of a.bin = %q, want the seeded object", got)
			}
			scan := seedScanCalls(t, calls)
			// The two listings are the same call.
			if !slices.Equal(scan[0].argv, scan[1].argv) || !slices.Equal(scan[0].env, scan[1].env) {
				t.Errorf("the listings %q and %q differ, want the same call twice", scan[0].argv, scan[1].argv)
			}
			// cat-file has no listing of its own and pins the hook names of
			// the ls-tree call.
			if got, want := scan[len(scan)-1].pins(t), scan[len(scan)-2].pins(t); !slices.Equal(got, want) {
				t.Errorf("cat-file pins %q, want the pins of the ls-tree call %q", got, want)
			}
			for _, c := range scan {
				dir, _ := lookupExact(c.env, "GIT_DIR")
				top, _ := lookupExact(c.env, "GIT_WORK_TREE")
				if canonicalPath(c.cwd) != canonicalPath(base) || canonicalPath(dir) != canonicalPath(gitDir) ||
					canonicalPath(top) != canonicalPath(base) {
					t.Errorf("call %q runs in %s with GIT_DIR=%s GIT_WORK_TREE=%s, want baseRepo", c.argv, c.cwd, dir, top)
				}
			}
		})
	}
}

// TestSeedScanCatFileStartsDuringLsTree: the cat-file call of the seed starts
// with the ls-tree call, before the first id, and before ls-tree ends
// (4c111e86, measured on Linux). The stub holds ls-tree for 500 ms before git
// prints the tree.
func TestSeedScanCatFileStartsDuringLsTree(t *testing.T) {
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.local(t, "a.bin", "alpha-object")
	timeLog := filepath.Join(t.TempDir(), "time.log")
	t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
	const hold = 500 * time.Millisecond
	slowGit(t, "ls-tree", "pre", hold, "", "")
	f.wantSuccess(t)
	if got := f.smudges(t)["a.bin"]; got != "present=true private=true" {
		t.Errorf("smudge line of a.bin = %q, want the seeded object", got)
	}
	b, err := os.ReadFile(timeLog)
	if err != nil {
		t.Fatal(err)
	}
	var lsPid string
	var lsStart, lsEnd, catStart int64
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		w := strings.Split(line, "\x1f")
		ns, _ := strconv.ParseInt(w[1], 10, 64)
		switch {
		case w[0] == "start" && slices.Contains(w, "ls-tree"):
			lsPid, lsStart = w[2], ns
		case w[0] == "start" && slices.Contains(w, "cat-file") && slices.Contains(w, "--buffer"):
			catStart = ns
		case w[0] == "end" && w[2] == lsPid:
			lsEnd = ns
		}
	}
	if lsStart == 0 || lsEnd == 0 || catStart == 0 {
		t.Fatalf("time log has no ls-tree start or end or no cat-file start:\n%s", b)
	}
	if catStart >= lsStart+int64(hold)/2 {
		t.Errorf("cat-file starts %v after the start of ls-tree, want it before the first id", time.Duration(catStart-lsStart))
	}
	if catStart >= lsEnd {
		t.Errorf("cat-file starts %v after the end of ls-tree, want it to start before", time.Duration(catStart-lsEnd))
	}
}

// TestSeedScanArgv: the ls-tree call of the seed has the argv of 4c111e86
// (measured on Linux). A pointer in a subfolder counts, also when the call
// runs in another subfolder (claustrum's own, not measured).
func TestSeedScanArgv(t *testing.T) {
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "sub/b.bin": "beta-object"})
	mustWrite(t, filepath.Join(f.repo, "other", "c.txt"), "c\n")
	runGit(t, f.repo, "add", "other")
	runGit(t, f.repo, "commit", "-q", "-m", "other")
	calls := logGitArgv(t)
	for _, dir := range []string{f.repo, filepath.Join(f.repo, "other")} {
		if got := commitPointers(context.Background(), gitUse{}, dir, "HEAD"); len(got) != 2 {
			t.Errorf("commitPointers in %s = %+v, want the two pointers", dir, got)
		}
	}
	want := []string{"-c", "core.commitGraph=false", "ls-tree", "--full-tree", "-r", "-l", "-z", "HEAD"}
	n := 0
	for _, c := range calls() {
		if slices.Contains(c, "ls-tree") {
			n++
			if !slices.Equal(c, want) {
				t.Errorf("ls-tree call = %q, want %q", c, want)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d ls-tree calls, want 2", n)
	}
}

// TestSeedObjectsStopsAtDeadline: a seed after the deadline of the caller puts
// no object into the private store, also by a hard link.
func TestSeedObjectsStopsAtDeadline(t *testing.T) {
	store, private := filepath.Join(t.TempDir(), "lfs"), filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("a", 64)
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _ = seedObjects(ctx, store, private, []pointerRef{{oid: oid, size: 12}})
	if p := storeObjectPath(private, oid); exists(p) {
		t.Errorf("%s exists, want no seed after the deadline", p)
	}
	_, _, _ = seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}})
	if p := storeObjectPath(private, oid); !exists(p) {
		t.Errorf("%s is missing, want the seed with no deadline", p)
	}
}

// TestLFSKeepRefusesSwappedShardFolder: a shard folder that turns into a link
// after its Lstat, here to another folder of `lfs/objects`, gets no keep and
// no sweep.
func TestLFSKeepRefusesSwappedShardFolder(t *testing.T) {
	gitDir, private := t.TempDir(), t.TempDir()
	oid := putObject(t, private, "x")
	a, b := oid[:2], oid[2:4]
	objects := filepath.Join(gitDir, "lfs", "objects")
	for _, d := range []string{filepath.Join(objects, a, b), filepath.Join(objects, "alt", b)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(objects, "alt", b, ".claude-aaaaaaaa-"+oid)
	mustWrite(t, stale, "stale")
	var removed []string
	seamLFSRemove(t, &removed)
	prev := openShardFolder
	swapped := false
	openShardFolder = func(parent *os.Root, name string) (*os.Root, error) {
		if name == a && !swapped {
			swapped = true
			if err := os.Rename(filepath.Join(objects, a), filepath.Join(objects, "moved")); err != nil {
				t.Error(err)
			}
			if err := os.Symlink("alt", filepath.Join(objects, a)); err != nil {
				t.Error(err)
			}
		}
		return prev(parent, name)
	}
	t.Cleanup(func() { openShardFolder = prev })
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownload(context.Background(), root, gitDir, private, oid, time.Now(), keepAfterGood)
	if !swapped {
		t.Fatal("the seam did not swap the shard folder")
	}
	for _, p := range []string{filepath.Join(objects, "alt", b, oid), filepath.Join(objects, "moved", b, oid)} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists, want no keep", p)
		}
	}
	if len(removed) != 0 {
		t.Errorf("removed %q, want no sweep", removed)
	}
}

// TestLFSFailedKeepAddsNoRemove: after a failed checkout a hard link that
// fails keeps nothing. No copy is made, no name is removed and the stale temp
// name of the same oid stays (claustrum's own, not measured).
func TestLFSFailedKeepAddsNoRemove(t *testing.T) {
	seamFreeSpace(t)
	prev := keepHardLink
	keepHardLink = func(*os.Root, string, string) error { return os.ErrPermission }
	t.Cleanup(func() { keepHardLink = prev })
	gitDir, private := t.TempDir(), t.TempDir()
	oid := putObject(t, private, "alpha-object")
	shard := filepath.Dir(storeObjectPath(filepath.Join(gitDir, "lfs"), oid))
	stale := filepath.Join(shard, ".claude-aaaaaaaa-"+oid)
	mustWrite(t, stale, "stale")
	var removed []string
	seamLFSRemove(t, &removed)
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownload(context.Background(), root, gitDir, private, oid, time.Now(), keepAfterFailure)
	if names, _ := os.ReadDir(shard); len(names) != 1 || len(removed) != 0 {
		t.Errorf("the shard holds %v and %q was removed, want only the stale name and no remove", names, removed)
	}
}

// TestSeedSpareMakesNoFile: a copy left out for seedSpare makes no file in
// the checkout folder, and the seed reports it (claustrum's own).
func TestSeedSpareMakesNoFile(t *testing.T) {
	prevSeed, prevFree := seedLink, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	storeFreeBytes = func(string) (uint64, bool) { return 0, true }
	t.Cleanup(func() { seedLink, storeFreeBytes = prevSeed, prevFree })
	store, private := filepath.Join(t.TempDir(), "lfs"), filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("a", 64)
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	if short, _, _ := seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}}); !short {
		t.Error("seedObjects = false, want a copy left out")
	}
	if names, _ := os.ReadDir(filepath.Dir(storeObjectPath(private, oid))); len(names) != 0 {
		t.Errorf("the shard of the private store holds %v, want nothing", names)
	}
}

// seamFreeSpace makes the free space of every folder 1 TiB for one test, so
// the keep does not read the free space of the host.
func seamFreeSpace(t *testing.T) {
	t.Helper()
	prev := storeFreeBytes
	storeFreeBytes = func(string) (uint64, bool) { return 1 << 40, true }
	t.Cleanup(func() { storeFreeBytes = prev })
}

// TestLFSKeepNeedsSpare: the keep puts an object into the store of the
// repository only when the file system of its shard has 1 GiB free plus the
// size of the object, also when both stores share a file system. Under the
// bound the empty shard folder stays (4c111e86, measured on Linux and macOS).
// After a failed checkout the same holds (measured on Linux and macOS).
// After a good checkout every row copies the object. The exact byte of the
// bound is inferred. A free space that cannot be read keeps the object
// (claustrum's own). The rows write 1 GiB as a number, so they pin seedSpare.
func TestLFSKeepNeedsSpare(t *testing.T) {
	const body = "beta-object"
	for _, row := range []struct {
		name               string
		link, kept, failed bool
		free               uint64
	}{
		{"a hard link one byte under the bound", true, false, false, 1<<30 + uint64(len(body)) - 1},
		{"a hard link at the bound", true, true, false, 1<<30 + uint64(len(body))},
		{"a copy one byte under the bound", false, false, false, 1<<30 + uint64(len(body)) - 1},
		{"a copy at the bound", false, true, false, 1<<30 + uint64(len(body))},
		{"a free space that cannot be read", true, true, false, 0},
		{"after a failed checkout one byte under the bound", true, false, true, 1<<30 + uint64(len(body)) - 1},
		{"after a failed checkout at the bound", true, true, true, 1<<30 + uint64(len(body))},
	} {
		t.Run(row.name, func(t *testing.T) {
			var asked []string
			prevFree, prevKeep := storeFreeBytes, keepHardLink
			storeFreeBytes = func(p string) (uint64, bool) { asked = append(asked, p); return row.free, row.free != 0 }
			if !row.link {
				keepHardLink = func(*os.Root, string, string) error { return os.ErrPermission }
			}
			t.Cleanup(func() { storeFreeBytes, keepHardLink = prevFree, prevKeep })
			gitDir, private := t.TempDir(), t.TempDir()
			oid := putObject(t, private, body)
			root, err := os.OpenRoot(gitDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			returnDownload(context.Background(), root, gitDir, private, oid, time.Now(), map[bool]keepForm{false: keepAfterGood, true: keepAfterFailure}[row.failed])
			p := storeObjectPath(filepath.Join(gitDir, "lfs"), oid)
			if want := []string{filepath.Dir(p)}; !slices.Equal(asked, want) {
				t.Errorf("free space asked for %q, want %q", asked, want)
			}
			if exists(p) != row.kept {
				t.Errorf("object kept = %v, want %v", exists(p), row.kept)
			}
			if names, err := os.ReadDir(filepath.Dir(p)); err != nil || (!row.kept && len(names) != 0) {
				t.Errorf("the shard folder = %v, %v, want it made and empty when nothing is kept", names, err)
			}
		})
	}
}

// TestLFSStoreFullCodes: a failed download whose write below the private store
// meets a full disk or a quota answers worktree_no_space or worktree_quota. Its
// start names the folder of the socket, and no note follows git's text. A
// write outside the store keeps worktree_add_failed and the note of a full disk
// (4c111e86, measured on Linux and macOS).
func TestLFSStoreFullCodes(t *testing.T) {
	for _, row := range []struct {
		name, write, words, start, code string
	}{
		{"full disk", "<store>/tmp/123456", "no space left on device", "The disk that holds <run> on the remote machine filled up during the checkout: Claude keeps this checkout's Git LFS downloads there while it runs." +
			" Free some space on that disk, or turn off Worktree for this session, then try again. git worktree add failed (checkout): ", "worktree_no_space"},
		{"quota", "<store>/tmp/123456", "Disk quota exceeded", "Your disk quota ran out during the checkout on the disk that holds <run> on the remote machine: Claude keeps this checkout's Git LFS downloads there while it runs." +
			" Free some space on that disk or raise the quota, or turn off Worktree for this session, then try again. git worktree add failed (checkout): ", "worktree_quota"},
		{"outside the store", "/outside/tmp/123456", "no space left on device", "", "worktree_add_failed"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newNamedLFSFixture(t, "lfs", map[string]string{"b.bin": "beta-object"})
			mustWrite(t, filepath.Join(f.remote, "error"), "Error downloading object: b.bin: Smudge error: write "+row.write+": "+row.words+"\n")
			raw, _ := f.create(t, nil)
			if row.start == "" {
				note := ". A disk on the remote machine filled up during the checkout: the one that holds the new worktree, or the one that holds <run>" + lfsStoreHoldsText
				want := strings.Trim(jsonQuoted(t, strings.ReplaceAll(note, "<run>", f.run)), `"`) + `","errorCode":"` + row.code + `"}}`
				if !strings.HasSuffix(raw, want) {
					t.Errorf("git.worktree_create = %s\nwant the end %s", raw, want)
				}
				f.wantGone(t)
				return
			}
			start := strings.Trim(jsonQuoted(t, strings.ReplaceAll(row.start, "<run>", f.run)), `"`)
			if !strings.Contains(raw, `"error":"`+start+"Error downloading object: b.bin: Smudge error: write ") {
				t.Errorf("git.worktree_create = %s\nwant the start %s", raw, start)
			}
			if !strings.HasSuffix(raw, `","errorCode":"`+row.code+`"}}`) || strings.Contains(raw, lfsStoreHoldsText) {
				t.Errorf("git.worktree_create = %s\nwant git's text with no note, then errorCode %s", raw, row.code)
			}
			f.wantGone(t)
		})
	}
}

// putObject writes body into the store store at the name of its sha256 oid,
// and returns that oid.
func putObject(t *testing.T, store, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	oid := hex.EncodeToString(sum[:])
	mustWrite(t, storeObjectPath(store, oid), body)
	return oid
}

// TestLFSNotKeptAfterDeadline: a create whose timeoutMs ends during the
// checkout, after copies of the seed that took a large part of it, answers
// worktree_lfs_not_kept. The copy of 3 s is over the bound of a quarter of
// 10 s (claustrum's own) and under half. An `incomplete` folder that git-lfs made in
// the private store keeps timeout. A copy past half of timeoutMs stops, and
// git-lfs then fails to find the object (4c111e86, measured on Linux and
// macOS). A seed that starts 3 s after the start of the request stops at
// (10 + 3) / 2 s from that start, so a copy of 4 s stops too. A bound of
// half of timeoutMs from the start of the seed lets it end (4c111e86, measured
// on Linux).
func TestLFSNotKeptAfterDeadline(t *testing.T) {
	for _, row := range []struct {
		name            string
		delay, copyTime time.Duration
		hang, inc       bool
		code, text      string
	}{
		{"long copy", 0, 3000 * time.Millisecond, true, false, "worktree_lfs_not_kept", "timed out after 10 seconds, and copying"},
		{"download begun", 0, 3000 * time.Millisecond, true, true, "timeout", "git worktree add timed out after 10000ms (deadline expired during the checkout)"},
		{"copy past half", 0, 6000 * time.Millisecond, false, false, "worktree_add_failed", "Object does not exist on the server"},
		{"late seed", 3000 * time.Millisecond, 4000 * time.Millisecond, false, false, "worktree_add_failed", "Object does not exist on the server"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
			f.local(t, "a.bin", "alpha-object")
			if row.hang {
				mustWrite(t, filepath.Join(f.remote, "hang"), "")
			}
			if row.inc {
				mustWrite(t, filepath.Join(f.remote, "incomplete"), "")
			}
			seamFreeSpace(t)
			prevLink, prevCopy := seedLink, seedCopy
			seedLink = func(string, string) error { return os.ErrPermission }
			seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
				time.Sleep(row.copyTime)
				return io.Copy(w, r)
			}
			t.Cleanup(func() { seedLink, seedCopy = prevLink, prevCopy })
			if row.delay > 0 {
				// The staged add runs before the seed. Its first Lstat waits.
				prevLstat, wait := regularLstatDone, sync.OnceFunc(func() { time.Sleep(row.delay) })
				regularLstatDone = func(string) { wait() }
				t.Cleanup(func() { regularLstatDone = prevLstat })
			}
			if row.hang {
				// The deadline fires once the download of b.bin hangs. The
				// timer of timeoutMs stays armed behind it.
				deadlineAt(t, f.smudgeBegan("b.bin", row.inc), nil)
			}
			raw, _ := f.create(t, map[string]any{"timeoutMs": 10000})
			if !strings.Contains(raw, `"errorCode":"`+row.code+`"`) || !strings.Contains(raw, row.text) {
				t.Errorf("git.worktree_create = %s\nwant errorCode %s and %q", raw, row.code, row.text)
			}
			if got := f.smudges(t)["a.bin"]; row.code == "worktree_add_failed" && !strings.HasPrefix(got, "present=false") {
				t.Errorf("smudge line of a.bin = %q, want no seeded copy past the bound of the seed", got)
			}
			f.wantGone(t)
		})
	}
}

// TestSeedChecksObjectWithSecondLink: an object of the store with a link count
// above 1 is read once in full before the link or the copy. With content that
// does not match its oid it is neither linked nor copied. With link count 1
// such content is linked (4c111e86, measured on Linux, the read also on
// macOS). The content is tested against the oid (claustrum's own form).
func TestSeedChecksObjectWithSecondLink(t *testing.T) {
	seamFreeSpace(t)
	right := "alpha-object"
	sum := sha256.Sum256([]byte(right))
	rightOID := hex.EncodeToString(sum[:])
	wrongOID := strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, oid  string
		second     bool
		reads      int
		wantSeeded bool
	}{
		{"right bytes, two links", rightOID, true, 1, true},
		{"wrong bytes, two links", wrongOID, true, 1, false},
		{"wrong bytes, one link", wrongOID, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "lfs")
			src := storeObjectPath(store, tc.oid)
			mustWrite(t, src, right)
			if tc.second {
				if err := os.Link(src, filepath.Join(t.TempDir(), "second")); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			prev := newObjectHash
			newObjectHash = func() hash.Hash { reads++; return prev() }
			t.Cleanup(func() { newObjectHash = prev })
			private := filepath.Join(t.TempDir(), "lfs")
			_, _, _ = seedObjects(context.Background(), store, private, []pointerRef{{oid: tc.oid, size: int64(len(right))}})
			_, err := os.Lstat(storeObjectPath(private, tc.oid))
			if reads != tc.reads || (err == nil) != tc.wantSeeded {
				t.Errorf("hash reads %d, seeded %v (%v), want %d reads, seeded %v", reads, err == nil, err, tc.reads, tc.wantSeeded)
			}
		})
	}
}

// TestSeedLeavesOutLargeObjectBelowTheBound: an 11 GiB object with 1 MiB less
// than the size plus a tenth free is left out (4c111e86, measured on Linux and macOS).
// The object is a sparse file, so this test is not on Windows.
func TestSeedLeavesOutLargeObjectBelowTheBound(t *testing.T) {
	const gib, mib = int64(1) << 30, int64(1) << 20
	store := filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("c", 64)
	src := storeObjectPath(store, oid)
	mustWrite(t, src, "")
	size := 11 * gib
	if err := os.Truncate(src, size); err != nil {
		t.Skipf("no sparse file of 11 GiB here: %v", err)
	}
	copies := 0
	prevLink, prevCopy, prevFree := seedLink, seedCopy, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	seedCopy = func(io.Writer, io.Reader) (int64, error) { copies++; return 0, os.ErrInvalid }
	storeFreeBytes = func(string) (uint64, bool) { return uint64(size + size/10 - mib), true }
	t.Cleanup(func() { seedLink, seedCopy, storeFreeBytes = prevLink, prevCopy, prevFree })
	short, _, _ := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), []pointerRef{{oid: oid, size: size}})
	if !short || copies != 0 {
		t.Errorf("11 GiB object 1 MiB below the bound: short %v, %d copies, want short and no copy", short, copies)
	}
}

// TestSeedCountsTheReadOfALinkedObject: the read of an object with a second
// link counts in the seed time, also when the seed then makes a hard link
// (claustrum's own, not measured).
func TestSeedCountsTheReadOfALinkedObject(t *testing.T) {
	seamFreeSpace(t)
	body := "alpha-object"
	sum := sha256.Sum256([]byte(body))
	oid := hex.EncodeToString(sum[:])
	store := filepath.Join(t.TempDir(), "lfs")
	src := storeObjectPath(store, oid)
	mustWrite(t, src, body)
	if err := os.Link(src, filepath.Join(t.TempDir(), "second")); err != nil {
		t.Fatal(err)
	}
	prev := newObjectHash
	newObjectHash = func() hash.Hash { return slowHash{prev()} }
	t.Cleanup(func() { newObjectHash = prev })
	private := filepath.Join(t.TempDir(), "lfs")
	_, copied, _ := seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: int64(len(body))}})
	fi, err := os.Stat(storeObjectPath(private, oid))
	if err != nil || hardLinks(fi) < 3 {
		t.Fatalf("the seeded object = %v, %v, want a hard link", fi, err)
	}
	if copied < 30*time.Millisecond {
		t.Errorf("seed with a read of 30 ms counts %v, want at least 30 ms", copied)
	}
}

// slowHash is a hash that waits 30 ms on each write.
type slowHash struct{ hash.Hash }

func (h slowHash) Write(p []byte) (int, error) {
	time.Sleep(30 * time.Millisecond)
	return h.Hash.Write(p)
}

// copySeedCase makes an object and the folder of its copy for a test of
// copySeed. dst is the name of the copy in that folder.
func copySeedCase(t *testing.T) (src, dst string) {
	t.Helper()
	src = filepath.Join(t.TempDir(), "obj")
	mustWrite(t, src, "alpha-object")
	dst = filepath.Join(t.TempDir(), "lfs", "objects", "ab", "cd", "obj")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	return src, dst
}

// TestCopySeedRemovesTheFileOfAFailedWrite: a copy of the seed writes at the
// name of the object. When a write fails midway, that one file is removed at
// once, by its name (4c111e86, measured on Linux). A good copy then makes the
// whole object there.
func TestCopySeedRemovesTheFileOfAFailedWrite(t *testing.T) {
	src, dst := copySeedCase(t)
	prev := seedCopy
	seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
		n, _ := io.CopyN(w, r, 3)
		return n, unix.ENOSPC
	}
	t.Cleanup(func() { seedCopy = prev })
	if err := copySeed(context.Background(), src, dst); err == nil {
		t.Fatal("copySeed with a failed write = nil, want the error")
	}
	if names, _ := os.ReadDir(filepath.Dir(dst)); len(names) != 0 {
		t.Errorf("the folder after a failed write holds %v, want nothing", names)
	}
	seedCopy = prev
	if err := copySeed(context.Background(), src, dst); err != nil || mustRead(t, dst) != "alpha-object" {
		t.Errorf("copySeed = %v, want the whole object at its name", err)
	}
	if err := copySeed(context.Background(), src, dst); err == nil || mustRead(t, dst) != "alpha-object" {
		t.Errorf("copySeed onto an object = %v, want an error and the object kept", err)
	}
}

// TestCopySeedRemovesTheFileOfAStoppedCopy: a copy of the seed after the
// deadline of the caller leaves no file. The one remove names the object.
func TestCopySeedRemovesTheFileOfAStoppedCopy(t *testing.T) {
	src, dst := copySeedCase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copySeed(ctx, src, dst); err == nil {
		t.Fatal("copySeed after the deadline = nil, want an error")
	}
	if names, _ := os.ReadDir(filepath.Dir(dst)); len(names) != 0 {
		t.Errorf("the folder after a stopped copy holds %v, want nothing", names)
	}
	var removed []string
	seamLFSRemove(t, &removed)
	_ = copySeed(ctx, src, dst)
	if !slices.Equal(removed, []string{"obj"}) {
		t.Errorf("removes %q, want one of the object name", removed)
	}
}

// TestCopySeedReportsAFileItCannotRemove: a stopped copy in a folder of mode
// 0555 cannot remove its file. copySeed reports that remove (4c111e86,
// measured on Linux). An empty folder at the name (its answer measured on
// Linux) and a gone file (claustrum's own) report nothing.
func TestCopySeedReportsAFileItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a file in a folder of mode 0555")
	}
	for _, row := range []struct {
		name   string
		swap   func(t *testing.T, dst string)
		report bool
	}{
		{"read-only folder", func(t *testing.T, dst string) {
			if err := os.Chmod(filepath.Dir(dst), 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(dst), 0o700) })
		}, true},
		{"folder at the name", func(t *testing.T, dst string) {
			if err := os.Remove(dst); err != nil {
				t.Fatal(err)
			}
			mkdirAll(t, dst)
		}, false},
		{"gone file", func(t *testing.T, dst string) {
			if err := os.Remove(dst); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			src, dst := copySeedCase(t)
			prev := seedCopy
			seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
				n, _ := io.CopyN(w, r, 3)
				row.swap(t, dst)
				return n, context.DeadlineExceeded
			}
			t.Cleanup(func() { seedCopy = prev })
			err := copySeed(context.Background(), src, dst)
			var left seedNotRemoved
			if got := errors.As(err, &left); got != row.report {
				t.Fatalf("copySeed = %v, a failed remove %v, want %v", err, got, row.report)
			}
			if row.report && err.Error() != "remove "+dst+": permission denied" {
				t.Errorf("copySeed = %q, want the remove of %s", err, dst)
			}
		})
	}
}

// TestCopySeedReportsARemoveWithNoEntryLeft: a remove that fails with an error
// other than ENOENT, and an Lstat of the name that fails after it, still
// report the remove (claustrum's own, not measured). The remove is the seam.
func TestCopySeedReportsARemoveWithNoEntryLeft(t *testing.T) {
	src, dst := copySeedCase(t)
	prevCopy, prevRemove := seedCopy, removeLFSStoreFile
	seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
		n, _ := io.CopyN(w, r, 3)
		if err := os.Remove(dst); err != nil {
			t.Error(err)
		}
		return n, context.DeadlineExceeded
	}
	removeLFSStoreFile = func(*os.Root, string) error { return unix.EACCES }
	t.Cleanup(func() { seedCopy, removeLFSStoreFile = prevCopy, prevRemove })
	err := copySeed(context.Background(), src, dst)
	if !errors.As(err, new(seedNotRemoved)) || err.Error() != "remove "+dst+": permission denied" {
		t.Errorf("copySeed = %v, want the failed remove of %s", err, dst)
	}
}

// TestLFSSeedFileNotRemovedFailsTheCreate: a copy of the seed that stops and
// cannot remove its file fails the create with worktree_add_failed and logs
// the remove (4c111e86, measured on Linux). No read-tree call runs (claustrum's
// own, not measured). The rollback runs. The remove is the seam, so nothing
// goes.
func TestLFSSeedFileNotRemovedFailsTheCreate(t *testing.T) {
	buf := captureLogBuf(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.local(t, "a.bin", "alpha-object")
	oid := f.oids["a.bin"]
	prevLink, prevCopy, prevRemove := seedLink, seedCopy, removeLFSStoreFile
	seedLink = func(string, string) error { return os.ErrPermission }
	seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
		n, _ := io.CopyN(w, r, 3)
		return n, context.DeadlineExceeded
	}
	removeLFSStoreFile = func(root *os.Root, name string) error {
		if name == oid {
			return unix.EACCES
		}
		return prevRemove(root, name)
	}
	t.Cleanup(func() { seedLink, seedCopy, removeLFSStoreFile = prevLink, prevCopy, prevRemove })
	raw, _ := f.create(t, map[string]any{"timeoutMs": 5000})
	const start = "WARN  [GitHandler] git.worktree_create: a file the helper put in the checkout's private Git LFS folder" +
		" was replaced or damaged and could not be removed: remove "
	var path string
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if p, ok := strings.CutPrefix(line, start); ok && strings.HasSuffix(p, "/"+oid+": permission denied") {
			path = strings.TrimSuffix(p, ": permission denied")
		}
	}
	folder, _, ok := strings.Cut(path, string(filepath.Separator)+checkoutDirPrefix)
	if !ok || !strings.Contains(folder, string(filepath.Separator)+"git-shadow"+string(filepath.Separator)) {
		t.Fatalf("no log line of the remove of the object in a folder of git-shadow:\n%s", buf.String())
	}
	want := "git worktree add failed (checkout): Claude could not prepare a private Git LFS folder under " + folder +
		" on the remote machine (a file Claude put there was replaced or damaged and could not be removed). Try again;" +
		" if it keeps failing, check that folder and its disk."
	if !strings.Contains(raw, `"error":`+jsonQuoted(t, want)+`,"errorCode":"worktree_add_failed"`) {
		t.Errorf("git.worktree_create = %s\nwant the text %q", raw, want)
	}
	if b, _ := os.ReadFile(f.log); len(b) != 0 {
		t.Errorf("smudge lines = %q, want no read-tree call", b)
	}
	f.wantGone(t)
}

// TestSeedFailedCopyOnAFullDiskReportsTheSpace: after a failed copy of the seed
// the free space is read again. Below the spare the seed reports the copy as
// left out, so the reply gets the note of the space (4c111e86, measured on
// Linux). With room after the failure it does not.
func TestSeedFailedCopyOnAFullDiskReportsTheSpace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		after     uint64
		wantShort bool
	}{
		{"full disk", 4096, true},
		{"room left", 1 << 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "lfs")
			oid := strings.Repeat("a", 64)
			mustWrite(t, storeObjectPath(store, oid), "alpha-object")
			reads := 0
			prevLink, prevCopy, prevFree := seedLink, seedCopy, storeFreeBytes
			seedLink = func(string, string) error { return unix.EXDEV }
			seedCopy = func(io.Writer, io.Reader) (int64, error) { return 0, unix.ENOSPC }
			storeFreeBytes = func(string) (uint64, bool) {
				reads++
				if reads == 1 {
					return 1 << 40, true
				}
				return tc.after, true
			}
			t.Cleanup(func() { seedLink, seedCopy, storeFreeBytes = prevLink, prevCopy, prevFree })
			short, _, _ := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), []pointerRef{{oid: oid, size: 12}})
			if short != tc.wantShort || reads != 2 {
				t.Errorf("short %v after %d reads of the free space, want %v after 2", short, reads, tc.wantShort)
			}
			rule := &filterRule{shadow: true, store: &storeUse{runDir: "/run/c1", short: short}}
			note := checkoutNote("error: smudge filter lfs failed", "smudge filter lfs failed", rule)
			if got := strings.Contains(note, "There isn't enough space on the remote machine (in /run/c1)"); got != tc.wantShort {
				t.Errorf("note %q, want the note of the space %v", note, tc.wantShort)
			}
		})
	}
}

// TestSeedCopyTime: the seed counts the time of its copies. A hard link adds
// no time (claustrum's own, not measured).
func TestSeedCopyTime(t *testing.T) {
	store := filepath.Join(t.TempDir(), "lfs")
	// The first seed links the object, so the second one reads it for its
	// sha256 first (seedObjects): the oid is that of its bytes.
	sum := sha256.Sum256([]byte("alpha-object"))
	oid := hex.EncodeToString(sum[:])
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	ps := []pointerRef{{oid: oid, size: 12}}
	if _, copied, _ := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), ps); copied != 0 {
		t.Errorf("seed by a hard link counts %v, want 0", copied)
	}
	prevLink, prevCopy, prevFree := seedLink, seedCopy, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
		time.Sleep(30 * time.Millisecond)
		return io.Copy(w, r)
	}
	storeFreeBytes = func(string) (uint64, bool) { return 1 << 40, true }
	t.Cleanup(func() { seedLink, seedCopy, storeFreeBytes = prevLink, prevCopy, prevFree })
	if _, copied, _ := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), ps); copied < 30*time.Millisecond {
		t.Errorf("seed by a copy of 30 ms counts %v, want at least 30 ms", copied)
	}
}

// TestSeedCopyTimeHoldsHash: the time of a copy of the seed holds the sha256
// of its bytes, computed in the same pass, so its time is near the seed time
// of 4c111e86 in the rows (claustrum's own).
func TestSeedCopyTimeHoldsHash(t *testing.T) {
	store := filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("a", 64)
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	prevLink, prevHash, prevFree := seedLink, seedHash, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	hashed := 0
	seedHash = func() io.Writer { return slowWriter{&hashed} }
	storeFreeBytes = func(string) (uint64, bool) { return 1 << 40, true }
	t.Cleanup(func() { seedLink, seedHash, storeFreeBytes = prevLink, prevHash, prevFree })
	private := filepath.Join(t.TempDir(), "lfs")
	_, copied, _ := seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}})
	if hashed != 12 || copied < 30*time.Millisecond {
		t.Errorf("seed by a copy with a hash of 30 ms: %d bytes hashed, %v counted, want 12 bytes and at least 30 ms", hashed, copied)
	}
	if got := mustRead(t, storeObjectPath(private, oid)); got != "alpha-object" {
		t.Errorf("seeded object = %q, want the whole object", got)
	}
}

// slowWriter counts the bytes that it takes and waits 30 ms on each write.
type slowWriter struct{ n *int }

func (w slowWriter) Write(p []byte) (int, error) {
	time.Sleep(30 * time.Millisecond)
	*w.n += len(p)
	return len(p), nil
}

// TestLFSKeepPastTheDeadlineAnswersSuccess: a keep whose copy runs past
// timeoutMs does not turn the reply into a timeout. A copy that ends in its
// budget keeps the object, and one that its budget stops keeps nothing. Both
// answer success (4c111e86, measured on Linux. The rule that leaves out the
// time of the keep is claustrum's own).
func TestLFSKeepPastTheDeadlineAnswersSuccess(t *testing.T) {
	seamFreeSpace(t)
	for _, row := range []struct {
		name   string
		budget time.Duration
		kept   bool
	}{
		{"a copy that ends in its budget", 10 * time.Second, true},
		{"a copy that its budget stops", 4200 * time.Millisecond, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			prevBudget, prevCopy := keepBudget, keepCopy
			keepBudget = row.budget
			keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
				time.Sleep(4500 * time.Millisecond)
				return io.Copy(w, r)
			}
			t.Cleanup(func() { keepBudget, keepCopy = prevBudget, prevCopy })
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
			f.onServer(t, "a.bin")
			raw, _ := f.create(t, map[string]any{"timeoutMs": 4000})
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if got := exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])); got != row.kept {
				t.Errorf("object kept = %v, want %v", got, row.kept)
			}
		})
	}
}

// TestKeepWithinDeadline: the deadline counts as passed only by the keep when
// it passed no earlier than the time of the keep before now.
func TestKeepWithinDeadline(t *testing.T) {
	dl := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), dl)
	defer cancel()
	for _, row := range []struct {
		name string
		ctx  context.Context
		took time.Duration
		at   time.Time
		want bool
	}{
		{"the deadline in the keep", ctx, 2 * time.Second, dl.Add(time.Second), true},
		{"the deadline at the start of the keep", ctx, time.Second, dl.Add(time.Second), true},
		{"the deadline before the keep", ctx, time.Second, dl.Add(2 * time.Second), false},
		{"no keep", ctx, 0, dl.Add(time.Millisecond), false},
		{"no deadline", context.Background(), time.Second, dl, false},
	} {
		if got := keepWithinDeadline(row.ctx, row.took, row.at); got != row.want {
			t.Errorf("%s: keepWithinDeadline = %v, want %v", row.name, got, row.want)
		}
	}
}

// TestSeedEndsAtTheFirstObjectLeftOut: with too little space for a copy the
// seed makes the shard folder of the first object and none of the second
// (4c111e86, measured on macOS with a private folder on a small disk image).
// Mutation: `continue` in place of the return after the space test of
// seedObjects (the shard folder of the second object appears).
func TestSeedEndsAtTheFirstObjectLeftOut(t *testing.T) {
	prevSeed, prevFree := seedLink, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	storeFreeBytes = func(string) (uint64, bool) { return 3 << 20, true }
	t.Cleanup(func() { seedLink, storeFreeBytes = prevSeed, prevFree })
	store, private := filepath.Join(t.TempDir(), "lfs"), filepath.Join(t.TempDir(), "lfs")
	first, second := strings.Repeat("f4", 32), strings.Repeat("d6", 32)
	mustWrite(t, storeObjectPath(store, first), "alpha-object")
	mustWrite(t, storeObjectPath(store, second), "beta-object")
	ps := []pointerRef{{oid: first, size: 12}, {oid: second, size: 11}}
	if short, _, _ := seedObjects(context.Background(), store, private, ps); !short {
		t.Error("seedObjects = false, want a copy left out")
	}
	if fi, err := os.Stat(filepath.Dir(storeObjectPath(private, first))); err != nil || !fi.IsDir() {
		t.Errorf("the shard folder of the first object: %v, want it made", err)
	}
	if _, err := os.Lstat(filepath.Join(private, "objects", second[:2])); !os.IsNotExist(err) {
		t.Errorf("the shard folder of the second object: %v, want none", err)
	}
}
