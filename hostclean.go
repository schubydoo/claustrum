//go:build linux || darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The host cleaner (reference build 19f30c46). The cleaner is a periodic,
// host-wide sweep the daemon starts at -serve startup: it ends orphaned Claude Code process
// groups, retires abandoned daemons and tidies their stale run dirs. It ends no stranded
// daemon (see Pass). This file holds the
// shared decision layer — the containment (roots + membership), the judges, the destructive
// enders, the run-dir tidy, and the Start/Pass/Keepalive lifecycle. The OS-specific reads it
// drives (process inspection, snapshot, socket-peer, lock and open-file probes) live in
// hostclean_linux.go (/proc, SO_PEERCRED, /proc/locks) and hostclean_darwin.go (sysctl
// kern.proc, KERN_PROCARGS2, lsof, LOCAL_PEERPID). Everything it touches is confined to the
// install root of the daemon's own socket. That is one folder, under the path as given
// and under its real path. It is also confined to this user's own processes. The sweep
// therefore never reaches an unrelated process or path.
//
// This path is DESTRUCTIVE: the cleaner ends OTHER daemons under its roots. Every process read,
// kill, dial, rename and remove goes through a package-var seam, so unit tests exercise the
// whole decision without touching a real process. A test that lets a rename or a remove run
// for real does so inside its own temp dir. The real sweep is validated only on
// a throwaway VM; a cleaner-enabled -serve must never run on a host that also runs siblings.
// Windows links no cleaner. It is a no-op (hostclean_other.go).

// hostRoots is the containment envelope. roots is the root list. It holds the install
// root dir that the cleaner acts within, as the socket path gives it. A daemon socket
// <root>/run/<id>/rpc.sock yields <root>. When the symlink-resolved form of that root differs, the list also
// holds that form. Both entries then name one folder. daemonBin is the deployed daemon
// binary's basename, which sits at <root>/srv/<id>/<daemonBin>. deriveRoots is the only
// producer outside the tests.
type hostRoots struct {
	roots     []string // the root of the daemon's own socket as given, then its resolved form if that differs
	daemonBin string   // the deployed daemon binary basename (from this daemon's own exe)
	socket    string   // this daemon's own socket, <root>/run/<id>/rpc.sock
	runDir    string   // this daemon's own run dir, <root>/run/<id>
}

// hcEvalSymlinks is filepath.EvalSymlinks behind a seam, so a test can make the resolution
// of the root fail.
var hcEvalSymlinks = filepath.EvalSymlinks

const (
	runComponent    = "run"
	srvComponent    = "srv"
	cliComponent    = "ccd-cli"
	rpcSockBasename = "rpc.sock"
)

// deriveRoots builds the containment from the daemon's own socket and, when haveExe is set,
// its executable. It returns an error when host cleaning stays off.
//
// The socket path is cleaned first. It must be absolute, and its last three components
// must read run/<id>/rpc.sock. The root is the folder above run, as the cleaned socket path
// gives it. No symlink of the socket path decides the shape. The root list holds that root.
// When the symlink-resolved form of the root differs from it, the list also holds the
// resolved form: the same folder under its real path. If the resolution fails, the list
// holds only the root as given. The root of the executable is never added.
//
// When haveExe is set, the executable is resolved through its symlinks. It must be
// <root>/srv/<id>/<name> under one root of the list, with exactly one folder level between
// srv and the file. An executable under the srv of another folder matches no root.
//
// The two error texts are the reasons of the "host cleaning off" line (startHostCleaner).
// The executable text names the srv of the root as given. The decisions of the gate and both texts
// equal those of 89cb6289 in the rows of a Linux VM. The start rows hold nine socket shapes in the
// install layout and in the plain layout, and six places of the executable. Rows KREL,
// KXB, KS-i-j, KS-i-k, KS-m-e, KS-q-e and KS-u-e ran with staged fixtures. A relative socket
// is "not <root>/run/<id>/rpc.sock" in both layouts. A socket path through a symlink to
// the root keeps the cleaner on (rows KS-m-e, KS-q-e). So does a symlink one level above
// the root.
//
// Not measured: a root whose resolution fails and an unreadable own executable (haveExe
// false). A macOS VM measured nine gate shapes against 89cb6289 (rows GSa to GSg3).
func deriveRoots(socket, exe string, haveExe bool) (*hostRoots, error) {
	socketShape := fmt.Errorf("socket path %q is not <root>/run/<id>/rpc.sock", socket)
	socket = filepath.Clean(socket)
	if !filepath.IsAbs(socket) || filepath.Base(socket) != rpcSockBasename {
		return nil, socketShape
	}
	runDir := filepath.Dir(socket)  // <root>/run/<id>
	runRoot := filepath.Dir(runDir) // <root>/run
	if filepath.Base(runRoot) != runComponent {
		return nil, socketShape
	}
	root := filepath.Dir(runRoot) // <root>
	r := &hostRoots{roots: []string{root}, socket: socket, runDir: runDir}
	if real, err := hcEvalSymlinks(root); err == nil && real != root {
		r.roots = append(r.roots, real)
	}
	if !haveExe {
		return r, nil
	}
	exe = filepath.Clean(exe)
	if real, err := hcEvalSymlinks(exe); err == nil {
		exe = real
	}
	r.daemonBin = filepath.Base(exe)
	if !r.isDaemonBinary(exe) {
		return nil, fmt.Errorf("executable %q is not a deployed daemon under %s", exe, filepath.Join(root, srvComponent))
	}
	return r, nil
}

// under reports whether path is <root>/<sub>/<...> for one of the roots, with exactly depth
// path components below <root>/<sub> (so under(exe, "srv", 2) matches <root>/srv/<id>/server).
func (r *hostRoots) under(path, sub string, depth int) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, root := range r.roots {
		prefix := filepath.Join(root, sub) + "/"
		if rest, ok := strings.CutPrefix(path, prefix); ok && rest != "" && strings.Count(rest, "/") == depth-1 {
			return true
		}
	}
	return false
}

// isDaemonBinary reports whether exe is a deployed daemon binary <root>/srv/<id>/<daemonBin>.
func (r *hostRoots) isDaemonBinary(exe string) bool {
	return filepath.Base(exe) == r.daemonBin && r.under(exe, srvComponent, 2)
}

