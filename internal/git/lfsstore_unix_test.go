//go:build !windows

package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// TestLFSNoKeepAfterFailedCheckout: git-lfs downloads a.bin, then b.bin is on
// no server and the checkout fails. The store of the repository lacks a.bin
// (claustrum's own, not measured).
func TestLFSNoKeepAfterFailedCheckout(t *testing.T) {
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	if raw, _ := f.create(t, nil); strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want a failure", raw)
	}
	if got := f.smudges(t)["a.bin"]; got != "present=false private=true" {
		t.Fatalf("smudge line of a.bin = %q, want a download", got)
	}
	if p := storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"]); exists(p) {
		t.Errorf("%s exists, want no keep after a failed checkout", p)
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
	returnDownloads(gitUse{gitDir: gitDir}, private)
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
