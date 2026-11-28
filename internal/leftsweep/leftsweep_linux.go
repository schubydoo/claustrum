//go:build linux

package leftsweep

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// The two marks of a child of this daemon. One mark keeps the process alive (13047a51,
// measured on Linux). Each mark must be one whole entry of the environment, with the
// exact run folder (claustrum's own, not measured).
const (
	runDirMark = "CLAUDE_SSH_RUN_DIR="
	childMark  = "CLAUDE_SSH_DAEMON_CHILD=1"
)

// childrenEntry is the entry that a different folder at the run folder path must
// lack before its process ends (13047a51, measured on Linux).
const childrenEntry = "children"

const (
	// maxPasses bounds the passes over /proc. 13047a51 ran a second pass after a
	// pass that ended a process, and stopped after a pass that ended none
	// (measured on Linux). The bound of 3 is claustrum's own, not measured.
	maxPasses = 3
	// passPause is the wait before the next pass. On 13047a51 about 29 ms passed
	// from the last open of one pass to the first open of the next (measured on
	// Linux). The 20 ms are claustrum's own, not measured.
	passPause = 20 * time.Millisecond
	// maxEnviron bounds the read of one environment. A longer environment counts
	// as one with no marks (claustrum's own, not measured).
	maxEnviron = 8 << 20
)

// The seams of Run. A test points them at a fake /proc tree and fake signals.
var (
	procDir  = procinfo.Root
	ownEUID  = os.Geteuid
	ownPID   = os.Getpid
	ownPPID  = os.Getppid
	readlink = os.Readlink
	holdPID  = procinfo.HoldPIDByFD
	waitPass = time.Sleep
)

// Run ends each process that the children of the daemon of runDir started and
// left. runDir is the exact value that the children got in CLAUDE_SSH_RUN_DIR. It
// returns the number of processes that took the signal.
//
// For each process of /proc it opens a pid file descriptor first, as 13047a51
// does (measured on Linux). Then the process must have the own uid of this daemon (the
// effective uid, claustrum's own) and both marks, and its root must show the run folder of this
// daemon, no entry at that path, or a different folder there with no children
// entry. The signal is one SIGKILL through the descriptor, never a group and never
// kill(pid). If the open fails, no signal goes out (claustrum's own, not measured).
//
// If runDir is not an absolute clean path or its stat fails, nothing is sent and an
// error comes back. The same holds when /proc/self of the /proc mount is not this
// process, as in a /proc of another pid namespace. A failed read of /proc ends the
// sweep with its error. These refusals are claustrum's own, not measured.
func Run(runDir string) (int, error) {
	if !filepath.IsAbs(runDir) || filepath.Clean(runDir) != runDir {
		return 0, errors.New("leftsweep: the run folder is no absolute clean path: " + strconv.Quote(runDir))
	}
	own, err := os.Stat(runDir)
	if err != nil {
		return 0, err
	}
	if self, err := readlink(filepath.Join(procDir(), "self")); err != nil || self != strconv.Itoa(ownPID()) {
		return 0, errors.New("leftsweep: /proc/self of " + procDir() + " is not this process")
	}
	ended := 0
	signalled := map[int]bool{}
	for pass := 1; ; pass++ {
		n, err := sweepOnce(runDir, own, signalled)
		ended += n
		if err != nil || n == 0 || pass == maxPasses {
			return ended, err
		}
		waitPass(passPause)
	}
}

// sweepOnce runs one pass over /proc and returns the number of processes that took
// the signal. A pid in signalled took it in an earlier pass and is passed over, so
// a dying process that still shows its marks gets one signal and counts once
// (claustrum's own, not measured).
func sweepOnce(runDir string, own fs.FileInfo, signalled map[int]bool) (int, error) {
	proc := procDir()
	entries, err := os.ReadDir(proc)
	if err != nil {
		return 0, err
	}
	ended := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		// 13047a51 opens no pid file descriptor for pid 1 or for the daemon
		// itself (measured on Linux). The parent skip is claustrum's own.
		if err != nil || pid < 2 || pid == ownPID() || pid == ownPPID() || signalled[pid] {
			continue
		}
		if endIfLeft(proc, pid, runDir, own) {
			signalled[pid] = true
			ended++
		}
	}
	return ended, nil
}

// endIfLeft holds pid by a pid file descriptor, runs the tests, and sends SIGKILL
// through that descriptor when all pass. The tests read /proc by path. If the pid
// names a new process by then, the descriptor still names the old one, so the
// signal fails and is not counted (claustrum's own, not measured).
func endIfLeft(proc string, pid int, runDir string, own fs.FileInfo) bool {
	send, release, err := holdPID(pid)
	if err != nil {
		return false
	}
	defer release()
	dir := filepath.Join(proc, strconv.Itoa(pid))
	if !ownedByUs(dir) || !hasBothMarks(dir, runDir) || !runDirLeft(dir, runDir, own) {
		return false
	}
	return send(syscall.SIGKILL) == nil
}

// ownedByUs reports whether the /proc entry of the process has the effective uid
// of this daemon. 13047a51 keeps a process of another uid alive (measured on Linux).
// The choice of the effective uid is claustrum's own.
func ownedByUs(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(ownEUID())
}

// hasBothMarks reports whether the environment of the process holds both marks as
// whole entries, with the exact run folder (claustrum's own, not measured).
func hasBothMarks(dir, runDir string) bool {
	f, err := os.Open(filepath.Join(dir, "environ"))
	if err != nil {
		return false
	}
	defer f.Close()
	env, err := io.ReadAll(io.LimitReader(f, maxEnviron+1))
	if err != nil || len(env) > maxEnviron {
		return false
	}
	wantRun := []byte(runDirMark + runDir)
	var run, child bool
	for _, kv := range bytes.Split(env, []byte{0}) {
		run = run || bytes.Equal(kv, wantRun)
		child = child || bytes.Equal(kv, []byte(childMark))
	}
	return run && child
}

// runDirLeft looks at the run folder path inside the root of the process. The
// same folder as this daemon's, or no entry at that path, lets the process end. A
// different folder lets it end only without a children entry. A link at that path
// lets it end only when it leads to the run folder of this daemon. A link to a folder
// with a children entry stays. 13047a51 does the same (measured on Linux). Every other
// error keeps it alive, and so do a link to anything else and a non-folder at that
// path (claustrum's own, not measured). The parts above the last name are followed,
// and an absolute link resolves in the root of this daemon (claustrum's own, not
// measured). The measured link case does not settle whether 13047a51 resolves the
// link in the root of the daemon or in the root of the process.
func runDirLeft(dir, runDir string, own fs.FileInfo) bool {
	at := filepath.Join(dir, "root", runDir)
	fi, err := os.Lstat(at)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		to, err := os.Stat(at)
		return err == nil && to.IsDir() && os.SameFile(to, own)
	}
	if err != nil || !fi.IsDir() {
		return false
	}
	if os.SameFile(fi, own) {
		return true
	}
	_, err = os.Lstat(filepath.Join(at, childrenEntry))
	return errors.Is(err, fs.ErrNotExist)
}
