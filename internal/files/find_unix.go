//go:build unix

package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// findHandler is files.find: Linux and macOS serve it (4c111e86, measured).
var findHandler = filesFind

// findParams holds the members that files.find reads. An extra member is
// ignored, and a member of a wrong type gives -32602 (4c111e86, measured on
// Linux and macOS). ExpandPaths does nothing, so a "~" root is not absolute and
// gets -32602 (claustrum's own, not measured).
type findParams struct {
	Roots     []string `json:"roots"`
	Suffix    string   `json:"suffix"`
	Limit     int64    `json:"limit"`
	TimeoutMs int64    `json:"timeoutMs"`
}

func (p *findParams) ExpandPaths() {}

// The bounds of a walk. The default limit 5, the cap 20 and the depth 12 are
// measured on 4c111e86 (Linux and macOS). The 20 links and the 4 walks at once
// are measured on Linux only. The default 1.5 s for an absent or 0 timeoutMs
// and the cap 5 s are measured on Linux.
const (
	findMaxRoots       = 8
	findMaxParts       = 8
	findDefaultHits    = 5
	findMaxHits        = 20
	findMaxLinks       = 20
	findMaxDepth       = 12
	findMaxWalks       = 4
	findDefaultTimeout = 1500 * time.Millisecond
	findMaxTimeout     = 5 * time.Second
)

// findMaxEntries is the entry bound. A test lowers it. The rows put the bound
// of 4c111e86 between 49000 and 51000 entries (measured on Linux). The exact
// value 50000 is claustrum's own within that range.
var findMaxEntries = 50000

// findNow is the clock of the time bound. A test moves it.
var findNow = time.Now

// findSlots holds one token for each walk that runs.
var findSlots = make(chan struct{}, findMaxWalks)

// findPassNames are the folder names that a walk passes over (4c111e86,
// measured on Linux and macOS). A file with such a name is not passed over.
var findPassNames = map[string]bool{".git": true, ".hg": true, ".svn": true, "node_modules": true}

// filesFind answers files.find: a read-only walk for files whose path ends with
// suffix. It writes and deletes nothing (from the code). 4c111e86 logs nothing
// for it (measured on Linux).
func filesFind(req *rpc.Request) rpc.Response {
	var p findParams
	if bad := rpc.Bind(req, &p); bad != nil {
		return *bad
	}
	if findParamsRefused(&p) {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params")
	}
	return rpc.OKResult(req.ID, findRun(&p))
}

