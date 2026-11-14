//go:build linux || darwin

package main

// The private temp folder of a spawn with "cliTempRoot":true (b3081b51, measured on
// Linux). Claude Code keeps its own temp files in <temp dir>/claude-<uid>. When that
// entry is unfit, the daemon makes a folder ccd-<10 hex> beside it, with an inner
// folder t, and gives the child <temp dir>/ccd-<10 hex>/t in CLAUDE_CODE_TMPDIR. The
// reply names the kind and the folder (4c111e86, measured on Linux).
//
// macOS runs the same step (4c111e86, measured on macOS). There a rule
// that names only Linux is claustrum's own, not measured. The temp dir and the
// registry name of each system are in clitemproot_linux.go and clitemproot_darwin.go.
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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The texts of the reply (measured on Linux). On macOS tempRootNoOpen is measured,
// and so is tempRootTooMany in its log line (c9125eb8 and 4c111e86 on macOS, not
// self-tested).
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
// gets tempRootTooMany (c9125eb8 and 4c111e86, the log line on macOS, not
// self-tested). Spawns that share
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

// tempRootCheckEvery is the time between two checks of a held folder (checkTempRoot).
// A change made 0.6 s to 0.7 s after the spawn ended the child 1.34 s to 1.46 s later
// (4c111e86, measured on Linux and macOS). The value of 2 s is claustrum's own. A
// variable for the tests.
var tempRootCheckEvery = 2 * time.Second

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
// exclusive flock, refreshed every tempRootRefresh and checked every tempRootCheckEvery
// while a spawn uses it. dir and name give its path, and uid is the account that
// owns it. The fields change under tempRoots.mu, except lost.
type heldTempRoot struct {
	f           *os.File
	dev         uint64
	ino         uint64
	dir, name   string
	uid         int
	users       []*tempRootUser
	stop        chan struct{}
	checkFailed bool        // the last check failed (checkTempRoot)
	lost        atomic.Bool // set once, when a check finds the folder lost
}

// tempRootUser is one spawn that uses a held folder. end is the call that ends its
// child after a loss, or nil when the spawn did not ask for that end. bound is true
// once the spawn has set end, and ended once a loss has called it.
type tempRootUser struct {
	mu           sync.Mutex
	end          func()
	bound, ended bool
}

// goneTempRoot is a folder that a check found lost, for the lost list of a reply.
type goneTempRoot struct {
	name string
	at   time.Time
}

var tempRoots = struct {
	mu   sync.Mutex
	held map[string]*heldTempRoot // by the path of the folder
	live int
	lost map[string]bool // the path of each folder that a check found lost, for the life of the daemon
}{held: map[string]*heldTempRoot{}, lost: map[string]bool{}}

// tempRootsLost is the lost list that the next reply with a cliTempRoot member
// carries. It has its own lock, so a step that a stuck mount holds does not hold it.
var tempRootsLost struct {
	mu   sync.Mutex
	list []goneTempRoot
}

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
func privateTempRoot(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	type answer struct {
		env     []string
		res     *rpc.CLITempRoot
		release func()
		bind    func(end func())
	}
	late := &tempRootLate{}
	done := make(chan answer, 1)
	in := slices.Clone(env)
	go func() {
		e, r, rel, b := tempRootStep(in, runDir, late)
		done <- answer{e, r, rel, b}
	}()
	t := time.NewTimer(tempRootLimit)
	defer t.Stop()
	var a answer
	select {
	case a = <-done:
	case <-t.C:
		if late.giveUp() {
			a = answer{env, &rpc.CLITempRoot{WhyNot: tempRootNoReply}, func() {}, func(func()) {}}
		} else {
			a = <-done // the step kept its folder right before the limit
		}
	}
	res, taken := addGoneList(a.res)
	// A spawn binds only once its child runs. A release before the bind is a spawn
	// that failed, and its reply has no member, so the list goes back.
	var mu sync.Mutex
	bound := false
	bind := func(end func()) {
		mu.Lock()
		bound = true
		mu.Unlock()
		a.bind(end)
	}
	release := func() {
		mu.Lock()
		if !bound {
			putBackGoneList(taken)
			taken = nil
		}
		mu.Unlock()
		a.release()
	}
	return a.env, res, release, bind
}

