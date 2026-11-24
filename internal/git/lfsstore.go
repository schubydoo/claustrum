package git

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/logging"
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
//     they are missing, with mode 0777 less the umask (measured on Linux). On
//     macOS they got 0755 under the default umask (measured). macOS under
//     another umask is claustrum's own. A missing `lfs` and `lfs/objects` get
//     0755 less the umask (claustrum's own, not measured). A link at `lfs`,
//     and a folder `lfs/objects` that cannot be written, keep nothing, and the
//     create succeeds (measured on Linux). An object whose bytes and size
//     differ from its pointer gets `lfs/objects` but no shard folder (measured
//     on Linux and macOS). The keep also runs after a failed or a stopped
//     checkout (measured on Linux and macOS). A stop for the space runs no
//     keep after the checkout (lfsroom.go, measured on Linux). While the
//     checkout runs, ticks keep the objects too (keepTicker).
//   - The sweep of stale temp names (sweepShardTemps).
//
// The seed reads the pointers with `ls-tree` and `cat-file` in baseRepo, with
// GIT_DIR=<repo>/.git and GIT_WORK_TREE=<repo> (4c111e86, measured on Linux).
//
// Claustrum's own, not measured: a baseRepo that is a linked worktree. The
// seed runs only when the repository has a folder `lfs/objects`. It reads the store of the repository by path,
// read only. It stops at the bound of seedContext. A link at a shard folder on the way gives a copy too.
// It keeps no object that the store already has. A link at
// `lfs/objects` or at a shard folder keeps nothing. The keep tests the bytes
// of an object against its oid, not its size. After a deadline it does not
// sweep. On Linux, after a checkout that failed by itself, it makes only a
// hard link at the name of the object, with no temp name, no copy and no
// sweep, so that path has no remove. On macOS the rest of the keep is
// claustrum's own too.
//
// Measured on macOS: the copy after a failed checkout, the uncut copy of a
// tick and the miss rule of keepPass.
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
	// seedSpare is the least free space that a copy of the seed leaves on
	// the file system of the private store (seedNeeds). On an 8 MB file
	// system (Linux) and a small disk image (macOS) 4c111e86 copies no object,
	// and with 1100 MB (Linux) it does (measured). Its text names 1 GiB. The
	// keep tests the size plus this bound (keepHasSpace).
	seedSpare = 1 << 30
)

var (
	oidPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	staleTempName = regexp.MustCompile(`^\.claude-[0-9a-f]{8}-([0-9a-f]{64})$`)
)

// privateStore is the private LFS store of one checkout. path is <P>/lfs, or
// "" for the store of the git directory. seed runs the seed into path and
// reports a copy that it left out for the space, the time of its copies and a
// failed remove (seedObjects). runDir is the folder of the socket. tickWait, when set, gets the time of the
// wait for the end of the keepTicker of the checkout.
type privateStore struct {
	path     string
	runDir   string
	seed     func() (short bool, copied time.Duration, damaged error)
	tickWait *time.Duration
}

// storeUse is what a checkout that named its private store gives the reply of
// its failure (checkoutNote, storeFullStart, notKeptText). path is the store,
// <P>/lfs. short reports a copy of the seed left out for the space. copied is
// the time that the copies of the seed took.
type storeUse struct {
	runDir string
	path   string
	short  bool
	copied time.Duration
	// left is what the keep after a failed checkout left out
	// (returnDownloads), or nil when no such keep ran.
	left *leftOut
}

// leftOut counts the downloaded objects that a keep left out, and sums their
// sizes. An object that the store of the repository has, a seeded one too,
// does not count.
type leftOut struct {
	n    int
	size uint64
}

// seedContext is the bound of the seed of a create with timeoutMs. arrival is
// the start of the deadline of timeoutMs (worktreeCreateCtx). The seed starts
// now, and it stops at seedDeadline. On 4c111e86 a seed whose copy needs more
// time stops, the object stays out, and git-lfs then fails to find it
// (measured on Linux). With no timeoutMs the seed has only the bound of ctx.
func seedContext(ctx context.Context, timeoutMs int, arrival time.Time) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, seedDeadline(arrival, time.Now(), timeoutMs))
}

