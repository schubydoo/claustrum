package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles the test binary as a tiny cross-platform fixture command
// (the stdlib helper-process pattern): when CLAUSTRUM_TEST_HELPER is set, the
// binary acts as an echo/cat/sleep/… stand-in instead of running the tests.
// This removes the suite's dependency on /bin/sh-style fixtures so it also
// runs on the Windows CI leg, and keeps the streamed bytes byte-identical
// across OSes (no CRLF translation, no cmd.exe quoting).
func TestMain(m *testing.M) {
	mode := os.Getenv("CLAUSTRUM_TEST_HELPER")

	// A re-exec'd daemon child must never run the suite. runServe's parent half
	// re-execs os.Executable() with daemonChildEnv=1 — and under `go test` that
	// executable IS this binary. So a test which reaches the parent path without
	// steering the child into a helper mode forks a detached copy of the whole
	// suite, which reaches the same test and forks again. The copies are
	// setsid-detached with their output redirected to a log, so they outlive the
	// `go test` run that started them and nothing reaps them: a fork bomb that
	// looks like a passing test. That is not hypothetical — it happened on
	// 2026-08-02 and took the host down hard.
	//
	// Every legitimate re-exec either sets a helper mode (see helperCommand) or
	// strips daemonChildEnv (see removeEnvKey in server_daemonize_unix_test.go),
	// so reaching here with the child marker and no mode means a test forgot the
	// seam. Refuse loudly: the cost is one dead process instead of a generation.
	if mode == "" && os.Getenv(daemonChildEnv) == "1" {
		fmt.Fprintln(os.Stderr, "test binary re-exec'd as a daemon child without CLAUSTRUM_TEST_HELPER: refusing to run the suite (a test reached runServe's parent path without the helper seam)")
		os.Exit(1)
	}

	if mode == "" {
		os.Exit(m.Run())
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// helperCommand returns this test binary's path plus the env overlay that
// makes a spawned child run it in the given helper mode. The overlay rides
// process.spawn's env-merge (request env layered over os.Environ()).
func helperCommand(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe, map[string]string{"CLAUSTRUM_TEST_HELPER": mode}
}

// runHelper implements the fixture commands. Each mode's output is fixed and
// newline-exact so stream assertions and goldens hold on every OS.
func runHelper(mode string, args []string) int {
	switch mode {
	case "echo": // /bin/echo: args joined by spaces, trailing newline
		fmt.Print(strings.Join(args, " ") + "\n")
	case "cat": // /bin/cat: copy stdin to stdout until EOF
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "ignore-term":
		// Escalation fixture (Unix): ignore SIGTERM so killAndWait's graceful
		// signal is a no-op and it must escalate to SIGKILL. Announce readiness on
		// stdout so the test only kills once the handler is installed.
		ignoreSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "pwd": // /bin/pwd -P: the physical working directory
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(wd + "\n")
	case "printenv": // print <name>=<value> for args[0] ("" when absent)
		fmt.Print(args[0] + "=" + os.Getenv(args[0]) + "\n")
	case "stdout3": // integration fixture: three stdout lines, exit 0
		fmt.Print("l0\nl1\nl2\n")
	case "stderr-exit5": // integration fixture: one stderr line, exit 5
		fmt.Fprint(os.Stderr, "err\n")
		return 5
	case "tree":
		// Fixture for the whole-tree kill tests: wait for the parent's
		// go-ahead on stdin (so confineProcess runs before any grandchild
		// exists), spawn a grandchild sleeper, record its PID in args[0],
		// then linger so the tree stays alive until it is killed.
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
	case "tree-stdout":
		// Like "tree", but the grandchild INHERITS this process's stdout and this
		// process lingers instead of exiting. That combination is what the
		// escalation test needs: the leader dies on the graceful signal while the
		// grandchild keeps the stdout pipe open, so the exit drain stays pending
		// past the grace and the leader is already reaped by the time the
		// escalation fires.
		//
		// No stdin go-ahead, unlike "tree": this mode is used by Unix-only tests
		// that set the process group at fork, so there is nothing to wait for.
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
	case "orphan-stdout":
		// Fixture for the bounded exit drain: start a grandchild that INHERITS
		// this process's stdout (child.Stdout, unlike "tree" above), print one
		// line, then exit. The spawned process is gone but the pipe stays open
		// for args[0] seconds, which is the only way the drain window is
		// reachable at all.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, args[0])
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print("early\n")
		return 7
	case "runlock-hold":
		// Run-dir eviction fixture (Unix): open the lock file, take the flock, write
		// an owner record naming this process as a serve daemon, announce readiness by
		// creating a ready-file, then linger so claimRunDir must evict it. args[0] =
		// lock path, args[1] = "term-ignore" to swallow SIGTERM (escalation case),
		// args[2] = ready-file path.
		return runlockHoldFixture(args)
	default:
		// "slow:N": sleep N seconds, then exit 0. The honest-but-slow CLI shape
		// divergence D11 is about — correct output, correct exit code, just not
		// fast. Windows counterpart of the `sleep N; exit 0` sh script slowCLI
		// writes elsewhere.
		if secs, ok := strings.CutPrefix(mode, "slow:"); ok {
			n, err := strconv.Atoi(secs)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			time.Sleep(time.Duration(n) * time.Second)
			return 0
		}
		// "exit:N": exit with code N (a stand-in CLI for the -install tests,
		// where the invocation is fixed at `<cli> --version`).
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
