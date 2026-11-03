//go:build unix

package runlock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The shipped SIGTERM grace is 4 s (measured). This pins the value itself.
func TestStopTermGraceIsFourSeconds(t *testing.T) {
	if stopTermGrace != 4*time.Second {
		t.Errorf("stopTermGrace = %s, want 4s", stopTermGrace)
	}
}

// The shipped post-SIGKILL wait is 1.5 s (measured). This pins the value itself.
func TestStopKillGraceIsOnePointFiveSeconds(t *testing.T) {
	if stopKillGrace != 1500*time.Millisecond {
		t.Errorf("stopKillGrace = %s, want 1.5s", stopKillGrace)
	}
}

// A free lock stays held by -stop until release runs, so the removals of the
// socket and daemon.token happen under the lock. A -serve that starts meanwhile
// cannot take the lock, so it cannot bind a socket that -stop then removes.
func TestStopHoldsFreeLockUntilRelease(t *testing.T) {
	dir := shortTempDir(t)
	lock := filepath.Join(dir, LockName)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	word, release := StopRunDirHolder(filepath.Join(dir, "rpc.sock"))
	if word != StopWordNone {
		t.Fatalf("word = %q, want %q for a free lock", word, StopWordNone)
	}
	other, err := syscall.Open(lock, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(other) }()
	if err := syscall.Flock(other, syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("before release another fd took the lock (err %v): -stop does not hold it across the removals", err)
	}
	release()
	if err := syscall.Flock(other, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("after release the lock is still held: %v", err)
	}
}
