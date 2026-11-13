package main

// The private temp folder of a spawn with "cliTempRoot":true (b3081b51, measured on
// Linux). Claude Code keeps its own temp files in <temp dir>/claude-<uid>. When that
// entry is unfit, the daemon makes a folder ccd-<10 hex> beside it, with an inner
// folder t, and gives the child <temp dir>/ccd-<10 hex>/t in CLAUDE_CODE_TMPDIR. The
// reply names the kind and the folder (4c111e86, measured on Linux).
//
// The removes of a folder here are two plain rmdirs after a failed test of the new
// folder (removeMadeTempRoot): the inner folder t through the handle of the new
// folder, then the name that this call made through a handle of the temp dir. They
// never delete a tree, another name or a moved folder. The other remove is one
// plain remove of the registry name in the run dir before it is written again.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The texts of the reply (measured on Linux).
const (
	tempRootNoOpen  = "the temp dir or a folder above it could not be opened for reading"
	tempRootOther   = "the temp dir or a folder above it belongs to, or can be written by, another account"
	tempRootTooMany = "too many sessions of yours on this host have a private temp folder of their own; end one, or try again in a minute"
	tempRootRemoved = "the new folder was not this account's own with mode 0700, and was removed"
	tempRootNoReply = "the temp dir did not answer in time"
	// tempRootSteppedAround answers a link or a file at the entry (4c111e86, measured
	// on Linux and macOS).
	tempRootSteppedAround = "only a folder of root's or of this account's own that others cannot write is stepped around: another account may have laid this"
)

// tempRootChildDir is the name of the inner folder that the child gets (4c111e86,
// measured on Linux and macOS).
const tempRootChildDir = "t"

// tempRootLimit is the time that the step gets for its answer. After it the reply
// says tempRootNoReply and the child gets the env as it was. On a stuck NFS mount
// b3081b51 answered after about 2.1 s (measured on Linux). The bound of 2 s is
// claustrum's own. A variable for the tests.
var tempRootLimit = 2 * time.Second

// tempRootLiveCap is the count of held folders. A spawn that needs one more folder
// gets tempRootTooMany (c9125eb8 and 4c111e86, measured on macOS). Spawns that share
// a held folder take no new place (measured on Linux). The count is of this daemon,
// and a folder gives its place back when its last spawn gives it back, or, while a
// step or a refresh holds the lock, once it ends (claustrum's own, not measured).
const tempRootLiveCap = 8

// tempRootMaxAge is the age of a folder that a later daemon no longer takes. A
// folder of 25 h gets a new name (measured on Linux). The bound of 24 h is
// claustrum's own, not measured. The daemon itself keeps a folder that it holds.
const tempRootMaxAge = 24 * time.Hour

// tempRootRefresh is the time between two refreshes of a held folder (measured on
// Linux: 600 s from the first hold). A variable for the tests.
var tempRootRefresh = 600 * time.Second

// tempRootNameRE is the shape of a folder name of this step: 10 hex (4c111e86,
// measured on Linux and macOS). They come from crypto/rand (claustrum's own, not
// measured).
var tempRootNameRE = regexp.MustCompile(`^ccd-[0-9a-f]{10}$`)

// Seams for the tests. Production never reassigns them.
var (
	tempRootUID             = os.Getuid
	newTempRootName         = randomTempRootName
	afterTempRootMkdir      = func(dir, name string) {}
	afterTempRootChildMkdir = func(dir, name string) {}
)

func randomTempRootName() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "ccd-" + hex.EncodeToString(b)
}

// heldTempRoot is one folder that this daemon holds: an open handle with an
// exclusive flock, refreshed every tempRootRefresh while a spawn uses it.
type heldTempRoot struct {
	f     *os.File
	dev   uint64
	ino   uint64
	users int
	stop  chan struct{}
}

var tempRoots = struct {
	mu   sync.Mutex
	held map[string]*heldTempRoot // by the path that the child gets
	live int
}{held: map[string]*heldTempRoot{}}

