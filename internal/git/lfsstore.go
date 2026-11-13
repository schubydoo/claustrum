package git

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// The private LFS store of git.worktree_create (storeInCheckout). In the state
// shadow the read-tree call of 4c111e86 carries lfs.storage=<P>/lfs, where P is
// the `.checkout-<n>` folder of the create (measured on Linux and macOS).
//
//   - The seed. Before the read-tree call, an object of the store of the
//     repository, <git dir>/lfs/objects, is in P when the checkout needs it.
//     An object with the size of its pointer is there, also with wrong bytes.
//     An object of another size, a link and a folder are not (measured on
//     Linux). On one file system the seed is a hard link: the object has link
//     count 2 in P. On another file system it is a copy, with link count 1
//     (measured on Linux). A link at `lfs` or at `lfs/objects` of the
//     repository still gives the seed (measured on Linux and macOS), as a copy
//     (measured on Linux).
//   - The keep. After a good checkout, an object that git-lfs downloaded into
//     P reaches the store of the repository. Its shard folders are made when
//     they are missing, with mode 0755 (measured on macOS, claustrum's own on
//     Linux). A link at `lfs`,
//     and a folder `lfs/objects` that cannot be written, keep nothing, and the
//     create succeeds (measured on Linux). No object is kept after a failed or
//     a stopped checkout (claustrum's own, not measured).
//   - The sweep of stale temp names (sweepShardTemps).
//
// Claustrum's own, not measured: the seed reads the pointers with `ls-tree`
// and `cat-file --batch` in the new worktree, and only when the repository
// has a folder `lfs/objects`. It reads the store of the repository by path,
// read only. It stops at the deadline of the caller. A link at a shard folder on the way gives a copy too. The keep
// makes the object by a hard link, or by a copy with mode 0644 across file
// systems. It keeps no object that the store already has. A link at
// `lfs/objects` or at a shard folder keeps nothing.
const (
	// pointerBlobMax is the largest blob that the seed reads as a pointer
	// (claustrum's own, the cutoff of git-lfs).
	pointerBlobMax = 1024
	// shardReadMax is the most entries that the sweep reads in one shard.
	// Of 4300 old names, 205 or 206 stayed (measured on Linux). Claustrum
	// reads at most 4096 entries.
	shardReadMax = 4096
	// staleTempAge is the age of a stale temp name of another object. A name 1
	// minute old stays, and one 12 minutes old goes (measured on Linux and
	// macOS). The bound of 10 minutes between them is claustrum's own.
	staleTempAge = 10 * time.Minute
)

var (
	oidPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	staleTempName = regexp.MustCompile(`^\.claude-[0-9a-f]{8}-([0-9a-f]{64})$`)
)

// privateStore is the private LFS store of one checkout. path is <P>/lfs, or
// "" for the store of the git directory. seed runs the seed into path.
type privateStore struct {
	path string
	seed func()
}

// pointerRef is one LFS pointer of the commit of a checkout.
type pointerRef struct {
	oid  string
	size int64
}

// storeObjectPath is the path of the object oid in the LFS store store.
func storeObjectPath(store, oid string) string {
	return filepath.Join(store, "objects", oid[:2], oid[2:4], oid)
}

// readPointerBlob reads one pointer blob (git-lfs spec v1).
func readPointerBlob(body []byte) (pointerRef, bool) {
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) < 3 || lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return pointerRef{}, false
	}
	var p pointerRef
	sized := false
	for _, l := range lines[1:] {
		if v, ok := strings.CutPrefix(l, "oid sha256:"); ok && oidPattern.MatchString(v) {
			p.oid = v
		}
		if v, ok := strings.CutPrefix(l, "size "); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return pointerRef{}, false
			}
			p.size, sized = n, true
		}
	}
	return p, p.oid != "" && sized
}

// lsTreeHeadMax is the most bytes that blobLister keeps of the text before
// the tab of one record. A longer text is no record of git, and the record
// counts as no blob (claustrum's own).
const lsTreeHeadMax = 256

// blobLister reads an `ls-tree -r -z -l` output as it comes, as the stdout of
// the call. It keeps the ids of the blobs of at most pointerBlobMax bytes, each
// once. Of each record it keeps only the text before the tab.
type blobLister struct {
	head   []byte
	inPath bool
	ids    []string
	seen   map[string]bool
}

func (l *blobLister) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			l.add(p)
			return n, nil
		}
		l.add(p[:i])
		l.end()
		p = p[i+1:]
	}
}