// isCliBinary reports whether exe is a CLI binary directly in <root>/ccd-cli, the flat
// <root>/ccd-cli/<version> layout. A nested <root>/ccd-cli/<dir>/<bin> is not one. On the
// Linux VM the reference kept groups with that layout (issue 429). That this path rule is
// the reason is inferred from the outcome, not seen.
func (r *hostRoots) isCliBinary(exe string) bool {
	for _, root := range r.roots {
		if filepath.Dir(exe) == filepath.Join(root, cliComponent) {
			return true
		}
	}
	return false
}

// runRoots returns each root's run dir, <root>/run.
func (r *hostRoots) runRoots() []string {
	out := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		out = append(out, filepath.Join(root, runComponent))
	}
	return out
}

// sameSocketPath reports whether a and b denote the same socket, either after cleaning or
// after stripping a common install root, so the same socket named under different root
// spellings compares equal.
func (r *hostRoots) sameSocketPath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, rb := r.stripRoot(a), r.stripRoot(b)
	return ra != "" && ra == rb
}

// stripRoot returns path relative to the first root that contains it, or "" when no root does.
func (r *hostRoots) stripRoot(path string) string {
	path = filepath.Clean(path)
	for _, root := range r.roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return ""
}

// serveArgv extracts the socket path from a daemon's argv. It requires argv[0]'s basename to
// be binName and the argv to carry a -serve/--serve flag with an absolute -socket/--socket
// value, and returns "" when the argv is a -bridge/-stop/-install invocation or lacks the
// serve-plus-socket pair.
func serveArgv(argv []string, binName string) string {
	if len(argv) < 2 || filepath.Base(argv[0]) != binName {
		return ""
	}
	serve := false
	socket := ""
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--serve" || a == "-serve":
			serve = true
		case a == "--socket" || a == "-socket":
			if i+1 < len(argv) {
				i++
				socket = argv[i]
			}
		case strings.HasPrefix(a, "--socket="):
			socket = strings.TrimPrefix(a, "--socket=")
		case strings.HasPrefix(a, "-socket="):
			socket = strings.TrimPrefix(a, "-socket=")
		case a == "--bridge" || a == "-bridge" || a == "--stop" || a == "-stop" || a == "--install" || a == "-install":
			return ""
		}
	}
	if serve && strings.HasPrefix(socket, "/") {
		return filepath.Clean(socket)
	}
	return ""
}

// ---- inspection layer (read-only /proc reads) ----

const (
	hcMaxSnapshot = 4096 // cap on the processes one snapshot enumerates
	hcLogName     = "remote-server.log"
	hcLogOldName  = "remote-server.log.old"
)

// hcGetuid is os.Getuid behind a seam so a snapshot test does not depend on the runner's uid.
var hcGetuid = os.Getuid

// hcTracked is one inspected candidate process — the record the judges act on.
type hcTracked struct {
	pid        int
	ppid       int
	pgid       int
	startTicks string    // /proc/<pid>/stat field 22, the pid-reuse-proof identity
	startWall  time.Time // wall-clock start, from the host uptime minus the start-ticks
	exe        string    // resolved /proc/<pid>/exe
	argv       []string  // /proc/<pid>/cmdline
	env        []string  // /proc/<pid>/environ (only when inspect is asked for it)
	haveAge    bool      // startWall was computable (uptime + start-ticks both read)
	sameUID    bool      // the real uid matches this daemon's
	sameNS     bool      // the pid and mnt namespaces match this daemon's
	stopped    bool      // process state T or t
}

// asTracked returns the pid-reuse-proof identity used by the signal helpers.
func (t hcTracked) asTracked() tracked {
	return tracked{pid: t.pid, pgid: t.pgid, startTicks: t.startTicks}
}

// hcHasEnv reports whether env contains an exact entry.
func hcHasEnv(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

// hcInspectResult is inspect's outcome.
type hcInspectResult int

const (
	hcInspectOK   hcInspectResult = iota // fully inspected; the record is valid
	hcInspectGone                        // the process vanished, or its pid was reused mid-read
	hcInspectErr                         // a /proc read failed for another reason
)

// hcClock is time.Now behind a seam, so the start-wall computation is testable.
var hcClock = time.Now

// hcSleep is time.Sleep behind a seam so the busy debounce does not really sleep in tests.
var hcSleep = time.Sleep

// hcDirIdle returns how long since the most recent modification among the run dir name under
// root and its regular log files (remote-server.log, remote-server.log.old). That is the dir's
// idleness, used to decide whether an abandoned run dir is old enough to tidy. Every read is
// an Lstat through root, so a symlinked run dir is not a directory here. The second result is
// false when the dir is missing, not a directory, or a log file cannot be examined for a
// reason other than its absence. A log that is not a regular file is ignored.
func hcDirIdle(root *os.Root, name string, now time.Time) (time.Duration, bool) {
	fi, err := root.Lstat(name)
	if err != nil || !fi.IsDir() {
		return 0, false
	}
	idle := now.Sub(fi.ModTime())
	for _, logName := range []string{hcLogName, hcLogOldName} {
		lfi, err := root.Lstat(filepath.Join(name, logName))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return 0, false
		}
		if !lfi.Mode().IsRegular() {
			continue
		}
		if age := now.Sub(lfi.ModTime()); age < idle {
			idle = age
		}
	}
	return idle, true
}

// ---- ownership / liveness / lock + socket probes ----

// hcDialTimeout and hcWaitPoll are claustrum's own values, not probe-measured.
const (
	hcDialTimeout = 300 * time.Millisecond // probeSocket dial timeout
	hcWaitPoll    = 100 * time.Millisecond // waitGone poll interval
	// The busy debounce below. All three values are claustrum's own and are NOT
	// probe-measured. Staging them live is expensive: the only caller is
	// retireAbandoned, behind a 5-minute process age and a 30-day idle age.
	//
	// Expensive, not impossible, and the distinction matters per value. The WINDOW
	// is observable in principle: a daemon whose client disconnects one second into
	// the sample is retired under a 3-second window and spared under a shorter one,
	// and that outcome is externally visible. The 50 ms interval and the 2-sample
	// minimum are not observable, since nothing outside can see how often the
	// sampler looked.
	hcBusySample = 50 * time.Millisecond // sleep between busy samples
	hcBusyWindow = 3 * time.Second       // how long a busy process must stay busy
	hcBusyMin    = 2                     // samples taken before the deadline counts
)

