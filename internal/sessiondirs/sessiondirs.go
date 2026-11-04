// Package sessiondirs keeps the session folders of the daemon: the folders that a
// Claude Code session works in. A git in such a folder does not run, and the folders
// are cut from the PATH of a git child.
//
// The record of the folders is a folder of files at <root>/session-folders, where
// <root> comes from the socket <root>/run/<client>/rpc.sock. Each file holds one
// folder. Every delete there is one plain os.Root.Remove of a file by its name,
// through a root at that one folder: the temp file of a failed write and the
// oldest records above the cap (prune). A name comes from this package or from a
// directory read of the folder, never from a request value.
package sessiondirs

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// RecordFolderName is the name of the record folder in <root>.
const RecordFolderName = "session-folders"

// recordCap is the count of record files that the folder keeps. After 2120
// spawns with other cwds the folder holds 2048 records (b3081b51, measured on
// Linux, macOS and Windows).
var recordCap = 2048

// recordNameRE matches the name of a record: the first 32 hex digits of the
// SHA-256 of the folder path (measured on Linux and macOS).
var recordNameRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// maxRecordSize bounds the read of one record at the start (claustrum's own).
const maxRecordSize = 64 << 10

// NoOtherGitError is the refusal of a git start when every git on PATH lies in a
// session folder. The text is that of b3081b51 (measured on Linux, macOS and
// Windows), with the install hint of the system.
type NoOtherGitError struct{}

func (NoOtherGitError) Error() string {
	return "For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as " + installHint
}

// SaveError is the refusal of a spawn whose new session folder cannot be saved in
// the record folder. The text is that of b3081b51 (measured on Linux, macOS and
// Windows).
// It names ~/.claude/remote also for a record folder outside home (claustrum's
// own, not measured).
type SaveError struct{}

func (SaveError) Error() string {
	return "Claude couldn't save its list of session folders to ~/.claude/remote on this host. Free up disk space or fix that folder's permissions"
}

// folder is one session folder: the path as recorded and its resolved path, or
// "" when the resolve failed. On macOS info is its stat, or nil when that failed.
type folder struct {
	path, real string
	info       os.FileInfo
}

type store struct {
	mu sync.Mutex
	// dir is the record folder, or "" when the folders are kept in memory only.
	dir     string
	folders []folder
	known   map[string]bool
	// saved holds the folders that have a record: read at the start or written
	// by this daemon.
	saved map[string]bool
	// passed holds the git paths that a line already named (claustrum's own).
	passed map[string]bool
}

// current is the store of the daemon. Open replaces it.
var current atomic.Pointer[store]

func init() { current.Store(newStore("")) }

// offMu guards offHolds, the count of running requests that carry the off switch.
var (
	offMu    sync.Mutex
	offHolds int
)

func newStore(dir string) *store {
	return &store{dir: dir, known: map[string]bool{}, saved: map[string]bool{}, passed: map[string]bool{}}
}

// RecordDirFor is the record folder of the socket sock, and false when sock is not
// <root>/run/<client>/rpc.sock. Any client name counts (claustrum's own, not
// measured).
func RecordDirFor(sock string) (string, bool) {
	if filepath.Base(sock) != "rpc.sock" {
		return "", false
	}
	client := filepath.Dir(sock)
	run := filepath.Dir(client)
	if filepath.Base(client) == "" || filepath.Base(run) != "run" {
		return "", false
	}
	return filepath.Join(filepath.Dir(run), RecordFolderName), true
}

// Open sets up the folders of a daemon on socket sock. It makes the record folder
// with mode 0700 and reads the records in it, so that the folders of an earlier
// daemon count (measured on Linux, macOS and Windows). b3081b51 makes the folder at
// the start (measured on Linux, macOS and Windows). For a socket of another layout
// it writes no record (measured on Linux, macOS and Windows), logs the line of
// b3081b51 (measured on Linux and macOS) and keeps the folders in memory only.
func Open(sock string) {
	dir, ok := RecordDirFor(sock)
	if !ok {
		logging.Infof("[daemon] socket %q is not <root>/run/<client>/rpc.sock: session folders are kept in memory only, so the next helper will not know them", filepath.ToSlash(sock))
		current.Store(newStore(""))
		return
	}
	s := newStore(dir)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		logging.Warnf("[sessiondirs] cannot make %q: %v", filepath.ToSlash(dir), err)
	}
	s.load()
	current.Store(s)
}