// add takes the bytes b of the record that is being read.
func (l *blobLister) add(b []byte) {
	if l.inPath {
		return
	}
	if i := bytes.IndexByte(b, '\t'); i >= 0 {
		b, l.inPath = b[:i], true
	}
	if len(l.head) <= lsTreeHeadMax {
		l.head = append(l.head, b[:min(len(b), lsTreeHeadMax+1-len(l.head))]...)
	}
}

// end closes the record that is being read. The output of git ends each record
// with a NUL, and the caller calls end once more after the call.
func (l *blobLister) end() {
	head, ok := l.head, l.inPath && len(l.head) <= lsTreeHeadMax
	l.head, l.inPath = l.head[:0], false
	if !ok {
		return
	}
	f := strings.Fields(string(head))
	if len(f) != 4 || f[1] != "blob" || l.seen[f[2]] {
		return
	}
	if n, err := strconv.Atoi(f[3]); err != nil || n > pointerBlobMax {
		return
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	l.seen[f[2]] = true
	l.ids = append(l.ids, f[2])
}

// batchHeaderMax is the most bytes of one header line of a `cat-file --batch`
// output. A longer line ends the read (claustrum's own).
const batchHeaderMax = 512

// batchReader reads a `cat-file --batch` output as it comes, as the stdout of
// the call. It keeps the pointers, each oid once. The first header that it
// cannot read ends the read. A body of more than pointerBlobMax bytes is read
// past and kept as no pointer (claustrum's own: the call asks only for blobs
// of at most that size).
type batchReader struct {
	line   []byte
	body   []byte
	need   int
	inBody bool
	keep   bool
	sep    bool
	done   bool
	ps     []pointerRef
	seen   map[string]bool
}

func (b *batchReader) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !b.done {
		switch {
		case b.sep:
			// The byte after a body, a newline in the output of git.
			p, b.sep = p[1:], false
		case b.inBody:
			k := min(b.need, len(p))
			if b.keep {
				b.body = append(b.body, p[:k]...)
			}
			b.need, p = b.need-k, p[k:]
			if b.need == 0 {
				b.take()
			}
		default:
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				b.line = append(b.line, p...)
				b.done = len(b.line) > batchHeaderMax
				return n, nil
			}
			b.line = append(b.line, p[:i]...)
			p = p[i+1:]
			b.header()
		}
	}
	return n, nil
}

// header reads the header line b.line.
func (b *batchReader) header() {
	f := strings.Fields(string(b.line))
	long := len(b.line) > batchHeaderMax
	b.line = b.line[:0]
	if long {
		b.done = true
		return
	}
	if len(f) == 2 && f[1] == "missing" {
		return
	}
	if len(f) != 3 {
		b.done = true
		return
	}
	n, err := strconv.Atoi(f[2])
	if err != nil || n < 0 {
		b.done = true
		return
	}
	b.body, b.need, b.inBody, b.keep = b.body[:0], n, true, n <= pointerBlobMax
	if n == 0 {
		b.take()
	}
}

// take ends a whole body and keeps it when it is a new pointer.
func (b *batchReader) take() {
	b.inBody, b.sep = false, true
	if !b.keep {
		return
	}
	if p, ok := readPointerBlob(b.body); ok && !b.seen[p.oid] {
		if b.seen == nil {
			b.seen = map[string]bool{}
		}
		b.seen[p.oid] = true
		b.ps = append(b.ps, p)
	}
}

// idLines is the stdin of `cat-file --batch`: each id on a line of its own.
type idLines struct {
	ids  []string
	part string
}

func (r *idLines) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.part == "" {
			if len(r.ids) == 0 {
				break
			}
			r.part, r.ids = r.ids[0]+"\n", r.ids[1:]
		}
		k := copy(p[n:], r.part)
		r.part, n = r.part[k:], n+k
	}
	if n == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	return n, nil
}

