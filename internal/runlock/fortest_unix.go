//go:build unix

package runlock

import (
	"syscall"
	"time"
)

// The functions below let the tests of package main swap the seams of this package.
// Only tests call them. Each getter returns the seam itself, so a stand-in can call the
// value that it replaced. Each setter returns the value that it replaced.

// HoldHolderForTest returns the current hold of the eviction.
func HoldHolderForTest() func(int) (func(syscall.Signal) error, func()) { return holdHolder }

// SetHoldHolderForTest sets the hold of the eviction and returns the old one.
func SetHoldHolderForTest(f func(int) (func(syscall.Signal) error, func())) (prev func(int) (func(syscall.Signal) error, func())) {
	prev, holdHolder = holdHolder, f
	return prev
}

// IsServeCmdlineForTest returns the current command line check of the holder.
func IsServeCmdlineForTest() func(int, string) bool { return isServeCmdline }

// SetIsServeCmdlineForTest sets the command line check of the holder and returns the old one.
func SetIsServeCmdlineForTest(f func(int, string) bool) (prev func(int, string) bool) {
	prev, isServeCmdline = isServeCmdline, f
	return prev
}

// SignalHolderForTest returns the current signal of -stop.
func SignalHolderForTest() func(int, syscall.Signal) bool { return signalHolder }

// SetSignalHolderForTest sets the signal of -stop and returns the old one.
func SetSignalHolderForTest(f func(int, syscall.Signal) bool) (prev func(int, syscall.Signal) bool) {
	prev, signalHolder = signalHolder, f
	return prev
}

// HolderGoneForTest returns the current existence probe of the holder.
func HolderGoneForTest() func(int) bool { return holderGone }

// SetHolderGoneForTest sets the existence probe of the holder and returns the old one.
func SetHolderGoneForTest(f func(int) bool) (prev func(int) bool) {
	prev, holderGone = holderGone, f
	return prev
}

// SetLateFlockForTest sets the flock of the late tries and returns the old one.
func SetLateFlockForTest(f func(int, int) error) (prev func(int, int) error) {
	prev, lateFlock = lateFlock, f
	return prev
}

// SetStopTermGraceForTest sets stopTermGrace and returns the old value.
func SetStopTermGraceForTest(d time.Duration) (prev time.Duration) {
	prev, stopTermGrace = stopTermGrace, d
	return prev
}

// SetStopKillGraceForTest sets stopKillGrace and returns the old value.
func SetStopKillGraceForTest(d time.Duration) (prev time.Duration) {
	prev, stopKillGrace = stopKillGrace, d
	return prev
}

// SetKillGraceForTest sets runDirKillGrace and returns the old value.
func SetKillGraceForTest(d time.Duration) (prev time.Duration) {
	prev, runDirKillGrace = runDirKillGrace, d
	return prev
}

// KillGraceForTest returns runDirKillGrace.
func KillGraceForTest() time.Duration { return runDirKillGrace }

// PollIntervalForTest returns runDirPollInterval.
func PollIntervalForTest() time.Duration { return runDirPollInterval }

// SetPollIntervalForTest sets runDirPollInterval and returns the old value.
func SetPollIntervalForTest(d time.Duration) (prev time.Duration) {
	prev, runDirPollInterval = runDirPollInterval, d
	return prev
}

// SetStopLockWaitForTest sets stopLockWait and returns the old value.
func SetStopLockWaitForTest(d time.Duration) (prev time.Duration) {
	prev, stopLockWait = stopLockWait, d
	return prev
}

// SetLatePeriodForTest sets runDirLatePeriod and returns the old value.
func SetLatePeriodForTest(d time.Duration) (prev time.Duration) {
	prev, runDirLatePeriod = runDirLatePeriod, d
	return prev
}

// LatePeriodForTest returns runDirLatePeriod.
func LatePeriodForTest() time.Duration { return runDirLatePeriod }

// StopTermGraceForTest returns stopTermGrace.
func StopTermGraceForTest() time.Duration { return stopTermGrace }

// StopKillGraceForTest returns stopKillGrace.
func StopKillGraceForTest() time.Duration { return stopKillGrace }

// SelfBaseForTest returns the base name of this executable, as the holder check reads it.
func SelfBaseForTest() string { return selfBase() }

// ReadOwnerRecordForTest reads the owner record at path.
func ReadOwnerRecordForTest(path string) (OwnerRecord, error) { return readOwnerRecord(path) }

// WriteOwnerRecordForTest writes rec into the locked fd, as the daemon does.
func WriteOwnerRecordForTest(fd int, rec OwnerRecord) { writeOwnerRecord(fd, rec) }
