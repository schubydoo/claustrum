//go:build linux || darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The orphan reap (reference build 19f30c46). At -serve startup, right after a daemon
// claims its run dir (which evicts a still-live predecessor on the same socket), it
// reaps children that a since-exited predecessor daemon of THIS run dir left behind. The
// daemon reads the per-spawn records the child registry wrote (childRecord, written by
// writeChildRecordIn to <runDir>/children/<pid>.json) and, for each one whose owning daemon
// is gone, verifies the live process really is that recorded child before ending its
// process group.
//
// This decision logic is shared across the unix targets; only how a live process is read
// (readLiveProc: /proc on linux, ps on darwin) and how this daemon's own node/host
// identity is gathered (ownReapIdentity) are OS-specific. On windows reapOrphans is a no-op
// (childreap_other.go): the reference reaps nothing there (VM-measured, planted records
// survive a startup), so there is no reap to share.
//
// This path is DESTRUCTIVE: a wrong reap ends a live process. So a record is reaped only
// when every guard below holds, and the kill and the live-process reads go through
// package-var seams so unit tests exercise the whole decision without ending a real
// process. The real reap runs only from a daemon at startup.
//
// The safety invariant: reap a process only when it is (a) the same process the record
// names (its start-time still matches, so the pid was not reused), (b) its own process
// group leader, (c) running the recorded program, or the program that the record's
// launcher was given (recordMatchesLive), (d) tagged with OUR run dir in its
// environment, and (e) marked as a direct daemon child (its CLAUDE_SSH_CHILD names its
// own pid and start). A record whose owning daemon is still alive, or that belongs to
// another boot or machine, is never reaped. Only a regular file is read as a record.

// reapGrace is how long the reap waits for a group to exit after SIGTERM before it
// escalates to SIGKILL. Measured on Linux against 89cb6289: the SIGKILL comes 2.04 to
// 2.09 s after the start of the daemon (rows RP02, RP03, RP04, RP10). reapEscalate is how
// long it waits after SIGKILL. That value is claustrum's own, not probe-measured.
const (
	reapGrace    = 2 * time.Second
	reapEscalate = 1 * time.Second
	// reapForeignTTL is how old a record from another boot or machine must be before the
	// reap forgets it. A younger foreign record is left in place, since a live process on
	// another boot cannot be verified from here. claustrum's own value, not probe-measured.
	reapForeignTTL = 30 * 24 * time.Hour
	// reapPollInterval is how often each wait re-checks a signaled group for exit.
	// claustrum's own value, not probe-measured.
	reapPollInterval = 100 * time.Millisecond
	// reapSettle is the pause before a second read of a process. A read can catch a
	// process in the middle of a change, and the pause lets that change end. claustrum's
	// own value, not probe-measured.
	reapSettle = 50 * time.Millisecond
)

// maxReapTargets caps how many groups one start signals: 64. More verified groups stay
// for the next start, with one line. Measured on Linux against 89cb6289 (row C4: 70
// recorded children, 64 SIGTERM at the first start and 6 records left). A var so a test
// can shrink it to exercise the overflow path.
var maxReapTargets = 64

// maxReapRecords is how many record names one start handles: 128. One more name ends
// the walk with one line, and the rest stays for the next start. Measured on Linux
// against 89cb6289 (row C1: 131 names, the first 128 in directory order are gone after
// the first start and the last 3 after the second). A record that the daemon keeps
// counts too (row P10: 128 kept records and then one live orphan. In two starts,
// 89cb6289 and claustrum sent no signal and removed no record).
const maxReapRecords = 128

// maxRecordBytes is how much of a record file the reap reads: 4096 bytes. A record
// whose first 4096 bytes are not one JSON value is removed, with no signal and no line.
// Measured on Linux against 89cb6289: records of 5259 and 5290 bytes were removed with
// no signal and no line while their children lived (rows C3a, C3b). Both held an argv0
// of 5000 characters, so the rows do not tell a read limit from a field limit. A valid
// record with blanks after it, 2 MiB in all, was read and its child got SIGTERM (row
// ED14g). Row P8 measured the bound with a pad in an added last field: records of
// 4095 and 4096 bytes got SIGTERM, and records of 4097 and 5000 bytes got no signal
// and no line and were removed. 89cb6289 and claustrum did the same in each size.
const maxRecordBytes = 4096

// maxPid is the largest usable pid (2^31 - 1). With the `pid < 2 || pid > maxPid` test it
// yields a [2, 2^31) window.
const maxPid = 1<<31 - 1

// verdict values from verifyOrphan (the per-record decision).
const (
	verdictReap        = 0 // a verified orphan of a dead predecessor daemon of this run dir
	verdictGone        = 1 // the live process is gone; forget the record, nothing to end
	verdictSkip        = 2 // skip with a reason (logged); forget the record
	verdictPassthrough = 3 // the process disowns us; skip WITHOUT forgetting the record
)

// live process states from readLiveProc.
const (
	procAlive   = 0 // the process is readable and running
	procGone    = 1 // the process is absent, or a zombie or dead
	procNotOurs = 2 // the process is unreadable, or not this daemon's to judge
)

