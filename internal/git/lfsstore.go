package git

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
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
// read only. A link at a shard folder on the way gives a copy too. The keep
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
// "" for the store of the git directory. seed runs the seed into path. runDir
// is the folder of the socket.
type privateStore struct {
	path   string
	runDir string
	seed   func()
}

// storeUse is what a checkout that named its private store gives the note of
// its failure (checkoutNote).
type storeUse struct {
	runDir string
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

// smallBlobs are the ids of the blobs of an `ls-tree -r -z -l` output whose
// size is at most pointerBlobMax, each once.
func smallBlobs(out []byte) []string {
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

// batchPointers are the pointers of a `cat-file --batch` output, each oid once.
func batchPointers(out []byte) []pointerRef {
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

// pointerScanGit runs one call of the seed in dir, with its config listing first,
// under a limit of its own, as a call of the content check (contentCheck.git).
func pointerScanGit(u gitUse, dir string, stdin []byte, args ...string) ([]byte, error) {
	s := u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	ctx := s.context()
	cmd := hardenedGitCmd(ctx, u, dir, false, nil, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return gitOutput(ctx, cmd)
}

// commitPointers are the pointers of the tree of rev, read in dir.
func commitPointers(u gitUse, dir, rev string) []pointerRef {
	out, err := pointerScanGit(u, dir, nil, "ls-tree", "-r", "-z", "-l", rev)
	if err != nil {
		return nil
	}
	ids := smallBlobs(out)
	if len(ids) == 0 {
		return nil
	}
	out, err = pointerScanGit(u, dir, []byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil
	}
	return batchPointers(out)
}

// fillCheckoutStore puts into the private store private each object of the store
// of gitDir that a pointer of rev names: a regular file with the size of the
// pointer. It makes a hard link, or a copy when the link fails or a folder on
// the way is a link. A failure leaves the object out, and git-lfs then
// downloads it.
func fillCheckoutStore(u gitUse, dir, gitDir, rev, private string) {
	store := filepath.Join(gitDir, "lfs")
	if fi, err := os.Stat(filepath.Join(store, "objects")); err != nil || !fi.IsDir() {
		return
	}
	viaLink := pathIsLink(store) || pathIsLink(filepath.Join(store, "objects"))
	for _, p := range commitPointers(u, dir, rev) {
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
			_ = copySeed(src, dst)
		}
	}
}

// pathIsLink reports whether the last name of p is a symbolic link.
func pathIsLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// seedLink and keepHardLink make the hard links of the seed and of the keep. A
// test replaces them to reach the copy.
var (
	seedLink     = os.Link
	keepHardLink = linkIntoRoot
)

// copySeed copies the object src to dst, a new file.
func copySeed(src, dst string) error {
	in, err := openObjectNoFollow(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
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

// realShard makes the folders of rel in root that are missing, with mode 0755.
// It reports false when one of them is a link or no folder, or cannot be made.
func realShard(root *os.Root, rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		fi, err := root.Lstat(p)
		if os.IsNotExist(err) {
			if err := root.Mkdir(p, 0o755); err != nil {
				return false
			}
			continue
		}
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// returnDownload keeps the object oid of the private store in the store of the
// git directory root at dir. After the object has its name it sweeps the
// shard (sweepShardTemps). An object that the store already has is left, with
// no sweep: the shard of a seeded object keeps its temp names (measured on
// Linux). A shard folder that is not the folder that realShard tested keeps
// nothing.
func returnDownload(root *os.Root, dir, private, oid string, now time.Time) {
	rel := filepath.Join("lfs", "objects", oid[:2], oid[2:4])
	if !realShard(root, rel) {
		return
	}
	want, err := root.Lstat(rel)
	if err != nil {
		return
	}
	shard, err := root.OpenRoot(rel)
	if err != nil {
		return
	}
	defer func() { _ = shard.Close() }()
	if got, err := shard.Stat("."); err != nil || !os.SameFile(want, got) {
		return
	}
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
	err = shard.Link(temp, oid)
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