// hcSettledBusy debounces hcBusy: it reports true only for a process that shows a
// live connection in every sample across the whole window. retireAbandoned uses it
// to spare an idle daemon that is still serving someone.
//
// It samples for hcBusyWindow with a minimum of hcBusyMin samples, and returns at
// once on the first sample that says not busy, so an idle daemon is never held for
// the window. The window, the minimum and the sample interval are claustrum's own
// values and are not probe-measured. See the const block. A sample that cannot read
// the connections answers busy at once: the retire refuses a daemon whose connections
// could not be read.
//
// claustrum's own previous debounce took ten fixed samples, sleeping hcWaitPoll
// (100 ms) between them, so it settled after about 900 ms. On linux it was a
// constant false, so the spare could never fire there. One shared implementation now
// serves both.
func hcSettledBusy(pid int) bool {
	deadline := hcClock().Add(hcBusyWindow)
	for n := 1; ; n++ {
		busy, canRead := hcBusyCheck(pid)
		if !canRead {
			return true
		}
		if !busy {
			return false
		}
		if n >= hcBusyMin && !hcClock().Before(deadline) {
			return true
		}
		hcSleep(hcBusySample)
	}
}

// tracked is a pid plus the identity that defeats pid reuse: its process group and its
// start-ticks. A reused pid has a different start-ticks, so fate can tell it apart.
type tracked struct {
	pid        int
	pgid       int
	startTicks string
}

// fate values.
const (
	hcFateAlive  = 0 // the same live process (pgid and start-ticks still match)
	hcFateGone   = 1 // the process is gone
	hcFateReused = 2 // the pid now belongs to a different process
)

// fate re-reads /proc and reports whether the tracked process is still itself, gone, or
// replaced by a reused pid.
func (t tracked) fate() int {
	pgid, startTicks, ok := hcReadIdent(t.pid)
	if !ok {
		return hcFateGone
	}
	if pgid == t.pgid && startTicks == t.startTicks {
		return hcFateAlive
	}
	return hcFateReused
}

// same reports whether the tracked process is still exactly itself.
func (t tracked) same() bool { return t.fate() == hcFateAlive }

// hcGroupExists reports whether a live process group pgid exists (kill(-pgid, 0) succeeds,
// or fails only for lack of permission).
func hcGroupExists(pgid int) bool {
	if pgid < 2 || pgid >= 1<<31 {
		return false
	}
	err := killGroup(pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// hcIsNoSuchProcess reports whether err means the target process no longer exists.
func hcIsNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}

// hcDial dials a unix socket with the probe timeout. A seam so a test can drive each
// probeSocket state without a real socket.
var hcDial = func(addr string) (net.Conn, error) {
	return net.DialTimeout("unix", addr, hcDialTimeout)
}

// socket-probe states.
const (
	hcSockUnknown = 0 // dialed but unusable, or an unclassified error
	hcSockMissing = 1 // the path does not exist (ENOENT/ENOTDIR)
	hcSockStale   = 2 // the path exists but nobody is listening (ECONNREFUSED)
	hcSockNotSock = 3 // the path exists but is not a socket (ENOTSOCK)
	hcSockLive    = 4 // a live listener answered; pid/uid are its peer credentials
)

// probeSocket classifies a unix socket path: live listener (with its peer pid/uid), missing,
// stale, wrong file type, or unknown.
func probeSocket(addr string) (state, pid int, uid uint32) {
	conn, err := hcDial(addr)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR):
			return hcSockMissing, 0, 0
		case errors.Is(err, syscall.ECONNREFUSED):
			return hcSockStale, 0, 0
		case errors.Is(err, syscall.ENOTSOCK):
			return hcSockNotSock, 0, 0
		default:
			return hcSockUnknown, 0, 0
		}
	}
	defer conn.Close()
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return hcSockUnknown, 0, 0
	}
	pid, uid, ok := peerOf(uc)
	if !ok || pid <= 1 {
		return hcSockUnknown, 0, 0
	}
	return hcSockLive, pid, uid
}

// lock-probe states.
const (
	hcLockStale      = 0 // no lock file, or a record or an empty file with no live holder
	hcLockHeld       = 1 // a record or an empty file, and a live process holds the lock
	hcLockUnknown    = 2 // (linux) the lock could not be opened, is no regular file, or its content does not parse
	hcLockUnexamined = 3 // (darwin) the lock could not be opened, is no regular file, or the holder query did not answer
)

// hcLockHolderRead is hcLockHeldAt behind a seam, so a test on either OS can stage each
// answer of the holder query.
var hcLockHolderRead = hcLockHeldAt

// hcStdioRead is hcStdioArePipes behind a seam, so a test on either OS can stage each answer
// of the stdio read of an orphan.
var hcStdioRead = hcStdioArePipes

// readLockRecord reads the first kilobyte of an open daemon.lock and parses it as the owner
// record. Returns nil when empty or not valid JSON.
func readLockRecord(f *os.File) *ownerRecord {
	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	var rec ownerRecord
	if json.Unmarshal(buf[:n], &rec) != nil {
		return nil
	}
	return &rec
}

// lockProbe classifies <dir>/daemon.lock as held by a live daemon, stale, or indeterminate,
// and returns the parsed owner record and whether the file was present.
func lockProbe(dir string) (state int, rec *ownerRecord, present bool) {
	path := filepath.Join(dir, runDirLockName)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return hcLockStale, nil, false
		}
		return hcLockUnreadState, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return hcLockUnreadState, nil, true
	}
	rec = readLockRecord(f)
	_ = f.Close()
	// An empty lock file carries no record. A daemon truncates its lock to 0 bytes on a
	// graceful exit, so an empty file is asked about its holder like any other. On linux,
	// content that is present but does not parse stays indeterminate. That is claustrum's
	// own answer, and the reference on linux is not measured for it. On darwin such content
	// is asked about its holder too: on a macOS VM 89cb6289 removed a run dir whose lock
	// held 7 bytes of text and had no holder (row C5, 3 of 3). On linux a starting daemon
	// holds no flock between its open and its flock. So its lock reads stale in that window,
	// empty or not. The post-rename lock probe in removeRunDir sees a flock taken between
	// this read and the rename. It does not see one taken after it, and a leftover staging
	// dir has no such probe.
	if hcLockNeedsRecord && rec == nil && fi.Size() != 0 {
		return hcLockUnknown, nil, true
	}
	held, asked := hcLockHolderRead(path, fi)
	if !asked {
		return hcLockUnexamined, rec, true
	}
	if held {
		return hcLockHeld, rec, true
	}
	return hcLockStale, rec, true
}