// liveProc is the live process state the reap reads to judge a record. The env fields are
// populated only when readLiveProc is called with wantEnv true (the full verify), not for
// the lighter start-time and leadership re-checks.
type liveProc struct {
	state      int    // procAlive, procGone, or procNotOurs
	startTicks string // the process start-time, compared as a string to childRecord.Start
	pgid       int    // the process group id
	program    string // argv[0]
	runDir     string // CLAUDE_SSH_RUN_DIR from the environment (wantEnv only)
	childMark  string // CLAUDE_SSH_CHILD from the environment (wantEnv only)
	// args is the arguments after the program. On linux they are the real arguments,
	// each one whole (wantEnv only). On darwin they are the words of cmdText after the
	// first. A record with a program key is matched against them, see recordMatchesLive.
	args []string
	// cmdText is the command text that `ps` prints for the process. Only the darwin
	// reader sets it. It is empty on linux.
	cmdText string
	// unreadable says why a wantEnv read gave procNotOurs, when the reader knows a
	// text for it. Only the linux reader sets it, for an environment it cannot read.
	unreadable string
}

// Seams. Each is a package var so a unit test replaces it to drive the decision tree and
// the two-phase escalation with no real process, no real kill, and no real wait.
var (
	// readLiveProc reads a live process. wantEnv also reads the program and env markers.
	// realReadLiveProc is OS-specific (childreap_linux.go / childreap_darwin.go).
	readLiveProc = realReadLiveProc
	// killGroup signals the whole process group led by pid (kill(-pid, sig)), the reap's
	// only destructive call.
	killGroup = func(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }
	// groupAlive reports whether the process group led by pid still has any member
	// (kill(-pid, 0) succeeds). It stays true after the leader exits while a group member
	// lingers, so the wait can clean up a survivor the leader's exit would otherwise hide.
	groupAlive = func(pid int) bool { return syscall.Kill(-pid, 0) == nil }
	// reapSleep is the delay between wait polls.
	reapSleep = time.Sleep
	// reapNow is the clock for the foreign-record age check and the wait deadline.
	reapNow = time.Now
)

// reapTarget is one process the sweep decided to end. start is kept for the pid-reuse
// re-check just before the kill, and name is the record to forget once handled. rec is
// the whole record, for the full test that comes before the SIGKILL.
type reapTarget struct {
	pid   int
	start string
	name  string
	rec   childRecord
}

// reapCounts is the tally of one sweep. The six fields are the six numbers of the
// summary line, in its order (see reapSummary). Measured on Linux and macOS against
// 89cb6289 (rows A4, C1, C4 and D1).
type reapCounts struct {
	signalled      int // groups that got SIGTERM
	reapedGrace    int // signalled groups that ended in the wait after SIGTERM ("ended on SIGTERM")
	reapedEscalate int // groups that got the SIGKILL at the end of the wait and ended ("on SIGKILL")
	survived       int // groups still alive after that SIGKILL and its wait
	forgotten      int // records dropped with no group ended ("stale record(s) dropped")
	skipped        int // records left in place ("kept"): owner alive, foreign and fresh, disowning, or over the 64 groups
}

// reapSummary is the summary line of a sweep. The text is the reference's, with its
// long dash. It prints when the sweep sent a SIGTERM or kept a record, and not for a
// sweep that only dropped records (89cb6289, Linux rows C1 start 2, ED01a and ED12a).
//
// Not measured: which number a record takes when no row shows it. claustrum counts
// every record that it removes with no SIGTERM in "dropped": a dead pid, a record with
// a reason line, a record that does not parse, an entry that is not a regular file, a
// record of an earlier boot, and a group that no longer verifies after its SIGTERM. The
// rows show the first two and the last (RP07, C1, PG05a).
//
// The levels of the reap lines are claustrum's own choice. The reference prints no
// level. Not measured: the place of a record line when the 128 line and the 64 line
// print too. claustrum prints the 128 line, then the record lines, then the 64 line.
func reapSummary(n reapCounts) string {
	return fmt.Sprintf("[daemon] serve: predecessor's children — %d orphaned group(s): %d ended on SIGTERM, %d on SIGKILL, %d survived; %d stale record(s) dropped, %d kept",
		n.signalled, n.reapedGrace, n.reapedEscalate, n.survived, n.forgotten, n.skipped)
}

// reapCtx is what one sweep works with: the children folder, held open, the run dir
// path that a child carries in its environment, and this daemon's own pids.
//
// Every read and every delete of the sweep goes through dir, with a bare entry name.
// So the sweep never leaves the children folder that it opened, also when someone
// swaps the children entry of the run dir while the sweep runs.
type reapCtx struct {
	dir                                 *os.Root
	runDir                              string
	ownDaemonPid, ownParentPid, ownPgid int
	// late holds the record names of the signalled groups that are done. The sweep
	// removes them together when the last group is done (forgetLate).
	late []string
}

