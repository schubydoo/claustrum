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
	"cmp"
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
	// tempRootNoReplyLine is the log line of that reply, for the spawn whose own step
	// hits the limit (4c111e86, measured on Linux).
	tempRootNoReplyLine = "[process.Manager] the temp dir has not answered in %v: Claude Code starts anyway, and may refuse its folder there"
	// tempRootSteppedAround answers a link or a file at the entry (4c111e86, measured
	// on Linux and macOS).
	tempRootSteppedAround = "only a folder of root's or of this account's own that others cannot write is stepped around: another account may have laid this"
	// tempRootNoMkdir and the error text answer a mkdir of the new folder that failed.
	// The text of EACCES is measured on macOS (4c111e86).
	tempRootNoMkdir = "no folder could be made in the temp dir: "
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

// tempRootMakeCap is the count of new folders that the daemon makes within
// tempRootMakeWindow. A spawn that needs one more gets tempRootTooMany and no
// folder (4c111e86, measured on macOS). A folder that a spawn takes again is no
// make, and a folder that was removed still counts. The count is of this daemon
// (measured on macOS, in one temp dir). These are claustrum's own, not measured: a
// window that slides with each make, its edge (a make 60 s old no longer counts), a
// count in memory, one count across all temp dirs, the cap on Linux and for each kind
// of entry, and no count for a check that makes a folder anew. A make whose new folder
// fails its test and is removed counts, and a mkdir that fails does not.
const tempRootMakeCap = 8

const tempRootMakeWindow = 60 * time.Second

// tempRootNow gives the time of a make. A seam for the tests. Production never
// reassigns it.
var tempRootNow = time.Now

// tempRootMakeFits cuts the makes older than tempRootMakeWindow and reports whether
// one more make fits the cap. The caller holds tempRoots.mu.
func tempRootMakeFits(now time.Time) bool {
	tempRoots.made = slices.DeleteFunc(tempRoots.made, func(at time.Time) bool { return now.Sub(at) >= tempRootMakeWindow })
	return len(tempRoots.made) < tempRootMakeCap
}

// tempRootMaxAge is the age of a folder that a later daemon no longer takes. A
// folder of 25 h gets a new name (measured on Linux). The bound of 24 h is
// claustrum's own, not measured. The daemon itself keeps a folder that it holds.
const tempRootMaxAge = 24 * time.Hour

// tempRootRefresh is the time between two refreshes of a held folder (measured on
// Linux: 600 s from the first hold). A variable for the tests.
var tempRootRefresh = 600 * time.Second

// tempRootCheckEvery is the time between two checks of the held folders
// (checkTempRoots). A change made 0.6 s to 0.7 s after the spawn ended the child 1.34 s
// to 1.46 s later (4c111e86, measured on Linux and macOS). The checks come every 2 s
// (4c111e86, measured on macOS, on Linux claustrum's own). A variable for the tests.
var tempRootCheckEvery = 2 * time.Second

// tempRootCheckTicks gives the ticks of the checks, one every d, and the call that stops
// them. A seam for the tests. Production never reassigns it.
var tempRootCheckTicks = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

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
	afterTempRootChecks     = func() {}
	afterTempRootOneCheck   = func() {}
)

func randomTempRootName() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "ccd-" + hex.EncodeToString(b)
}

// heldTempRoot is one folder that this daemon holds: an open handle with an
// exclusive flock, refreshed every tempRootRefresh while a spawn uses it, and checked
// every tempRootCheckEvery. dir and name give its path, and uid is the account that
// owns it. seq is the order of the hold. The fields change under tempRoots.mu, except
// lost.
type heldTempRoot struct {
	f           *os.File
	dev         uint64
	ino         uint64
	dir, name   string
	uid         int
	seq         uint64
	users       []*tempRootUser
	stop        chan struct{}
	checkFailed bool        // the last check failed (checkTempRoot)
	changes     int         // the change lines of the checks so far (tempRootChangeCap)
	lost        atomic.Bool // set once, when a check finds the folder lost
	// given is true once the last spawn gave the folder back, at givenAt. The folder
	// keeps its flock, and the checks go on with no write (checkTempRoots). gone is true
	// once a check of a given-back folder found its temp dir gone, no entry or another
	// folder at the name.
	given, gone bool
	givenAt     time.Time
	// probe is true for the copy of a folder that a check looks at with no lock
	// (checkCopy). A loss there only sets lossWhy and lossWord.
	probe             bool
	lossWhy, lossWord string
	// of is the folder of a copy of a held folder (heldCopies), and handedBack is set
	// once its last spawn gives that folder back. A check of the copy then writes no
	// more (mayWrite).
	of         *heldTempRoot
	handedBack atomic.Bool
	// xdev is the device of the temp dir at the hold, and dirLink is true when the
	// temp dir path was a link then (checkTempRoot).
	xdev    uint64
	dirLink bool
}

// tempRootUser is one spawn that uses a held folder. end is the call that ends its
// child after a loss, or nil when the spawn did not ask for that end. bound is true
// once the spawn has set end, and ended once a loss has called it.
type tempRootUser struct {
	mu           sync.Mutex
	end          func()
	bound, ended bool
}

// goneTempRoot is a folder that a check found lost, for the lost list of a reply. why
// is the word of the list.
type goneTempRoot struct {
	name, why string
	at        time.Time
}

// tempRootLostKeep is the count of lost folders that the list keeps. A loss past it
// cuts the oldest (4c111e86, measured on macOS).
const tempRootLostKeep = 16