// tempRootLate decides between the answer of the step and the time limit. A file
// call that a stuck mount holds cannot be stopped, so the step goes on after the
// limit. The step keeps its folder only if keep comes before giveUp.
type tempRootLate struct {
	mu           sync.Mutex
	gaveUp, kept bool
}

func (l *tempRootLate) keep() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kept = !l.gaveUp
	return l.kept
}

func (l *tempRootLate) giveUp() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gaveUp = !l.kept
	return l.gaveUp
}

// privateTempRoot runs the step for the env of one child. It returns the env, the
// member of the reply (nil for none) and the function that gives the folder back.
// The step runs in its own goroutine on a copy of the env. After tempRootLimit the
// spawn goes on with the env as it was. A folder that a late step made stays, with
// no registry write and no hold (b3081b51, measured on Linux). A late step closes
// a folder that it took (claustrum's own, not measured).
func privateTempRoot(env []string, runDir string) ([]string, *rpc.CLITempRoot, func()) {
	type answer struct {
		env     []string
		res     *rpc.CLITempRoot
		release func()
	}
	late := &tempRootLate{}
	done := make(chan answer, 1)
	in := slices.Clone(env)
	go func() {
		e, r, rel := tempRootStep(in, runDir, late)
		done <- answer{e, r, rel}
	}()
	t := time.NewTimer(tempRootLimit)
	defer t.Stop()
	select {
	case a := <-done:
		return a.env, a.res, a.release
	case <-t.C:
	}
	if late.giveUp() {
		return env, &rpc.CLITempRoot{WhyNot: tempRootNoReply}, func() {}
	}
	a := <-done // the step kept its folder right before the limit
	return a.env, a.res, a.release
}

// childTempDir is the temp dir of the child: the first value that is not empty of
// CLAUDE_CODE_TMPDIR, TMPDIR, TMP and TEMP, or /tmp. Measured on Linux:
// CLAUDE_CODE_TMPDIR comes before TMPDIR, an empty TMPDIR goes on to TMP, and TMP
// or TEMP alone is taken. The rest of the order and /tmp are claustrum's own, not
// measured.
func childTempDir(env []string) string {
	for _, k := range []string{"CLAUDE_CODE_TMPDIR", "TMPDIR", "TMP", "TEMP"} {
		if v := tempEnvValue(env, k); v != "" {
			return v
		}
	}
	return "/tmp"
}

