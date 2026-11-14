package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lfsSmudgeHelper is a stand-in git-lfs smudge filter (the helper mode
// lfs-smudge). It reads the pointer on stdin and the store from the item
// lfs.storage that git passes on. It logs `<oid 8> present=<bool> store=<store>`
// to log. A present object goes to stdout. Else it copies remote/<oid> into
// the store, as git-lfs does after a download, or fails with exit 2 and the
// text of remote/error, or a 404 text with no such file. `<store>` in that text
// is the store. With a file remote/hang it waits for its kill.
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
	if exists(filepath.Join(remote, "hang")) {
		// A download that does not end, as a slow server. With remote/incomplete
		// it first makes <store>/incomplete, as git-lfs does when a download
		// begins. The deadline of the create kills it with its process group.
		if exists(filepath.Join(remote, "incomplete")) {
			_ = os.MkdirAll(filepath.Join(store, "incomplete"), 0o755)
		}
		time.Sleep(time.Minute)
		return 2
	}
	b, err := os.ReadFile(filepath.Join(remote, p.oid))
	if err != nil {
		text := "Object does not exist on the server: [404]\n"
		if e, rerr := os.ReadFile(filepath.Join(remote, "error")); rerr == nil {
			text = strings.ReplaceAll(string(e), "<store>", store)
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

// listBlobs feeds out to a blobLister in pieces of size step, as a pipe
// gives it, and returns the ids.
func listBlobs(out []byte, step int) []string {
	var l blobLister
	for len(out) > 0 {
		k := min(step, len(out))
		_, _ = l.Write(out[:k])
		out = out[k:]
	}
	l.end()
	return l.ids
}

// readBatch feeds out to a batchReader in pieces of size step.
func readBatch(out []byte, step int) []pointerRef {
	var b batchReader
	for len(out) > 0 {
		k := min(step, len(out))
		_, _ = b.Write(out[:k])
		out = out[k:]
	}
	return b.ps
}

// TestSmallBlobs: the blobs of at most pointerBlobMax bytes, each once.
func TestSmallBlobs(t *testing.T) {
	out := "100644 blob aaaa     120\ta.bin\x00100644 blob bbbb    1025\tbig\x00" +
		"160000 commit cccc       -\tsub\x00100644 blob aaaa     120\tcopy.bin\x00100644 blob dddd    1024\tedge\x00"
	for _, step := range []int{1, 7, len(out)} {
		if got, want := listBlobs([]byte(out), step), []string{"aaaa", "dddd"}; !slices.Equal(got, want) {
			t.Errorf("step %d: ids = %q, want %q", step, got, want)
		}
	}
}

// TestBatchPointers: the pointers of a `cat-file --batch` output, with a
// missing object and a blob that is no pointer.
func TestBatchPointers(t *testing.T) {
	oid := strings.Repeat("0f", 32)
	ptr := "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 7\n"
	out := "1111 missing\n" + fmt.Sprintf("2222 blob %d\n%s\n", len(ptr), ptr) + "3333 blob 3\nab\n\n" +
		fmt.Sprintf("4444 blob %d\n%s\n", len(ptr), ptr)
	for _, step := range []int{1, 7, len(out)} {
		got := readBatch([]byte(out), step)
		if len(got) != 1 || got[0].oid != oid || got[0].size != 7 {
			t.Errorf("step %d: pointers = %+v, want the one pointer", step, got)
		}
	}
}

// wholeSmallBlobs and wholeBatchPointers read a whole output at once, as the
// seed did before it read the output as it comes. They are the reference of
// TestLargeScanOutputs.
func wholeSmallBlobs(out []byte) []string {
	var ids []string
	seen := map[string]bool{}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		head, _, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(head))
		if !ok || len(f) != 4 || f[1] != "blob" || seen[f[2]] {
			continue
		}
		if n, err := strconv.Atoi(f[3]); err != nil || n > pointerBlobMax {
			continue
		}
		seen[f[2]] = true
		ids = append(ids, f[2])
	}
	return ids
}

func wholeBatchPointers(out []byte) []pointerRef {
	var ps []pointerRef
	seen := map[string]bool{}
	r := bufio.NewReader(bytes.NewReader(out))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return ps
		}
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 {
			return ps
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 0 {
			return ps
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return ps
		}
		_, _ = r.ReadByte()
		if p, ok := readPointerBlob(body); ok && !seen[p.oid] {
			seen[p.oid] = true
			ps = append(ps, p)
		}
	}
}