var tempRoots = struct {
	mu   sync.Mutex
	held map[string]*heldTempRoot // by the path of the folder
	live int
	made []time.Time     // the time of each make within tempRootMakeWindow, oldest first
	lost map[string]bool // the path of each folder that a check found lost, for the life of the daemon
	seq  uint64          // the seq of the last hold
	// given holds the folders that their last spawn gave back, while they are checked.
	given []*heldTempRoot
	// checks is open while the loop of the checks runs (checkTempRoots). A close
	// stops it, and the loop closes checksEnded at its end.
	checks, checksEnded chan struct{}
	// checkLost holds the losses of the running check, for one step into the lost list.
	checkLost []goneTempRoot
	// givenPass is the look at the given-back folders that runs or waits to count.
	givenPass *givenPass
	// givenOpen counts the passes that have not answered, given-up ones too.
	givenOpen int
}{held: map[string]*heldTempRoot{}, lost: map[string]bool{}}

// tempRootsLost is the lost list that each later reply with a cliTempRoot member
// carries, oldest first. It has its own lock, so a step that a stuck mount holds does
// not hold it.
var tempRootsLost struct {
	mu   sync.Mutex
	list []goneTempRoot
}

// tempRootLate decides between the answer of the step and the time limit. A file
// call that a stuck mount holds cannot be stopped, so the step goes on after the
// limit. The step keeps its folder only if keep comes before markGaveUp. dir is the
// temp dir of the step. While a step goes on after its limit, its dir is in
// tempRootLateDirs, from markGaveUp to the end of the step (ended). A step that at
// its limit only waits for tempRoots.mu (lockWait), whoever holds it, is no late step:
// its temp dir answers, so the next spawn runs its own step.
type tempRootLate struct {
	mu                            sync.Mutex
	gaveUp, kept, ended, lockWait bool
	dir                           string
}

// waitsForLock marks the wait of the step for tempRoots.mu, from just before the Lock
// to just after it.
func (l *tempRootLate) waitsForLock(w bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lockWait = w
}

// tempRootLateDirs counts the steps of each temp dir that go on after their limit. A
// spawn whose temp dir is in it gets tempRootNoReply at once, with no step and no line:
// 4c111e86 answers so while that temp dir does not answer, and runs the step of another
// temp dir (measured on Linux, on a dead NFS mount). These are claustrum's own, not
// measured: macOS, the end of that time when the late step returns, the key by the path
// of the temp dir as given, a step that starts while an earlier one of the same temp
// dir is within its limit, and no late step for a step that only waits for the lock.
var tempRootLateDirs = struct {
	mu   sync.Mutex
	dirs map[string]int
}{dirs: map[string]int{}}

// tempDirStepIsLate reports whether a step of dir goes on after its limit.
func tempDirStepIsLate(dir string) bool {
	tempRootLateDirs.mu.Lock()
	defer tempRootLateDirs.mu.Unlock()
	return tempRootLateDirs.dirs[dir] > 0
}

// addLateDir counts one more late step of dir, or one less with n at -1. The caller
// holds the mu of the tempRootLate.
func addLateDir(dir string, n int) {
	tempRootLateDirs.mu.Lock()
	defer tempRootLateDirs.mu.Unlock()
	if tempRootLateDirs.dirs[dir] += n; tempRootLateDirs.dirs[dir] <= 0 {
		delete(tempRootLateDirs.dirs, dir)
	}
}

// stepEnded marks the end of the step, and takes a late step out of tempRootLateDirs.
func (l *tempRootLate) stepEnded() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ended = true
	if l.gaveUp && l.dir != "" {
		addLateDir(l.dir, -1)
	}
}

func (l *tempRootLate) keep() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kept = !l.gaveUp
	return l.kept
}

func (l *tempRootLate) markGaveUp() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gaveUp = !l.kept
	if l.gaveUp && !l.ended && !l.lockWait && l.dir != "" {
		addLateDir(l.dir, 1)
	} else {
		l.dir = "" // no late step: stepEnded takes nothing out
	}
	return l.gaveUp
}

// privateTempRoot runs the step for the env of one child. It returns the env, the
// member of the reply (nil for none) and the function that gives the folder back.
// The step runs in its own goroutine on a copy of the env. After tempRootLimit the
// spawn goes on with the env as it was. A late step still takes its folder for the next
// spawn (tempRootStep). A spawn whose own step hits the limit logs tempRootNoReplyLine
// (4c111e86, measured on Linux; on macOS claustrum's own). A spawn of a temp dir with a
// late step returns before it, with no line. A spawn whose step waits past the limit for
// the lock, which a stuck step or a refresh holds, logs it too (claustrum's own).
func privateTempRoot(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	type answer struct {
		env     []string
		res     *rpc.CLITempRoot
		release func()
		bind    func(end func())
	}
	dir := childTempDir(env)
	if filepath.IsAbs(dir) && tempDirStepIsLate(dir) {
		return env, addGoneList(&rpc.CLITempRoot{WhyNot: tempRootNoReply}), func() {}, func(func()) {}
	}
	late := &tempRootLate{dir: dir}
	done := make(chan answer, 1)
	in := slices.Clone(env)
	go func() {
		e, r, rel, b := tempRootStep(in, runDir, late)
		late.stepEnded()
		done <- answer{e, r, rel, b}
	}()
	t := time.NewTimer(tempRootLimit)
	defer t.Stop()
	var a answer
	select {
	case a = <-done:
	case <-t.C:
		if late.markGaveUp() {
			logging.Warnf(tempRootNoReplyLine, tempRootLimit)
			a = answer{env, &rpc.CLITempRoot{WhyNot: tempRootNoReply}, func() {}, func(func()) {}}
		} else {
			a = <-done // the step kept its folder right before the limit
		}
	}
	return a.env, addGoneList(a.res), a.release, a.bind
}