// addGoneList moves the lost list into a member of a reply and returns the entries
// that it took. A reply with no member leaves the list for a later one. The list goes
// out once, in a reply of a spawn whose child starts (claustrum's own, not measured).
// The why "changed" is measured on macOS for a link at the name. The same word for the
// other losses and the seconds rounded down are claustrum's own.
func addGoneList(res *rpc.CLITempRoot) (*rpc.CLITempRoot, []goneTempRoot) {
	if res == nil {
		return nil, nil
	}
	tempRootsLost.mu.Lock()
	defer tempRootsLost.mu.Unlock()
	for _, l := range tempRootsLost.list {
		res.Gone = append(res.Gone, rpc.GoneTempRootEntry{Name: l.name, Why: "changed", AgoSeconds: int64(time.Since(l.at) / time.Second)})
	}
	taken := tempRootsLost.list
	tempRootsLost.list = nil
	return res, taken
}

// putBackGoneList puts the entries that addGoneList took back before the list, for
// the next reply with a member.
func putBackGoneList(taken []goneTempRoot) {
	if len(taken) == 0 {
		return
	}
	tempRootsLost.mu.Lock()
	defer tempRootsLost.mu.Unlock()
	tempRootsLost.list = append(taken, tempRootsLost.list...)
}

func tempRootStep(env []string, runDir string, late *tempRootLate) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	none, noBind := func() {}, func(func()) {}
	dir := childTempDir(env)
	if !filepath.IsAbs(dir) {
		return env, nil, none, noBind // measured on Linux for TMPDIR
	}
	uid := tempRootUID()
	// A temp dir that does not exist gives no member (measured on Linux). Another
	// failed open gives the text of a folder that cannot be opened (claustrum's own,
	// not measured).
	x, otherAccount, missing, ok := openTempDirChain(dir, uid)
	if missing {
		return env, nil, none, noBind
	}
	if !ok {
		return env, &rpc.CLITempRoot{WhyNot: tempRootNoOpen}, none, noBind
	}
	defer func() { _ = x.Close() }()
	xfd := int(x.Fd())
	var xst unix.Stat_t
	if err := unix.Fstat(xfd, &xst); err != nil {
		return env, &rpc.CLITempRoot{WhyNot: tempRootNoOpen}, none, noBind
	}
	var est unix.Stat_t
	if err := unix.Fstatat(xfd, "claude-"+strconv.Itoa(uid), &est, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return env, nil, none, noBind // a missing entry, or one that cannot be read (claustrum's own)
	}
	kind := tempEntryUnfit(&est, statDev(&xst), uid)
	if kind == "" {
		return env, nil, none, noBind
	}
	refuse := func(why string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
		logging.Infof("[process.Manager] Claude Code's temp folder is unfit (it is %s), but no other was made: %s", kind, why)
		return env, &rpc.CLITempRoot{Refused: kind, WhyNot: why}, none, noBind
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
		if h != nil {
			h.dir, h.name, h.uid = dir, name, uid
		}
		if removed {
			return refuse(tempRootRemoved)
		}
		if h == nil {
			return env, nil, none, noBind // the mkdir failed (claustrum's own, not measured)
		}
		holdTempRoot(h.f)
		fresh, made = true, true
	}
	if !late.keep() {
		// The limit came first. The child got the env as it was.
		if fresh {
			_ = h.f.Close()
		}
		return env, nil, none, noBind
	}
	if made && root != nil {
		writeTempRootRegistry(root, reg, name)
	}
	if fresh {
		tempRoots.held[filepath.Join(dir, name)] = h
		go tendTempRoot(h, tempRootRefresh, tempRootCheckEvery)
	}
	u := &tempRootUser{}
	h.users = append(h.users, u)
	tempRoots.live++
	logging.Infof("[process.Manager] Claude Code's temp folder is unfit (it is %s): Claude Code gets %s/%s beside it", kind, name, tempRootChildDir)
	env = sysproc.ReplaceOrAppendEnv(env, "CLAUDE_CODE_TMPDIR", filepath.Join(dir, name, tempRootChildDir))
	giveBack := func() { // the caller holds tempRoots.mu
		defer tempRoots.mu.Unlock()
		tempRoots.live--
		h.users = slices.DeleteFunc(h.users, func(o *tempRootUser) bool { return o == u })
		if len(h.users) == 0 {
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
	// The bind sets the end of the child once it runs. A folder lost before that ends
	// the child at once (claustrum's own, not measured).
	bind := func(end func()) {
		u.mu.Lock()
		u.end, u.bound = end, true
		call := end != nil && h.lost.Load() && !u.ended
		u.ended = u.ended || call
		u.mu.Unlock()
		if call {
			end()
		}
	}
	return env, &rpc.CLITempRoot{Refused: kind, Name: name}, release, bind
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
// Linux). On macOS a link, a file and a folder of root are measured, and so are own
// folders of mode 0500, 0700, 0755, 0770 and 0777. The order of the tests is
// claustrum's own.
func tempEntryUnfit(st *unix.Stat_t, dev uint64, uid int) string {
	switch {
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return "a link"
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return "not a folder"
	case int(st.Uid) != uid:
		return "another account's"
	case statDev(st) != dev:
		return "on another file system"
	case st.Mode&0o400 == 0:
		return "not readable by its owner"
	}
	return ""
}

// tempRootRegistryNameOf is the name of the registry file in the run dir: the first 8
// bytes of the SHA-256 of the boot id, "/", the pid namespace, a NUL and the temp dir
// as given (measured on Linux). tempRootRegistryName gives the two ids of the system.
func tempRootRegistryNameOf(boot, ns, dir string) string {
	sum := sha256.Sum256([]byte(boot + "/" + ns + "\x00" + dir))
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
	if tempRoots.lost[filepath.Join(dir, name)] {
		return nil, "", false // a lost folder is not given again (claustrum's own)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(xfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, "", false
	}
	if h := tempRoots.held[filepath.Join(dir, name)]; h != nil {
		if h.dev != statDev(&st) || h.ino != st.Ino {
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
	return &heldTempRoot{f: f, dev: statDev(&fst), ino: fst.Ino, dir: dir, name: name, uid: uid, stop: make(chan struct{})}, name, true
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
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != uid || statMode(&st)&0o7777 != mode {
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
	return &heldTempRoot{f: f, dev: statDev(&st), ino: st.Ino, stop: make(chan struct{})}, name, false
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

// tendTempRoot holds the folder again every refresh, and checks it every check,
// until the last spawn that uses it gives it back. After a loss it only holds. The
// caller reads tempRootRefresh and tempRootCheckEvery, so a test can set them.
func tendTempRoot(h *heldTempRoot, refresh, check time.Duration) {
	rt, ct := time.NewTicker(refresh), time.NewTicker(check)
	defer rt.Stop()
	defer ct.Stop()
	for {
		checking := false
		select {
		case <-h.stop:
			return
		case <-rt.C:
		case <-ct.C:
			checking = true
		}
		tempRoots.mu.Lock()
		select {
		case <-h.stop:
		default:
			if !checking {
				holdTempRoot(h.f)
			} else if !h.lost.Load() {
				checkTempRoot(h)
			}
		}
		tempRoots.mu.Unlock()
	}
}

// The log lines of a check (4c111e86, see checkTempRoot).
const (
	tempRootLostLine        = "[process.Manager] Claude Code's private temp folder %s counts as lost: %s"
	tempRootRunOnLine       = "[process.Manager] %d process(es) run on in %s, which may be another account's: their starts did not ask to be ended over it (cliTempRootEnd); end those sessions"
	tempRootRemadeLine      = "[process.Manager] Claude Code's private temp folder %s was no longer at its name: it is made anew"
	tempRootSwapLine        = "[process.Manager] Claude Code's private temp folder %s was no longer at its name: this account's folder that stands there is held instead"
	tempRootCheckFailedLine = "[process.Manager] Claude Code's private temp folder %s: the temp dir could not be looked at: %v; nobody is ended over that, and it is looked at again"
	tempRootWellAgainLn     = "[process.Manager] Claude Code's private temp folder %s: all is well with it again"
)

// checkTempRoot checks the held folder by its name in the temp dir, with no link
// followed. The caller holds tempRoots.mu. These rows are measured on 4c111e86:
//
//   - A link at the name, a folder of another account (uid 0), or a folder of mode
//     0777 counts as lost (Linux and macOS). The "open" test takes a group or other
//     write bit (claustrum's own, only 0777 is measured).
//   - An own folder that took the place of the held one is held instead, and gets t
//     and mode 0500 (Linux and macOS). No child is ended.
//   - With no entry at the name, the folder is made anew (macOS). No child is ended.
//   - A held folder whose t was removed gets t and mode 0500 again, with no line
//     (macOS). A t of mode 0777 changes nothing (Linux and macOS).
//   - A temp dir that cannot be read logs the failed-check line, and a later good check
//     logs the line that all is well. No child is ended, also after 320 s (Linux).
//
// One line per run of failed checks, a temp dir that is gone as a failed check, and
// no action for a file at the name are claustrum's own, not measured.
func checkTempRoot(h *heldTempRoot) {
	xfd, err := unix.Open(h.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	var st unix.Stat_t
	missing := false
	if err == nil {
		defer func() { _ = unix.Close(xfd) }()
		err = unix.Fstatat(xfd, h.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			err, missing = nil, true
		}
	}
	if err != nil {
		if !h.checkFailed {
			logging.Infof(tempRootCheckFailedLine, h.name, err)
		}
		h.checkFailed = true
		return
	}
	if h.checkFailed {
		h.checkFailed = false
		logging.Infof(tempRootWellAgainLn, h.name)
	}
	switch {
	case missing:
		logging.Infof(tempRootRemadeLine, h.name)
		if unix.Mkdirat(xfd, h.name, 0o700) == nil {
			takeTempRootAt(h, xfd)
		}
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		loseTempRoot(h, "it is a link")
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
	case int(st.Uid) != h.uid:
		loseTempRoot(h, fmt.Sprintf("it is another account's (uid %d)", st.Uid))
	case st.Mode&0o022 != 0:
		loseTempRoot(h, "it is open to other accounts")
	case statDev(&st) != h.dev || st.Ino != h.ino:
		logging.Infof(tempRootSwapLine, h.name)
		takeTempRootAt(h, xfd)
	default:
		fitTempRoot(int(h.f.Fd()))
	}
}

// takeTempRootAt opens the own folder at the name of h, gives it t when it has none
// and mode 0500, holds it, and puts it in the place of the old handle. A folder that is not an own
// folder others cannot write is left as it is (claustrum's own, not measured).
func takeTempRootAt(h *heldTempRoot, xfd int) {
	fd, err := unix.Openat(xfd, h.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), h.name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != h.uid || st.Mode&0o022 != 0 {
		_ = f.Close()
		return
	}
	fitTempRoot(fd)
	_ = unix.Fchmod(fd, 0o500)
	holdTempRoot(f)
	_ = h.f.Close()
	h.f, h.dev, h.ino = f, statDev(&st), st.Ino
}

// fitTempRoot gives the folder of fd an own t of mode 0700 when it has no entry t,
// and then mode 0500. A folder that has t is left as it is.
func fitTempRoot(fd int) {
	var st unix.Stat_t
	if err := unix.Fstatat(fd, tempRootChildDir, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return
	}
	_ = unix.Fchmod(fd, 0o700)
	_ = unix.Mkdirat(fd, tempRootChildDir, 0o700)
	_ = unix.Fchmod(fd, 0o500)
}

// loseTempRoot marks h lost. The next spawn in its temp dir makes a new folder, and
// the next good reply with a member lists it. Each child that asked with cliTempRootEnd
// is ended. For the others one line counts them (4c111e86, see checkTempRoot). The
// caller holds tempRoots.mu.
func loseTempRoot(h *heldTempRoot, why string) {
	logging.Infof(tempRootLostLine, h.name, why)
	h.lost.Store(true)
	key := filepath.Join(h.dir, h.name)
	tempRoots.lost[key] = true
	if tempRoots.held[key] == h {
		delete(tempRoots.held, key)
	}
	tempRootsLost.mu.Lock()
	tempRootsLost.list = append(tempRootsLost.list, goneTempRoot{name: h.name, at: time.Now()})
	tempRootsLost.mu.Unlock()
	runOn := 0
	for _, u := range h.users {
		u.mu.Lock()
		end := u.end
		call := end != nil && !u.ended
		u.ended = u.ended || call
		if u.bound && end == nil {
			runOn++
		}
		u.mu.Unlock()
		if call {
			end()
		}
	}
	if runOn > 0 {
		logging.Infof(tempRootRunOnLine, runOn, h.name)
	}
}
