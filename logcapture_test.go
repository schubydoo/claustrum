package main

import (
	"bytes"
	"log"
	"sync"
	"testing"
)

// syncBuffer is captureLog's sink. log.Logger serializes its own Write, but the
// buffer is also READ by the test goroutine, and daemon goroutines started by
// OTHER tests can still be logging into the process-global logger at that
// moment — so the read needs the same lock. Observed once as a -race failure
// under -shuffle: bytes.Buffer.String raced a Conn.WriteResponse log line from a
// serveConn goroutine that outlived the test which started it.
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

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
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

// captureLogBuf points the default logger at a locked buffer, with no timestamp,
// until the test ends, and returns the buffer. Use it, not a plain bytes.Buffer: see
// syncBuffer.
func captureLogBuf(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return buf
}