// openChildrenDir opens the children folder below root, which is the run dir. The
// entry must be a real folder. A symlink is not followed, so a children entry that
// leads out of the run dir is never listed. Measured on Linux (rows SYa and SYb):
// with a children entry that is a symlink to a folder, outside or inside the run dir,
// f6010b97 and 89cb6289 send no signal and remove nothing. No other shape of the
// entry is measured.
func openChildrenDir(root *os.Root) (*os.Root, error) {
	fi, err := root.Lstat(childrenDirName)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errChildrenNotDir
	}
	return root.OpenRoot(childrenDirName)
}

// reapOrphans is the -serve startup reap. It lists the children folder of the run dir
// that the daemon holds open, decides each record, ends the verified orphans in two
// phases, and logs a summary. It runs on every socket shape: measured on Linux and
// macOS against f6010b97 and 89cb6289, a new daemon on a socket of another shape ends
// the recorded child of a dead daemon (rows EV01a to EV01i). It is a no-op for a
// manager with no run dir, and when the children entry is absent or is not a real
// folder.
func (m *procManager) reapOrphans() {
	if m.runDir == "" {
		return
	}
	root, err := m.runDirRoot()
	if err != nil {
		return
	}
	dir, err := openChildrenDir(root)
	if errors.Is(err, errChildrenNotDir) {
		// The text is the reference's. Rows SYa and SYb measure it on f6010b97 and
		// 89cb6289 for a symlink to a folder, before the listening line. claustrum
		// prints it for every children entry that is not a real folder. The other
		// shapes are not measured. The level is that of the sibling line of a spawn.
		logErrorf("[process.Registry] %s/%s is not a directory; reaping nothing", m.runDir, childrenDirName)
	}
	if err != nil {
		return // no registry dir means nothing was ever recorded here
	}
	defer dir.Close()
	childrenPath := m.runDir + "/" + childrenDirName
	ownInstance := m.instanceID
	c := &reapCtx{dir: dir, runDir: m.runDir, ownDaemonPid: os.Getpid(), ownParentPid: os.Getppid()}
	c.ownPgid, _ = syscall.Getpgid(c.ownDaemonPid)
	ownNode, ownHost := ownReapIdentity()

	entries, more := c.recordEntries()
	if more {
		logWarnf("[process.Registry] more than %d records in %s; leaving the rest for the next start", maxReapRecords, childrenPath)
	}
	var counts reapCounts
	var targets []reapTarget
	over := 0 // verified groups over maxReapTargets
	for _, e := range entries {
		name := e.Name()
		// Only a regular file is a record. A symlink, a FIFO or a folder with a .json
		// name is removed and never opened, so a record outside children/ cannot make
		// the reap signal a process, and a FIFO cannot hold the start. The removal is
		// one plain remove of the entry itself: a symlink goes and its target stays,
		// and a folder goes only when it is empty. Measured on Linux (rows ED16a to
		// ED16c): the reference removes each of the three, sends no signal and logs
		// no line.
		if !e.Type().IsRegular() {
			c.forget(name)
			counts.forgotten++
			continue
		}
		rec, ok := c.read(name)
		// A record that will not parse, names an out-of-range pid, or does not sit at its
		// own "<pid>.json" path is dropped. (A leftover temp name does not end in ".json".)
		if !ok || rec.Pid < 2 || rec.Pid > maxPid || name != strconv.Itoa(rec.Pid)+".json" {
			c.forget(name)
			counts.forgotten++
			continue
		}
		if rec.Instance == ownInstance {
			counts.skipped++ // our own child; leave it running
			continue
		}
		if rec.Node == ownNode {
			// Same boot and machine: a predecessor of this run dir. Reap its children only
			// once that daemon itself is gone.
			if daemonAlive(rec.DaemonPid, rec.DaemonStart, c.ownDaemonPid) {
				counts.skipped++
				continue
			}
			verdict, why := c.verify(rec)
			switch verdict {
			case verdictReap:
				if len(targets) < maxReapTargets {
					targets = append(targets, reapTarget{pid: rec.Pid, start: rec.Start, name: name, rec: rec})
				} else {
					over++
					counts.skipped++ // batch full; leave for the next startup
				}
			case verdictGone:
				c.forget(name)
				counts.forgotten++
			case verdictSkip:
				// The text is the reference's, with its reason (see verify). The record
				// goes at once, before any signal of this sweep.
				logInfof("[process.Registry] record %s: pid %d is not a child to end (%s); dropping the record, signalling nothing", name, rec.Pid, why.ref)
				c.forget(name)
				counts.forgotten++
			case verdictPassthrough:
				// This line is claustrum's own. No row shows a record that the
				// reference keeps for this cause at the first test.
				logInfof("[process.Reaper] child %d (%s): %s", rec.Pid, name, why.own)
				counts.skipped++ // the process disowns us; keep the record
			}
			continue
		}
		// Another boot or machine. Forget a record from an earlier boot of THIS machine, and
		// forget a foreign record only once it is past the stale window.
		if earlierBootOfThisMachine(rec.Host, rec.Node, ownHost, ownNode) {
			c.forget(name)
			counts.forgotten++
			continue
		}
		if rec.At != 0 && reapNow().Sub(time.UnixMilli(rec.At)) < reapForeignTTL {
			counts.skipped++ // foreign and fresh; cannot verify it from here
			continue
		}
		c.forget(name)
		counts.forgotten++
	}
	if over > 0 {
		logWarnf("[process.Registry] %d more orphaned group(s) recorded in %s than the %d one start ends; leaving those for the next start", over, childrenPath, maxReapTargets)
	}

	reapTargets(c, targets, &counts)

	if counts.signalled > 0 || counts.skipped > 0 {
		logInfof("%s", reapSummary(counts))
	}
}