// pointerScanGit runs one call of the seed in dir, with its config listing first,
// under a limit of its own, as a call of the content check (contentCheck.git).
// The call ends at the deadline of ctx. Its stdout goes to stdout as it comes.
func pointerScanGit(ctx context.Context, u gitUse, dir string, stdin io.Reader, stdout io.Writer, args ...string) error {
	s := u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	ctx = context.WithValue(ctx, gitStopKey{}, s)
	cmd := hardenedGitCmd(ctx, u, dir, false, nil, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	return gitRun(ctx, cmd)
}

// commitPointers are the pointers of the tree of rev, read in dir. It keeps no
// output of git but the ids of the small blobs and the pointers.
func commitPointers(ctx context.Context, u gitUse, dir, rev string) []pointerRef {
	var l blobLister
	if pointerScanGit(ctx, u, dir, nil, &l, "ls-tree", "-r", "-z", "-l", rev) != nil {
		return nil
	}
	l.end()
	if len(l.ids) == 0 || ctx.Err() != nil {
		return nil
	}
	var b batchReader
	if pointerScanGit(ctx, u, dir, &idLines{ids: l.ids}, &b, "cat-file", "--batch") != nil {
		return nil
	}
	return b.ps
}

// fillCheckoutStore puts into the private store private each object of the store
// of gitDir that a pointer of rev names: a regular file with the size of the
// pointer. It makes a hard link, or a copy when the link fails or a folder on
// the way is a link. A failure leaves the object out, and git-lfs then
// downloads it. At the deadline of ctx the seed stops, and the objects that it
// did not reach stay out.
func fillCheckoutStore(ctx context.Context, u gitUse, dir, gitDir, rev, private string) {
	if ctx.Err() != nil {
		return
	}
	store := filepath.Join(gitDir, "lfs")
	if fi, err := os.Stat(filepath.Join(store, "objects")); err != nil || !fi.IsDir() {
		return
	}
	seedObjects(ctx, store, private, commitPointers(ctx, u, dir, rev))
}

// seedObjects puts the objects of ps from the store store into private.
func seedObjects(ctx context.Context, store, private string, ps []pointerRef) {
	viaLink := pathIsLink(store) || pathIsLink(filepath.Join(store, "objects"))
	for _, p := range ps {
		if ctx.Err() != nil {
			return
		}
		src := storeObjectPath(store, p.oid)
		fi, err := os.Lstat(src)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != p.size {
			continue
		}
		dst := storeObjectPath(private, p.oid)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			continue
		}
		shard := filepath.Dir(src)
		if viaLink || pathIsLink(shard) || pathIsLink(filepath.Dir(shard)) || seedLink(src, dst) != nil {
			_ = copySeed(ctx, src, dst, filepath.Dir(private))
		}
	}
}

// pathIsLink reports whether the last name of p is a symbolic link.
func pathIsLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// seedLink and keepHardLink make the hard links of the seed and of the keep. A
// test replaces them to reach the copy. seedCopy is the copy of the seed, which
// a test replaces to fail it.
var (
	seedLink     = os.Link
	keepHardLink = linkIntoRoot
	seedCopy     = io.Copy
)

// seedTempPattern is the pattern of the temp name of a copy of the seed in
// the checkout folder. No oid has its form.
const seedTempPattern = "lfs-seed-*"