// waitEntry is a tracked process the wait may SIGKILL (kill set) if it dies but leaves a
// live process group behind.
type waitEntry struct {
	tracked
	kill bool
}

// waitGone polls the entries until they are gone or the deadline passes. A tracked process
// that dies while its process group still has members has that group SIGKILLed (reaping the
// orphaned children); a pid-reused entry is dropped. It returns the entries still alive.
func waitGone(entries []waitEntry, deadline time.Time) []waitEntry {
	for {
		var survivors []waitEntry
		for _, e := range entries {
			switch e.fate() {
			case hcFateAlive:
				survivors = append(survivors, e)
			case hcFateGone:
				if e.kill && e.pid > 1 && hcGroupExists(e.pid) {
					_ = killGroup(e.pid, syscall.SIGKILL)
				}
			}
			// hcFateReused: dropped, never signaled.
		}
		entries = survivors
		if len(entries) == 0 || !hcClock().Before(deadline) {
			return entries
		}
		hcSleep(hcWaitPoll)
	}
}

// ---- the cleaner: lifecycle, summary, and the destructive enders ----

// Timing and count gates. The 15 s first-pass delay matches the reference, as measured on
// Linux and macOS VMs (issue 429). The 3 s grace after SIGTERM and the cap of 64 orphan
// groups match the reference too. A Linux VM measured both against f6010b97 and 89cb6289
// (rows HC21a and HC22). The other values are claustrum's own, not probe-measured.
const (
	hcMinAge     = 5 * time.Minute     // a daemon or orphan younger than this is never touched
	hcIdleAge    = 30 * 24 * time.Hour // a run dir must be idle this long before tidy acts on it, unless its name carries the staging marker
	hcTermGrace  = 3 * time.Second     // wait after SIGTERM before SIGKILL
	hcKillSettle = 1 * time.Second     // wait after SIGKILL before warning
	hcPassDelay  = 15 * time.Second    // delay before the first pass
	hcPassEvery  = 24 * time.Hour      // interval between passes and keepalives
	hcMaxGroups  = 64                  // per-pass cap on orphan groups ended
)

// hcSummary counts what one pass did. Its zero value renders as "nothing to do".
type hcSummary struct {
	orphanSignalled int
	orphanSurvived  int
	daemonsRetired  int
	runDirsRemoved  int
	undecided       int
}

// String renders the pass summary in the reference's measured words, and returns a short
// "nothing to do" when the pass counted nothing. The stranded-daemon part keeps its text
// with zeros. The cleaner ends no stranded daemon (see Pass), and the reference printed
// zeros there in every measured row.
func (s hcSummary) String() string {
	if s == (hcSummary{}) {
		return "nothing to do"
	}
	return fmt.Sprintf("stranded daemons signalled 0 (ended 0, survived 0) plus 0 process(es)/group(s) they left behind; orphaned Claude Code groups signalled %d (survived %d); abandoned daemons retired %d; dead run dirs removed %d; candidates left undecided %d",
		s.orphanSignalled, s.orphanSurvived, s.daemonsRetired, s.runDirsRemoved, s.undecided)
}

// hostCleaner is the periodic host-wide sweep. It touches only processes and run dirs within
// its roots, and never itself (selfPid / selfStart).
type hostCleaner struct {
	roots     *hostRoots
	ownSocket string
	ownRunDir string
	selfPid   int
	selfStart string // this daemon's own /proc start-ticks
}

// hcHoldPid takes hold of a single process (not its group) before the last identity check
// of retireAbandoned, its one caller. It returns send, which signals that process, and
// release, which drops the hold. A seam so tests never signal a real process. The group
// signal reuses killGroup from the reap. The real hold is OS-specific (hcHoldDaemon). On
// linux it is a pid file descriptor: pidfd_open here, pidfd_send_signal in send. On darwin
// there is no hold, and send is kill.
var hcHoldPid = hcHoldDaemon

// newHostCleaner builds a cleaner for this daemon's own socket and executable, or returns an
// error when the socket is not run-dir shaped or the executable is not a deployed daemon.
func newHostCleaner(socket string) (*hostCleaner, error) {
	exe, haveExe := hcSelfExe()
	roots, err := deriveRoots(socket, exe, haveExe)
	if err != nil {
		return nil, err
	}
	c := &hostCleaner{
		roots:     roots,
		ownSocket: filepath.Clean(socket),
		ownRunDir: filepath.Dir(filepath.Clean(socket)),
		selfPid:   os.Getpid(),
	}
	if _, start, ok := hcReadIdent(c.selfPid); ok {
		c.selfStart = start
	}
	return c, nil
}

// endGroupsTwoPhase signals a set of process groups SIGTERM, waits the grace, SIGKILLs the
// survivors, waits the settle, and reports the ones that outlived SIGKILL. Each entry is a
// group leader (pid == pgid), and every signal goes to the group (kill(-pid)). label names
// the entity in the logs ("orphaned process group").
// termLog, when not empty, is a format with one %d (the pid) logged before each SIGTERM.
// It returns the count signalled and the count that survived SIGKILL.
//
// The line before the SIGKILL is the reference's, measured on a Linux VM against f6010b97
// and 89cb6289 (row HC21a). A group that waitGone ends early, because its leader is gone,
// gets no line. The reference printed none there either (row HC21, the kid variant).
func endGroupsTwoPhase(groups []tracked, label, termLog string) (signalled, survived int) {
	var live []waitEntry
	for _, g := range groups {
		// Re-validate identity immediately before signalling: a child can exit (and its pid be
		// reused by an unrelated process) between the snapshot and now, so signal only when the
		// tracked pgid and start-ticks still match.
		if g.pid < 2 || !g.same() {
			continue
		}
		if termLog != "" {
			logInfof(termLog, g.pid)
		}
		err := killGroup(g.pid, syscall.SIGTERM)
		if err != nil {
			if hcIsNoSuchProcess(err) {
				continue // already gone
			}
			if errors.Is(err, syscall.EPERM) {
				logInfof("[hostclean] %s %d belongs to another owner; not signalling it", label, g.pid)
				continue
			}
			// Any other error: still wait to see whether it exits.
		}
		signalled++
		// kill-enabled for a group leader: if it exits during the grace, waitGone SIGKILLs its
		// process group, reaping any members that outlive the leader.
		live = append(live, waitEntry{tracked: g, kill: g.pid == g.pgid})
	}
	survivors := waitGone(live, hcClock().Add(hcTermGrace))
	for _, g := range survivors {
		if !g.same() {
			continue // exited or was replaced during the grace: never SIGKILL a reused pid
		}
		logInfof("[hostclean] group %d outlived SIGTERM for %s: SIGKILL", g.pid, hcTermGrace)
		_ = killGroup(g.pid, syscall.SIGKILL)
	}
	for _, g := range waitGone(survivors, hcClock().Add(hcKillSettle)) {
		survived++
		logInfof("[hostclean] %s %d survived SIGKILL", label, g.pid)
	}
	return signalled, survived
}

