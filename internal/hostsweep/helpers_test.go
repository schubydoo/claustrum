//go:build linux || darwin

package hostsweep

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sockroute"
)

// noSuchPid is the base of the fake pids behind the signal seams of this package. It is
// above the pid limit of Linux and macOS, so noSuchPid plus a small number names no
// process and no group. If a change skips a seam, the signal finds nothing.
const noSuchPid = 1 << 30

// TestMain runs the test binary as a fixture when CLAUSTRUM_TEST_HELPER names a
// mode, as the TestMain of package main does for the modes that the tests of this
// package use. Otherwise it runs the tests.
func TestMain(m *testing.M) {
	// Each helper is a copy of this binary. Under -race the tsan runtime of a
	// child sleeps 1 s before it exits unless GORACE says otherwise. An
	// operator-supplied GORACE still wins.
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}

	mode := os.Getenv("CLAUSTRUM_TEST_HELPER")

	// The fork-bomb guard of the TestMain of package main. A copy of this binary
	// that starts with the daemon child marker and no helper mode must never run
	// the suite: a detached copy of the suite that starts copies of itself takes
	// the host down.
	if mode == "" && os.Getenv("CLAUSTRUM_DAEMON_CHILD") == "1" {
		fmt.Fprintln(os.Stderr, "test binary re-exec'd as a daemon child without CLAUSTRUM_TEST_HELPER: refusing to run the suite (a test reached runServe's parent path without the helper seam)")
		os.Exit(1)
	}

	if mode == "" {
		os.Exit(m.Run())
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// runHelper implements the fixture commands that the tests of this package use, as
// the runHelper of package main does.
func runHelper(mode string, args []string) int {
	switch mode {
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "stall-quiet":
		// A stand-in for an lsof that hangs: it ignores every argument, writes
		// nothing, and outlives any bound a test would set. The argument-ignoring
		// part matters, because the runner under test prepends its own flags.
		time.Sleep(60 * time.Second)
	case "print-quiet":
		// The control for stall-quiet: same argument handling, answers at once.
		fmt.Print("p1\n")
	case "stderr-exit5": // integration fixture: one stderr line, exit 5
		fmt.Fprint(os.Stderr, "err\n")
		return 5
	default:
		// "exit:N": exit with code N.
		if code, ok := strings.CutPrefix(mode, "exit:"); ok {
			n, err := strconv.Atoi(code)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return n
		}
		fmt.Fprintf(os.Stderr, "unknown CLAUSTRUM_TEST_HELPER mode %q\n", mode)
		return 2
	}
	return 0
}

// syncBuffer is the sink of captureLogBuf. The read of the test takes the same lock
// as the writes of the logger.
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

// listenSocket listens at path as the daemon does.
func listenSocket(path string) (net.Listener, error) { return sockroute.Listen(path) }

// shortTempDir returns a /tmp-rooted dir so AF_UNIX paths stay under the macOS
// sun_path limit (t.TempDir embeds the long test name).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// longSocketPath returns a path of exactly total bytes under a fresh temp folder
// that ends in base. base can hold slashes. The folder of the path exists.
func longSocketPath(t *testing.T, total int, base string) string {
	t.Helper()
	root := shortTempDir(t)
	pad := total - len(root) - len(base) - 2
	if pad < 1 || pad > 255 {
		t.Fatalf("cannot build a %d-byte path under %s with base %d bytes", total, root, len(base))
	}
	p := filepath.Join(root, strings.Repeat("d", pad), base)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if len(p) != total {
		t.Fatalf("built a %d-byte path, want %d", len(p), total)
	}
	return p
}