// copySeed copies the object src to dst, a new file. The copy goes to a temp
// name in the folder tmpDir, the checkout folder, and gets the name dst only
// when it is whole. A failed temp stays in that folder and goes with it. The
// copy stops at the deadline of ctx.
func copySeed(ctx context.Context, src, dst, tmpDir string) error {
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = fs.ErrExist
		}
		return err
	}
	in, err := openObjectNoFollow(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(tmpDir, seedTempPattern)
	if err != nil {
		return err
	}
	_, err = seedCopy(out, ctxReader{ctx, in})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

// ctxReader is r that fails each read after the end of ctx.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// downloadedOIDs are the oids of the regular files in the private store
// private, each at its shard.
func downloadedOIDs(private string) []string {
	var oids []string
	objects := filepath.Join(private, "objects")
	a, _ := os.ReadDir(objects)
	for _, x := range a {
		b, _ := os.ReadDir(filepath.Join(objects, x.Name()))
		for _, y := range b {
			c, _ := os.ReadDir(filepath.Join(objects, x.Name(), y.Name()))
			for _, z := range c {
				n := z.Name()
				if oidPattern.MatchString(n) && n[:2] == x.Name() && n[2:4] == y.Name() && z.Type().IsRegular() {
					oids = append(oids, n)
				}
			}
		}
	}
	return oids
}

// returnDownloads puts each object of the private store private that the store
// of the repository lacks into that store. The writes go through the held git
// directory of the create. A create that holds none keeps nothing (claustrum's
// own, not measured).
func returnDownloads(u gitUse, private string) {
	a := u.accepted
	if a == nil || a.root == nil {
		return
	}
	for _, oid := range downloadedOIDs(private) {
		returnDownload(a.root, a.path, private, oid, time.Now())
	}
}

// openShard opens the shard folder rel of root one folder at a time, each from
// the handle of its parent (shardStep). The caller closes the handle. A folder
// that is a link, no folder or not the folder that its Lstat saw gives nil. The
// path of the git directory is not read again.
func openShard(root *os.Root, rel string) *os.Root {
	cur := root
	for _, name := range strings.Split(filepath.ToSlash(rel), "/") {
		next := shardStep(cur, name)
		if cur != root {
			_ = cur.Close()
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// shardStep opens the folder name of parent. It makes the folder when it is
// missing, with mode 0755. The folder must be a real folder by Lstat through
// parent, and the opened folder must be that folder.
func shardStep(parent *os.Root, name string) *os.Root {
	fi, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		if parent.Mkdir(name, 0o755) != nil {
			return nil
		}
		fi, err = parent.Lstat(name)
	}
	if err != nil || !fi.IsDir() {
		return nil
	}
	next, err := openShardFolder(parent, name)
	if err != nil {
		return nil
	}
	if got, err := next.Stat("."); err != nil || !os.SameFile(fi, got) {
		_ = next.Close()
		return nil
	}
	return next
}

// openShardFolder is the open of one folder of shardStep. A test replaces it
// to swap the folder after its Lstat.
var openShardFolder = (*os.Root).OpenRoot

// returnDownload keeps the object oid of the private store in the store of the
// git directory root at dir. After the object has its name it sweeps the
// shard (sweepShardTemps). An object that the store already has is left, with
// no sweep: the shard of a seeded object keeps its temp names (measured on
// Linux). The keep and the sweep act through the handle of openShard, and a
// folder that openShard refuses keeps nothing.
func returnDownload(root *os.Root, dir, private, oid string, now time.Time) {
	rel := filepath.Join("lfs", "objects", oid[:2], oid[2:4])
	shard := openShard(root, rel)
	if shard == nil {
		return
	}
	defer func() { _ = shard.Close() }()
	if _, err := shard.Lstat(oid); err == nil {
		return
	}
	shardPath := filepath.Join(dir, rel)
	var n [4]byte
	if _, err := rand.Read(n[:]); err != nil {
		return
	}
	temp := ".claude-" + hex.EncodeToString(n[:]) + "-" + oid
	src := storeObjectPath(private, oid)
	if keepHardLink(shard, src, temp) != nil && !copyIntoRoot(shard, shardPath, src, temp) {
		return
	}
	err := shard.Link(temp, oid)
	// temp is the name that the keep built above, not a name from a read.
	removeLFSStoreEntry(shard, shardPath, temp)
	if err == nil {
		sweepShardTemps(shard, shardPath, oid, now)
	}
}

// copyIntoRoot copies the file src to the new file name of root, with mode
// 0644, and reports success. A failed copy removes the file that it made.
func copyIntoRoot(root *os.Root, rootPath, src, name string) bool {
	in, err := openObjectNoFollow(src)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// name is the temp name that the keep built, not a name from a read.
		removeLFSStoreEntry(root, rootPath, name)
		return false
	}
	return true
}

// sweepShardTemps removes stale temp names from the shard of the object oid
// that the keep just put in the store. It reads at most shardReadMax
// entries. A regular file named `.claude-<8 hex>-<64 hex>` goes when its oid
// is oid, or when it is older than staleTempAge (measured on Linux and macOS).
// A name in upper case, a short oid and every other name stay (measured).
// Only a regular file goes: that test is claustrum's own. Each remove is one
// `unlinkat` of a file by a name from the directory read
// (removeLFSStoreEntry). A name that turns into an empty folder after the
// Lstat stays, because the remove takes only a file.
func sweepShardTemps(shard *os.Root, shardPath, oid string, now time.Time) {
	d, err := shard.Open(".")
	if err != nil {
		return
	}
	entries, _ := d.ReadDir(shardReadMax)
	_ = d.Close()
	for _, e := range entries {
		m := staleTempName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		fi, err := shard.Lstat(e.Name())
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if m[1] != oid && now.Sub(fi.ModTime()) <= staleTempAge {
			continue
		}
		removeLFSStoreEntry(shard, shardPath, e.Name())
	}
}

// removeLFSStoreFile is the one remove of the LFS store of a repository: one
// unlinkat of a file (removeFileAt), so a folder at name stays. A test
// replaces it.
var removeLFSStoreFile = removeFileAt

// removeLFSStoreEntry removes name from the shard folder shard, at
// shardPath. homeguard.WipesHomeDir on the full path comes first, as for each
// delete in the folder git-shadow (removeEntry).
func removeLFSStoreEntry(shard *os.Root, shardPath, name string) {
	if homeguard.WipesHomeDir(filepath.Join(shardPath, name)) {
		return
	}
	_ = removeLFSStoreFile(shard, name)
}