// endGroups ends a set of orphaned Claude Code process groups (Pass step 9).
func (c *hostCleaner) endGroups(groups []tracked, sum *hcSummary) {
	sig, surv := endGroupsTwoPhase(groups, "orphaned process group",
		"[hostclean] ending orphaned Claude Code process group %d (its daemon is gone): SIGTERM")
	sum.orphanSignalled += sig
	sum.orphanSurvived += surv
}

// hcDaemonChildMarker is the environment entry a daemon-spawned process carries.
const hcDaemonChildMarker = "CLAUDE_SSH_DAEMON_CHILD=1"

// verifyListener returns "" when the process answering a socket is verifiably one of our
// daemons serving that socket, or a short reason otherwise. It is the strict gate before the
// cleaner retires a live listener as our daemon.
//
// Two reasons are the reference's, measured on a Linux VM against f6010b97 and 89cb6289.
// They are "not our daemon binary" (row HC04) and "not serving that socket" (row ST06).
// The other four keep claustrum's own wording. Their reference texts are not measured.
func (c *hostCleaner) verifyListener(pid int, socket string) string {
	t, res := inspect(pid, true)
	if res != hcInspectOK {
		return "the listener could not be inspected"
	}
	if !t.sameUID {
		return "the listener belongs to another user"
	}
	if !t.sameNS {
		return "the listener runs in another namespace"
	}
	if t.exe == "" || !c.roots.isDaemonBinary(t.exe) {
		return "the listener could not be verified as a daemon of ours (not our daemon binary)"
	}
	if s := serveArgv(t.argv, c.roots.daemonBin); s == "" || !c.roots.sameSocketPath(s, socket) {
		return "the listener could not be verified as a daemon of ours (not serving that socket)"
	}
	if !hcHasEnv(t.env, hcDaemonChildMarker) {
		return "it lacks the daemon-child marker"
	}
	return ""
}

// hcArgvHasStreamJSON reports whether argv marks a Claude Code stream process.
func hcArgvHasStreamJSON(argv []string) bool {
	for _, a := range argv {
		if a == "stream-json" || strings.HasSuffix(a, "=stream-json") {
			return true
		}
	}
	return false
}

// judgeOrphan reports whether a process group leader is an orphaned Claude Code child whose
// owning daemon is gone. The caller passes only ppid-1 group leaders that run one of our CLI
// binaries. It must be this user's own process, in this daemon's namespace, old enough, a
// stream-json process, carrying the daemon-child marker, and its stdin, stdout and stderr
// must all be pipes, the stdio a daemon gives its child. The uid, namespace and CLI-binary
// gates keep the cleaner from ending an unrelated same-user process that merely inherited the
// marker and happens to carry a stream-json argument. The reason is for the spare log; an
// empty reason with reap=false is a silent skip.
func (c *hostCleaner) judgeOrphan(t hcTracked) (reap bool, reason string) {
	if t.stopped {
		return false, "could not read it (it is stopped)"
	}
	if !t.sameUID || !t.sameNS {
		return false, "" // not ours: a foreign uid or namespace
	}
	if !t.haveAge {
		return false, "its age could not be determined"
	}
	if hcClock().Sub(t.startWall) < hcMinAge {
		return false, "" // too young
	}
	if t.exe == "" || !c.roots.isCliBinary(t.exe) {
		return false, "" // not one of our deployed CLI binaries
	}
	if t.argv == nil {
		return false, "its argv could not be read"
	}
	if !hcArgvHasStreamJSON(t.argv) {
		return false, "" // not a stream process
	}
	if t.env == nil {
		return false, "its environ could not be read"
	}
	if !hcHasEnv(t.env, hcDaemonChildMarker) {
		return false, "" // not a daemon child
	}
	pipes, canRead, answered := hcStdioRead(t.pid)
	if !answered {
		return false, hcOrphanUninspected
	}
	if !canRead {
		return false, "its open descriptors were unreadable" // cannot confirm it, so spare it
	}
	if !pipes {
		return false, "" // its stdio is not the pipes a daemon gives its child
	}
	return true, ""
}

// hcOrphanUninspected is the reason for an orphan whose stdio read got no answer. The pass
// prints it in the line of the reference, measured on a macOS VM against 89cb6289 (row E5):
// there the lsof command did not start. A run that writes to stderr gives the same line
// here. That case is not measured for an orphan.
const hcOrphanUninspected = "its descriptors could not be inspected"

// hcGetppid is os.Getppid behind a seam. The cleaner never retires its own parent.
var hcGetppid = os.Getppid

// hcRetireBusyReason is the measured refusal text for a daemon that still has a client, or
// whose connections could not be read.
const hcRetireBusyReason = "a connection is attached to it right now, or that could not be read: in use after all"