// TestLargeScanOutputs: a large `ls-tree` output with long paths and a large
// `cat-file --batch` output, read in pieces, give the result of a whole read.
func TestLargeScanOutputs(t *testing.T) {
	var lsTree, batch bytes.Buffer
	long := strings.Repeat("p/", 3000)
	for i := range 60000 {
		id := fmt.Sprintf("%040x", i%50000)
		size := []int{0, 120, 1024, 1025, 4096}[i%5]
		path := fmt.Sprintf("f%d.bin", i)
		if i%97 == 0 {
			path = long + path
		}
		fmt.Fprintf(&lsTree, "100644 blob %s %7d\t%s\x00", id, size, path)
		if i%11 == 0 {
			fmt.Fprintf(&lsTree, "160000 commit %s       -\tsub%d\x00", id, i)
		}
		if i%13 == 0 {
			fmt.Fprintf(&batch, "%s missing\n", id)
			continue
		}
		body := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%064x\nsize %d\n", i%40000, i)
		if i%7 == 0 {
			body = "not a pointer"
		}
		fmt.Fprintf(&batch, "%s blob %d\n%s\n", id, len(body), body)
	}
	wantIDs, wantPs := wholeSmallBlobs(lsTree.Bytes()), wholeBatchPointers(batch.Bytes())
	if len(wantIDs) == 0 || len(wantPs) == 0 {
		t.Fatalf("the fixture gives %d ids and %d pointers, want some", len(wantIDs), len(wantPs))
	}
	for _, step := range []int{1, 4093, 65536, lsTree.Len()} {
		if got := listBlobs(lsTree.Bytes(), step); !slices.Equal(got, wantIDs) {
			t.Errorf("step %d: %d ids, want the %d ids of a whole read", step, len(got), len(wantIDs))
		}
	}
	for _, step := range []int{1, 4093, 65536, batch.Len()} {
		if got := readBatch(batch.Bytes(), step); !slices.Equal(got, wantPs) {
			t.Errorf("step %d: %d pointers, want the %d pointers of a whole read", step, len(got), len(wantPs))
		}
	}
}

// TestIDLines: the stdin of `cat-file --batch` is each id on its own line.
func TestIDLines(t *testing.T) {
	b, err := io.ReadAll(&idLines{ids: []string{"aaaa", "bbbb", "cccc"}})
	if err != nil || string(b) != "aaaa\nbbbb\ncccc\n" {
		t.Errorf("idLines = %q, %v", b, err)
	}
}

// TestCopySeedLeavesNoPartialObject: a copy of the seed that fails midway
// leaves no file at the name of the object. Its temp stays in the checkout
// folder.
func TestCopySeedLeavesNoPartialObject(t *testing.T) {
	src := filepath.Join(t.TempDir(), "obj")
	mustWrite(t, src, "alpha-object")
	checkout := t.TempDir()
	dst := filepath.Join(checkout, "lfs", "obj")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := seedCopy
	seedCopy = func(w io.Writer, r io.Reader) (int64, error) {
		n, _ := io.CopyN(w, r, 3)
		return n, io.ErrUnexpectedEOF
	}
	t.Cleanup(func() { seedCopy = prev })
	if err := copySeed(context.Background(), src, dst, checkout); err == nil {
		t.Fatal("copySeed with a failed copy = nil, want the error")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s after a failed copy: %v, want no file", dst, err)
	}
	seedCopy = prev
	if err := copySeed(context.Background(), src, dst, checkout); err != nil || mustRead(t, dst) != "alpha-object" {
		t.Errorf("copySeed = %v, want the whole object at its name", err)
	}
}

