package shellenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestMain runs the test binary as the sleep helper when CLAUSTRUM_TEST_HELPER
// is sleep. Otherwise it stubs both login-shell steps as the TestMain of package
// main does, and runs the tests.
func TestMain(m *testing.M) {
	switch mode := os.Getenv("CLAUSTRUM_TEST_HELPER"); mode {
	case "":
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		time.Sleep(time.Duration(secs) * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", mode)
		os.Exit(2)
	}
	AgentSocket = func() string { return "" }
	LoginPATHExtractor = func(context.Context) (string, error) {
		return "", errors.New("the suite stubs the login-shell read")
	}
	os.Exit(m.Run())
}

// syncBuffer is the locked sink of captureLogBuf. The test goroutine reads it
// while other goroutines can still write to the logger.
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
