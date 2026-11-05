//go:build linux || darwin

package childrec

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
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
	case "term-exit0-gate":
		// On SIGTERM, wait until the file args[0] exists, then exit with code 0. So
		// the test decides the moment at which the child ends.
		exitZeroOnSigtermAfter(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exec":
		// On SIGTERM, replace this program with args[0], run as the "sleep 60"
		// helper. The pid and the start value stay, the program changes.
		execOnSigterm(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "tree-stdout":
		// Start a "sleep 60" grandchild that inherits this process's stdout, write
		// its pid to the file args[0], and linger.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", mode)
		return 2
	}
	return 0
}

// execOnSigterm makes the helper child replace its program with path when SIGTERM
// comes (see the "term-exec" helper mode).
func execOnSigterm(path string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		env := sysproc.ReplaceOrAppendEnv(os.Environ(), "CLAUSTRUM_TEST_HELPER", "sleep")
		_ = syscall.Exec(path, []string{path, "60"}, env)
		os.Exit(3)
	}()
}

// exitZeroOnSigtermAfter makes the helper child exit with code 0 after a SIGTERM, but
// only once the file gate exists (see the "term-exit0-gate" helper mode).
func exitZeroOnSigtermAfter(gate string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		for {
			if _, err := os.Stat(gate); err == nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	}()
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

// waitPIDFile polls until the file holds a parseable PID.
func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				if pid, err := strconv.Atoi(s); err == nil {
					return pid
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never became readable", path)
	return 0
}

// waitProcessGone polls signal-0 until the pid no longer exists (ESRCH).
func waitProcessGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true // ESRCH: the process is gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
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

// syncBuffer is the sink of captureLog. The test goroutine reads it while other
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

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// captureLog points the default logger at a buffer for the duration of fn and
// returns what was logged.
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

// childrenNames returns the entry names of dir.
func childrenNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