// recordEntries lists the children folder in directory order, the order of `ls -U`,
// and returns its first maxReapRecords entries with a .json name. more says that one
// more such name exists: the walk ends there. A listing that fails part of the way
// still gives the entries before the failure.
//
// Measured on Linux against 89cb6289. The names that one start handles are the first
// 128 of the directory order, not of the name order (row C1). The SIGTERM calls go out
// in that order too (rows C2 and C4). An entry with another name stays: a file
// <pid>.txt is there after the start (row ED15b). Not measured: whether such a name
// counts among the 128. claustrum does not count it.
func (c *reapCtx) recordEntries() (entries []fs.DirEntry, more bool) {
	f, err := c.dir.Open(".")
	if err != nil {
		return nil, false
	}
	defer f.Close()
	for {
		batch, err := f.ReadDir(256)
		for _, e := range batch {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if len(entries) == maxReapRecords {
				return entries, true
			}
			entries = append(entries, e)
		}
		if err != nil {
			return entries, false // io.EOF, or a listing that failed part of the way
		}
	}
}

// skipReason says why a record is not a child to end, in two wordings. own is
// claustrum's wording, which verifyOrphan returns. ref is the wording of 89cb6289 for
// the same test, measured on Linux, which the two [process.Registry] lines print: the
// record line of the walk and the no-longer-verifies line. Where no row shows a
// reference text, ref equals own.
type skipReason struct{ own, ref string }

// sameReason is a reason with no measured reference text.
func sameReason(text string) skipReason { return skipReason{own: text, ref: text} }

// verifyOrphan is the record test with claustrum's wording only.
func verifyOrphan(rec childRecord, runDir string, ownDaemonPid, ownParentPid, ownPgid int) (int, string) {
	c := &reapCtx{runDir: runDir, ownDaemonPid: ownDaemonPid, ownParentPid: ownParentPid, ownPgid: ownPgid}
	verdict, why := c.verify(rec)
	return verdict, why.own
}

// verify decides one record whose owning daemon is gone and whose node matches ours.
// It returns a verdict and, for a skip, the reason. The reference texts are measured on
// Linux against 89cb6289 (rows RP07, ED01a, ED02a, ED04b, ED07, ED10a to ED10d, and
// PG05a for the unreadable environment). claustrum quotes each value with %q. Not
// measured: a value that %q escapes.
func (c *reapCtx) verify(rec childRecord) (int, skipReason) {
	pid := rec.Pid
	switch {
	case pid < 2 || pid > maxPid:
		return verdictSkip, sameReason("pid is out of range")
	case pid == c.ownDaemonPid:
		return verdictSkip, sameReason("pid is this daemon")
	case pid == c.ownParentPid || (c.ownPgid > 1 && pid == c.ownPgid):
		return verdictSkip, sameReason("pid is this daemon's parent or process group")
	case rec.Start == "":
		return verdictSkip, sameReason("record has no start time to verify against")
	case rec.Argv0 == "":
		return verdictSkip, skipReason{"record names no program to verify against", "the record names no program to verify against"}
	}

	lp := readLiveProc(pid, true)
	switch lp.state {
	case procGone:
		return verdictGone, skipReason{}
	case procNotOurs:
		// The process disowns this daemon (another pid namespace on linux, or an unreadable
		// live-process read). Keep the record and let a daemon that owns it decide.
		//
		// A read can also fail because the process has just ended. So this arm settles
		// like the skips below: a process that is gone after the pause has ended, and is
		// never one that no longer verifies (Linux rows K5 and ED04a).
		if verdict, _ := settleThenVerdict(pid, verdictPassthrough, ""); verdict == verdictGone {
			return verdictGone, skipReason{}
		}
		why := sameReason("process is not in this daemon's namespace")
		if lp.unreadable != "" {
			why.ref = lp.unreadable
		}
		return verdictPassthrough, why
	}

	skip := func(own, ref string) (int, skipReason) {
		verdict, _ := settleThenVerdict(pid, verdictSkip, own)
		if verdict == verdictGone {
			return verdictGone, skipReason{}
		}
		return verdictSkip, skipReason{own, ref}
	}
	// The pid-reuse guard: the live start-time must still match the record's. The
	// compare is exact on every system, as on 89cb6289. On macOS the start is a date
	// text with one blank between its words (darwinProcStart). A record or a
	// CLAUDE_SSH_CHILD entry of an older claustrum build holds two blanks before a day
	// of one digit. It does not match: the record is dropped and no signal goes out.
	// 89cb6289 and claustrum did the same with such a record, and with such a marker
	// (macOS rows TBr, TBe, TBmr and TBme).
	if lp.startTicks != rec.Start {
		return skip("the pid was reused", fmt.Sprintf("it did not start at the recorded %q: the pid has been reused", rec.Start))
	}
	if pid != lp.pgid {
		return skip("process is not its own group leader", "it does not lead its own process group")
	}
	if !recordMatchesLive(rec, lp) {
		ref := fmt.Sprintf("it does not run the recorded program %q", rec.Argv0)
		if rec.Program != "" {
			ref += fmt.Sprintf(" and neither runs nor was handed %q", rec.Program)
		}
		return skip("process is not running the recorded program", ref)
	}
	if lp.runDir != c.runDir {
		if lp.runDir == "" {
			return skip("process has no run-dir marker", "it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it")
		}
		return skip("process belongs to another run dir", "it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one")
	}
	if lp.childMark != strconv.Itoa(pid)+":"+lp.startTicks {
		return skip("process was not started directly by a daemon", "it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)")
	}
	return verdictReap, skipReason{}
}