func tempRootStep(env []string, runDir string, late *tempRootLate) ([]string, *rpc.CLITempRoot, func()) {
	none := func() {}
	dir := childTempDir(env)
	if !filepath.IsAbs(dir) {
		return env, nil, none // measured on Linux for TMPDIR
	}
	uid := tempRootUID()
	// A temp dir that does not exist gives no member (measured on Linux). Another
	// failed open gives the text of a folder that cannot be opened (claustrum's own,
	// not measured).
	x, otherAccount, missing, ok := openTempDirChain(dir, uid)
	if missing {
		return env, nil, none
	}
	if !ok {
		return env, &rpc.CLITempRoot{WhyNot: tempRootNoOpen}, none
	}
	defer func() { _ = x.Close() }()
	xfd := int(x.Fd())
	var xst unix.Stat_t
	if err := unix.Fstat(xfd, &xst); err != nil {
		return env, &rpc.CLITempRoot{WhyNot: tempRootNoOpen}, none
	}
	var est unix.Stat_t
	if err := unix.Fstatat(xfd, "claude-"+strconv.Itoa(uid), &est, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return env, nil, none // a missing entry, or one that cannot be read (claustrum's own)
	}
	kind := tempEntryUnfit(&est, xst.Dev, uid)
	if kind == "" {
		return env, nil, none
	}
	refuse := func(why string) ([]string, *rpc.CLITempRoot, func()) {
		logging.Infof("[process.Manager] Claude Code's temp folder is unfit (it is %s), but no other was made: %s", kind, why)
		return env, &rpc.CLITempRoot{Refused: kind, WhyNot: why}, none
	}
	if otherAccount {
		return refuse(tempRootOther)
	}
	// A link or a file gets no folder (4c111e86, measured on Linux and macOS). The
	// place after the account test is claustrum's own, not measured.
	if kind == "a link" || kind == "not a folder" {
		return refuse(tempRootSteppedAround)
	}
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	canAdd := len(tempRoots.held) < tempRootLiveCap
	var root *os.Root
	if runDir != "" {
		if r, err := os.OpenRoot(runDir); err == nil {
			root = r
			defer func() { _ = r.Close() }()
		}
	}
	reg := tempRootRegistryName(dir)
	h, name, fresh := reuseTempRoot(xfd, dir, root, reg, uid, canAdd)
	made := false
	if h == nil {
		if !canAdd {
			return refuse(tempRootTooMany)
		}
		var removed bool
		h, name, removed = makeTempRoot(xfd, dir, uid)
		if removed {
			return refuse(tempRootRemoved)
		}
		if h == nil {
			return env, nil, none // the mkdir failed (claustrum's own, not measured)
		}
		holdTempRoot(h.f)
		fresh, made = true, true
	}
	if !late.keep() {
		// The limit came first. The child got the env as it was.
		if fresh {
			_ = h.f.Close()
		}
		return env, nil, none
	}
	if made && root != nil {
		writeTempRootRegistry(root, reg, name)
	}
	if fresh {
		tempRoots.held[filepath.Join(dir, name)] = h
		go refreshTempRoot(h, tempRootRefresh)
	}
	h.users++
	tempRoots.live++
	logging.Infof("[process.Manager] Claude Code's temp folder is unfit (it is %s): Claude Code gets %s/%s beside it", kind, name, tempRootChildDir)
	env = sysproc.ReplaceOrAppendEnv(env, "CLAUDE_CODE_TMPDIR", filepath.Join(dir, name, tempRootChildDir))
	giveBack := func() { // the caller holds tempRoots.mu
		defer tempRoots.mu.Unlock()
		tempRoots.live--
		h.users--
		if h.users == 0 {
			close(h.stop)
			_ = h.f.Close()
			if tempRoots.held[filepath.Join(dir, name)] == h {
				delete(tempRoots.held, filepath.Join(dir, name))
			}
		}
	}
	// The release runs before the exit of the child ends a killAndWait wait. A step
	// or a refresh can hold tempRoots.mu across a file call that a stuck mount does
	// not answer. The release then gives the folder back in a goroutine once the lock
	// is free, so the wait ends at the exit (b3081b51 answers died:true there,
	// measured on Linux).
	var once sync.Once
	release := func() {
		once.Do(func() {
			if tempRoots.mu.TryLock() {
				giveBack()
				return
			}
			go func() {
				tempRoots.mu.Lock()
				giveBack()
			}()
		})
	}
	return env, &rpc.CLITempRoot{Refused: kind, Name: name}, release
}

func tempEnvValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// openTempDirChain opens the temp dir and each folder above it for reading. ok is
// false when one of them cannot be opened. missing is true when the links of the
// path do not resolve because a name does not exist. otherAccount is true when one of them
// belongs to an account other than uid and root, or when group or others can
// write it without the sticky bit. Measured on Linux: a 0777 temp dir is refused
// and /tmp passes. The group bit, a sticky temp dir and the links of the path,
// which are resolved first, are claustrum's own, not measured. The caller closes x.
func openTempDirChain(dir string, uid int) (x *os.File, otherAccount, missing, ok bool) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, false, errors.Is(err, os.ErrNotExist), false
	}
	p := "/"
	rest := strings.Split(strings.TrimPrefix(filepath.Clean(real), "/"), "/")
	for i := 0; ; i++ {
		f, err := os.Open(p)
		if err != nil {
			return nil, false, false, false
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = f.Close()
			return nil, false, false, false
		}
		if (int(st.Uid) != uid && st.Uid != 0) || (st.Mode&0o022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			otherAccount = true
		}
		if i == len(rest) || rest[0] == "" {
			return f, otherAccount, false, true
		}
		_ = f.Close()
		p = filepath.Join(p, rest[i])
	}
}