// findParamsRefused reports the shapes that get -32602 (4c111e86, measured on
// Linux and macOS): no root or more than 8, a root that is empty, relative, not
// clean or holds a NUL, a suffix that is empty, holds a NUL, starts or ends
// with "/", has an empty, "." or ".." part or more than 8 parts, and a negative
// limit or timeoutMs.
func findParamsRefused(p *findParams) bool {
	if len(p.Roots) == 0 || len(p.Roots) > findMaxRoots || p.Limit < 0 || p.TimeoutMs < 0 {
		return true
	}
	for _, r := range p.Roots {
		if !filepath.IsAbs(r) || strings.ContainsRune(r, 0) || filepath.Clean(r) != r {
			return true
		}
	}
	if p.Suffix == "" || strings.ContainsRune(p.Suffix, 0) {
		return true
	}
	parts := strings.Split(p.Suffix, "/")
	if len(parts) > findMaxParts {
		return true
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return false
}

// findNameCut reports a suffix that gets the cut "name" with no walk. The rows
// tested U+0001, TAB, LF, DEL, U+0085, U+200F, U+202E, U+2028, U+FFFD (also a
// lone surrogate), and a space or U+2800 at the start or end (4c111e86,
// measured on Linux and macOS). The other format characters, U+2029, the
// other blanks and a blank at the edge of an inner part are claustrum's own,
// not measured.
func findNameCut(suffix string) bool {
	for _, r := range suffix {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	for _, part := range strings.Split(suffix, "/") {
		first, _ := utf8.DecodeRuneInString(part)
		last, _ := utf8.DecodeLastRuneInString(part)
		if findBlank(first) || findBlank(last) {
			return true
		}
	}
	return false
}

func findBlank(r rune) bool { return unicode.IsSpace(r) || r == 0x2800 }

// findState is the state of one call. The walk runs in its own goroutine, and
// mu guards the members that the reply reads: cut, paths, passed, links and
// overflow. The walk alone touches the others.
type findState struct {
	tail       string // "/" + suffix
	limit      int
	deadline   time.Time
	now        func() time.Time
	readDir    func(*os.File, int) ([]os.DirEntry, error)
	maxEntries int
	entries    int
	stop       bool
	seen       map[string]bool

	mu       sync.Mutex
	cut      string
	paths    []string
	passed   []string
	links    []rpc.FindLink
	overflow bool // a link past the 20 met
}

// note keeps the first cut that the walk meets. The rows fit that order: a
// link that matches gives not_a_file, a walk of "/" that meets an unreadable
// folder before the entry bound gives unreadable (4c111e86, measured on Linux).
// The order of any other two cuts is claustrum's own, not measured.
func (w *findState) note(cut string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keep(cut)
}

// keep is note with mu held.
func (w *findState) keep(cut string) {
	if w.cut == "" {
		w.cut = cut
	}
}

// findRun runs the checks that need no walk, then walks each root in the order
// sent. The reply comes when the walk ends or when the time bound passes,
// whichever is first. A folder read that blocks past the bound gives the reply
// at the bound with the cut "time", and the walk keeps its slot until it ends
// (4c111e86, measured on Linux). The walk in its own goroutine is claustrum's
// own. It stops at its next entry after the bound.
func findRun(p *findParams) rpc.FindResult {
	w := &findState{tail: "/" + p.Suffix, seen: map[string]bool{},
		now: findNow, readDir: findReadDir, maxEntries: findMaxEntries}
	switch {
	case findNameCut(p.Suffix):
		w.cut = "name"
	case !findTakeSlot():
		w.cut = "busy"
	default:
		w.limit = int(min(p.Limit, findMaxHits))
		if w.limit == 0 {
			w.limit = findDefaultHits
		}
		// The bound is clamped in milliseconds first, so a huge timeoutMs
		// cannot wrap the Duration.
		timeout := findDefaultTimeout
		if p.TimeoutMs > 0 {
			timeout = time.Duration(min(p.TimeoutMs, findMaxTimeout.Milliseconds())) * time.Millisecond
		}
		w.deadline = w.now().Add(timeout)
		done := make(chan struct{})
		go func() {
			// The slot goes back before done closes, so a reply that waits
			// for the walk comes after its slot is free.
			defer close(done)
			defer func() { <-findSlots }()
			defer w.recovered()
			for _, root := range p.Roots {
				if w.stop {
					break
				}
				w.walkOne(root)
			}
		}()
		bound := time.NewTimer(timeout)
		defer bound.Stop()
		select {
		case <-done:
		case <-bound.C:
			return w.result(true)
		}
	}
	return w.result(false)
}

// recovered ends a walk that panicked, so the panic does not end the daemon. It
// logs one line, and the reply keeps what the walk found. With no cut yet, the
// cut is "time" if the bound passed, else "unreadable". The text and the cut are
// claustrum's own, not measured.
func (w *findState) recovered() {
	v := recover()
	if v == nil {
		return
	}
	logging.Errorf("[Files] find: recovered panic in the walk: %v", v)
	if w.now().After(w.deadline) {
		w.note("time")
	} else {
		w.note("unreadable")
	}
}

// result builds the reply under mu. A late reply keeps the first cut that the
// walk met, else it gives "time" (claustrum's own, not measured).
func (w *findState) result(late bool) rpc.FindResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	if late {
		w.keep("time")
	}
	w.noteLinks()
	return w.reply()
}

// noteLinks gives the cut of the links after the walk. More than 20 links give
// "many_links", and 1 to 20 give "links" (4c111e86, measured on Linux). Either
// comes only if the walk met no other cut. The rows fit that order: 300
// matching links give not_a_file (measured on Linux). The order of these two
// words against the other cuts is claustrum's own, not measured.
func (w *findState) noteLinks() {
	switch {
	case w.overflow:
		w.keep("many_links")
	case len(w.links) > 0:
		w.keep("links")
	}
}

// findTakeSlot takes a slot of the walks at once with no wait. A call that finds
// none free gets the cut "busy" (4c111e86, measured on Linux: 20 of 24 calls at
// once).
func findTakeSlot() bool {
	select {
	case findSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

// walkOne opens one root and walks it. A root that is missing, no folder, or
// whose open handle has another real path (a link at or above it) gets the cut
// "root", and the walk goes on with the next root (4c111e86, measured on Linux
// and macOS). A root that fails to open for another reason gets "root" too
// (claustrum's own, not measured).
func (w *findState) walkOne(root string) {
	if cut := findRootPreCut(root); cut != "" {
		w.note(cut)
		return
	}
	fd, err := openMkdirParent(root)
	if err != nil {
		w.note("root")
		return
	}
	f := os.NewFile(uintptr(fd), root)
	defer f.Close()
	if real, err := openParentPath(fd); err != nil || real != root {
		w.note("root")
		return
	}
	w.readFolder(f, fd, root, root, 0)
}

// findReadDir reads the next entries of a folder. A test swaps it.
var findReadDir = (*os.File).ReadDir

// readFolder reads one folder in the order of the file system, depth first. A
// depth-first walk is inferred from the rows of 4c111e86 (Linux and macOS). If
// the entries of the folder change while it is read, 4c111e86 gives the cut
// "changed" (measured on Linux). The test of the stamp of the open folder
// before and after the read is claustrum's own. So is the walk that goes on.
func (w *findState) readFolder(f *os.File, fd int, dir, root string, depth int) {
	before, stamped := findStamp(fd)
	for !w.stop {
		ents, err := w.readDir(f, 256)
		for _, e := range ents {
			if w.stop {
				return
			}
			w.entry(e, fd, dir, root, depth)
		}
		if errors.Is(err, io.EOF) {
			if after, ok := findStamp(fd); stamped && ok && after != before {
				w.note("changed")
			}
			return
		}
		if err != nil {
			w.note("unreadable")
			return
		}
	}
}

// entry takes one entry of a folder.
func (w *findState) entry(e fs.DirEntry, fd int, dir, root string, depth int) {
	w.entries++
	if w.entries > w.maxEntries {
		w.note("entries")
		w.stop = true
		return
	}
	if w.now().After(w.deadline) {
		w.note("time")
		w.stop = true
		return
	}
	name := e.Name()
	full := findJoin(dir, name)
	match := strings.HasSuffix(full, w.tail)
	typ := e.Type()
	switch {
	case typ.IsRegular():
		if match {
			w.hit(full)
		}
	case typ&fs.ModeSymlink != 0:
		// A link is never followed. A link that matches gives not_a_file
		// (4c111e86, measured on Linux and macOS).
		w.link(fd, dir, name, full, root)
		if match {
			w.note("not_a_file")
		}
	case typ.IsDir():
		if match {
			w.note("not_a_file")
		}
		w.dir(fd, name, full, root, depth+1)
	default:
		if match {
			w.note("not_a_file")
		}
	}
}

// hit adds a matching file. A hit past the limit gives the cut "count" and ends
// the walk, so a walk that finds exactly the limit stays whole (claustrum's own,
// not measured). A path met twice counts once (4c111e86, measured on Linux and
// macOS).
func (w *findState) hit(full string) {
	if w.seen[full] {
		return
	}
	if len(w.paths) >= w.limit {
		w.note("count")
		w.stop = true
		return
	}
	w.seen[full] = true
	w.mu.Lock()
	w.paths = append(w.paths, full)
	w.mu.Unlock()
}

// dir passes over a folder, or walks into it. A folder below the depth bound is
// not read and gives the cut "depth". A folder that does not open gives
// "unreadable" (4c111e86, measured on Linux and macOS). The pass test comes
// before the depth test (claustrum's own, not measured).
func (w *findState) dir(fd int, name, full, root string, depth int) {
	if findPassOver(fd, name) {
		if !w.seen[full] {
			w.seen[full] = true
			w.mu.Lock()
			w.passed = append(w.passed, full)
			w.mu.Unlock()
		}
		return
	}
	if depth > findMaxDepth {
		w.note("depth")
		return
	}
	sub, err := findOpenSub(fd, name)
	if err != nil {
		w.note("unreadable")
		return
	}
	f := os.NewFile(uintptr(sub), full)
	defer f.Close()
	w.readFolder(f, sub, full, root, depth)
}

// findPassOver reports a folder that the walk passes over: a name of
// findPassNames, or a folder that holds a regular file pyvenv.cfg (4c111e86,
// measured on Linux and macOS). The pyvenv.cfg test follows no link at its last
// name (claustrum's own, not measured).
func findPassOver(fd int, name string) bool {
	if findPassNames[name] {
		return true
	}
	var st unix.Stat_t
	err := unix.Fstatat(fd, name+"/pyvenv.cfg", &st, unix.AT_SYMLINK_NOFOLLOW)
	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFREG
}

// findStamp reads the modify time and the status change time of the open
// folder fd.
func findStamp(fd int) ([2]unix.Timespec, bool) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return [2]unix.Timespec{}, false
	}
	return [2]unix.Timespec{st.Mtim, st.Ctim}, true
}

// findOpenSub opens a folder below fd with no link followed.
func findOpenSub(fd int, name string) (int, error) {
	for {
		sub, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			return sub, err
		}
	}
}