// openDir opens a root at the record folder. The folder must be a real folder
// and the one that the root holds (claustrum's own test): no write and no remove
// goes through a link at its path.
func (s *store) openDir() (*os.Root, error) {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	lfi, lerr := os.Lstat(s.dir)
	rfi, rerr := root.Stat(".")
	if lerr != nil || rerr != nil || !lfi.IsDir() || !os.SameFile(lfi, rfi) {
		_ = root.Close()
		return nil, &os.PathError{Op: "open", Path: s.dir, Err: errors.New("not a folder of its own")}
	}
	return root, nil
}

// load reads the records of the folder into memory. A file that is no regular
// file, has another name shape or holds no record is not read.
func (s *store) load() {
	root, err := s.openDir()
	if err != nil {
		return
	}
	defer root.Close()
	for _, name := range s.recordNames(root) {
		if _, p, ok := readRecord(root, name); ok {
			s.add(p)
			s.saved[p] = true
		}
	}
}

// recordNames lists the names of the record files of root.
func (s *store) recordNames(root *os.Root) []string {
	d, err := root.Open(".")
	if err != nil {
		return nil
	}
	entries, _ := d.ReadDir(-1)
	_ = d.Close()
	var names []string
	for _, e := range entries {
		if recordNameRE.MatchString(e.Name()) && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names
}

// readRecord reads the record name: the ms field of its first line `<ms> <dev>
// <inode>` and the folder path after that line.
func readRecord(root *os.Root, name string) (uint64, string, bool) {
	f, err := root.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return 0, "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRecordSize))
	if err != nil {
		return 0, "", false
	}
	head, p, ok := strings.Cut(string(b), "\n")
	fields := strings.Fields(head)
	if !ok || len(fields) != 3 || p == "" || !filepath.IsAbs(p) {
		return 0, "", false
	}
	var nums [3]uint64
	for i, f := range fields {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, "", false
		}
		nums[i] = n
	}
	return nums[0], p, true
}

// add puts p in memory, once.
func (s *store) add(p string) {
	if s.known[p] {
		return
	}
	s.known[p] = true
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		real = ""
	}
	f := folder{path: p, real: real}
	if matchByIdentity {
		f.info, _ = os.Stat(p)
	}
	s.folders = append(s.folders, f)
}

// Remember adds the session folders of one spawn. A new folder gets a record
// file (measured on Linux, macOS and Windows). A folder that has a record already
// gets no new one (claustrum's own, not measured). After a good write
// the oldest records above the cap go. A spawn that carries the off switch does not
// call it (process.spawn).
//
// A record that cannot be made or written returns SaveError when the first entry
// of the daemon PATH lies in that folder (b3081b51, measured on Linux). The spawn
// then starts no child (measured on Linux, macOS and Windows with a file at the
// record folder, and with a folder that denies the write). A later entry counts
// too, and a link entry or an entry in other letter case on macOS does not
// (claustrum's own, not measured). With no such entry the spawn starts (measured
// on Linux, and on macOS for an empty folder). The folder stays in memory,
// and a later spawn tries the write again (measured on Linux and macOS). A failed
// rename, as for a folder at the name of the record, returns nil (measured on
// Linux, macOS and Windows). The other folders of the spawn still get their
// records (claustrum's own, not measured).
func Remember(paths ...string) error {
	s := current.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	wrote, refused := false, false
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		s.add(abs)
		if s.saved[abs] || s.dir == "" {
			continue
		}
		if renamed, err := s.write(abs); err != nil {
			logging.Warnf("[sessiondirs] cannot write to the record of session folders in %q: %v; they are kept in memory until this helper ends, and written at a later start", filepath.ToSlash(s.dir), err)
			refused = refused || !renamed && pathEntryIn(abs)
			continue
		}
		s.saved[abs] = true
		wrote = true
	}
	if wrote {
		s.prune()
	}
	if refused {
		return SaveError{}
	}
	return nil
}