// tempEntryUnfit names the kind of an unfit claude-<uid> entry, or "" for an own
// folder that its owner can read on the file system of the temp dir (measured on
// Linux). The order of the tests is claustrum's own, not measured.
func tempEntryUnfit(st *unix.Stat_t, dev uint64, uid int) string {
	switch {
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return "a link"
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return "not a folder"
	case int(st.Uid) != uid:
		return "another account's"
	case st.Dev != dev:
		return "on another file system"
	case st.Mode&0o400 == 0:
		return "not readable by its owner"
	}
	return ""
}

// tempRootRegistryName is the name of the registry file in the run dir: the first 8
// bytes of the SHA-256 of the boot id, "/", the pid namespace, a NUL and the temp
// dir as given (measured on Linux). A part that cannot be read is empty
// (claustrum's own, not measured).
func tempRootRegistryName(dir string) string {
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	ns, _ := os.Readlink("/proc/self/ns/pid")
	sum := sha256.Sum256([]byte(strings.TrimRight(string(boot), "\n") + "/" + ns + "\x00" + dir))
	return "cli-temp-root-" + hex.EncodeToString(sum[:8])
}

// reuseTempRoot returns the folder that the registry names when the daemon can take
// it: one that this daemon holds already, or an own folder younger than
// tempRootMaxAge that no other process holds (measured on Linux). With canAdd false
// it takes only a held folder. fresh is true for a folder that this daemon did not
// hold yet: the caller adds it to tempRoots.held or closes it. The caller holds
// tempRoots.mu. The shape of a folder that the daemon did not hold, mode 0500 with an
// own inner folder t of mode 0700, is claustrum's own, not measured.
func reuseTempRoot(xfd int, dir string, root *os.Root, reg string, uid int, canAdd bool) (h *heldTempRoot, name string, fresh bool) {
	if root == nil {
		return nil, "", false
	}
	b, err := root.ReadFile(reg)
	name = string(b)
	if err != nil || !tempRootNameRE.MatchString(name) {
		return nil, "", false
	}
	var st unix.Stat_t
	if err := unix.Fstatat(xfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, "", false
	}
	if h := tempRoots.held[filepath.Join(dir, name)]; h != nil {
		if h.dev != st.Dev || h.ino != st.Ino {
			return nil, "", false // the name is no longer the held folder (claustrum's own)
		}
		holdTempRoot(h.f)
		return h, name, false
	}
	if !canAdd {
		return nil, "", false
	}
	f, ok := openTempRoot(xfd, name, uid, 0o500)
	if !ok || !childDirFit(int(f.Fd()), uid) {
		if ok {
			_ = f.Close()
		}
		return nil, "", false
	}
	fd := int(f.Fd())
	var fst unix.Stat_t
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil || unix.Fstat(fd, &fst) != nil ||
		time.Since(time.Unix(fst.Mtim.Unix())) >= tempRootMaxAge {
		_ = f.Close()
		return nil, "", false
	}
	holdTempRoot(f)
	return &heldTempRoot{f: f, dev: fst.Dev, ino: fst.Ino, stop: make(chan struct{})}, name, true
}