// link adds a link to the list, up to 20 (4c111e86, measured on Linux). A
// relative target is joined to the folder of the link. If that path stays in
// the root and is a folder, the link leads to "folder". If it is a regular
// file, the link is left out. Every other link leads to "other": an absolute
// target, a missing target and a target whose last name is a link (4c111e86,
// measured on Linux, an absolute target on macOS too). The in-root test, a
// relative target out of the root and a link that does not read are claustrum's
// own, not measured. A link past the 20 sets the overflow, and the walk goes on
// (measured on Linux). A link that is left out does not count toward the 20
// (claustrum's own, not measured).
func (w *findState) link(fd int, dir, name, full, root string) {
	if w.seen[full] {
		return
	}
	leadsTo := "other"
	if target, err := findReadlink(fd, name); err == nil && !filepath.IsAbs(target) {
		at := filepath.Join(dir, target)
		var st unix.Stat_t
		if findInRoot(at, root) && unix.Lstat(at, &st) == nil {
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				leadsTo = "folder"
			case unix.S_IFREG:
				return
			}
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.links) >= findMaxLinks {
		w.overflow = true
		return
	}
	w.seen[full] = true
	w.links = append(w.links, rpc.FindLink{Path: full, LeadsTo: leadsTo})
}

// findReadlink reads the target of the link name in the folder fd.
func findReadlink(fd int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(fd, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// findInRoot reports a clean absolute path that is root or below it.
func findInRoot(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// findJoin joins a folder and a name. The root "/" gives "/name".
func findJoin(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// reply sorts each list by the number of path parts, then by bytes (4c111e86,
// measured on Linux and macOS). The order of the walk sets which entries a
// bound keeps. The caller holds mu.
func (w *findState) reply() rpc.FindResult {
	res := rpc.FindResult{
		Paths:      findSorted(w.paths),
		Whole:      w.cut == "",
		Cut:        w.cut,
		PassedOver: findSorted(w.passed),
		Links:      append([]rpc.FindLink{}, w.links...),
	}
	sort.SliceStable(res.Links, func(i, j int) bool { return findLess(res.Links[i].Path, res.Links[j].Path) })
	return res
}

func findSorted(s []string) []string {
	out := append([]string{}, s...)
	sort.SliceStable(out, func(i, j int) bool { return findLess(out[i], out[j]) })
	return out
}

func findLess(a, b string) bool {
	if na, nb := strings.Count(a, "/"), strings.Count(b, "/"); na != nb {
		return na < nb
	}
	return a < b
}

// findDataVolume is the folder of the macOS data volume. Its firmlinks show the
// same folders under a second path.
const findDataVolume = "/System/Volumes/Data"

// findMacRootCut gives the cut of a macOS root before it opens. A root below
// the data volume gets "root". The data volume, home and each folder above home
// get "home" (4c111e86, measured on macOS: home, its parent, "/", /Users,
// /private, /private/tmp). holdsHome is the home test.
func findMacRootCut(root string, holdsHome func(string) bool) string {
	if strings.HasPrefix(root, findDataVolume+"/") {
		return "root"
	}
	if root == findDataVolume || holdsHome(root) {
		return "home"
	}
	return ""
}
