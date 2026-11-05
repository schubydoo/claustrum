//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/runlock"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// TestNotLockHolderReapsAndRecordsNothing is a measured case (`5fd08069`, measured on Linux): a daemon runs no reap
// and writes no child record while another process holds the flock of the run-dir lock. The test holds the
// lock itself, so nothing is there to signal. The control is the same start with a free lock: the recorded
// group gets its SIGTERM and the daemon writes a record.
func TestNotLockHolderReapsAndRecordsNothing(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	const orphan = 800000401
	for _, tc := range []struct {
		name   string
		holder bool
	}{
		{"another process holds the lock", false},
		{"control: the lock is free", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			sock := filepath.Join(dir, "rpc.sock")
			if !tc.holder {
				fd, err := syscall.Open(filepath.Join(dir, runlock.LockName), syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Close(fd) })
				if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatalf("flock: %v", err)
				}
				oldHold := runlock.HoldHolderForTest()
				t.Cleanup(func() { runlock.SetHoldHolderForTest(oldHold) })
				runlock.SetHoldHolderForTest(func(int) (func(syscall.Signal) error, func()) {
					return func(syscall.Signal) error { return syscall.ESRCH }, func() {}
				})
			}
			name := writeOrphanRec(t, dir, orphan, "/n")
			sim := newOrphanSim(t, orphan, "/n", dir, syscall.SIGTERM)

			s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
			// The record of this test process stands for the record of a spawned child.
			own := strconv.Itoa(os.Getpid()) + ".json"
			s.procs.RecordChild(os.Getpid(), "/n", "")
			t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "children", own)) })

			if tc.holder {
				if sim.sigCount(syscall.SIGTERM) != 1 || recordExists(dir, name) || !recordExists(dir, own) {
					t.Errorf("control: kills = %v, old record there = %v, new record there = %v, want one SIGTERM, no old record and a new record",
						sim.kills(), recordExists(dir, name), recordExists(dir, own))
				}
				return
			}
			if len(sim.kills()) != 0 {
				t.Errorf("kills = %v, want no signal from a daemon without the lock", sim.kills())
			}
			if !recordExists(dir, name) {
				t.Error("the record of the predecessor is gone, want it left alone")
			}
			if recordExists(dir, own) {
				t.Error("a daemon without the lock wrote a child record")
			}
		})
	}
}
