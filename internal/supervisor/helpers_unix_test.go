//go:build !windows

package supervisor

import (
	"bytes"
	"log"
	"os"
	"sync"
	"testing"
)

// noSuchPid is the base of the fake pids of the tests of this package. It is above
// the pid limit of Linux and macOS, so it names no process and no group. If a change
// skips a seam, the signal finds nothing.
const noSuchPid = 1 << 30

// helperCommand returns this test binary's path plus the env overlay that makes a
// child run it in the given helper mode.
func helperCommand(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe, map[string]string{"CLAUSTRUM_TEST_HELPER": mode}
}

// syncBuffer is the sink of captureLogBuf. The test goroutine reads it while other
// goroutines can still log, so every access takes the lock.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogBuf points the default logger at a locked buffer, with no timestamp,
// until the test ends, and returns the buffer.
func captureLogBuf(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return buf
}