// oneBlank makes each run of blanks in text one blank and removes the blanks at both
// ends. On darwin it gives the start text its recorded form (darwinProcStart).
func oneBlank(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// settleThenVerdict re-reads the process after a 50ms pause before it returns a skip, so a
// process that exited in the meantime is reported gone rather than skipped.
func settleThenVerdict(pid, verdict int, reason string) (int, string) {
	reapSleep(reapSettle)
	if readLiveProc(pid, false).state == procGone {
		return verdictGone, ""
	}
	return verdict, reason
}

// programMatches reports whether the live process runs the recorded program, by an exact
// match on the full argv[0] or on its basename.
func programMatches(recorded, live string) bool {
	if recorded == "" || live == "" {
		return false
	}
	return recorded == live || filepath.Base(recorded) == filepath.Base(live)
}

// recordMatchesLive reports whether the live process is the program that the record
// names. A record holds argv0, and a spawn with a launcher also holds program: argv0
// is then the launcher and program is the command. Three cases pass, as measured on
// Linux against 89cb6289:
//
//   - the process runs argv0, by its full argv[0] or by its base name
//   - the process runs program, by its full argv[0] or by its base name (rows PG02a,
//     ED03a, ED03b): a launcher that replaced itself with the command
//   - one whole argument of the process equals program byte for byte (rows PG02b,
//     ED04a, ED04e): a launcher that still runs and was given the command
//
// On darwin the process is judged by the command text of `ps`, so "runs" and "one
// whole argument" have the meaning that runsProgram gives.
//
// An argument does not match by its base name, by a cleaned path, or as a part of a
// longer argument (rows ED04b, ED04c, ED04d). An empty program adds nothing (rows
// ED06a, ED06b). The caller refuses an empty argv0 before it comes here, whatever
// program holds (row ED02c). f6010b97 sends no signal in rows ED03a to ED04e. On macOS
// the rows B1 to B8 measure the command text rule of runsProgram.
func recordMatchesLive(rec childRecord, lp liveProc) bool {
	if runsProgram(rec.Argv0, lp) {
		return true
	}
	if rec.Program == "" {
		return false
	}
	return runsProgram(rec.Program, lp) || slices.Contains(lp.args, rec.Program)
}

// runsProgram reports whether the live process runs the program p of a record.
//
// On linux the process is read from /proc: lp.program is argv[0] and lp.args are the
// real arguments. p matches when it equals argv[0] or has its last path component.
//
// On darwin the process is judged by the command text that `ps` prints (lp.cmdText),
// as the macOS rows of 89cb6289 do. lp.program is the first word of that text and
// lp.args are the other words, cut at blanks. p matches in three cases:
//
//   - p equals the whole command text (a program with a blank in its path and no
//     arguments)
//   - the command text starts with p and one blank (rows PG04s and PG02s: a program
//     at "/tmp/e/bin/my stub" is reaped)
//   - p has the last path component of the first word (row PG05b: the same base name
//     in another folder)
//
// A program key with a blank is never "handed" on darwin, because no single word
// equals it (row ED04e: no signal). On linux it is, because the argument is whole
// (Linux row ED04e: SIGTERM).
//
// One more case follows from the rule: a record that names "/tmp/e/bin/my" matches a
// live process that runs "/tmp/e/bin/my stub", on darwin. Its first word is that path.
// The macOS rows B2 and B2f measure it: SIGTERM on 89cb6289 and on claustrum.
func runsProgram(p string, lp liveProc) bool {
	if programMatches(p, lp.program) {
		return true
	}
	return p != "" && lp.cmdText != "" && (p == lp.cmdText || strings.HasPrefix(lp.cmdText, p+" "))
}

// splitCommandText cuts the command text of `ps` into its words: the program, which is
// the first word, and the arguments. A word ends at a blank, the space character, so two
// blanks give an empty word. The macOS rows B4a to B4c measure two blanks and a tab, equal
// on 89cb6289 and claustrum. There `ps` prints a tab as the four characters \011.
func splitCommandText(text string) (program string, args []string) {
	words := strings.Split(text, " ")
	return words[0], words[1:]
}

// parsePSLine cuts one line of `ps -o pgid=,stat=,command=` into the group id, the
// state and the command text. `ps` pads the first two columns with blanks. The command
// text is the rest of the line after those blanks, with its own blanks kept. ok is
// false for a line with fewer than three parts.
func parsePSLine(line string) (pgid int, stat, text string, ok bool) {
	first, rest, found := strings.Cut(strings.TrimLeft(line, " "), " ")
	if !found {
		return 0, "", "", false
	}
	stat, rest, found = strings.Cut(strings.TrimLeft(rest, " "), " ")
	text = strings.TrimLeft(rest, " ")
	if !found || text == "" {
		return 0, "", "", false
	}
	pgid, _ = strconv.Atoi(first)
	return pgid, stat, text, true
}

// daemonAlive reports whether the daemon that spawned a child is still running. A daemon
// pid below 2, or equal to this daemon's own pid (a reused slot for a different instance),
// counts as gone so the child is treated as an orphan. Otherwise it is alive only when the
// process is running and, if the record carries a daemon start-time, that start-time still
// matches (a pid-reuse guard on the daemon itself).
func daemonAlive(daemonPid int, daemonStart string, ownDaemonPid int) bool {
	if daemonPid < 2 || daemonPid == ownDaemonPid {
		return false
	}
	lp := readLiveProc(daemonPid, false)
	if lp.state != procAlive {
		return false
	}
	if daemonStart == "" {
		return true
	}
	return lp.startTicks == daemonStart
}

// earlierBootOfThisMachine reports whether a record is from an earlier boot of THIS same
// machine: the machine ids (host) match but the boot part of the node differs. The node is
// "<boot>/<...>" on linux and a bare boot-session id on darwin. strings.Cut on "/" returns
// the boot prefix on linux and, when there is no "/", the whole string in `before` — so it
// yields the boot part on linux and the whole id on darwin. The found flag is deliberately
// ignored: a darwin node has no "/" yet still carries a full boot id, so gating on it would
// make this always return false on darwin and never prune an earlier boot's records there. A
// record from a different machine, or one with an empty boot id, returns false.
func earlierBootOfThisMachine(recHost, recNode, ownHost, ownNode string) bool {
	if recHost == "" || recHost != ownHost {
		return false
	}
	recBoot, _, _ := strings.Cut(recNode, "/")
	ownBoot, _, _ := strings.Cut(ownNode, "/")
	if recBoot == "" || ownBoot == "" {
		return false
	}
	return recBoot != ownBoot
}

// reapTargets ends the collected orphans in two phases: SIGTERM the group and wait the
// grace, then SIGKILL any survivor and wait the escalate. The targets come in the order
// of the walk, and the SIGTERM calls go out in that order. It re-reads the process
// (sameLeader) before the SIGTERM, so a pid reused between the sweep and the signal is
// dropped rather than signalled. During the grace every survivor takes the whole record
// test again at each poll, and once more before the SIGKILL. A group that no longer
// passes is dropped at that poll and gets nothing more. When a target's leader exits
// while a group
// member lingers, the wait SIGKILLs the group so the survivor is not left behind. It counts
// the groups that got SIGTERM and splits them into the grace and escalate
// buckets. A group still alive after the escalate is counted survived and its record is KEPT,
// so a later daemon can still find a group that this one did not end. It updates counts. (The survivor arm is claustrum's own, not probe-measured:
// a process cannot be made to outlive SIGKILL on demand.)
//
// The records of the groups that got SIGTERM go together, when the last group is done,
// and not one by one as each group ends. Measured on Linux against 89cb6289 (row RP04):
// of five groups, two ended 0.5 s and 1.5 s after the start and three got SIGKILL at
// 2.09 s, and all five records were gone at the 2.1 s poll, none earlier. A daemon that
// is killed in its wait therefore leaves the records, and the next start signals the
// groups again (row RP13).
//
// The two lines of a group are the reference's (89cb6289, Linux and macOS rows A4 and
// D1): the ending line at its SIGTERM, and the outlived line at its SIGKILL.
func reapTargets(c *reapCtx, targets []reapTarget, counts *reapCounts) {
	if len(targets) == 0 {
		return
	}
	defer c.forgetLate()
	// Phase 1: re-check identity, then SIGTERM the whole group.
	var signaled []reapTarget
	for _, t := range targets {
		switch sameLeader(t.pid, t.start) {
		case verdictGone, verdictSkip:
			// Ended, reused or no longer a group leader since the walk: never signal.
			// Not measured: claustrum counts the record as dropped.
			counts.forgotten++
			c.forget(t.name)
		default: // verdictReap: still the same live leader
			if err := killGroup(t.pid, syscall.SIGTERM); err != nil {
				counts.forgotten++
				c.forget(t.name)
				continue
			}
			logInfof("[process.Registry] ending orphaned process group %d recorded by daemon instance %q (pid %d): SIGTERM", t.pid, t.rec.Instance, t.rec.DaemonPid)
			counts.signalled++
			signaled = append(signaled, t)
		}
	}

	// Grace wait: poll, with the whole record test at each poll. A process can change
	// after the SIGTERM: it can replace itself with another program. Such a group is
	// given up at the poll that sees it, with the no-longer-verifies line, and is counted
	// in forgotten, never as reaped. Its record goes at the end of the sweep (see drop). Measured on Linux against 89cb6289 (row PG05a,
	// three sets of ten runs): in 21 runs the reference logs that line, sends nothing
	// more and drops the record. In the last two sets the line comes 0.1 to 0.3 s after
	// its start. The line and its reason are the reference's.
	//
	// In the other 9 runs the reference sent a SIGKILL within 4 ms of the SIGTERM, and
	// the child ended. claustrum waits for a settle before that signal: see settleGone.
	survivors := reapWait(c, signaled, reapGrace, &counts.reapedGrace, counts, true)

	// Phase 2: SIGKILL the groups that still pass the record test after the grace, then
	// wait the escalate. The test runs once more here, right before the signal.
	var killed, gone []reapTarget
	for _, t := range survivors {
		verdict, reason := c.check(t, true)
		switch verdict {
		case verdictReap:
			c.killAfterGrace(t)
			killed = append(killed, t)
		case verdictGone:
			gone = append(gone, t) // the leader ended between the last poll and this test
		default:
			c.drop(t, reason, counts)
		}
	}
	// A leader that is alive again after the settle pause gets one last test. If that
	// test cannot tell either, the target is dropped and gets no signal. That last arm
	// and its text are claustrum's own, not measured.
	for _, t := range c.settleGone(gone, &counts.reapedGrace) {
		verdict, reason := c.check(t, true)
		switch verdict {
		case verdictReap:
			c.killAfterGrace(t)
			killed = append(killed, t)
		case verdictGone:
			c.drop(t, "its state changed between two reads", counts)
		default:
			c.drop(t, reason, counts)
		}
	}
	for _, t := range reapWait(c, killed, reapEscalate, &counts.reapedEscalate, counts, false) {
		counts.survived++ // outlived SIGKILL (a zombie or an uninterruptible sleep)
		// This line is claustrum's own: no row has a group that outlived SIGKILL.
		logInfof("[process.Reaper] child %d (%s): still present after SIGKILL", t.pid, t.name)
		// The record is KEPT so a later daemon can find a group this one could not end.
	}
}

// killAfterGrace sends the SIGKILL to a group that still passes the record test at the
// end of the grace, with the reference's line for it.
func (c *reapCtx) killAfterGrace(t reapTarget) {
	logWarnf("[process.Registry] group %d outlived SIGTERM for %s: SIGKILL", t.pid, reapGrace)
	_ = killGroup(t.pid, syscall.SIGKILL)
}

// forgetLater notes the record of a signalled group that is done. forgetLate removes
// the noted records together.
func (c *reapCtx) forgetLater(name string) { c.late = append(c.late, name) }

// forgetLate removes the records that forgetLater noted, at the end of the sweep.
func (c *reapCtx) forgetLate() {
	for _, name := range c.late {
		c.forget(name)
	}
	c.late = nil
}

// reapWait polls up to timeout for each target's process group to exit, and tests the
// leader again on every poll. With full set it runs the whole record test (the grace).
// Without it, it tests the start value and the group only (the escalate, after the
// SIGKILL). A target
// whose leader is gone, and still gone after the settle pause, is counted reaped (via
// reapedCounter, so the caller buckets it as
// grace or escalate), and if its group still has members they are SIGKILLed; a target
// that fails the test is dropped without a signal; a target that still passes stays
// pending. It notes the record of every resolved target for the removal at the end of
// the sweep (forgetLater) and
// returns the targets still pending at the deadline.
func reapWait(c *reapCtx, targets []reapTarget, timeout time.Duration, reapedCounter *int, counts *reapCounts, full bool) []reapTarget {
	if len(targets) == 0 {
		return nil
	}
	deadline := reapNow().Add(timeout)
	pending := targets
	for {
		var still, gone []reapTarget
		for _, t := range pending {
			verdict, reason := c.check(t, full)
			switch verdict {
			case verdictReap:
				still = append(still, t) // still the recorded child; keep waiting
			case verdictGone:
				gone = append(gone, t)
			default: // no longer ours to signal
				c.drop(t, reason, counts)
			}
		}
		// A leader that is alive again after the pause stays pending. The next poll,
		// or the test before the SIGKILL, gives it the normal test.
		pending = append(still, c.settleGone(gone, reapedCounter)...)
		if len(pending) == 0 || !reapNow().Before(deadline) {
			return pending
		}
		reapSleep(reapPollInterval)
	}
}

// check tests one signalled target again. With full set it is the whole record test,
// and the reason is the reference's wording. Without it, it is sameLeader, with no
// reason. A gone verdict is not final: settleGone decides it.
func (c *reapCtx) check(t reapTarget, full bool) (int, string) {
	if !full {
		return sameLeader(t.pid, t.start), ""
	}
	verdict, why := c.verify(t.rec)
	return verdict, why.ref
}

// settleGone decides the targets whose leader read as gone at one poll. It waits
// reapSettle once for all of them and reads each leader again. A leader that is still
// gone is settled by reapGoneLeader. A leader that is alive again is returned: it is
// not counted reaped and its group gets no signal here.
//
// The pause is the guard of the group SIGKILL in reapGoneLeader. A read can take a live
// leader for gone. Seen on the development host, not on the VM: the stat of a Go
// program that replaces its program shows state Z and a vsize of 0 for a moment, on
// two reads in a row, while the group still answers a signal 0. A group SIGKILL in
// that moment ends a live child that no longer runs the recorded program. Measured on
// Linux (row PG05a): that outcome came in 2 of 10 runs of claustrum before this pause,
// and in 9 of the first 30 of 89cb6289. With the pause it came in no measured run (D20). The
// pause is a threshold: a replacement that takes longer than it still gets the SIGKILL.
//
// The read after the pause also keeps the older guard: while the pid is absent, any
// process still in group <pid> is a descendant of the original orphan, because a live
// process can hold pgid == pid only by being pid itself. If the pid was recycled as a
// new group leader in the meantime, the leader reads alive and the group gets no signal.
//
// The cost: a leader that really ended, with members that linger, gets the group
// SIGKILL one pause later (Linux row RP05a measures that SIGKILL right after the
// SIGTERM, on 89cb6289 and on claustrum before this pause).
func (c *reapCtx) settleGone(gone []reapTarget, reapedCounter *int) []reapTarget {
	if len(gone) == 0 {
		return nil
	}
	reapSleep(reapSettle)
	var alive []reapTarget
	for _, t := range gone {
		if readLiveProc(t.pid, false).state == procGone {
			reapGoneLeader(c, t, reapedCounter)
		} else {
			alive = append(alive, t)
		}
	}
	return alive
}

// drop gives up a signalled target that is no longer ours to signal, and counts it in
// forgotten. With a reason it logs the reference's line for it. Its record goes with
// the records of the other signalled groups, at the end of the sweep. That time is
// claustrum's own choice. When the reference removes such a record is not measured:
// row PG05a gives no time for it.
func (c *reapCtx) drop(t reapTarget, reason string, counts *reapCounts) {
	if reason != "" {
		logInfof("[process.Registry] group %d no longer verifies (%s); nothing more is sent to it", t.pid, reason)
	}
	counts.forgotten++
	c.forgetLater(t.name)
}

// reapGoneLeader settles a target whose leader is gone, as settleGone confirmed with its
// read after the pause right before this call: it SIGKILLs the group when members linger, counts
// the target reaped (in the bucket that reapedCounter names), and notes the record for
// the removal at the end of the sweep.
func reapGoneLeader(c *reapCtx, t reapTarget, reapedCounter *int) {
	if groupAlive(t.pid) {
		_ = killGroup(t.pid, syscall.SIGKILL) // leader gone, group members linger
	}
	*reapedCounter++
	c.forgetLater(t.name)
}

// read reads and parses the record name in the children folder. It returns the record
// and true on success, and false when the entry is unreadable, is not a regular file,
// or is not valid JSON, in which case the reap forgets the record.
//
// The entry must be a regular file itself (Lstat, which follows no symlink). The open
// does not block on a FIFO (O_NONBLOCK), and the read happens only after the open file
// proves to be that same regular file. The sweep already skips every entry that is not
// a regular file, so this is the second guard, for an entry that someone swaps between
// the directory read and the open.
func (c *reapCtx) read(name string) (childRecord, bool) {
	entry, err := c.dir.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return childRecord{}, false
	}
	f, err := c.dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return childRecord{}, false
	}
	defer f.Close()
	// The read comes last in this test, so it runs only for the entry that Lstat saw.
	var rec childRecord
	if opened, err := f.Stat(); err != nil || !os.SameFile(entry, opened) || !decodeRecord(f, &rec) {
		return childRecord{}, false
	}
	return rec, true
}

