package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// lfsSmudgeHelper is a stand-in git-lfs smudge filter (the helper mode
// lfs-smudge). It reads the pointer on stdin and the store from the item
// lfs.storage that git passes on. It logs `<oid 8> present=<bool> store=<store>`
// to log. A present object goes to stdout. Else it copies remote/<oid> into
// the store, as git-lfs does after a download, or fails with exit 2 and the
// text of remote/error, or a 404 text with no such file.
func lfsSmudgeHelper(remote, log string) int {
	body, _ := io.ReadAll(os.Stdin)
	p, ok := readPointerBlob(body)
	if !ok {
		_, _ = os.Stdout.Write(body)
		return 0
	}
	store := ""
	m := regexp.MustCompile(`'lfs\.storage'='([^']*)'|'lfs\.storage=([^']*)'`).FindStringSubmatch(os.Getenv("GIT_CONFIG_PARAMETERS"))
	if m != nil {
		store = m[1] + m[2]
	}
	obj := storeObjectPath(store, p.oid)
	_, err := os.Stat(obj)
	appendLine(log, fmt.Sprintf("%s present=%v store=%s", p.oid[:8], err == nil, store))
	if err == nil {
		b, err := os.ReadFile(obj)
		if err != nil {
			return 2
		}
		_, _ = os.Stdout.Write(b)
		return 0
	}
	b, err := os.ReadFile(filepath.Join(remote, p.oid))
	if err != nil {
		text := "Object does not exist on the server: [404]\n"
		if e, rerr := os.ReadFile(filepath.Join(remote, "error")); rerr == nil {
			text = string(e)
		}
		fmt.Fprint(os.Stderr, text)
		return 2
	}
	if os.MkdirAll(filepath.Dir(obj), 0o755) != nil || os.WriteFile(obj+".tmp", b, 0o644) != nil || os.Rename(obj+".tmp", obj) != nil {
		return 2
	}
	_, _ = os.Stdout.Write(b)
	return 0
}

// TestReadPointerBlob: a pointer of the git-lfs spec v1 gives its oid and
// size. Other text gives none.
func TestReadPointerBlob(t *testing.T) {
	oid := strings.Repeat("ab", 32)
	for _, row := range []struct {
		body string
		ok   bool
	}{
		{"version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 12\n", true},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "x\nsize 12\n", false},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "a\nsize 12\n", false},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:../../" + oid + "\nsize 12\n", false},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.ToUpper(oid) + "\nsize 12\n", false},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\n", false},
		{"oid sha256:" + oid + "\nsize 12\nversion https://git-lfs.github.com/spec/v1\n", false},
		{"plain text\n", false},
	} {
		p, ok := readPointerBlob([]byte(row.body))
		if ok != row.ok || (ok && (p.oid != oid || p.size != 12)) {
			t.Errorf("readPointerBlob(%q) = %+v, %v, want ok %v", row.body, p, ok, row.ok)
		}
	}
}

// TestSmallBlobs: the blobs of at most pointerBlobMax bytes, each once.
func TestSmallBlobs(t *testing.T) {
	out := "100644 blob aaaa     120\ta.bin\x00100644 blob bbbb    1025\tbig\x00" +
		"160000 commit cccc       -\tsub\x00100644 blob aaaa     120\tcopy.bin\x00100644 blob dddd    1024\tedge\x00"
	if got, want := smallBlobs([]byte(out)), []string{"aaaa", "dddd"}; !slices.Equal(got, want) {
		t.Errorf("smallBlobs = %q, want %q", got, want)
	}
}

// TestBatchPointers: the pointers of a `cat-file --batch` output, with a
// missing object and a blob that is no pointer.
func TestBatchPointers(t *testing.T) {
	oid := strings.Repeat("0f", 32)
	ptr := "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 7\n"
	out := "1111 missing\n" + fmt.Sprintf("2222 blob %d\n%s\n", len(ptr), ptr) + "3333 blob 3\nab\n\n" +
		fmt.Sprintf("4444 blob %d\n%s\n", len(ptr), ptr)
	got := batchPointers([]byte(out))
	if len(got) != 1 || got[0].oid != oid || got[0].size != 7 {
		t.Errorf("batchPointers = %+v, want the one pointer", got)
	}
}

// lfsStorageItemOK reports whether item is the lfs.storage item of a create
// on this system: the store of the checkout folder on Linux and macOS, and the
// store of gitDir on Windows.
func lfsStorageItemOK(item, gitDir string) bool {
	v, ok := strings.CutPrefix(item, "lfs.storage=")
	if !ok {
		return false
	}
	if !storeInCheckout {
		return v == filepath.Join(gitDir, "lfs")
	}
	return filepath.Base(v) == "lfs" && strings.HasPrefix(filepath.Base(filepath.Dir(v)), checkoutDirPrefix)
}

// TestLFSStoreRemoveHomeGuard: homeguard.WipesHomeDir refuses a remove of the
// home folder. The remove is the seam, so nothing goes.
func TestLFSStoreRemoveHomeGuard(t *testing.T) {
	dir := t.TempDir()
	name := ".claude-00000000-" + strings.Repeat("7", 64)
	mustWrite(t, filepath.Join(dir, name), "")
	var removed []string
	seamLFSRemove(t, &removed)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	t.Setenv("HOME", filepath.Join(dir, name))
	t.Setenv("USERPROFILE", filepath.Join(dir, name))
	removeLFSStoreEntry(root, dir, name)
	if len(removed) != 0 {
		t.Errorf("removed %q, want no remove of the home folder", removed)
	}
	other := t.TempDir()
	t.Setenv("HOME", other)
	t.Setenv("USERPROFILE", other)
	removeLFSStoreEntry(root, dir, name)
	if !slices.Equal(removed, []string{name}) {
		t.Errorf("removed %q, want %q", removed, name)
	}
}

// seamLFSRemove makes removeLFSStoreFile record each name and remove nothing.
func seamLFSRemove(t *testing.T, removed *[]string) {
	t.Helper()
	prev := removeLFSStoreFile
	removeLFSStoreFile = func(_ *os.Root, name string) error {
		*removed = append(*removed, name)
		return nil
	}
	t.Cleanup(func() { removeLFSStoreFile = prev })
}

// TestLFSSweepReadsAtMost4096Entries: the sweep reads at most
// shardReadMax entries of a shard (measured on Linux: of 4300 old names
// 205 or 206 stay). The remove is the seam, so nothing goes.
func TestLFSSweepReadsAtMost4096Entries(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	for i := range 4100 {
		p := filepath.Join(dir, fmt.Sprintf(".claude-%08x-%s", i, strings.Repeat("5", 64)))
		mustWrite(t, p, "")
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	var removed []string
	seamLFSRemove(t, &removed)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	sweepShardTemps(root, dir, strings.Repeat("6", 64), time.Now())
	if len(removed) != shardReadMax {
		t.Errorf("the sweep removed %d names, want %d", len(removed), shardReadMax)
	}
}

// TestLFSNoSweepAfterFailedKeep: a keep that makes no object sweeps nothing,
// also a temp name of the same oid.
func TestLFSNoSweepAfterFailedKeep(t *testing.T) {
	gitDir := t.TempDir()
	oid := strings.Repeat("e", 64)
	shard := filepath.Join(gitDir, "lfs", "objects", "ee", "ee")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(shard, ".claude-aaaaaaaa-"+oid), "stale")
	var removed []string
	seamLFSRemove(t, &removed)
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	returnDownload(root, gitDir, t.TempDir(), oid, time.Now())
	if len(removed) != 0 {
		t.Errorf("removed %q, want no sweep after a failed keep", removed)
	}
}
