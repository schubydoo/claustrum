//go:build unix

package runlock

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

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
	case "runlock-hold":
		// Run-dir eviction fixture: open the lock file, take the flock, write an owner
		// record naming this process as a serve daemon, announce readiness by creating
		// a ready-file, then linger so LockRunFolder must evict it.
		return runlockHoldFixture(args)
	case "runlock-hold-empty":
		// Late-lock fixture: hold the flock on an empty lock file, as a process that
		// is not a daemon does.
		return runlockHoldEmptyFixture(args)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", mode)
		return 2
	}
	return 0
}

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

// buildEnv lays env over the environment of this process. os/exec keeps the last
// value of a name that appears twice.
func buildEnv(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// newDaemonInstanceID returns 32 hex digits, the shape of a daemon instance.
func newDaemonInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ignoreSigterm makes the helper child ignore SIGTERM, so the eviction must
// escalate to SIGKILL.
func ignoreSigterm() { signal.Ignore(syscall.SIGTERM) }

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

// syncBuffer is the sink of captureLogBuf. The test reads the buffer while another
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

// wantLinesInOrder asserts that got holds each line of want, in that order.
func wantLinesInOrder(t *testing.T, got string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w+"\n")
		if i < 0 {
			t.Errorf("log lacks %q after offset %d\nlog:\n%s", w, at, got)
			return
		}
		at += i + len(w)
	}
}

// captureStopLog sends the stdlib log output to a buffer for the rest of the test.
func captureStopLog(t *testing.T) *syncBuffer {
	t.Helper()
	return captureLogBuf(t)
}

// seamServeHolder makes the holder check accept the test's lock-holder child.
// Its real argv is the test binary, not "-serve -socket <sock>".
func seamServeHolder(t *testing.T) {
	t.Helper()
	old := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = old })
}

func requireNodeID(t *testing.T) {
	t.Helper()
	if nodeID() == "" {
		t.Skip("the lock ladder needs a machine identity (nodeID)")
	}
}

// stageToken writes a daemon.token beside sock and returns its path.
func stageToken(t *testing.T, sock string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(sock), persistedTokenName)
	if err := os.WriteFile(p, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stageStaleSocket binds sock and closes the listener without unlinking it, as a
// killed daemon leaves it.
func stageStaleSocket(t *testing.T, sock string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("net.Listen(unix) returned %T, want *net.UnixListener", ln)
	}
	ul.SetUnlinkOnClose(false)
	if err := ul.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertKept(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s removed by -stop (Lstat err = %v), want kept", what, err)
	}
}

func assertGone(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still present after -stop (Lstat err = %v)", what, err)
	}
}
