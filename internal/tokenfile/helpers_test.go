package tokenfile

import (
	"bytes"
	"log"
	"sync"
	"testing"
)

// syncBuffer is captureLog's sink. The test reads the buffer while another
// goroutine can still write a log line into it, so both take the same lock.
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

// captureLog redirects the stdlib default logger into a buffer for the duration
// of fn, mirroring how the other suites assert on log output.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf syncBuffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	fn()
	return buf.String()
}
