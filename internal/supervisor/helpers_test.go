package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
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
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
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

// buildEnv lays env over the environment of this process. os/exec keeps the last
// value of a name that appears twice.
func buildEnv(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
