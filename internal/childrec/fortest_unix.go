//go:build linux || darwin

package childrec

import (
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// The functions below let the tests of package main swap the seams of this package.
// Only tests call them. Each setter returns the value that it replaced.

// SetReapSleepForTest sets the sleep between the wait polls of the reap and returns
// the old one.
func SetReapSleepForTest(f func(time.Duration)) (prev func(time.Duration)) {
	prev, reapSleep = reapSleep, f
	return prev
}

// ShutdownRecordWaitForTest returns the bound of AwaitDone.
func ShutdownRecordWaitForTest() time.Duration { return shutdownRecordWait }

// SetShutdownRecordWaitForTest sets the bound of AwaitDone and returns the old one.
func SetShutdownRecordWaitForTest(d time.Duration) (prev time.Duration) {
	prev, shutdownRecordWait = shutdownRecordWait, d
	return prev
}

// SetRecordTempMadeForTest sets the hook that runs right after the temp file of a
// record exists, and returns the old one.
func SetRecordTempMadeForTest(f func()) (prev func()) {
	prev, recordTempMade = recordTempMade, f
	return prev
}

// OwnReapIdentityForTest returns the node and host of this daemon, as the reap reads
// them.
func OwnReapIdentityForTest() (node, host string) { return ownReapIdentity() }

// WriteOrphanRecordForTest writes the record of a child of a gone daemon of this boot
// below runDir: pid, the command argv0, the start "9000" and the identity of this
// node. It returns the name of the record in the children folder.
func WriteOrphanRecordForTest(runDir string, pid int, argv0 string) (string, error) {
	node, host := ownReapIdentity()
	rec := childRecord{
		Pid: pid, Node: node, Host: host, Instance: "inst-old",
		DaemonPid: 999999999, DaemonStart: "1", Argv0: argv0,
		Start: "9000", At: time.Now().UnixMilli(),
	}
	root, err := os.OpenRoot(runDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := writeChildRecordIn(root, rec); err != nil {
		return "", err
	}
	return strconv.Itoa(pid) + ".json", nil
}

// GroupSignalForTest is one group signal that FakeOrphanForTest kept.
type GroupSignalForTest struct {
	Pid int
	Sig syscall.Signal
}

// FakeOrphanForTest seams every read of a live process, every group signal, the clock
// and the sleep of the reap. pid reads as a live group leader with the start "9000"
// that runs program, with runDir and its own child marker in its environment. A group
// signal dieOn ends it. Every other pid reads as gone. No signal reaches a process:
// each one is kept, in order. The clock moves only by the sleep of the reap. signals
// returns the kept signals, and restore puts every seam back.
func FakeOrphanForTest(pid int, program, runDir string, dieOn syscall.Signal) (signals func() []GroupSignalForTest, restore func()) {
	var sent []GroupSignalForTest
	alive := true
	now := time.Now()
	oldRead, oldKill, oldGroup, oldNow, oldSleep := readLiveProc, procinfo.KillGroupForTest(), groupAlive, reapNow, reapSleep
	readLiveProc = func(p int, wantEnv bool) liveProc {
		if p != pid || !alive {
			return liveProc{state: procGone}
		}
		return liveProc{state: procAlive, startTicks: "9000", pgid: pid,
			program: program, runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}
	}
	procinfo.SetKillGroupForTest(func(p int, sig syscall.Signal) error {
		sent = append(sent, GroupSignalForTest{p, sig})
		if p == pid && sig == dieOn {
			alive = false
		}
		return nil
	})
	groupAlive = func(int) bool { return false }
	reapNow = func() time.Time { return now }
	reapSleep = func(d time.Duration) { now = now.Add(d) }
	signals = func() []GroupSignalForTest { return append([]GroupSignalForTest(nil), sent...) }
	restore = func() {
		procinfo.SetKillGroupForTest(oldKill)
		readLiveProc, groupAlive, reapNow, reapSleep = oldRead, oldGroup, oldNow, oldSleep
	}
	return signals, restore
}

// ReadsAliveForTest reports whether the reap reads pid as a live process, in the read
// that has no environment.
func ReadsAliveForTest(pid int) bool { return readLiveProc(pid, false).state == procAlive }