// retireAbandoned SIGTERMs a still-live daemon whose run dir e has been idle past the
// threshold. pid and uid are the peer credentials of e's socket. It refuses this daemon, its
// parent, pid 1 and another user's process with no reason. It then requires the daemon be
// verifiably ours and old enough, spares one that still shows a live connection across the
// sampling window, re-verifies identity (rejecting a reused pid), then SIGTERMs the single pid
// and waits the grace. It returns true only when the daemon exited. Otherwise the reason says
// why, for the tidy's log, and is empty for a silent refusal. It never escalates to SIGKILL.
func (c *hostCleaner) retireAbandoned(e runDirEntry, pid int, uid uint32) (bool, string) {
	if pid < 2 || pid == c.selfPid || pid == hcGetppid() {
		return false, ""
	}
	if uid != uint32(hcGetuid()) {
		return false, ""
	}
	// The listener check comes before the sampler. It is the cheapest way to reject a
	// candidate, and putting it last means spending the whole sampling window on a process
	// that is about to be refused anyway. On darwin that window is hcBusyWindow of lsof runs.
	if detail := c.verifyListener(pid, e.socket); detail != "" {
		return false, detail
	}
	t, res := inspect(pid, false)
	if res != hcInspectOK || !t.haveAge {
		return false, ""
	}
	if hcClock().Sub(t.startWall) < hcMinAge {
		// Measured on a macOS VM against f6010b97 (issue 429): the reference logs this reason.
		return false, "it is too young to judge"
	}
	if hcSettledBusy(pid) {
		return false, hcRetireBusyReason
	}
	// Re-validate identity immediately before signalling, the way endGroupsTwoPhase does.
	// Everything between the inspect above and here takes the bare pid: the busy sampler
	// holds it for up to hcBusyWindow. A daemon that exits in that span can have its pid
	// taken by an unrelated process, and the SIGTERM below would then reach whatever now
	// holds the number.
	//
	// The order is: take the hold, check the identity, signal through the hold. On linux
	// the hold is a pid file descriptor, so a pid that changes hands after the check gets
	// no signal. On a Linux VM 89cb6289 made the same calls in that order in 26 of 26
	// retires (strace). On darwin there is no such hold, and the check is the only guard.
	send, release := hcHoldPid(pid)
	if !t.asTracked().same() {
		release()
		return false, "it is no longer the process that was inspected"
	}
	logInfof("[hostclean] retiring abandoned daemon pid %d: its run dir %q has seen no connection for %s: SIGTERM", pid, e.dirPath, hcDays(e.idle))
	err := send(syscall.SIGTERM)
	release()
	if err != nil && !hcIsNoSuchProcess(err) {
		return false, fmt.Sprintf("the SIGTERM was refused (%v)", err)
	}
	if len(waitGone([]waitEntry{{tracked: t.asTracked()}}, hcClock().Add(hcTermGrace))) == 0 {
		return true, ""
	}
	return false, "it did not exit within the grace after SIGTERM; left running"
}

// ---- run-dir tidy + the pass orchestration + lifecycle ----

const (
	hcMaxRunDirs    = 64           // run dirs examined per pass
	hcMaxRetire     = 8            // retire attempts per pass
	hcStagingMarker = ".removing-" // a run dir whose name contains this is mid-removal
)

// Filesystem seams so a tidy test can fail or count a rename or remove. Both act inside the
// run root, so neither can reach a path outside it.
var (
	hcRename    = func(root *os.Root, from, to string) error { return root.Rename(from, to) }
	hcRemoveAll = func(root *os.Root, name string) error { return root.RemoveAll(name) }
	hcChtimes   = os.Chtimes
)

// runDirEntry is one run dir the tidy pass considers.
type runDirEntry struct {
	root    *os.Root      // the run root <root>/run, opened for this pass
	name    string        // the entry's name in root
	dirPath string        // <root>/run/<name>
	socket  string        // <root>/run/<name>/rpc.sock
	idle    time.Duration // how long since the dir last showed activity
}

// hcDays renders an idle age as whole days, the unit of the tidy's log lines.
func hcDays(d time.Duration) string {
	return fmt.Sprintf("%d days", int64(d/(24*time.Hour)))
}

