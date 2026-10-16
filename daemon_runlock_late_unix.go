//go:build unix

package main

import (
	"os"
	"sync"
	"syscall"
	"time"
)

// runDirLatePeriod is the period of the late tries of the run-dir lock. 30 s is
// measured against 5fd08069 (round task005-01, cells L1 and L2 on Linux and macOS VMs,
// cell L3 on a Linux VM): the late line came about 30 s, 60 s or 120 s after the
// survivor lines, on a fixed grid. A var so a test can shrink it.
var runDirLatePeriod = 30 * time.Second

// lateFlock is syscall.Flock behind a seam for the late tries, so a test can count
// the tries. Production never reassigns it.
var lateFlock = syscall.Flock

// lateRunDirLock is the run-dir lock of a daemon that started while another process
// held daemon.lock and stayed. Such a daemon keeps its descriptor of daemon.lock and
// tries the lock again on a fixed period. The behavior is measured against 5fd08069 in
// round task005-01: cells L1 and L2 on Linux and macOS VMs, cells L3 and L6 on a Linux
// VM. The holder there was a process that is not a daemon, on an empty daemon.lock.
//
// The tries begin when the start completes the record, right after the bind. That is
// a few ms after the survivor lines (claustrum's own). The time at which the holder
// lets go does not move the grid (cell L2).
type lateRunDirLock struct {
	fd   int
	role string

	mu      sync.Mutex
	stopped bool // no later try takes the lock
	held    bool // a try took the lock
	closed  bool // the descriptor is closed
	done    chan struct{}
}

// lateRunDirClaim gives back the claim of a daemon that does not hold the lock at its
// start. complete starts the tries, stopLate ends them, and release ends them and then
// drops the lock if a try took it. The claim stays holder false, so a late holder
// writes no child record for a later spawn. 5fd08069 writes none either (round
// task005-02, cell R1, Linux and macOS VMs).
func lateRunDirClaim(fd int, role string) runDirClaim {
	l := &lateRunDirLock{fd: fd, role: role, done: make(chan struct{})}
	return runDirClaim{release: l.release, complete: l.start, stopLate: l.stop}
}

// start runs the tries in their own goroutine. The record of a good try is the full
// record of a start that got the lock at once. Its startedAt is the start of the
// daemon, not the time of the try (cell L1, Linux and macOS VMs).
func (l *lateRunDirLock) start(instanceID string, startedAt int64) {
	rec := ownerRecord{Pid: os.Getpid(), Role: l.role, Node: nodeID(), InstanceID: instanceID, StartedAt: startedAt}
	go l.tryEvery(runDirLatePeriod, rec)
}

// tryEvery tries the lock once each period until a try takes it or the tries stop.
func (l *lateRunDirLock) tryEvery(period time.Duration, rec ownerRecord) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			if l.try(rec) {
				return
			}
		}
	}
}

// try makes one try. It reports whether the tries are over. A failed try logs nothing
// and writes nothing (cell L3, Linux VM). A good try writes the record and logs one line, and the
// daemon then holds the lock until it exits (cells L1 and L2 on Linux and macOS VMs, L6
// on a Linux VM).
func (l *lateRunDirLock) try(rec ownerRecord) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return true
	}
	if lateFlock(l.fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	l.held = true
	writeOwnerRecord(l.fd, rec)
	logInfof("[daemon] run-dir lock acquired late (previous holder gone); recorded pid %d (instance %q)", rec.Pid, rec.InstanceID)
	return true
}

// stop ends the tries. After it returns, no try takes the lock. The daemon calls it
// when its shutdown begins (claustrum's own, not measured).
func (l *lateRunDirLock) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
}

func (l *lateRunDirLock) stopLocked() {
	if !l.stopped {
		l.stopped = true
		close(l.done)
	}
}

// release ends the tries and closes the descriptor. A late holder first truncates its
// record, as a first holder does at its exit (cell L4, Linux VM).
func (l *lateRunDirLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
	if l.closed {
		return
	}
	l.closed = true
	if l.held {
		_ = syscall.Ftruncate(l.fd, 0)
	}
	_ = syscall.Close(l.fd)
}
