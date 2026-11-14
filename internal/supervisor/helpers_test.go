package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/shellenv"
)

// TestMain runs the test binary as the exec-child trampoline or as a fixture, as the
// TestMain of package main does for the modes that the tests of this package use.
// Otherwise it runs the tests.
func TestMain(m *testing.M) {
	// Intercept the exec-child trampoline first, exactly as main does. A test re-execs
	// this binary as `--exec-child …`, and that copy must run the trampoline and not the
	// suite. A no-op off Linux and macOS and for any other argv.
	MaybeRunExecChild()

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
		fmt.Fprintln(os.Stderr, guardRefusal)
		os.Exit(1)
	}

	if mode == "" {
		// A fixture spawn starts no login shell, for the SSH agent or for the PATH
		// read, as in the TestMain of package main.
		shellenv.AgentSocket = func() string { return "" }
		shellenv.LoginPATHExtractor = func(context.Context) (string, error) {
			return "", errors.New("the suite stubs the login-shell read")
		}
		os.Exit(m.Run())
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// guardRefusal is the line of the fork-bomb guard of TestMain.
const guardRefusal = "test binary re-exec'd as a daemon child without CLAUSTRUM_TEST_HELPER: refusing to run the suite (a test reached runServe's parent path without the helper seam)"

// TestMainRefusesADaemonChildCopy: a copy of this test binary with the daemon child
// marker and no helper mode exits 1 with the guard line and runs no test. The run
// pattern of the copy names no test, so a copy without the guard exits 0 and starts
// nothing.
func TestMainRefusesADaemonChildCopy(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	env := []string{"CLAUSTRUM_DAEMON_CHILD=1"}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUSTRUM_TEST_HELPER=") && !strings.HasPrefix(e, "CLAUSTRUM_DAEMON_CHILD=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Errorf("the copy ended with %v, want exit status 1", err)
	}
	if got := strings.TrimSpace(string(out)); got != guardRefusal {
		t.Errorf("the copy wrote %q, want the guard line", got)
	}
}

// runHelper implements the fixture commands that the tests of this package use, as
// the runHelper of package main does.
func runHelper(mode string, args []string) int {
	switch mode {
	case "echo": // /bin/echo: args joined by spaces, trailing newline
		fmt.Print(strings.Join(args, " ") + "\n")
	case "cat": // /bin/cat: copy stdin to stdout until EOF
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "pwd": // /bin/pwd -P: the physical working directory
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(wd + "\n")
	case "printenv": // print <name>=<value> for args[0] ("" when absent)
		fmt.Print(args[0] + "=" + os.Getenv(args[0]) + "\n")
	case "environ":
		// print the whole environment, one entry per line, in its order.
		for _, e := range os.Environ() {
			fmt.Print(e + "\n")
		}
	case "ignore-term":
		// Ignore SIGTERM (Unix), then announce readiness on stdout and linger.
		ignoreSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "ignore-hup":
		// Ignore SIGHUP (Unix), then announce readiness on stdout and linger.
		ignoreSighup()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exit0":
		// Exit with code 0 on SIGTERM (Unix), then announce readiness and linger.
		exitZeroOnSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "leave-group-term-exit0":
		// Leave the group of the spawn (Unix), exit with code 0 on SIGTERM (Unix), then
		// announce readiness and linger.
		leaveSpawnGroup()
		exitZeroOnSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exit0-kid":
		// Start a grandchild in the group with no stdio that sleeps 60 s, record its
		// PID in args[0], exit with code 0 on SIGTERM (Unix), then announce
		// readiness and linger.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = BuildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		exitZeroOnSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "tree-stdout":
		// Like "tree", with no go-ahead, and the grandchild inherits stdout. The
		// leader lingers.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = BuildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
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
	case "orphan-stdout":
		// Start a grandchild that inherits stdout and sleeps args[0] seconds, print
		// one line, then exit 7. The pipe stays open after the exit.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, args[0])
		child.Env = BuildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print("early\n")
		return 7
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "tree":
		// Fixture for the whole-tree kill tests: wait for the parent's go-ahead on
		// stdin, spawn a grandchild sleeper, record its PID in args[0], then linger
		// so the tree stays alive until it is killed.
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = BuildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
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

// syncBuffer is the sink of captureLog and captureLogBuf. The test goroutine reads
// it while other goroutines can still log, so every access takes the lock.
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

// captureLog points the default logger at a buffer, with no timestamp, for the run
// of fn, and returns what was logged.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	buf := captureLogBuf(t)
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

// newTestProcManager builds a Manager whose prune sweep ends with the test. A
// test that builds a manager has no teardown of a server to end it.
func newTestProcManager(tb testing.TB) *Manager {
	tb.Helper()
	m := NewManager()
	tb.Cleanup(m.Close)
	return m
}

// sessionArgs are the argv tail that makes a spawn a stream-json session of SESS.
var sessionArgs = []string{"--output-format", "stream-json", "--session-id", "SESS"}

// endOwnChild ends a child of the test that no kill of the daemon reaches, and waits
// for its end. A child that outlives its test writes its exit line into the log
// capture of a later test.
func endOwnChild(t *testing.T, p *Proc) {
	t.Helper()
	p.signalIfLive("KILL", "")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Errorf("the child %s of the test did not end in 10 s", p.id)
	}
}

// countSignalsTo counts the calls of the signalGroup seam for the process p, until the
// test ends. A late signal to a child of an earlier test does not count.
func countSignalsTo(t *testing.T, p *Proc) *atomic.Int32 {
	t.Helper()
	realSignal := signalGroup
	var signals atomic.Int32
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		if proc == p.cmd.Process {
			signals.Add(1)
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	return &signals
}

// activityPipeConn builds a conn whose net.Conn is wrapped as the accept loop wraps
// it. The peer end discards what it reads, so no write blocks.
func activityPipeConn(t *testing.T) (c *conn.Conn, ac *conn.ActivityConn) {
	t.Helper()
	client, server := net.Pipe()
	ac = conn.NewActivityConn(client)
	c = conn.New(ac, ac, 0, nil, make(chan struct{}), nil)
	go func() { _, _ = io.Copy(io.Discard, server) }()
	t.Cleanup(func() { client.Close(); server.Close() })
	return c, ac
}

// stubShellAgentSocket replaces the login-shell lookup for one test and counts the
// calls.
func stubShellAgentSocket(t *testing.T, sock string) *int {
	t.Helper()
	calls := 0
	old := shellenv.AgentSocket
	shellenv.AgentSocket = func() string { calls++; return sock }
	t.Cleanup(func() { shellenv.AgentSocket = old })
	return &calls
}

// unsetForTest removes key from the process env for one test and restores it.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// waitExitFrame reads frames until the exit frame, so the exit line of the child
// goes into the log of this test.
func waitExitFrame(t *testing.T, frames <-chan StreamFrame) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return
			}
		case <-deadline:
			t.Fatal("no exit frame within deadline")
		}
	}
}