// seedDeadline is the end of a seed that starts at start, for a create whose
// deadline of timeoutMs counts from arrival. With s the time from arrival to
// start, the seed stops at (timeoutMs + s) / 2 after arrival. 4c111e86 cuts a
// seed there, and the size of the object plays no part (measured on Linux).
// macOS follows Linux (claustrum's own, not measured).
func seedDeadline(arrival, start time.Time, timeoutMs int) time.Time {
	return arrival.Add((time.Duration(timeoutMs)*time.Millisecond + start.Sub(arrival)) / 2)
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

// blobLister reads an `ls-tree --full-tree -r -l -z` output as it comes, as the stdout of
// the call. It keeps the ids of the blobs of at most pointerBlobMax bytes, each
// once, and gives each new id to found when found is set. Of each record it
// keeps only the text before the tab.
type blobLister struct {
	head   []byte
	inPath bool
	ids    []string
	seen   map[string]bool
	found  func(id string)
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
	if l.found != nil {
		l.found(f[2])
	}
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

// idLines is the stdin of `cat-file --batch`: each id on a line of its own. The
// ids come by add while ls-tree still runs, and close ends them. A read waits
// for the next id or the end. An add never waits, so the reader of ls-tree
// does not wait on cat-file.
type idLines struct {
	mu     sync.Mutex
	more   *sync.Cond
	ids    []string
	part   string
	closed bool
}

func newIDLines() *idLines {
	r := &idLines{}
	r.more = sync.NewCond(&r.mu)
	return r
}

// add puts id at the end of the lines.
func (r *idLines) add(id string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
	r.more.Signal()
}

// close ends the lines after the ids added so far.
func (r *idLines) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.more.Signal()
}

func (r *idLines) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.part == "" && len(r.ids) == 0 && !r.closed {
		r.more.Wait()
	}
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
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// seedScanContext is ctx with a stop of its own for the time limits of one call
// of the seed, as for a call of the content check (contentCheck.git).
func seedScanContext(ctx context.Context, u gitUse) context.Context {
	s := u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	return context.WithValue(ctx, gitStopKey{}, s)
}

// pointerScanGit runs one call of the seed in dir, under a limit of its own. The
// call pins the hook names of pre and runs no listing. With a nil pre its config
// listing runs first. The call ends at the deadline of ctx. Its stdout goes to
// stdout as it comes.
func pointerScanGit(ctx context.Context, u gitUse, dir string, pre *configListing, stdin io.Reader, stdout io.Writer, args ...string) error {
	ctx = seedScanContext(ctx, u)
	cmd := hardenedGitCmd(ctx, u, dir, false, pre, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	return gitRun(ctx, cmd)
}

// commitPointers are the pointers of the tree of rev, read in dir. It keeps no
// output of git but the ids of the small blobs and the pointers. The same
// listing runs twice before the ls-tree call, and both calls pin the hook names
// of the second one. The `cat-file --batch --buffer` call has no listing of its
// own. cat-file starts with ls-tree,
// before the first id, and gets each id as ls-tree lists it. Its input ends
// with the end of ls-tree. The argv of both calls, the listing and that start
// follow 4c111e86 (measured on Linux, a pointer at the top of the tree).
// Claustrum's own, not measured: a pointer in a subfolder, a tree with no small
// blob, the order of the two starts, and which listing gives the hook names.
func commitPointers(ctx context.Context, u gitUse, dir, rev string) []pointerRef {
	u = u.atCommand()
	hookPrecursor(seedScanContext(ctx, u), u, dir, false)
	pre := &configListing{hooks: hookPrecursor(seedScanContext(ctx, u), u, dir, false).hooks()}
	ids := newIDLines()
	l := blobLister{found: ids.add}
	listErr := make(chan error, 1)
	go func() {
		err := pointerScanGit(ctx, u, dir, pre, nil, &l, "-c", "core.commitGraph=false", "ls-tree", "--full-tree", "-r", "-l", "-z", rev)
		if err == nil {
			l.end()
		}
		ids.close()
		listErr <- err
	}()
	var b batchReader
	batchErr := pointerScanGit(ctx, u, dir, pre, ids, &b, "cat-file", "--batch", "--buffer")
	if <-listErr != nil || batchErr != nil || ctx.Err() != nil {
		return nil
	}
	return b.ps
}

// fillCheckoutStore puts into the private store private each object of the store
// of gitDir that a pointer of rev names: a regular file with the size of the
// pointer. It makes a hard link, or a copy when the link fails or a folder on
// the way is a link. A failure leaves the object out, and git-lfs then
// downloads it. A copy that has less than seedNeeds free is left out
// too, with no temp, and short reports it. The seed then ends. A free space that cannot be read
// does not stop a copy (claustrum's own, not measured). At the deadline of ctx
// the seed stops, and the objects that it did not reach stay out. copied is
// the time of its copies. damaged is the failed remove of a copy that stopped
// (seedObjects).
func fillCheckoutStore(ctx context.Context, u gitUse, dir, gitDir, rev, private string) (short bool, copied time.Duration, damaged error) {
	if ctx.Err() != nil {
		return false, 0, nil
	}
	store := filepath.Join(gitDir, "lfs")
	if fi, err := os.Stat(filepath.Join(store, "objects")); err != nil || !fi.IsDir() {
		return false, 0, nil
	}
	return seedObjects(ctx, store, private, commitPointers(ctx, u, dir, rev))
}

// seedObjects puts the objects of ps from the store store into private. short
// reports a copy left out for seedSpare, or a failed copy with less than the
// spare of seedNeeds free after it (seedRoomLow). copied is the sum of the
// times of the copies, a failed copy too. A hard link adds no time (claustrum's
// own, not measured). The time of a copy holds the sha256 of its bytes
// (copySeed).
//
// An object with a link count above 1 is read once in full before the link or
// the copy, so a copy reads it twice. Content that does not match its oid is
// neither linked nor copied. With link count 1 such content is linked
// (4c111e86, measured on Linux, the read also on macOS). Claustrum's own, not
// measured: the time of that read in copied, also before a link, and the
// content is tested against the oid (claustrum's own form).
//
// The first copy left out for seedSpare ends the seed, after the shard folder
// of its object. On 4c111e86 the second of two objects then got no shard
// folder (measured on macOS). Linux, a later object that a hard link can
// seed and a later copy that fits are claustrum's own, not measured.
//
// A copy whose file the remove left behind ends the seed, and damaged is that
// remove (seedNotRemoved). The create then fails before the read-tree call
// (seedDamagedText). Claustrum's own, not measured: the end of the seed and no
// read-tree call.
func seedObjects(ctx context.Context, store, private string, ps []pointerRef) (short bool, copied time.Duration, damaged error) {
	viaLink := pathIsLink(store) || pathIsLink(filepath.Join(store, "objects"))
	for _, p := range ps {
		if ctx.Err() != nil {
			return short, copied, nil
		}
		src := storeObjectPath(store, p.oid)
		fi, err := os.Lstat(src)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != p.size {
			continue
		}
		if hardLinks(fi) > 1 {
			start := time.Now()
			ok := objectHoldsOID(ctx, src, p.oid)
			copied += time.Since(start)
			if !ok {
				continue
			}
		}
		dst := storeObjectPath(private, p.oid)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			continue
		}
		shard := filepath.Dir(src)
		if viaLink || pathIsLink(shard) || pathIsLink(filepath.Dir(shard)) || seedLink(src, dst) != nil {
			if free, ok := storeFreeBytes(filepath.Dir(dst)); ok && free < seedNeeds(p.size) {
				return true, copied, nil
			}
			start := time.Now()
			err := copySeed(ctx, src, dst)
			copied += time.Since(start)
			var left seedNotRemoved
			if errors.As(err, &left) {
				return short, copied, left.err
			}
			if err != nil && seedRoomLow(filepath.Dir(dst), p.size) {
				short = true
			}
		}
	}
	return short, copied, nil
}

// seedNeeds is the free space that a copy of the seed of an object of size
// bytes needs: the size plus the larger of seedSpare and a tenth of the size.
// For a 200 MiB object the bound of 4c111e86 is near the size plus 1 GiB
// (measured on Linux, and on macOS at 200 and 800 MB). It refuses an 11 GiB
// object 1 MiB below the size plus a tenth (measured on Linux and macOS), and
// macOS copies it 1 MiB above. The bound at its exact byte is not measured.
func seedNeeds(size int64) uint64 {
	s := uint64(size)
	return s + max(seedSpare, s/10)
}

// seedRoomLow reports whether the file system of dir has less free space than
// the spare of seedNeeds for an object of size bytes. After a failed copy of
// the seed 4c111e86 reads the free space, and a full disk then gives the note
// of the space (measured on Linux). The spare of that test is claustrum's own,
// not measured. A free space that cannot be read gives false.
func seedRoomLow(dir string, size int64) bool {
	free, ok := storeFreeBytes(dir)
	return ok && free < seedNeeds(size)-uint64(size)
}

// The text of worktree_lfs_not_kept, around the words of the deadline
// (4c111e86, measured on Linux and macOS).
const (
	notKeptStart = "Turn off Worktree for this session, then try again. A plain retry won't help: git worktree add timed out after "
	notKeptEnd   = ", and copying the repository's Git LFS files into a folder of Claude's own took a large part of that time," +
		" as it does when Claude can't hard-link them there (a repository on another disk, a store reached through a link," +
		" a relative lfs.storage); a retry copies them again."
)

// notKeptText is the text of worktree_lfs_not_kept for a deadline during the
// checkout. 4c111e86 blames the copies of the seed when they took a large
// part of timeoutMs and git-lfs began no download. Then the private store
// has no entry `incomplete` (measured on
// Linux and macOS). The bound of claustrum is a quarter of timeoutMs
// (claustrum's own). An object with a second link adds a read of its sha256
// to the seed time (seedObjects). Whether the macOS boundary then matches
// 4c111e86 is not measured. An `incomplete` folder in the store of the repository
// does not count (measured on macOS). An empty `incomplete` folder counts
// (measured on Linux). Claustrum's own, not measured: any kind of entry there
// counts, and an Lstat that fails counts as no entry.
//
// gitDir is the git directory of the repository. A link at its `lfs` or at
// `lfs/objects` adds a note to that text (linkNote, measured on Linux).
//
// After Git LFS downloads began, a downloaded file that the keep cannot put in
// the store of the repository gives the text of a link (linkText) or of the
// room (notKeptRoomStart). Both are measured on Linux. Downloads began when
// the private store has an entry `incomplete`. A file that the
// keep left out (storeUse.left, else downloadsLeftOut) with no such entry keeps
// timeout (4c111e86, measured on Linux and macOS). The text of the room needs
// a file left out (claustrum's own). ok is false in every other case.
func notKeptText(timeoutMs int, rule *filterRule, gitDir string) (text string, ok bool) {
	if timeoutMs <= 0 || rule == nil || rule.store == nil || rule.store.path == "" {
		return "", false
	}
	s := rule.store
	words := deadlineWords(timeoutMs)
	_, err := os.Lstat(filepath.Join(s.path, "incomplete"))
	incomplete := err == nil
	name, link := linkedStoreFolder(gitDir)
	if 4*s.copied >= time.Duration(timeoutMs)*time.Millisecond && !incomplete {
		text = notKeptStart + words + notKeptEnd
		if link != "" {
			text += linkNote(name, link)
		}
		return text, true
	}
	if gitDir == "" || !incomplete {
		return "", false
	}
	if link != "" {
		return linkText(name, link, words), true
	}
	var left int
	var size uint64
	if s.left != nil {
		left, size = s.left.n, s.left.size
	} else {
		left, size = downloadsLeftOut(s.path, filepath.Join(gitDir, "lfs"))
	}
	if left > 0 && repoRoomShort(gitDir, size) {
		return notKeptRoomStart + words + notKeptRoomEnd, true
	}
	return "", false
}

// The texts of worktree_lfs_not_kept after Git LFS downloads began, around the
// words of the deadline and the path of a link (4c111e86, measured on Linux).
const (
	rmLinkWords      = " (exactly so, with no -r: that way rm removes a link but refuses a real folder)"
	notKeptRoomStart = "Free up space on the disk that holds the repository on the remote machine (or raise your quota there)" +
		" until the repository's Git LFS files fit with at least 1 GiB to spare, or turn off Worktree for this session," +
		" then try again. A plain retry won't help: git worktree add timed out after "
	notKeptRoomEnd = " once Git LFS downloads had begun, and there is too little room left on the disk that holds the repository," +
		" or in your disk quota there, so Claude can't keep the downloaded files for the next try."
)

// linkedStoreFolder names the folder of the store of the repository at gitDir
// that is a symbolic link, with its path: `lfs`, else `lfs/objects`. With no
// such link, or no gitDir, both are "". The order of the two tests is
// claustrum's own, not measured. The tests read by path, as the seed does.
func linkedStoreFolder(gitDir string) (name, path string) {
	if gitDir == "" {
		return "", ""
	}
	lfs := filepath.Join(gitDir, "lfs")
	if pathIsLink(lfs) {
		return "folder", lfs
	}
	if objects := filepath.Join(lfs, "objects"); pathIsLink(objects) {
		return "objects folder", objects
	}
	return "", ""
}

// linkText is the text of worktree_lfs_not_kept for a link at the folder name
// of the store of the repository, at path. The path is put in as it is, also
// with a quote mark in it (claustrum's own, not measured).
func linkText(name, path, words string) string {
	return "The repository's Git LFS " + name + " on the remote machine, `" + path + "`, is a symbolic link." +
		" If you didn't make that link, remove only the link, not the folder it points to: run rm '" + path + "'" +
		rmLinkWords + ", then try again. If you did make it, turn off Worktree for this session instead." +
		" A plain retry won't help: git worktree add timed out after " + words + " once Git LFS downloads had begun," +
		" and Claude can't keep the downloaded files in a linked folder for the next try."
}

// linkNote is the note after the text of a long copy of the seed for a link at
// the folder name of the store of the repository, at path.
func linkNote(name, path string) string {
	return " Also, the repository's Git LFS " + name + " on the remote machine, `" + path + "`, is a symbolic link." +
		" If you didn't make that link, remove just the link: run rm '" + path + "'" + rmLinkWords + "."
}

// downloadsLeftOut is the walk of the private store for a store whose keep
// did not report (storeUse.left). It counts the objects of the private store private that the
// store of the repository, repoStore, does not have after the keep, and sums
// their sizes. Such an object is one that git-lfs downloaded and the keep left
// out. A seeded object is in repoStore. The store of the repository is read by
// path, as the seed reads it.
func downloadsLeftOut(private, repoStore string) (n int, size uint64) {
	var l leftOut
	for _, oid := range downloadedOIDs(private) {
		l.add(private, repoStore, oid)
	}
	return l.n, l.size
}

// add counts the object oid of the private store private when the store of
// the repository, repoStore, does not have it. The test reads by path, as the
// seed reads that store.
func (l *leftOut) add(private, repoStore, oid string) {
	if _, err := os.Stat(storeObjectPath(repoStore, oid)); err == nil {
		return
	}
	l.n++
	if fi, err := os.Lstat(storeObjectPath(private, oid)); err == nil && fi.Size() > 0 {
		l.size += uint64(fi.Size())
	}
}

// repoRoomShort reports whether the disk that holds the git directory gitDir
// has less free space than size plus seedSpare, the bound that the text of the
// room names. On 4c111e86 a free space of 8, 30 and 400 MiB was too little for
// a 5 or 25 MiB object (measured on Linux). Claustrum's own, not measured: the
// exact bound, the sum of the sizes of all objects left out, and the read of the
// free space at `lfs/objects`, else at gitDir. A free space that cannot be read
// gives false. A quota is not read (claustrum's own).
func repoRoomShort(gitDir string, size uint64) bool {
	free, ok := storeFreeBytes(filepath.Join(gitDir, "lfs", "objects"))
	if !ok {
		free, ok = storeFreeBytes(gitDir)
	}
	return ok && free < size+seedSpare
}

// deadlineWords are the words of timeoutMs in the text of notKeptText. A whole
// number of minutes gives minutes, and every other value seconds, in the %v
// form of a float of Go: 60000 gives "1 minute", 120000 "2 minutes", 90000
// "90 seconds", 2389 "2.3890000000000002 seconds" (measured on Linux and
// macOS). The singular "1 second" for 1000 is not measured.
func deadlineWords(timeoutMs int) string {
	if timeoutMs%60000 == 0 {
		return plural(timeoutMs/60000, "minute")
	}
	secs := (time.Duration(timeoutMs) * time.Millisecond).Seconds()
	if secs == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%v seconds", secs)
}

// plural is n and word, with an s unless n is 1.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// pathIsLink reports whether the last name of p is a symbolic link.
func pathIsLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// seedLink and keepHardLink make the hard links of the seed and of the keep. A
// test replaces them to reach the copy. seedCopy is the copy of the seed, which
// a test replaces to fail it. seedHash is the hash of a copy of the seed, which
// a test replaces to slow it.
var (
	seedLink     = os.Link
	keepHardLink = linkIntoRoot
	seedCopy     = io.Copy
	seedHash     = func() io.Writer { return sha256.New() }
)

// storeFreeBytes is the free space of the file system of a folder, for the
// test of seedSpare. A test replaces it.
var storeFreeBytes = freeBytes

// keepBudget bounds the keep of one object: the reads of its bytes test
// (objectHoldsOID) and its copy (copyIntoRoot). After it the object is not
// kept. On 4c111e86 a 900 MiB keep stopped 3.0 s after the open of the object
// in five rows, with 1.75 to 2.03 s of that in the copy (measured on Linux).
// The exact bound and its start are claustrum's own, not measured.
// On macOS a tick has no such bound (keepTickRunsToExit), and after a failed
// checkout one keepBudget covers the pass (keepPassBound).
// newObjectHash is the hash of the bytes test. keepCopy is the copy of the
// keep. A test replaces all three.
var (
	keepBudget    = 3 * time.Second
	newObjectHash = sha256.New
	keepCopy      = io.Copy
)

// keepStopsAtMiss, keepSumsCopy, keepTickRunsToExit, keepFailedByCopy and
// keepPassBound are the rules of the keep on macOS. Linux keeps its older form,
// because no row measures these rules there. A test sets them.
//
// keepTickRunsToExit: keepBudget bounds neither the bytes test nor the copy of
// a tick. Both stop at the exit of git (keepTicker.halt). Of 85 copies that
// began before the last download, none was cut, and one ran 3.09 s. With an
// 800 MB object that came 4 s before a 2 MB object, 87375cc7 began its copy up
// to 6.8 s after the download, and never kept the 2 MB object alone (0 of 40).
// Claustrum's 3 s bound on the bytes test of a tick kept the 2 MB object alone
// in 7 of 40. In the traced runs each object got one bytes test of at most
// 3.0 s, with no cut and no retry. No bound on the bytes test is claustrum's own.
//
// keepFailedByCopy: after a checkout that failed by itself the keep copies to a
// temp name, as after a deadline, on one file system and on two (measured).
// Claustrum's own, not measured: no sweep after that copy, and other failures.
var (
	keepStopsAtMiss    = runtime.GOOS == "darwin"
	keepSumsCopy       = runtime.GOOS == "darwin"
	keepTickRunsToExit = runtime.GOOS == "darwin"
	keepFailedByCopy   = runtime.GOOS == "darwin"
	keepPassBound      = runtime.GOOS == "darwin"
)

// keepPass is the state of the keep of one create, shared by its ticks and its
// keep after the checkout. The ticks end before that keep starts. On macOS one
// pass walks the objects in oid order. A failed checkout with two objects in
// one pass followed the oid order and not the file names (3 of 3 runs on
// each build). Each object is tested against the free space at that moment,
// with no test of a sum. The first object that fails ends the keep for the
// rest of the request, also when the free space rises later. With no miss
// before it, an object first tested after a rise is kept (all measured on
// macOS).
//
// After a failed checkout one keepBudget covers the whole pass (keepPassBound).
// A second object gets no bound of its own. When the first object ended inside
// 3 s, the second got no temp file (87375cc7, measured on macOS with two
// objects of about 500 MB each or of about 650 MB each). The rows fit this
// shared bound. Claustrum's own, not measured: the same bound after a deadline,
// a stop during the copy of a later object, and a bound for each object after a
// good checkout.
type keepPass struct {
	ended bool
}

// markMiss ends the keep after an object that failed the test of the space.
func (p *keepPass) markMiss() {
	if p != nil && keepStopsAtMiss {
		p.ended = true
	}
}

// over reports whether a miss ended the keep.
func (p *keepPass) over() bool {
	return p != nil && p.ended
}

// keepForm is how the checkout before a keep ended.
type keepForm int

const (
	// keepAfterGood is a checkout that exited 0.
	keepAfterGood keepForm = iota
	// keepAfterDeadline is a checkout that the deadline of timeoutMs ended.
	// 4c111e86 keeps then by the copy of a good checkout (measured on Linux).
	keepAfterDeadline
	// keepAfterFailure is a checkout that failed by itself, or that another
	// stop ended (claustrum's own, not measured). Only Linux gives this form.
	// macOS keeps as after a deadline (keepFailedByCopy).
	keepAfterFailure
	// keepDuringCheckout is a keep while the read-tree call runs
	// (keepTicker). It copies as after a deadline, with no sweep (claustrum's
	// own, not measured).
	keepDuringCheckout
)

// copySeed copies the object src to dst, a new file. The copy writes dst at
// its own name, in 32 KiB chunks, through a handle of its folder
// (createSeedFile). When the copy stops at the deadline of ctx or a write or
// the close fails, the file is closed and removed at once by one unlinkat of
// that name through the same handle (removeLFSStoreEntry). 4c111e86 writes at the oid name and removes the file at the deadline
// (measured on Linux and macOS), and after a failed write (measured on Linux). The mode 0600 of the file is claustrum's own.
//
// The copy computes the sha256 of the bytes in the same pass, so its time is
// near the seed time of 4c111e86 in the rows (notKeptText).
// Claustrum's own, not measured: the result is not used.
//
// A remove that fails gives a seedNotRemoved error. On 4c111e86 a shard folder
// of mode 0555 failed it with EACCES (measured on Linux). An empty folder at
// the name kept the answer of a stopped copy, with no log line (measured on
// Linux). Claustrum's own, not measured: a gone file and any folder at the name
// count as removed.
func copySeed(ctx context.Context, src, dst string) error {
	in, err := openObjectNoFollow(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	dir, err := openRootNoWait(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(dst)
	out, err := createSeedFile(dir, name, 0o600)
	if err != nil {
		return err
	}
	_, err = seedCopy(io.MultiWriter(out, seedHash()), ctxReader{ctx, in})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// name is the file that this call just made, not a name from a read.
		rerr := removeLFSStoreEntry(dir, filepath.Dir(dst), name)
		if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			if fi, lerr := dir.Lstat(name); lerr != nil || !fi.IsDir() {
				return seedNotRemoved{&os.PathError{Op: "remove", Path: dst, Err: rerr}}
			}
		}
	}
	return err
}

// seedDamagedText is the text after "git worktree add failed (checkout): "
// when a remove of the seed failed (seedNotRemoved). folder is the folder of
// the daemon, <socket folder>/git-shadow/<name> (4c111e86, measured on Linux).
func seedDamagedText(folder string) string {
	return "Claude could not prepare a private Git LFS folder under " + folder + " on the remote machine" +
		" (a file Claude put there was replaced or damaged and could not be removed). Try again; if it keeps failing," +
		" check that folder and its disk."
}

// seedNotRemoved is the failed remove of the file of a copy of the seed that
// stopped or failed (copySeed).
type seedNotRemoved struct{ err *os.PathError }

func (e seedNotRemoved) Error() string { return e.err.Error() }

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
// own, not measured). form says how the checkout ended. The result counts
// the objects that the keep left out (leftOut), for notKeptText. Only an
// object that the keep did not put in the store is read again, by its path
// below u.gitDir, so a seeded object does not count.
func returnDownloads(u gitUse, private string, form keepForm) *leftOut {
	l := &leftOut{}
	repoStore := filepath.Join(u.gitDir, "lfs")
	a := u.accepted
	if a == nil || a.root == nil {
		for _, oid := range downloadedOIDs(private) {
			l.add(private, repoStore, oid)
		}
		return l
	}
	// Each budget has its own context, so a keep after a deadline of the
	// checkout still runs. After a failed checkout one bound covers the pass.
	pass := context.Background()
	if keepPassBound && form != keepAfterGood {
		bound, stop := context.WithTimeout(pass, keepBudget)
		defer stop()
		pass = bound
	}
	for _, oid := range downloadedOIDs(private) {
		if u.keeps.over() {
			l.add(private, repoStore, oid)
			continue
		}
		ctx, cancel := context.WithTimeout(pass, keepBudget)
		kept, short := returnDownload(ctx, a.root, a.path, private, oid, time.Now(), form)
		cancel()
		if short {
			u.keeps.markMiss()
		}
		if !kept {
			l.add(private, repoStore, oid)
		}
	}
	return l
}

// keepTickEvery is the time between two keeps of keepTicker. On 4c111e86 the
// passes came at 2 s, 4 s and 6 s of the checkout, and none at 1 s (measured on
// Linux and macOS). No pass at time zero is claustrum's own, not measured. A
// test shrinks it.
var keepTickEvery = 2 * time.Second

// newKeepTick gives the ticks of a keepTicker with the period d and their
// stop. Its first tick comes after d. A test replaces it.
var newKeepTick = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// keepTicker keeps the objects that git-lfs downloaded into a private store
// while the read-tree call runs. Each tick keeps each new object of the store
// once, as returnDownloads does, in the form keepDuringCheckout. An object
// that a tick did not keep waits for the keep after the checkout.
//
// halt stops a keep that still runs when the read-tree call has exited
// (stop). On 4c111e86 the read of a 900 MiB object that began during the
// checkout ended at the exit of git, with no copy. The keep after the
// checkout then read it again, and only it wrote a temp file (measured on
// Linux).
// On macOS a copy of a tick that still ran at an early exit of git left a temp
// file that went at once, in 7 runs of a failed download. Claustrum's own, not
// measured: that stop on Linux, the rest of the keep on macOS, and the ticks
// after a stop for the space, until git exits.
type keepTicker struct {
	quit  chan struct{}
	ended chan struct{}
	once  sync.Once
	// ctx is the parent of the context of each keep of a tick. stop ends it.
	ctx  context.Context
	stop context.CancelFunc
}

// keepTickerMade sees each new keepTicker. A test replaces it.
var keepTickerMade = func(*keepTicker) {}

// startKeepTicker starts the ticks for the private store private of the
// create u. A create that holds no git directory gets no tick.
func startKeepTicker(u gitUse, private string) *keepTicker {
	a := u.accepted
	if a == nil || a.root == nil {
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	k := &keepTicker{quit: make(chan struct{}), ended: make(chan struct{}), ctx: ctx, stop: stop}
	keepTickerMade(k)
	every := keepTickEvery
	go func() {
		defer close(k.ended)
		// A panic of a keep ends only the ticks, with a log line.
		defer func() {
			if r := recover(); r != nil {
				logging.Warnf("[GitHandler] the keep of Git LFS objects during the checkout stopped: %v", r)
			}
		}()
		tick, stopTick := newKeepTick(every)
		defer stopTick()
		tried := map[string]bool{}
		for {
			select {
			case <-k.quit:
				return
			case <-tick:
			}
			for _, oid := range downloadedOIDs(private) {
				if u.keeps.over() {
					return
				}
				if tried[oid] {
					continue
				}
				select {
				case <-k.quit:
					return
				default:
				}
				tried[oid] = true
				ctx, cancel := context.WithTimeout(k.ctx, keepBudget)
				if keepTickRunsToExit {
					cancel()
					ctx, cancel = context.WithCancel(k.ctx)
				}
				_, short := returnDownload(ctx, a.root, a.path, private, oid, time.Now(), keepDuringCheckout)
				cancel()
				if short {
					u.keeps.markMiss()
				}
			}
		}
	}()
	return k
}

// halt stops the ticks and the keep that runs, with no wait. A second call
// does nothing. A nil k does nothing.
func (k *keepTicker) halt() {
	if k == nil {
		return
	}
	k.once.Do(func() {
		close(k.quit)
		k.stop()
	})
}

// end halts k and waits for the keep that runs. A second call only waits. A
// nil k does nothing.
func (k *keepTicker) end() {
	if k == nil {
		return
	}
	k.halt()
	<-k.ended
}

// openShard opens the shard folder rel of root one folder at a time, each from
// the handle of its parent (shardStep). A missing folder gets mode, less the
// umask. The caller closes the handle. A folder that is a link, no folder or not
// the folder that its Lstat saw gives nil. The path of the git directory is not
// read again.
func openShard(root *os.Root, rel string, mode os.FileMode) *os.Root {
	cur := root
	for _, name := range strings.Split(filepath.ToSlash(rel), "/") {
		next := shardStep(cur, name, mode)
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
// missing, with mode. The folder must be a real folder by Lstat through
// parent, and the opened folder must be that folder.
func shardStep(parent *os.Root, name string, mode os.FileMode) *os.Root {
	fi, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		if parent.Mkdir(name, mode) != nil {
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
// folder that openShard refuses keeps nothing. A keep with too little space
// (keepHasSpace) leaves the shard folder that openShard made, with no object
// (measured on Linux and macOS).
//
// `lfs/objects` is made first. An object whose bytes do not give its oid then
// stops the keep, with no shard folder (objectHoldsOID). After a good checkout
// and after a deadline the keep copies the object to a temp name and links it
// at the name oid. The downloaded copy then turns into that object
// (swapInKept), and the temp name goes (measured on Linux). A failed or stopped
// copy keeps nothing. After a deadline the shard gets no sweep (claustrum's
// own, not measured). On Linux, after a checkout that failed by itself the keep
// is one hard link at the name oid (claustrum's own, not measured). The result is
// true when the store has the object at the end: it was there, or the keep
// put it there. short reports an object that failed keepHasSpace (keepPass).
func returnDownload(ctx context.Context, root *os.Root, dir, private, oid string, now time.Time, form keepForm) (kept, short bool) {
	// A missing `lfs` and `lfs/objects` get 0755 (claustrum's own, not
	// measured). The two shard levels get 0777 (measured on Linux).
	objects := openShard(root, filepath.Join("lfs", "objects"), 0o755)
	if objects == nil {
		return false, false
	}
	defer func() { _ = objects.Close() }()
	rel := filepath.Join(oid[:2], oid[2:4])
	if _, err := objects.Lstat(filepath.Join(rel, oid)); err == nil {
		return true, false
	}
	src := storeObjectPath(private, oid)
	if !objectHoldsOID(ctx, src, oid) {
		return false, false
	}
	shard := openShard(objects, rel, 0o777)
	if shard == nil {
		return false, false
	}
	defer func() { _ = shard.Close() }()
	if _, err := shard.Lstat(oid); err == nil {
		return true, false
	}
	shardPath := filepath.Join(dir, "lfs", "objects", rel)
	if !keepHasSpace(shardPath, src) {
		return false, true
	}
	if form == keepAfterFailure {
		return keepHardLink(shard, src, oid) == nil, false
	}
	var n [4]byte
	if _, err := rand.Read(n[:]); err != nil {
		return false, false
	}
	temp := ".claude-" + hex.EncodeToString(n[:]) + "-" + oid
	if !copyIntoRoot(ctx, shard, shardPath, src, temp, oid) {
		return false, false
	}
	err := shard.Link(temp, oid)
	if err == nil {
		swapInKept(shard, private, oid, temp)
	}
	// temp is the name that the keep built above, not a name from a read.
	_ = removeLFSStoreEntry(shard, shardPath, temp)
	if err == nil && form == keepAfterGood {
		sweepShardTemps(shard, shardPath, oid, now)
	}
	return err == nil, false
}

// objectHoldsOID reports whether the bytes of the regular file at path give
// the sha256 oid. An object whose bytes and size differ from its pointer gets
// no shard folder and no keep (4c111e86, measured on Linux and macOS). The
// test of the bytes, and not of the size, is claustrum's own, not measured.
// A read after the end of ctx fails the test, so the object is not kept.
func objectHoldsOID(ctx context.Context, path, oid string) bool {
	f, err := openObjectNoFollow(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := newObjectHash()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == oid
}

// keepHasSpace reports whether the file system of the shard folder shardPath
// has seedSpare free plus the size of the object src. 4c111e86 keeps no
// object below that bound, also when both stores share a file system
// (measured on Linux and macOS). The rows fit a test of the space after the checkout wrote its
// files, as here. That moment and the bound at its exact byte are inferred,
// not measured. The free space is read by the path, with no write. A size or a
// free space that cannot be read does not stop the keep (claustrum's own).
func keepHasSpace(shardPath, src string) bool {
	fi, err := os.Lstat(src)
	if err != nil {
		return true
	}
	free, ok := storeFreeBytes(shardPath)
	return !ok || free >= uint64(fi.Size())+seedSpare
}

// keptPrefix starts the name of the link that swapInKept puts beside the
// downloaded copy (measured on Linux).
const keptPrefix = ".kept-"

// swapInKept puts the object oid of the shard folder shard at the downloaded
// copy in the private store private. It links the name oid of shard at
// `.kept-<oid>` beside the copy, then renames that link over the copy
// (measured on Linux). Both steps act through a handle of the shard folder of
// private, which openShard opens one folder at a time. That folder is in the
// `.checkout-<n>` folder of the create. The rename needs the link to be the
// file temp of shard, the copy that the keep just made. A step that fails
// leaves the copy as it is (claustrum's own, not measured).
func swapInKept(shard *os.Root, private, oid, temp string) {
	src := storeObjectPath(private, oid)
	if homeguard.WipesHomeDir(src) {
		return
	}
	store, err := openRootNoWait(private)
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()
	dir := openShard(store, filepath.Join("objects", oid[:2], oid[2:4]), 0o755)
	if dir == nil {
		return
	}
	defer func() { _ = dir.Close() }()
	kept := keptPrefix + oid
	if linkAcrossRoots(shard, oid, dir, kept) != nil {
		return
	}
	made, err := shard.Lstat(temp)
	if err != nil {
		return
	}
	if got, err := dir.Lstat(kept); err != nil || !os.SameFile(made, got) {
		return
	}
	_ = dir.Rename(kept, oid)
}

// copyIntoRoot copies the file src to the new file name of root, and reports
// success. The file has mode 0666 before the umask, O_EXCL and O_NOFOLLOW, and
// gets the bytes in 32 KiB writes, with no fsync (measured on Linux). A write
// that fails or the end of ctx stops the copy. The file is then closed and
// removed at once (measured on Linux). On macOS the copy also computes the
// sha256 of the bytes that it reads, as the copy of the seed does. Bytes that
// do not give oid fail the copy, and the file goes by that same remove
// (claustrum's own form, not measured).
func copyIntoRoot(ctx context.Context, root *os.Root, rootPath, src, name, oid string) bool {
	in, err := openObjectNoFollow(src)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }()
	out, err := createSeedFile(root, name, 0o666)
	if err != nil {
		return false
	}
	var from io.Reader = ctxReader{ctx, in}
	var sum hash.Hash
	if keepSumsCopy {
		sum = newObjectHash()
		from = io.TeeReader(from, sum)
	}
	_, err = keepCopy(out, from)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum != nil && hex.EncodeToString(sum.Sum(nil)) != oid {
		err = errKeepBytes
	}
	if err != nil {
		// name is the temp name that the keep built, not a name from a read.
		_ = removeLFSStoreEntry(root, rootPath, name)
		return false
	}
	return true
}

// errKeepBytes is a keep copy whose bytes do not give the oid of the object.
var errKeepBytes = errors.New("the bytes of the copy do not match the oid")

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
		_ = removeLFSStoreEntry(shard, shardPath, e.Name())
	}
}

// removeLFSStoreFile is the one remove of the LFS store of a repository and
// of the object that a failed copy of the seed made: one unlinkat of a file
// (removeFileAt), so a folder at name stays. A test replaces it.
var removeLFSStoreFile = removeFileAt

// removeLFSStoreEntry removes name from the shard folder shard, at
// shardPath, or from the folder of a copy of the seed. homeguard.WipesHomeDir on the full path comes first, as for each
// delete in the folder git-shadow (removeEntry). The error is that of the
// remove, or nil when the guard refused it.
func removeLFSStoreEntry(shard *os.Root, shardPath, name string) error {
	if homeguard.WipesHomeDir(filepath.Join(shardPath, name)) {
		return nil
	}
	return removeLFSStoreFile(shard, name)
}
