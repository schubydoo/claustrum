//go:build !windows

package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	runGit(t, f.repo, append([]string{"add"}, names...)...)
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
	// A folder made with mode 0755 here gets the same umask.
	ref := filepath.Join(t.TempDir(), "ref")
	if err := os.Mkdir(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(ref)
	for _, d := range []string{filepath.Dir(p), filepath.Dir(filepath.Dir(p)), filepath.Join(f.gitDir, "lfs")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != want.Mode().Perm() {
			t.Errorf("the folder %s = %v, %v, want mode 0755", d, fi, err)
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
// seed and the keep copy the object (the seed is measured on Linux, the copy
// of the keep and its mode 0644 are claustrum's own).
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
	// A file made with mode 0644 here gets the same umask.
	ref := filepath.Join(t.TempDir(), "ref")
	if err := os.WriteFile(ref, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(ref)
	if b, rerr := os.ReadFile(p); err != nil || rerr != nil || string(b) != "beta-object" || fi.Mode().Perm() != want.Mode().Perm() {
		t.Errorf("the kept copy = %v, %q, %v, want the object with mode 0644", fi, b, err)
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
// macOS for bytes and a size that differ from the pointer).
func TestLFSKeepNeedsItsBytes(t *testing.T) {
	seamFreeSpace(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	mustWrite(t, filepath.Join(f.remote, f.oids["a.bin"]), "other-bytes-of-another-size")
	f.wantSuccess(t)
	objects := filepath.Join(f.gitDir, "lfs", "objects")
	if names, err := os.ReadDir(objects); err != nil || len(names) != 0 {
		t.Errorf("%s = %v, %v, want a made folder with no shard", objects, names, err)
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
	returnDownloads(gitUse{gitDir: gitDir}, private, false)
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
	if err := pointerScanGit(ctx, gitUse{}, f.repo, nil, io.Discard, "ls-tree", "HEAD"); err == nil {
		t.Error("the ls-tree call of the seed after the deadline = nil error, want a failure")
	}
	if got := commitPointers(ctx, gitUse{}, f.repo, "HEAD"); len(got) != 0 {
		t.Errorf("commitPointers after the deadline = %+v, want none", got)
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
	seedObjects(ctx, store, private, []pointerRef{{oid: oid, size: 12}})
	if p := storeObjectPath(private, oid); exists(p) {
		t.Errorf("%s exists, want no seed after the deadline", p)
	}
	seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}})
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
	returnDownload(root, gitDir, private, oid, time.Now(), false)
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
	returnDownload(root, gitDir, private, oid, time.Now(), true)
	if names, _ := os.ReadDir(shard); len(names) != 1 || len(removed) != 0 {
		t.Errorf("the shard holds %v and %q was removed, want only the stale name and no remove", names, removed)
	}
}

// TestSeedSpareMakesNoTemp: a copy left out for seedSpare makes no temp copy
// in the checkout folder, and the seed reports it (claustrum's own).
func TestSeedSpareMakesNoTemp(t *testing.T) {
	prevSeed, prevFree := seedLink, storeFreeBytes
	seedLink = func(string, string) error { return os.ErrPermission }
	storeFreeBytes = func(string) (uint64, bool) { return 0, true }
	t.Cleanup(func() { seedLink, storeFreeBytes = prevSeed, prevFree })
	store, private := filepath.Join(t.TempDir(), "lfs"), filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("a", 64)
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	if !seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}}) {
		t.Error("seedObjects = false, want a copy left out")
	}
	if temps, _ := filepath.Glob(filepath.Join(filepath.Dir(private), seedTempPattern)); len(temps) != 0 || exists(storeObjectPath(private, oid)) {
		t.Errorf("temps %q or the object exist, want neither", temps)
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
// The rows run the bound by a hard link and by a copy. The exact byte of the
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
			returnDownload(root, gitDir, private, oid, time.Now(), row.failed)
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