// hcReadNames lists the names in root's top directory, in directory order.
func hcReadNames(root *os.Root) ([]string, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// runDirs lists the run dirs under the roots, with each one's idle age read now. A run root
// reached twice (the same device and inode) is listed once. This daemon's own run dir is
// skipped, and so is an entry that is not a plain directory (a symlink included) or whose age
// cannot be read. It stops at hcMaxRunDirs. The returned func closes the opened roots.
func (c *hostCleaner) runDirs() ([]runDirEntry, func()) {
	var out []runDirEntry
	var opened []*os.Root
	closeAll := func() {
		for _, r := range opened {
			_ = r.Close()
		}
	}
	seen := make(map[string]bool)
	ownName := filepath.Base(c.ownRunDir)
	now := hcClock()
	for _, runRoot := range c.roots.runRoots() {
		fi, err := os.Stat(runRoot)
		if err != nil || !fi.IsDir() {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			// st.Dev is uint64 on linux and int32 on darwin, so format with %d rather than
			// strconv.FormatUint; the value is only a dedup key, and %d renders both identically.
			key := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		root, err := os.OpenRoot(runRoot)
		if err != nil {
			continue
		}
		opened = append(opened, root)
		names, err := hcReadNames(root)
		if err != nil {
			continue
		}
		for _, name := range names {
			if name == "." || name == ".." || name == ownName {
				continue
			}
			if len(out) >= hcMaxRunDirs {
				// The reference's line, measured on a Linux VM (row HC06b).
				logInfof("[hostclean] more than %d run dirs under %s; only the first %d are examined", hcMaxRunDirs, runRoot, hcMaxRunDirs)
				return out, closeAll
			}
			idle, ok := hcDirIdle(root, name, now)
			if !ok {
				continue
			}
			dirPath := filepath.Join(runRoot, name)
			out = append(out, runDirEntry{root: root, name: name, dirPath: dirPath, socket: filepath.Join(dirPath, rpcSockBasename), idle: idle})
		}
	}
	return out, closeAll
}

// tidyRunDirs retires stale run dirs from the list runDirs made at the start of the pass. A
// dir idle past the threshold (or a leftover staging dir) is read again first, so one that
// came back into use during the pass is kept. A daemon still answering its socket is retired
// first. The dir is then renamed aside and removed, if its socket no longer answers and no
// live process holds its lock. At most hcMaxRetire retires are attempted per pass.
func (c *hostCleaner) tidyRunDirs(entries []runDirEntry, sum *hcSummary) {
	tries := 0
	for _, e := range entries {
		staging := strings.Contains(e.name, hcStagingMarker)
		if e.idle < hcIdleAge && !staging {
			continue // too fresh and not an interrupted removal
		}
		if tries >= hcMaxRetire {
			// The reference's line, measured on a Linux VM (row HC14c). claustrum counts
			// attempts, refused ones included. The reference count after a refused attempt
			// is not measured.
			logInfof("[hostclean] run dir %q unused for %s: left for a later pass (%d retirements this pass already)", e.dirPath, hcDays(e.idle), tries)
			continue
		}
		idle, ok := hcDirIdle(e.root, e.name, hcClock())
		if !ok {
			logInfof("[hostclean] run dir %q kept: it is not a directory any more, or its age could not be read", e.dirPath)
			continue
		}
		if idle < hcIdleAge && !staging {
			logInfof("[hostclean] run dir %q: kept, in use again since this pass listed it", e.dirPath)
			continue
		}
		e.idle = idle
		firstProbe, peer, uid := probeSocket(e.socket)
		state := firstProbe
		if firstProbe == hcSockLive {
			tries++
			retired, reason := c.retireAbandoned(e, peer, uid)
			if !retired {
				if reason != "" {
					logInfof("[hostclean] run dir %q unused for %s: its daemon (pid %d) was not retired: %s", e.dirPath, hcDays(e.idle), peer, reason)
				}
				continue
			}
			sum.daemonsRetired++
			state, _, _ = probeSocket(e.socket)
		}
		switch state {
		case hcSockUnknown:
			logInfof("[hostclean] run dir %q kept: the probe of its socket was inconclusive", e.dirPath)
			continue
		case hcSockLive:
			logInfof("[hostclean] run dir %q kept: its socket still answers", e.dirPath)
			continue
		}
		if firstProbe == hcSockLive && state != hcSockMissing {
			logInfof("[hostclean] run dir %q kept: its socket is still there after its daemon was retired", e.dirPath)
			continue
		}
		switch ls, _, _ := lockProbe(e.dirPath); ls {
		case hcLockHeld:
			// The reference's line, measured on a Linux VM (row HC03).
			logInfof("[hostclean] run dir %q unused for %s: kept, a live process holds its %s", e.dirPath, hcDays(e.idle), runDirLockName)
			continue
		case hcLockUnexamined:
			// The reference's line, measured on a macOS VM against 89cb6289 (rows C3, D2p,
			// D2f, D4, E7a and E7b).
			logInfof("[hostclean] run dir %q unused for %s: kept, its %s could not be examined", e.dirPath, hcDays(e.idle), runDirLockName)
			continue
		case hcLockUnknown:
			logInfof("[hostclean] run dir %q kept: whether a process holds its run-dir lock could not be determined", e.dirPath)
			continue
		}
		idle, ok = hcDirIdle(e.root, e.name, hcClock())
		if !ok {
			logInfof("[hostclean] run dir %q kept: it is not a directory any more", e.dirPath)
			continue
		}
		if idle < hcIdleAge && firstProbe != hcSockLive && !staging {
			logInfof("[hostclean] run dir %q: kept, in use again since this pass listed it", e.dirPath)
			continue
		}
		if c.removeRunDir(e, staging, state) {
			sum.runDirsRemoved++
		}
	}
}

// removeRunDir removes a run dir in two steps inside its run root: rename it to
// <name>.removing-<pid>-<base36 time>, then remove it. A leftover staging dir skips the
// rename. If its lock is no longer stale after the rename, or an entry named rpc.sock appears
// where the probe saw none, the rename is undone. probeState is the socket state the tidy
// acted on.
func (c *hostCleaner) removeRunDir(e runDirEntry, staging bool, probeState int) bool {
	target := e.name
	if !staging {
		target = e.name + hcStagingMarker + strconv.Itoa(c.selfPid) + "-" + strconv.FormatInt(hcClock().UnixNano(), 36)
		if err := hcRename(e.root, e.name, target); err != nil {
			logInfof("[hostclean] run dir %q: could not stage it for removal: %v; kept", e.dirPath, err)
			return false
		}
		if ls, _, _ := lockProbe(filepath.Join(filepath.Dir(e.dirPath), target)); ls != hcLockStale {
			c.undoRename(e, target, "its run-dir lock turned held or unreadable during removal")
			return false
		}
		if _, err := e.root.Lstat(filepath.Join(target, rpcSockBasename)); err == nil && probeState == hcSockMissing {
			c.undoRename(e, target, "a socket appeared in it during removal")
			return false
		}
	}
	if err := hcRemoveAll(e.root, target); err != nil {
		logInfof("[hostclean] run dir %q unused for %s: renamed to %q but removal failed: %v", e.dirPath, hcDays(e.idle), target, err)
		return false
	}
	logInfof("[hostclean] removed run dir %q: unused for %s, nothing answers on its socket, no process holds its lock", e.dirPath, hcDays(e.idle))
	return true
}

// undoRename renames a staging dir back to its original name when a removal is aborted
// because the dir came back into use. The "could not be restored" line is claustrum's own.
func (c *hostCleaner) undoRename(e runDirEntry, target, reason string) {
	if err := hcRename(e.root, target, e.name); err != nil {
		logInfof("[hostclean] run dir %q could not be restored (%s): %v", e.dirPath, reason, err)
		return
	}
	// The line for a socket that appeared is the reference's, as measured on a Linux VM
	// against f6010b97 (issue 429). The lock reason is claustrum's own.
	logInfof("[hostclean] run dir %q: kept, %s", e.dirPath, reason)
}

// Pass runs one host-wide sweep and returns what it did. It refuses to act unless its own
// socket still leads to itself. It lists the run dirs right after the process snapshot. It
// then ends orphaned Claude Code groups. Unless the host has too many processes, it tidies
// the run dirs it listed.
//
// The pass ends no stranded daemon, and it signals no child of one. A stranded daemon is a
// daemon whose socket path no longer leads to it. On a Linux VM, f6010b97 and 89cb6289
// sent no signal to such a daemon or to its children. The rows are ST01, ST02, ST04 to
// ST10, HC16 to HC19 and HC23 to HC26. The daemon ends by itself after its own timer
// (exitWhenOrphaned). On a macOS VM the two references sent none either (rows ST01, ST09,
// ST10, HC16, HC18).
//
// The two lines about a limit are the reference's, measured on the Linux VM. One is for
// more than hcMaxSnapshot processes (rows HC24 and HC25). The other is for more than
// hcMaxGroups orphan groups (rows HC22 and HC26). Row HC24f has 4097 processes. In one
// earlier run of it 89cb6289 printed no limit line. In five later runs both sides printed
// the line, 5 of 5 each.
func (c *hostCleaner) Pass() hcSummary {
	var sum hcSummary
	logInfof("[hostclean] pass starting (the next connection logged is this pass probing its own socket)")
	switch state, peer, _ := probeSocket(c.ownSocket); {
	case state != hcSockLive:
		logInfof("[hostclean] own socket %q does not lead to this daemon (state %d, pid %d); skipping this pass", c.ownSocket, state, peer)
		return sum
	case peer != c.selfPid:
		logInfof("[hostclean] own socket %q is answered by pid %d, not this daemon; skipping this pass", c.ownSocket, peer)
		return sum
	}
	pids, tooMany, ok := hcSnapshot()
	if !ok {
		logInfof("[hostclean] could not enumerate this user's process list; skipping this sweep")
		return sum
	}
	if tooMany {
		logInfof("[hostclean] more than %d processes for this user: this pass only acts on per-process facts (no run-dir retirement)", hcMaxSnapshot)
	}
	procs := make([]hcTracked, 0, len(pids))
	for _, pid := range pids {
		if t, res := inspect(pid, true); res == hcInspectOK {
			procs = append(procs, t)
		}
	}
	// The run dirs and their idle ages are read here, before the tidy dials anything.
	// A dial writes a line to the dialled daemon's log, which would make its dir read fresh.
	entries, closeAll := c.runDirs()
	defer closeAll()

	// Other daemons of ours whose open files cannot be read. The line and the count are the
	// reference's, measured on a macOS VM against 89cb6289 (rows D1i, D1k, E1, E2 and E5).
	// There the line came before the orphan line and before every run dir line. Only a
	// daemon that is old enough and serves an idle run dir gets the line: in row E1 a
	// daemon of 33 s on a fresh run dir got none. Which of those two gates decides is not
	// measured, and claustrum applies both. On linux hcDaemonFilesUnread is a constant false.
	for _, t := range procs {
		socket := serveArgv(t.argv, c.roots.daemonBin)
		if t.pid < 2 || t.pid == c.selfPid || !t.sameUID || !c.roots.isDaemonBinary(t.exe) || socket == "" {
			continue
		}
		if !t.haveAge || hcClock().Sub(t.startWall) < hcMinAge {
			continue
		}
		idle := false
		for _, e := range entries {
			if c.roots.sameSocketPath(socket, e.socket) && (e.idle >= hcIdleAge || strings.Contains(e.name, hcStagingMarker)) {
				idle = true
			}
		}
		if !idle {
			continue
		}
		if hcDaemonFilesUnread(t.pid) {
			logInfof("[hostclean] daemon pid %d left alone this pass: its open files could not be inspected", t.pid)
			sum.undecided++
		}
	}

	// Orphaned Claude Code groups: a group leader re-parented to pid 1 that runs one of our
	// CLI binaries.
	var orphanGroups []tracked
	for _, t := range procs {
		if t.pid < 2 || t.pid == c.selfPid || t.ppid != 1 || t.pgid != t.pid || t.exe == "" || !c.roots.isCliBinary(t.exe) {
			continue
		}
		reap, reason := c.judgeOrphan(t)
		if reason == hcOrphanUninspected {
			logInfof("[hostclean] process group %d left alone this pass: %s", t.pid, reason)
			sum.undecided++
		} else if reason != "" {
			logInfof("[hostclean] spared process group %d this sweep: %s", t.pid, reason)
			sum.undecided++
		}
		if !reap {
			continue
		}
		if len(orphanGroups) >= hcMaxGroups {
			logInfof("[hostclean] more orphaned Claude Code groups than one pass ends (%d); leaving the rest for the next pass", hcMaxGroups)
			break // per-pass cap on orphan groups ended
		}
		orphanGroups = append(orphanGroups, t.asTracked())
	}
	c.endGroups(orphanGroups, &sum)
	if !tooMany {
		c.tidyRunDirs(entries, &sum)
	}
	return sum
}

// Keepalive refreshes the mtime of the cleaner's own run dir so an age-based tidy never
// retires the dir hosting this very daemon.
func (c *hostCleaner) Keepalive() {
	now := hcClock()
	if err := hcChtimes(c.ownRunDir, now, now); err != nil {
		logInfof("[hostclean] could not bump the mtime of own run dir %q (the keepalive that keeps the retire sweep off it): %v", c.ownRunDir, err)
	}
}

// Start launches the two background loops: a keepalive that touches the own run dir, and the
// periodic sweep (first pass after an initial delay, then once per interval). Fire-and-forget.
func (c *hostCleaner) Start() {
	go func() {
		for {
			c.Keepalive()
			hcSleep(hcPassEvery)
		}
	}()
	go func() {
		hcSleep(hcPassDelay)
		for {
			start := hcClock()
			sum := c.Pass()
			logInfof("[hostclean] pass done in %s: %s", hcClock().Sub(start).Round(time.Millisecond), sum)
			hcSleep(hcPassEvery)
		}
	}()
}

// startHostCleaner builds the cleaner for this daemon's socket and starts its background
// sweep. The cleaner stays off when the socket as given is not an absolute
// <root>/run/<id>/rpc.sock. It also stays off when the executable is not
// <root>/srv/<id>/<name> under the root of that socket. One line then says why (see
// deriveRoots for the measured rows).
// Called from -serve startup, right after the listening line.
//
// The "host cleaning off" line and its two reasons are the reference's, measured on a Linux
// VM against f6010b97 and 89cb6289. The reference printed it right after its listening
// line. On Windows the reference printed no such line (rows WN03 and WN06), and
// hostclean_other.go prints none.
func startHostCleaner(socket string) {
	if socket == "" {
		return
	}
	c, err := newHostCleaner(socket)
	if err != nil {
		logInfof("[daemon] host cleaning off: %v", err) // not a deployed run-dir daemon
		return
	}
	c.Start()
}