// decodeRecord reads at most maxRecordBytes of r and parses them as one record. A
// longer file parses only when the rest of its first maxRecordBytes are blanks.
func decodeRecord(r io.Reader, rec *childRecord) bool {
	data, err := io.ReadAll(io.LimitReader(r, maxRecordBytes))
	return err == nil && json.Unmarshal(data, rec) == nil
}

// forget removes one entry of the children folder. The reap calls it to drop a record
// it has handled (reaped, gone, or skipped-and-stale), and an entry that is not a
// regular file. It is one plain remove of the entry itself, by its bare name, through
// the held children folder: never recursive, and a symlink is removed, not followed.
// Best-effort: a removal error is ignored.
func (c *reapCtx) forget(name string) {
	_ = c.dir.Remove(name)
}

// sameLeader re-reads the process just before a kill and reports whether the target is
// still the same, group-leading process. It returns verdictGone when the process is gone,
// verdictSkip when the pid was reused (start-time changed) or the process no longer leads
// its group, and verdictReap when it is still safe to signal.
func sameLeader(pid int, recStart string) int {
	lp := readLiveProc(pid, false)
	// A start-time mismatch is only meaningful when the live process HAS a start-time. A
	// gone process has none, and that is not a reuse; it falls through to the gone check.
	if lp.startTicks != "" && lp.startTicks != recStart {
		return verdictSkip
	}
	if lp.state == procGone {
		return verdictGone
	}
	if lp.state == procAlive && pid != lp.pgid {
		return verdictSkip
	}
	return verdictReap
}