// addGoneList copies the lost list into a member of a reply. Each later reply with a
// member carries the whole list again, and no entry expired within 130 s (4c111e86,
// measured on macOS). An entry that never expires and the seconds rounded down are
// claustrum's own, not measured.
func addGoneList(res *rpc.CLITempRoot) *rpc.CLITempRoot {
	if res == nil {
		return nil
	}
	tempRootsLost.mu.Lock()
	defer tempRootsLost.mu.Unlock()
	for _, l := range tempRootsLost.list {
		res.Gone = append(res.Gone, rpc.GoneTempRootEntry{Name: l.name, Why: l.why, AgoSeconds: int64(time.Since(l.at) / time.Second)})
	}
	return res
}

// addLostEntry adds a lost folder at the end of the lost list and cuts the oldest
// entries past tempRootLostKeep.
func addLostEntry(name, why string) {
	addLostEntries([]goneTempRoot{{name: name, why: why, at: time.Now()}})
}

// addLostEntries adds the losses of one check at the end of the lost list in one step,
// so a reply gets all of them or none, and cuts the oldest entries past
// tempRootLostKeep.
func addLostEntries(gone []goneTempRoot) {
	if len(gone) == 0 {
		return
	}
	tempRootsLost.mu.Lock()
	defer tempRootsLost.mu.Unlock()
	tempRootsLost.list = append(tempRootsLost.list, gone...)
	if n := len(tempRootsLost.list) - tempRootLostKeep; n > 0 {
		tempRootsLost.list = slices.Delete(tempRootsLost.list, 0, n)
	}
}