// pathEntryIn reports whether an absolute entry of the daemon PATH is the folder
// dir or lies below it, by text or by the resolved path of dir.
func pathEntryIn(dir string) bool {
	real, _ := filepath.EvalSymlinks(dir)
	for _, e := range filepath.SplitList(os.Getenv("PATH")) {
		if e == "" || !filepath.IsAbs(e) {
			continue
		}
		e = filepath.Clean(e)
		if under(e, dir) || real != "" && under(e, real) {
			return true
		}
	}
	return false
}

// RecordName is the name of the record of the folder p.
func RecordName(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:32]
}

// write writes the record of p: `<ms since epoch> <dev> <inode>\n<path>`, with no
// newline at the end, mode 0600 (measured on Linux and macOS). It goes to a new
// temp file `.new-<pid>-<ns>` first, and then by one rename to its name. A temp
// file whose step fails goes by one plain remove (measured on Linux). renamed is
// true when the step reached the rename.
func (s *store) write(p string) (renamed bool, err error) {
	root, err := s.openDir()
	if errors.Is(err, fs.ErrNotExist) {
		// A record folder that is gone is made again, with the mode of Open (b3081b51
		// makes it and writes the record, measured on Linux).
		if merr := os.Mkdir(s.dir, 0o700); merr == nil || errors.Is(merr, fs.ErrExist) {
			root, err = s.openDir()
		}
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	dev, ino := folderIdentity(p)
	body := fmt.Sprintf("%d %d %d\n%s", time.Now().UnixMilli(), dev, ino, p)
	tmp := fmt.Sprintf(".new-%d-%d", os.Getpid(), time.Now().UnixNano())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	_, werr := io.WriteString(f, body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	name := RecordName(p)
	renamed = werr == nil
	if werr == nil {
		// A folder at the name fails the step with "file exists", as on b3081b51
		// (measured on Linux, macOS and Windows). The test before the rename is
		// claustrum's own, so that the text is the same on every system.
		if fi, err := root.Lstat(name); err == nil && fi.IsDir() {
			werr = &os.LinkError{Op: "renameat", Old: tmp, New: name, Err: syscall.EEXIST}
		} else {
			werr = root.Rename(tmp, name)
		}
	}
	if werr != nil {
		_ = root.Remove(tmp)
	}
	return renamed, werr
}

// prune removes the oldest record files while the folder holds more than
// recordCap of them. A record of a folder that is gone stays (measured on Linux,
// macOS and Windows). Other files stay. Each record goes by one plain remove of
// its name. The order key is the ms field of the body, the lowest first (measured
// on Linux, macOS and Windows), then the name. A file that holds no record counts
// for no record and stays, and a failed remove logs one line (claustrum's own).
func (s *store) prune() {
	root, err := s.openDir()
	if err != nil {
		return
	}
	defer root.Close()
	type rec struct {
		name string
		ms   uint64
	}
	var recs []rec
	for _, name := range s.recordNames(root) {
		if ms, _, ok := readRecord(root, name); ok {
			recs = append(recs, rec{name, ms})
		}
	}
	if len(recs) <= recordCap {
		return
	}
	slices.SortFunc(recs, func(a, b rec) int {
		if c := cmp.Compare(a.ms, b.ms); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	for _, r := range recs[:len(recs)-recordCap] {
		if err := root.Remove(r.name); err != nil {
			logging.Warnf("[sessiondirs] cannot remove the old record %q: %v", r.name, err)
		}
	}
}

// holds reports whether the file p lies in a session folder, or is reached
// through one: by its path, by the resolved path of its folder, or by its own
// resolved path.
func (s *store) holds(p string) bool {
	p = filepath.Clean(p)
	views := []string{p}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		views = append(views, filepath.Join(r, filepath.Base(p)))
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		views = append(views, r)
	}
	for _, f := range s.folders {
		for _, v := range views {
			if under(v, f.path) || f.real != "" && under(v, f.real) {
				return true
			}
		}
	}
	return matchByIdentity && s.holdsByIdentity(views)
}

// holdsByIdentity reports whether a folder above one of views is a session folder
// by its device and inode (claustrum's own test). b3081b51 on macOS passes over a
// git whose PATH entry names a session folder in other letter case (measured on
// macOS).
func (s *store) holdsByIdentity(views []string) bool {
	for _, v := range views {
		for d := filepath.Dir(v); ; d = filepath.Dir(d) {
			if fi, err := os.Stat(d); err == nil {
				for _, f := range s.folders {
					if f.info != nil && os.SameFile(fi, f.info) {
						return true
					}
				}
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return false
}

// under reports whether p is dir or lies below it, by text.
func under(p, dir string) bool {
	if samePath(p, dir) {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return len(p) > len(dir) && samePath(p[:len(dir)], dir)
}

// on reports whether the check runs: no request that carries the off switch runs.
//
// The first check after the last such request ends logs the on-again line. In
// b3081b51 the line comes at the next request without the switch, and 3 s with no
// request write nothing. A request that came during the hold logs it at its first
// check after the hold, before its passing-over line (measured on Linux).
func on() bool {
	offMu.Lock()
	defer offMu.Unlock()
	if offHolds > 0 {
		return false
	}
	if offLogged {
		offLogged = false
		logging.Infof("[sessiondirs] the check of session folders is on again: no request that carries the app's off switch holds it any more")
	}
	return true
}

// PickGit makes cmd, a command of git by its bare name, skip a git in a session
// folder. b3081b51 runs the next git on PATH and logs each git that it passes
// over (measured on Linux, macOS and Windows). With no other git, cmd fails to
// start with NoOtherGitError and no process starts. While a request with the off
// switch runs, cmd stays as it is (measured). A lookup that failed stays as it is.
// A relative PATH entry after the passed git is not tried (claustrum's own). The
// line names each passed path once in the life of the daemon (claustrum's own, not
// measured).
func PickGit(cmd *exec.Cmd) {
	s := current.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on() || cmd.Err != nil || len(s.folders) == 0 || !s.holds(cmd.Path) {
		return
	}
	s.passOver(cmd.Path)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p, err := exec.LookPath(filepath.Join(dir, "git"))
		if err != nil {
			continue
		}
		if s.holds(p) {
			s.passOver(p)
			continue
		}
		cmd.Path = p
		return
	}
	cmd.Err = NoOtherGitError{}
}

func (s *store) passOver(p string) {
	if s.passed[p] {
		return
	}
	s.passed[p] = true
	logging.Infof("[sessiondirs] passing over %q: it lies in, or is reached through, a folder that Claude works in. Each file in %q ends in the path of one such folder; remove the file to have that folder forgotten", filepath.ToSlash(p), filepath.ToSlash(s.dir))
}

// WarnBareCommand logs the warning of b3081b51 for a bare command name that PATH
// found in a session folder (measured on Linux and macOS). The command still runs.
// Windows logs no such line (measured). A request with the off switch is not
// measured, and the line is not logged while the check is off.
func WarnBareCommand(name, path string) {
	s := current.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on() || !warnBare || len(s.folders) == 0 || !s.holds(path) {
		return
	}
	logging.Warnf("[sessiondirs] WARNING: starting %q from %q, which is in or reached through a folder that a Claude Code session can write. A sandboxed session could replace it with a program of its own, which would then run outside the sandbox. To prevent this, see that PATH on this host finds %q outside such folders, or, where the command is configured (an MCP server's settings, for one), give the full path of a copy outside them.", name, filepath.ToSlash(path), name)
}

// EditorNames are the eight names of the daemon environment that a git child
// does not get: a spawn of a bare git (measured on Linux, macOS and Windows) and
// each git call of git.status and git.info (measured on Linux, macOS and Windows).
// The other git calls of the git methods lose them too (claustrum's own, not
// measured). A name in the env of a spawn stays (measured).
var EditorNames = []string{
	"PAGER", "EDITOR", "VISUAL", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR",
	"GIT_EXTERNAL_DIFF", "GIT_PROXY_COMMAND",
}

// WithoutEditors is env without the entries of EditorNames, except the names that
// keep holds.
func WithoutEditors(env []string, keep map[string]string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		if _, given := keep[name]; given {
			return false
		}
		return envNameIn(name, EditorNames)
	})
}

// cutListNames are the list variables of a git child whose entries in a session
// folder go by their text. PATH is measured on Linux, macOS and Windows, and so is
// GIT_EXEC_PATH. LD_LIBRARY_PATH is measured on Linux, and the three DYLD_ names on
// macOS. claustrum cuts each name on every system (claustrum's own, not measured).
// A link to a session folder in
// PATH stays (measured on Windows, Linux and macOS not measured).
var cutListNames = []string{
	"PATH", "GIT_EXEC_PATH", "LD_LIBRARY_PATH",
	"DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_INSERT_LIBRARIES",
}

// cutProgramNames are the program variables of a git child that go when their
// value lies in a session folder by text (measured on Linux, macOS and Windows).
var cutProgramNames = []string{"GIT_SSH", "GIT_ASKPASS", "SSH_ASKPASS"}

// CutPath is env with the session folders cut out of the variables of a git
// child: the entries of cutListNames that lie in a session folder go, and a name
// with no entry left goes. The names of cutProgramNames go when their value lies
// in one. Other names, such as PYTHONPATH, PERL5LIB, MANPATH, PATHEXT and a list
// of the app, stay as given (measured). While a request with the off switch runs,
// env stays as given (measured on Linux, macOS and Windows). An env with nothing to
// cut is returned as it is.
func CutPath(env []string) []string {
	s := current.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on() || len(s.folders) == 0 {
		return env
	}
	var out []string
	cut := false
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		switch {
		case ok && envNameIn(name, cutListNames):
			list := filepath.SplitList(value)
			var kept []string
			for _, e := range list {
				if !s.holdsText(e) {
					kept = append(kept, e)
				}
			}
			if len(kept) != len(list) {
				cut = true
				if len(kept) > 0 {
					out = append(out, name+"="+strings.Join(kept, string(os.PathListSeparator)))
				}
				continue
			}
		case ok && envNameIn(name, cutProgramNames) && s.holdsText(value):
			cut = true
			continue
		}
		out = append(out, kv)
	}
	if !cut {
		return env
	}
	return out
}

// envNameIn reports whether name is one of names, by the name rule of the system.
func envNameIn(name string, names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return sameEnvName(name, n) })
}

// holdsText reports whether the PATH entry e lies in a session folder by text.
func (s *store) holdsText(e string) bool {
	if e == "" || !filepath.IsAbs(e) {
		return false
	}
	e = filepath.Clean(e)
	for _, f := range s.folders {
		if under(e, f.path) || f.real != "" && under(e, f.real) {
			return true
		}
	}
	return matchByIdentity && s.entryByIdentity(e)
}

// entryByIdentity reports whether the PATH entry e or a folder above it is a
// session folder by its device and inode, read with Lstat, so a link entry does
// not match. b3081b51 on macOS cuts an entry that names a session folder in other
// letter case (measured on macOS). The test by identity is claustrum's own.
func (s *store) entryByIdentity(e string) bool {
	for d := e; ; d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil {
			for _, f := range s.folders {
				if f.info != nil && os.SameFile(fi, f.info) {
					return true
				}
			}
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// TraceOff is env with the trace names of git set to 0 at its end, for a spawn of a
// bare git: GIT_TRACE2, GIT_TRACE2_EVENT and GIT_TRACE2_PERF. Other entries of
// these names go. b3081b51 gives a spawned bare git the three entries (measured on
// Linux, macOS and Windows), and none while a request with the off switch runs
// (measured on Linux and macOS). Their place and a value in the env of the spawn
// are not measured.
func TraceOff(env []string) []string {
	if !on() {
		return env
	}
	names := []string{"GIT_TRACE2", "GIT_TRACE2_EVENT", "GIT_TRACE2_PERF"}
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return envNameIn(name, names)
	})
	for _, n := range names {
		out = append(out, n+"=0")
	}
	return out
}

// OffSwitchSet reports whether params hold "sessionFoldersOff":true. Every other
// value counts as absent.
func OffSwitchSet(params json.RawMessage) bool {
	var p struct {
		Off rpc.JSONTrueSwitch `json:"sessionFoldersOff"`
	}
	return json.Unmarshal(params, &p) == nil && bool(p.Off)
}

// offLogged is true from the OFF line to the on-again line.
var offLogged bool

// PauseCheck turns the check off until release runs. The first request with the
// switch logs the OFF line. Later ones log nothing until the on-again line
// (measured on Linux).
func PauseCheck() (release func()) {
	offMu.Lock()
	offHolds++
	if !offLogged {
		offLogged = true
		logging.Infof("[sessiondirs] the check of session folders is OFF: the app sends its off switch. While a request that carries it runs, git is found by its bare name and its search lists are cut by text alone")
	}
	offMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			offMu.Lock()
			offHolds--
			offMu.Unlock()
		})
	}
}