// openTempRoot opens the folder name of the temp dir with no link followed. ok is
// true for a folder of uid with the given mode. A changed mode is measured on Linux.
// The owner test is claustrum's own, not measured: a racer chown came too late there.
func openTempRoot(xfd int, name string, uid int, mode uint32) (*os.File, bool) {
	fd, err := unix.Openat(xfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != uid || st.Mode&0o7777 != mode {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

// makeTempRoot makes a new folder ccd-<10 hex> with mode 0700, then its inner folder
// t with mode 0700, then sets the new folder to mode 0500. The child gets t
// (4c111e86, measured on Linux and macOS: the folder has mode 0500, t mode 0700).
// If the new folder is not an own folder of mode 0700 after the mkdir, the name goes
// by one rmdir and removed is true. The reply then says "removed" whether the rmdir
// worked or not (measured on Linux, b3081b51). A t that cannot be made, or that is
// no own folder of mode 0700, or a failed mode change, removes t and then the name
// in the same way (claustrum's own, not measured).
func makeTempRoot(xfd int, dir string, uid int) (h *heldTempRoot, name string, removed bool) {
	name = newTempRootName()
	if !tempRootNameRE.MatchString(name) || unix.Mkdirat(xfd, name, 0o700) != nil {
		return nil, "", false
	}
	afterTempRootMkdir(dir, name)
	f, ok := openTempRoot(xfd, name, uid, 0o700)
	if !ok {
		removeMadeTempRoot(xfd, -1, dir, name)
		return nil, "", true
	}
	fd := int(f.Fd())
	if unix.Mkdirat(fd, tempRootChildDir, 0o700) != nil {
		_ = f.Close()
		removeMadeTempRoot(xfd, -1, dir, name)
		return nil, "", true
	}
	afterTempRootChildMkdir(dir, name)
	if !childDirFit(fd, uid) || unix.Fchmod(fd, 0o500) != nil {
		removeMadeTempRoot(xfd, fd, dir, name)
		_ = f.Close()
		return nil, "", true
	}
	var st unix.Stat_t
	_ = unix.Fstat(fd, &st)
	return &heldTempRoot{f: f, dev: st.Dev, ino: st.Ino, stop: make(chan struct{})}, name, false
}

// childDirFit reports whether the folder of fd holds t as an own folder of
// mode 0700, with no link followed.
func childDirFit(fd, uid int) bool {
	var st unix.Stat_t
	return unix.Fstatat(fd, tempRootChildDir, &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		st.Mode&unix.S_IFMT == unix.S_IFDIR && int(st.Uid) == uid && st.Mode&0o7777 == 0o700
}

// removeMadeTempRoot holds the removes of a folder of this step, each one plain
// rmdir. With madeFd at 0 or more it first removes the inner folder t through
// madeFd, the handle of the folder that makeTempRoot just made and opened. Then it
// removes the name that makeTempRoot just made, through the handle of the temp dir.
// An rmdir removes no link and no folder with content. The name must have the shape
// of this step, and homeguard.WipesHomeDir must pass on the full path of the name.
// That test covers t too: a t that is home lies in the name, which then holds home.
func removeMadeTempRoot(xfd, madeFd int, dir, made string) {
	if !tempRootNameRE.MatchString(made) || homeguard.WipesHomeDir(filepath.Join(dir, made)) {
		return
	}
	if madeFd >= 0 {
		_ = unix.Unlinkat(madeFd, tempRootChildDir, unix.AT_REMOVEDIR)
	}
	_ = unix.Unlinkat(xfd, made, unix.AT_REMOVEDIR)
}

// writeTempRootRegistry writes the folder name, with no newline, to the registry
// file with mode 0600. An old entry at that name goes first by one plain remove,
// never as a tree (measured on Linux). A failure is not logged (claustrum's own).
func writeTempRootRegistry(root *os.Root, reg, name string) {
	if err := root.Remove(reg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	f, err := root.OpenFile(reg, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(name)
	_ = f.Close()
}

// holdTempRoot sets the write time of the folder to now and takes its exclusive
// flock (measured on Linux). A failure does not stop the spawn (claustrum's own).
func holdTempRoot(f *os.File) {
	_ = unix.UtimesNanoAt(int(f.Fd()), ".", nil, 0)
	_ = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// refreshTempRoot holds the folder again every interval until the last spawn that
// uses it gives it back. The caller reads tempRootRefresh, so a test can set it.
func refreshTempRoot(h *heldTempRoot, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			tempRoots.mu.Lock()
			select {
			case <-h.stop:
			default:
				holdTempRoot(h.f)
			}
			tempRoots.mu.Unlock()
		}
	}
}