// TestCopySeedStopsAtDeadline: a copy of the seed after the deadline of the
// caller makes no object.
func TestCopySeedStopsAtDeadline(t *testing.T) {
	src := filepath.Join(t.TempDir(), "obj")
	mustWrite(t, src, "alpha-object")
	checkout := t.TempDir()
	dst := filepath.Join(checkout, "obj")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copySeed(ctx, src, dst, checkout); err == nil || exists(dst) {
		t.Errorf("copySeed after the deadline = %v, exists %v, want an error and no object", err, exists(dst))
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
	returnDownload(context.Background(), root, gitDir, t.TempDir(), oid, time.Now(), false)
	if len(removed) != 0 {
		t.Errorf("removed %q, want no sweep after a failed keep", removed)
	}
}

// TestNotKeptText: a deadline during the checkout blames the copies of the
// seed when they took a large part of timeoutMs and the private store has no
// `incomplete` entry (4c111e86, measured on Linux and macOS). The bound of a
// quarter is claustrum's own. An `incomplete` folder in the store of the
// repository does not count (measured on macOS). An empty one in the private
// store counts (measured on Linux).
func TestNotKeptText(t *testing.T) {
	private := filepath.Join(t.TempDir(), "lfs")
	repoStore := filepath.Join(t.TempDir(), "lfs")
	if err := os.MkdirAll(filepath.Join(repoStore, "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}
	rule := func(copied time.Duration) *filterRule {
		return &filterRule{store: &storeUse{path: private, copied: copied}}
	}
	for _, row := range []struct {
		name      string
		timeoutMs int
		copied    time.Duration
		want      bool
	}{
		{"a copy of a quarter", 4000, time.Second, true},
		{"a copy just under a quarter", 4000, time.Second - time.Millisecond, false},
		{"a copy of more than a quarter", 4145, 1150 * time.Millisecond, true},
		{"a copy of less than a quarter", 4560, 1100 * time.Millisecond, false},
		{"a hard link only", 4000, 0, false},
		{"no timeoutMs", 0, time.Second, false},
	} {
		got, ok := notKeptText(row.timeoutMs, rule(row.copied))
		if ok != row.want {
			t.Errorf("%s: notKeptText(%d, %v) = %q, %v, want %v", row.name, row.timeoutMs, row.copied, got, ok, row.want)
		}
	}
	const want = "Turn off Worktree for this session, then try again. A plain retry won't help: git worktree add timed out after 4 seconds," +
		" and copying the repository's Git LFS files into a folder of Claude's own took a large part of that time, as it does when Claude" +
		" can't hard-link them there (a repository on another disk, a store reached through a link, a relative lfs.storage); a retry copies them again."
	if got, _ := notKeptText(4000, rule(time.Second)); got != want {
		t.Errorf("notKeptText = %q\nwant %q", got, want)
	}
	if _, ok := notKeptText(4000, nil); ok {
		t.Error("notKeptText with no rule = ok, want no text")
	}
	if err := os.MkdirAll(filepath.Join(private, "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := notKeptText(4000, rule(time.Second)); ok {
		t.Errorf("notKeptText with an empty incomplete folder in the private store = %q, want no text", got)
	}
}

// TestDeadlineWords: the words of timeoutMs in the text of
// worktree_lfs_not_kept, each from a reply of 4c111e86 (measured on Linux and
// macOS). 1000 is not measured.
func TestDeadlineWords(t *testing.T) {
	for ms, want := range map[int]string{
		4000: "4 seconds", 3000: "3 seconds", 2763: "2.763 seconds", 2389: "2.3890000000000002 seconds",
		2986: "2.9859999999999998 seconds", 4300: "4.3 seconds", 11713: "11.713 seconds", 15129: "15.129 seconds",
		60000: "1 minute", 90000: "90 seconds", 120000: "2 minutes", 1000: "1 second",
	} {
		if got := deadlineWords(ms); got != want {
			t.Errorf("deadlineWords(%d) = %q, want %q", ms, got, want)
		}
	}
}

// TestSeedContext: the seed of a create with timeoutMs ends at half of it
// (4c111e86, measured on Linux and macOS). With no timeoutMs it keeps ctx.
func TestSeedContext(t *testing.T) {
	start := time.Now()
	ctx, cancel := seedContext(context.Background(), 4000)
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok || d.Sub(start) < 2*time.Second-500*time.Millisecond || d.Sub(start) > 2*time.Second+500*time.Millisecond {
		t.Errorf("seedContext(4000) deadline after %v, %v, want 2 s", d.Sub(start), ok)
	}
	plain, cancelPlain := seedContext(context.Background(), 0)
	defer cancelPlain()
	if _, ok := plain.Deadline(); ok {
		t.Error("seedContext(0) has a deadline, want none")
	}
}

// TestSeedCopyTime: the seed counts the time of its copies. A hard link adds
// no time (claustrum's own, not measured).
func TestSeedCopyTime(t *testing.T) {
	store := filepath.Join(t.TempDir(), "lfs")
	oid := strings.Repeat("a", 64)
	mustWrite(t, storeObjectPath(store, oid), "alpha-object")
	ps := []pointerRef{{oid: oid, size: 12}}
	if _, copied := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), ps); copied != 0 {
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
	if _, copied := seedObjects(context.Background(), store, filepath.Join(t.TempDir(), "lfs"), ps); copied < 30*time.Millisecond {
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
	_, copied := seedObjects(context.Background(), store, private, []pointerRef{{oid: oid, size: 12}})
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