func tempRootStep(env []string, runDir string, late *tempRootLate) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	none, noBind := func() {}, func(func()) {}
	dir := childTempDir(env)
	if !filepath.IsAbs(dir) {
		return env, nil, none, noBind // measured on Linux for TMPDIR
	}
	uid := tempRootUID()
	// A temp dir that does not exist gives no member (measured on Linux). So does a
	// temp dir whose attributes cannot be read (4c111e86, measured on macOS). Another
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
	// A temp dir that cannot be searched gives the kind "out of reach" (4c111e86,
	// measured on macOS). A failed stat of an entry that exists, other than EACCES,
	// gives no member (claustrum's own, not measured).
	var est unix.Stat_t
	var kind string
	if err := unix.Fstatat(xfd, "claude-"+strconv.Itoa(uid), &est, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.EACCES) {
		kind = "out of reach"
	} else if err != nil {
		return env, nil, none, noBind // a missing entry
	} else {
		kind = tempEntryUnfit(&est, statDev(&xst), uid)
	}
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
	// A link or a file gets no folder (4c111e86, measured on Linux and macOS), and an
	// entry out of reach neither (measured on macOS). The place after the account test
	// is claustrum's own, not measured.
	if kind == "a link" || kind == "not a folder" || kind == "out of reach" {
		return refuse(tempRootSteppedAround)
	}
	late.waitsForLock(true)
	tempRoots.mu.Lock()
	late.waitsForLock(false)
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
		if !canAdd || !tempRootMakeFits(tempRootNow()) {
			return refuse(tempRootTooMany)
		}
		var removed bool
		var mkErr error
		h, name, removed, mkErr = makeTempRoot(xfd, dir, uid)
		if h != nil || removed {
			tempRoots.made = append(tempRoots.made, tempRootNow()) // the mkdir worked
		}
		if h != nil {
			h.dir, h.name, h.uid = dir, name, uid
		}
		if removed {
			return refuse(tempRootRemoved)
		}
		if mkErr != nil {
			return refuse(tempRootNoMkdir + mkErr.Error()) // EACCES measured on macOS (4c111e86)
		}
		if h == nil {
			return env, nil, none, noBind // a name of another shape (claustrum's own, not measured)
		}
		holdTempRoot(h.f)
		fresh, made = true, true
	}
	// When the limit came first, the child got the env as it was. The step still writes
	// the record, logs its line and takes the folder, so the next spawn gets it (4c111e86,
	// measured on Linux on a dead NFS mount). It then gives the folder back at once if no
	// spawn uses it, as a spawn whose child ended, and the folder keeps its flock (the
	// give-back claustrum's own, the flock 4c111e86, measured on Linux; all of it on macOS).
	onTime := late.keep()
	if made && root != nil {
		writeTempRootRegistry(root, reg, name)
	}
	if fresh {
		var lst unix.Stat_t
		h.xdev, h.dirLink = statDev(&xst), unix.Lstat(noTrailingSlash(dir), &lst) == nil && lst.Mode&unix.S_IFMT == unix.S_IFLNK
		tempRoots.seq++
		h.seq = tempRoots.seq
		tempRoots.held[filepath.Join(dir, name)] = h
		// A given-back record of the same folder ends: the new hold checks it.
		dropGivenTempRoots(func(g *heldTempRoot) bool { return g.dir == dir && g.name == name })
		go tendTempRoot(h, tempRootRefresh)
		if tempRoots.checks == nil {
			tempRoots.checks, tempRoots.checksEnded = make(chan struct{}), make(chan struct{})
			ticks, stopTicks := tempRootCheckTicks(tempRootCheckEvery)
			go checkTempRoots(tempRoots.checks, tempRoots.checksEnded, ticks, stopTicks, afterTempRootChecks, afterTempRootOneCheck)
		}
	}
	logging.Infof("[process.Manager] Claude Code's temp folder is unfit (it is %s): Claude Code gets %s/%s beside it", kind, name, tempRootChildDir)
	if !onTime {
		if len(h.users) == 0 {
			giveBackTempRoot(h, filepath.Join(dir, name))
		}
		return env, nil, none, noBind
	}
	u := &tempRootUser{}
	h.users = append(h.users, u)
	tempRoots.live++
	env = sysproc.ReplaceOrAppendEnv(env, "CLAUDE_CODE_TMPDIR", filepath.Join(dir, name, tempRootChildDir))
	giveBack := func() { // the caller holds tempRoots.mu
		defer tempRoots.mu.Unlock()
		tempRoots.live--
		h.users = slices.DeleteFunc(h.users, func(o *tempRootUser) bool { return o == u })
		if len(h.users) == 0 {
			giveBackTempRoot(h, filepath.Join(dir, name))
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

// giveBackTempRoot gives back the folder h at path once no spawn uses it. The caller
// holds tempRoots.mu.
func giveBackTempRoot(h *heldTempRoot, path string) {
	close(h.stop)
	if tempRoots.held[path] != h {
		_ = h.f.Close() // a lost folder is not checked again
		return
	}
	delete(tempRoots.held, path)
	// The folder keeps its flock (4c111e86, measured on Linux) until its record ends
	// and its handle closes (claustrum's own). The checks go on, so a later loss still
	// counts (4c111e86, measured on macOS).
	h.handedBack.Store(true)
	h.given, h.givenAt = true, time.Now()
	tempRoots.given = append(tempRoots.given, h)
	if n := len(tempRoots.given) - tempRootGivenKeep; n > 0 {
		for _, g := range tempRoots.given[:n] {
			_ = g.f.Close()
		}
		tempRoots.given = slices.Delete(tempRoots.given, 0, n)
	}
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
// path do not resolve because a name does not exist, or because its attributes
// cannot be read (EACCES). otherAccount is true when one of them
// belongs to an account other than uid and root, or when group or others can
// write it without the sticky bit. Measured on Linux: a 0777 temp dir is refused
// and /tmp passes. The group bit, a sticky temp dir and the links of the path,
// which are resolved first, are claustrum's own, not measured. The caller closes x.
func openTempDirChain(dir string, uid int) (x *os.File, otherAccount, missing, ok bool) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, false, errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.EACCES), false
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
	// A given-back folder keeps the flock of its handle, which a second open of it
	// cannot take. So the hold of such a folder takes a copy of that handle, which shares
	// the flock (claustrum's own). A copy that fails (no free fd) gives a new folder.
	var f *os.File
	ok := false
	if g := givenTempRootOf(&st); g == nil {
		f, ok = openTempRoot(xfd, name, uid, 0o500)
	} else if fd, err := unix.FcntlInt(g.f.Fd(), unix.F_DUPFD_CLOEXEC, 0); err == nil {
		f, ok = ownTempRoot(os.NewFile(uintptr(fd), name), uid, 0o500)
	}
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
	return ownTempRoot(os.NewFile(uintptr(fd), name), uid, mode)
}

// ownTempRoot gives back f when it is a folder of uid with the given mode, and closes it
// otherwise.
func ownTempRoot(f *os.File, uid int, mode uint32) (*os.File, bool) {
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || int(st.Uid) != uid || statMode(&st)&0o7777 != mode {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

// givenTempRootOf gives the given-back record whose folder st is, or nil. The caller
// holds tempRoots.mu.
func givenTempRootOf(st *unix.Stat_t) *heldTempRoot {
	for _, g := range tempRoots.given {
		if g.dev == statDev(st) && g.ino == st.Ino {
			return g
		}
	}
	return nil
}

// makeTempRoot makes a new folder ccd-<10 hex> with mode 0700, then its inner folder
// t with mode 0700, then sets the new folder to mode 0500. The child gets t
// (4c111e86, measured on Linux and macOS: the folder has mode 0500, t mode 0700).
// If the new folder is not an own folder of mode 0700 after the mkdir, the name goes
// by one rmdir and removed is true. The reply then says "removed" whether the rmdir
// worked or not (measured on Linux, b3081b51). A t that cannot be made, or that is
// no own folder of mode 0700, or a failed mode change, removes t and then the name
// in the same way (claustrum's own, not measured). A failed mkdir of the new folder
// returns its error in mkErr.
func makeTempRoot(xfd int, dir string, uid int) (h *heldTempRoot, name string, removed bool, mkErr error) {
	name = newTempRootName()
	if !tempRootNameRE.MatchString(name) {
		return nil, "", false, nil
	}
	if err := unix.Mkdirat(xfd, name, 0o700); err != nil {
		return nil, "", false, err
	}
	afterTempRootMkdir(dir, name)
	f, ok := openTempRoot(xfd, name, uid, 0o700)
	if !ok {
		removeMadeTempRoot(xfd, -1, dir, name)
		return nil, "", true, nil
	}
	fd := int(f.Fd())
	if unix.Mkdirat(fd, tempRootChildDir, 0o700) != nil {
		_ = f.Close()
		removeMadeTempRoot(xfd, -1, dir, name)
		return nil, "", true, nil
	}
	afterTempRootChildMkdir(dir, name)
	if !childDirFit(fd, uid) || unix.Fchmod(fd, 0o500) != nil {
		removeMadeTempRoot(xfd, fd, dir, name)
		_ = f.Close()
		return nil, "", true, nil
	}
	var st unix.Stat_t
	_ = unix.Fstat(fd, &st)
	return &heldTempRoot{f: f, dev: statDev(&st), ino: st.Ino, stop: make(chan struct{})}, name, false, nil
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

// tendTempRoot holds the folder again every refresh, until the last spawn that uses
// it gives it back. The caller reads tempRootRefresh, so a test can set it.
func tendTempRoot(h *heldTempRoot, refresh time.Duration) {
	rt := time.NewTicker(refresh)
	defer rt.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-rt.C:
		}
		tempRoots.mu.Lock()
		select {
		case <-h.stop:
		default:
			holdTempRoot(h.f)
		}
		tempRoots.mu.Unlock()
	}
}

// tempRootGivenKeep and tempRootGivenFor bound the given-back folders that are still
// checked: at most 8, each for 10 min after its give-back (claustrum's own, not
// measured: 4c111e86 still listed a loss made 5 s after the end of its child).
const (
	tempRootGivenKeep = 8
	tempRootGivenFor  = 10 * time.Minute
)

// dropGivenTempRoots closes and drops each given-back folder for which end is true.
// The caller holds tempRoots.mu.
func dropGivenTempRoots(end func(*heldTempRoot) bool) {
	tempRoots.given = slices.DeleteFunc(tempRoots.given, func(g *heldTempRoot) bool {
		if !end(g) {
			return false
		}
		_ = g.f.Close()
		return true
	})
}

// checkTempRoots checks all held folders together every check, in the order of their
// hold. A folder that its last spawn gave back is still checked. Its handle closes
// once it is lost, held again, older than tempRootGivenFor or pushed out past
// tempRootGivenKeep, or once a check finds its temp dir gone, no entry or another
// folder at its name. The loop ends with no folder.
// Measured on 4c111e86 (macOS): the first check comes about 2 s after the first hold,
// not 2 s after the daemon start, and the checks then come every 2 s. The losses of
// several folders come at one check and fill the lost list together. A loss made 5 s
// after the end of the last child of a folder still counts at the next check.
// These are claustrum's own, not measured: the order of the hold within one check
// (4c111e86 gave an order that changed from check to check), the checks of a given-back
// folder past 8 s and their end, the end of the loop, and no write and no line but a
// loss line and the lines of checkTempRootTimed (those measured on Linux) for
// a given-back folder. The caller gives the ticks
// (tempRootCheckTicks), the call after each check and the call after each folder of a
// check, so a test can set them.
func checkTempRoots(stop, ended chan struct{}, ticks <-chan time.Time, stopTicks, after, afterOne func()) {
	defer close(ended)
	defer stopTicks()
	for {
		select {
		case <-stop:
			tempRoots.mu.Lock()
			abandoned := abandonGivenPass()
			tempRoots.mu.Unlock()
			closeProbes(abandoned)
			return
		case <-ticks:
		}
		// The given-back folders are looked at through copies, in a goroutine of their
		// own and with no lock, so a temp dir that does not answer holds up no live
		// folder, no spawn and no later check. The check waits tempRootGivenWait for
		// them. Copies that answer later count at the first check after their answer.
		// While they have not answered, no new pass starts.
		tempRoots.mu.Lock()
		pass := tempRoots.givenPass
		if pass == nil && len(tempRoots.given) > 0 && tempRoots.givenOpen < tempRootGivenPassCap {
			pass = startGivenPass()
		}
		tempRoots.mu.Unlock()
		if pass != nil {
			wait := time.NewTimer(tempRootGivenWait)
			select {
			case <-pass.done:
			case <-wait.C:
			}
			wait.Stop()
		}
		// The held folders are looked at through copies too, with no lock that a spawn
		// takes, so the check of a held folder in a temp dir that does not answer holds up
		// no spawn: 4c111e86 gives a spawn of another temp dir that answers its folder at
		// once while the mount of a held folder is dead (measured on Linux). Each copy
		// gives its result to its folder under the lock right after its look (takeCheck).
		// The losses of one check still reach the lost list together, at its end. A spawn
		// between two folders of a check gets a new folder for a folder just lost, and the
		// list of its reply lacks that folder (claustrum's own).
		// The held and the given-back folders take their results in one loop, in the
		// order of the hold, so the loss lines and the lost list keep that order.
		tempRootChecking.Lock()
		tempRoots.mu.Lock()
		var probes map[*heldTempRoot]*heldTempRoot
		if pass != nil && pass.answered {
			probes, tempRoots.givenPass = pass.probes, nil
		}
		held := heldCopies()
		tempRoots.mu.Unlock()
		for _, h := range held.order {
			c := held.copies[h]
			if c != nil {
				checkTempRootTimed(c)
			}
			tempRoots.mu.Lock()
			switch p, err := probes[h], held.failed[h]; {
			case c != nil:
				takeCheck(h, c)
			case h.lost.Load():
			case err != nil:
				if !h.given {
					if !h.checkFailed {
						logTempRootChange(h, tempRootCheckFailedLine, h.name, err)
					}
					h.checkFailed = true
				}
			case !slices.Contains(tempRoots.given, h): // no longer checked
			case p != nil && p.lossWhy != "":
				markTempRootLost(h, p.lossWhy, p.lossWord)
			case p != nil:
				h.gone = p.gone
			}
			tempRoots.mu.Unlock()
			afterOne()
		}
		tempRoots.mu.Lock()
		select {
		case <-stop:
			abandoned := abandonGivenPass()
			tempRoots.mu.Unlock()
			tempRootChecking.Unlock()
			closeProbes(held.copies)
			closeProbes(probes)
			closeProbes(abandoned)
			return
		default:
		}
		addLostEntries(tempRoots.checkLost)
		tempRoots.checkLost = nil
		dropGivenTempRoots(func(g *heldTempRoot) bool {
			return g.lost.Load() || g.gone || time.Since(g.givenAt) >= tempRootGivenFor
		})
		done := len(tempRoots.held) == 0 && len(tempRoots.given) == 0
		var abandoned map[*heldTempRoot]*heldTempRoot
		if done {
			tempRoots.checks = nil
			abandoned = abandonGivenPass()
		}
		tempRoots.mu.Unlock()
		tempRootChecking.Unlock()
		closeProbes(held.copies)
		closeProbes(probes)
		closeProbes(abandoned)
		after()
		if done {
			return
		}
	}
}

// tempRootGivenWait is the time that a check waits for the copies of the given-back
// folders (claustrum's own, not measured).
const tempRootGivenWait = 200 * time.Millisecond

// tempRootGivenPassCap is the count of passes that may wait for their answer at once,
// given-up passes too, so a temp dir that stops answering holds up at most this many
// goroutines and copied handles (claustrum's own, not measured).
const tempRootGivenPassCap = 1

// givenPass is one look at the copies of the given-back folders. done closes once
// they all answered, and answered is set then, under tempRoots.mu.
type givenPass struct {
	probes   map[*heldTempRoot]*heldTempRoot
	done     chan struct{}
	answered bool
}

// startGivenPass copies the given-back folders and looks at the copies in a goroutine.
// A pass that the loop gave up closes its own copies once they answer. The caller
// holds tempRoots.mu.
func startGivenPass() *givenPass {
	p := &givenPass{probes: map[*heldTempRoot]*heldTempRoot{}, done: make(chan struct{})}
	for _, g := range tempRoots.given {
		if c, _ := checkCopy(g); c != nil {
			p.probes[g] = c
		}
	}
	tempRoots.givenPass = p
	tempRoots.givenOpen++
	go func() {
		for _, c := range p.probes {
			checkTempRootTimed(c)
		}
		tempRoots.mu.Lock()
		p.answered = true
		tempRoots.givenOpen--
		mine := tempRoots.givenPass != p
		tempRoots.mu.Unlock()
		close(p.done)
		if mine {
			closeProbes(p.probes)
		}
	}()
	return p
}

// abandonGivenPass gives up the running pass. It returns the copies to close when they
// already answered, and otherwise the pass closes them itself. The caller holds
// tempRoots.mu.
func abandonGivenPass() map[*heldTempRoot]*heldTempRoot {
	p := tempRoots.givenPass
	tempRoots.givenPass = nil
	if p != nil && p.answered {
		return p.probes
	}
	return nil
}

// checkCopy gives a copy of the folder g for a check with no lock, with its own handle,
// or nil and the error when the handle cannot be copied. The copy of a held folder carries
// its failed state and its count of change lines, which the check gives back (takeCheck).
// The caller holds tempRoots.mu. The copy is close-on-exec from the start (F_DUPFD_CLOEXEC),
// so a child that starts at the same moment cannot inherit it.
func checkCopy(g *heldTempRoot) (*heldTempRoot, error) {
	fd, err := unix.FcntlInt(g.f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := &heldTempRoot{f: os.NewFile(uintptr(fd), g.name), dev: g.dev, ino: g.ino, dir: g.dir, name: g.name,
		uid: g.uid, given: g.given, probe: true, xdev: g.xdev, dirLink: g.dirLink}
	if !g.given {
		c.checkFailed, c.changes = g.checkFailed, g.changes
	}
	return c, nil
}

// tempRootChecking is held by the loop of the checks from the copies of the held folders
// to the end of takeCheck, so only one check of the held folders runs at a time. A test
// takes it to keep the checks out.
var tempRootChecking sync.Mutex

// heldCheck holds the copies of the held folders for one check, by folder, the error of
// each held folder whose handle could not be copied, and the held and the given-back
// folders in the order of the hold.
type heldCheck struct {
	order  []*heldTempRoot
	copies map[*heldTempRoot]*heldTempRoot
	failed map[*heldTempRoot]error
}

// heldCopies copies each held folder for a check with no lock, and lists the held and
// the given-back folders in the order of the hold. The caller holds tempRoots.mu.
func heldCopies() heldCheck {
	k := heldCheck{copies: map[*heldTempRoot]*heldTempRoot{}, failed: map[*heldTempRoot]error{}}
	for _, h := range tempRoots.held {
		c, err := checkCopy(h)
		if c != nil {
			c.of, k.copies[h] = h, c
		} else {
			k.failed[h] = err
		}
		k.order = append(k.order, h)
	}
	k.order = append(k.order, tempRoots.given...)
	slices.SortFunc(k.order, func(a, b *heldTempRoot) int { return cmp.Compare(a.seq, b.seq) })
	return k
}

// takeCheck gives the folder h the result of the check of its copy c: the failed state,
// the count of change lines, the handle of an own folder that the check held in its
// place, and a loss, which ends each child that asked for that (markTempRootLost). A
// folder given back during the check takes only that handle and the loss. It keeps the
// flock of a folder that the check made anew or held at its name, as a given-back folder
// keeps its flock (4c111e86, measured on Linux; the give-back during the check is
// claustrum's own). Otherwise the handle of the copy shares the open file of h, so its
// close keeps the flock of h. A folder that is lost or no longer checked takes nothing.
// The caller holds tempRoots.mu.
func takeCheck(h, c *heldTempRoot) {
	if h.lost.Load() || (tempRoots.held[filepath.Join(h.dir, h.name)] != h && !slices.Contains(tempRoots.given, h)) {
		_ = c.f.Close()
		c.f = nil // a remade folder of a record that left the lists keeps no flock
		return
	}
	if !h.given {
		h.checkFailed, h.changes = c.checkFailed, c.changes
	}
	if c.dev != h.dev || c.ino != h.ino {
		_ = h.f.Close()
		h.f, h.dev, h.ino, c.f = c.f, c.dev, c.ino, nil
	}
	if c.lossWhy != "" {
		markTempRootLost(h, c.lossWhy, c.lossWord)
	}
}

// closeProbes closes the handles of the copies of a check.
func closeProbes(probes map[*heldTempRoot]*heldTempRoot) {
	for _, p := range probes {
		_ = p.f.Close()
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
	tempRootDirGoneLine     = "[process.Manager] Claude Code's private temp folder %s: the temp dir is gone; nobody is ended over that, and it is looked at again"
	tempRootOwnerShutLine   = "[process.Manager] Claude Code's private temp folder %s: its owner cannot use it (chmod 500 it); nobody is ended over that, and it is looked at again"
	tempRootChangeCapLine   = "[process.Manager] Claude Code's private temp folder %s: its state keeps changing, and is logged no more unless it is lost"
	tempRootNoAnswerLine    = "[process.Manager] Claude Code's private temp folder %s: its name does not answer; nobody is ended over that, and it is looked at again"
)

// tempRootNameWait is the time that one check of a folder gets before the line that its
// name does not answer. On a dead NFS mount 4c111e86 logs that line once, for a held
// folder and for one given back (measured on Linux). The bound of 4 s is claustrum's
// own. A variable for the tests.
var tempRootNameWait = 4 * time.Second

// checkTempRootTimed runs checkTempRoot(h). A check that has not returned after
// tempRootNameWait logs the line that the name of h does not answer. Once that check
// returns with all well, it logs the line that all is well with h again (measured on
// Linux, 4c111e86). These are claustrum's own, not measured: the line that the name does
// not answer does not count toward tempRootChangeCap, and for a held folder a check that
// returns with a failure or a loss logs its own line in place of the second one. For a held
// folder the second line counts toward tempRootChangeCap (logTempRootChange). A given-back
// folder is looked at through a new copy each pass, so its second line is not capped.
func checkTempRootTimed(h *heldTempRoot) {
	failedBefore := h.checkFailed
	logged := make(chan struct{})
	t := time.AfterFunc(tempRootNameWait, func() {
		logging.Infof(tempRootNoAnswerLine, h.name)
		close(logged)
	})
	checkTempRoot(h)
	if t.Stop() {
		return
	}
	<-logged
	switch {
	case failedBefore || h.checkFailed || h.lost.Load() || h.lossWhy != "" || h.gone:
	case h.given:
		logging.Infof(tempRootWellAgainLn, h.name)
	default:
		logTempRootChange(h, tempRootWellAgainLn, h.name)
	}
}

// tempRootChangeCap is the count of change lines of one hold of a folder: a failed-check
// line or the line that all is well again. 4c111e86 logs 49 lines of a folder of mode 0 and
// of all is well again, then the cap line in place of the 50th, and no change line after
// (measured on Linux). Not measured, claustrum's own: the cap on macOS, a count for each
// folder, the lines of a gone or unreadable temp dir in the count, the line of a loss after
// the cap, and a new count when a given-back folder is held again.
const tempRootChangeCap = 50

// logTempRootChange logs a change line of h, or the cap line in place of the change
// at tempRootChangeCap, or nothing after it.
func logTempRootChange(h *heldTempRoot, format string, args ...any) {
	h.changes++
	switch {
	case h.changes < tempRootChangeCap:
		logging.Infof(format, args...)
	case h.changes == tempRootChangeCap:
		logging.Infof(tempRootChangeCapLine, h.name)
	}
}

// mayWrite reports whether a check of h may write to the folder or to its name: h is
// held and was not given back since its copy was made (claustrum's own). A give-back
// right after this test can still meet each write that follows it: the remake of a
// missing folder (Mkdirat, then takeTempRootAt), the hold of an own folder at the name
// (takeTempRootAt: fitTempRoot, Fchmod, UtimesNanoAt and the exclusive flock), or the
// new t of a folder (fitTempRoot). takeCheck then gives the given-back folder the handle
// of the copy, so it keeps that flock.
func (h *heldTempRoot) mayWrite() bool {
	return !h.given && (h.of == nil || !h.of.handedBack.Load())
}

// tempRootCheckDev gives the device of st in a check. A seam for the tests, as a
// test cannot mount. Production never reassigns it.
var tempRootCheckDev = statDev

// checkTempRoot checks the temp dir and the held folder by its name in it, with no
// link followed. h is a copy (checkCopy), so the caller holds no lock. These rows are
// measured on 4c111e86:
//
//   - A link at the name, a folder of another account (uid 0), or a folder of mode
//     0777 counts as lost (Linux and macOS). The "open" test takes a group or other
//     write bit (claustrum's own, only 0777 is measured). The words of the lost list
//     are "changed", "anothers" and "open" (macOS).
//   - On macOS these count as lost too: a file at the name, a mount on the held
//     folder or on its t, a t that is a file or a link, a temp dir that is a file or
//     a link or a new mount, and a temp dir of mode 0777, also of root.
//   - An own folder that took the place of the held one is held instead, and gets t
//     and mode 0500 (Linux and macOS). No child is ended.
//   - With no entry at the name, the folder is made anew (macOS). No child is ended.
//   - A held folder whose t was removed gets t and mode 0500 again, with no line
//     (macOS). A t of mode 0777 changes nothing (Linux and macOS).
//   - A temp dir that cannot be read logs the failed-check line, and a later good check
//     logs the line that all is well. No child is ended, also after 320 s (Linux).
//   - A temp dir that is gone, and a held folder of mode 0, log their own line and
//     end no child (macOS). The mode 0 line comes once for a run of checks that find
//     mode 0 (Linux). A sticky temp dir of mode 1777 changes nothing (macOS).
//
// Not measured, claustrum's own: one line per run of other failed checks, the gone temp
// dir and the folder of mode 0 as failed checks, the order of the tests, the word
// "changed" for the kinds of the macOS rows, an other write bit of the temp dir in a
// mode other than 0777 (a group write bit alone ends nobody), an
// owner read or search bit of the folder, and the prefix of the gone line and, on
// macOS, of the mode 0 line.
func checkTempRoot(h *heldTempRoot) {
	failed := func(format string, args ...any) {
		if !h.checkFailed && !h.given {
			logTempRootChange(h, format, args...)
		}
		h.checkFailed = true
	}
	var lst unix.Stat_t
	if err := unix.Lstat(noTrailingSlash(h.dir), &lst); errors.Is(err, unix.ENOENT) {
		failed(tempRootDirGoneLine, h.name)
		h.gone = h.given
		return
	} else if k := lst.Mode & unix.S_IFMT; err == nil && k != unix.S_IFDIR && (k != unix.S_IFLNK || !h.dirLink) {
		loseTempRoot(h, "it is in a temp dir that is no longer a folder", "changed")
		return
	}
	xfd, err := unix.Open(h.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	var xst, st unix.Stat_t
	missing := false
	if err == nil {
		defer func() { _ = unix.Close(xfd) }()
		err = unix.Fstat(xfd, &xst)
	}
	if err == nil {
		switch {
		case tempRootCheckDev(&xst) != h.xdev:
			loseTempRoot(h, "it is in a temp dir whose path now leads to another file system (a new mount?)", "changed")
			return
		case xst.Mode&0o002 != 0 && xst.Mode&unix.S_ISVTX == 0:
			loseTempRoot(h, "it is in a temp dir that another account can rearrange", "changed")
			return
		}
		err = unix.Fstatat(xfd, h.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			err, missing = nil, true
		}
	}
	if err != nil {
		failed(tempRootCheckFailedLine, h.name, err)
		return
	}
	if !missing && st.Mode&unix.S_IFMT == unix.S_IFDIR && int(st.Uid) == h.uid && st.Mode&0o022 == 0 &&
		statDev(&st) == h.dev && st.Ino == h.ino && statMode(&st)&0o500 != 0o500 {
		failed(tempRootOwnerShutLine, h.name)
		return
	}
	if h.checkFailed && !h.given {
		h.checkFailed = false
		logTempRootChange(h, tempRootWellAgainLn, h.name)
	}
	switch {
	case missing && h.given:
		// A folder that was given back is not made anew, nor held instead below. Its
		// checks end.
		h.gone = true
	case missing:
		if !h.mayWrite() {
			return
		}
		// No folder is made anew in a temp dir that group or others can write without
		// the sticky bit, or that an account other than h.uid and root owns, as for a
		// new folder (openTempDirChain). That gate and no line then are claustrum's own,
		// not measured.
		if (xst.Mode&0o022 != 0 && xst.Mode&unix.S_ISVTX == 0) || (int(xst.Uid) != h.uid && xst.Uid != 0) {
			return
		}
		logging.Infof(tempRootRemadeLine, h.name)
		if unix.Mkdirat(xfd, h.name, 0o700) == nil {
			takeTempRootAt(h, xfd)
		}
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		loseTempRoot(h, "it is a link", "changed")
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		loseTempRoot(h, "it is not a folder", "changed")
	case int(st.Uid) != h.uid:
		loseTempRoot(h, fmt.Sprintf("it is another account's (uid %d)", st.Uid), "anothers")
	case st.Mode&0o022 != 0:
		loseTempRoot(h, "it is open to other accounts", "open")
	case tempRootCheckDev(&st) != tempRootCheckDev(&xst):
		loseTempRoot(h, "it is on another file system", "changed")
	case statDev(&st) != h.dev || st.Ino != h.ino:
		if h.mayWrite() {
			logging.Infof(tempRootSwapLine, h.name)
			takeTempRootAt(h, xfd)
		}
		h.gone = h.given
	default:
		checkChildDir(h, &st)
	}
}

// noTrailingSlash cuts the trailing slashes of dir, so that an lstat looks at its
// last name and follows no link there. "/" stays "/".
func noTrailingSlash(dir string) string {
	if d := strings.TrimRight(dir, "/"); d != "" {
		return d
	}
	return dir
}

// checkChildDir checks the t of the held folder, whose stat is st. A t that is gone
// is made again (fitTempRoot). A t that is a link, no folder or on another file
// system counts as lost (4c111e86, measured on macOS).
func checkChildDir(h *heldTempRoot, st *unix.Stat_t) {
	fd := int(h.f.Fd())
	var tst unix.Stat_t
	err := unix.Fstatat(fd, tempRootChildDir, &tst, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
		if h.mayWrite() {
			fitTempRoot(fd)
		}
	case err != nil:
	case tst.Mode&unix.S_IFMT == unix.S_IFLNK:
		loseTempRoot(h, "it is holding a t that is a link", "changed")
	case tst.Mode&unix.S_IFMT != unix.S_IFDIR:
		loseTempRoot(h, "it is holding a t that is not a folder", "changed")
	case tempRootCheckDev(&tst) != tempRootCheckDev(st):
		loseTempRoot(h, "it is holding a t that is on another file system", "changed")
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

// loseTempRoot marks h lost, or for a copy keeps why and word for the check
// (markTempRootLost).
func loseTempRoot(h *heldTempRoot, why, word string) {
	if h.probe {
		h.lossWhy, h.lossWord = why, word
		return
	}
	markTempRootLost(h, why, word)
}

// markTempRootLost marks h lost. The next spawn in its temp dir makes a new folder, and
// each later reply with a member lists it with the word word, from the end of the
// check. Each child that asked with cliTempRootEnd is ended. For the others one line
// counts them (4c111e86, see checkTempRoot). The caller holds tempRoots.mu.
func markTempRootLost(h *heldTempRoot, why, word string) {
	logging.Infof(tempRootLostLine, h.name, why)
	h.lost.Store(true)
	key := filepath.Join(h.dir, h.name)
	tempRoots.lost[key] = true
	if tempRoots.held[key] == h {
		delete(tempRoots.held, key)
	}
	tempRoots.checkLost = append(tempRoots.checkLost, goneTempRoot{name: h.name, why: word, at: time.Now()})
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
