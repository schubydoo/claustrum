package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// TestMain runs the test binary as a stand-in CLI or launcher when
// CLAUSTRUM_TEST_HELPER names a mode, as the TestMain of package main does for
// the modes that the tests of this package use. Otherwise it runs the tests.
func TestMain(m *testing.M) {
	// Each helper is a copy of this binary. Under -race the tsan runtime of a
	// child sleeps 1 s before it exits unless GORACE says otherwise. An
	// operator-supplied GORACE still wins.
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}
	mode := os.Getenv("CLAUSTRUM_TEST_HELPER")
	if mode == "" {
		os.Exit(m.Run())
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// runHelper runs the helper mode and returns its exit code.
func runHelper(mode string, args []string) int {
	switch mode {
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "wrap":
		// A managed-launcher stand-in: it writes "WRAP:<args>" to stderr, then runs
		// args[0] with args[1:] and exits with its code. The run gets
		// CLAUSTRUM_TEST_HELPER=$CLAUSTRUM_TEST_WRAP_NEXT. When CLAUSTRUM_TEST_WRAP_LOG
		// names a file, a "LAUNCH <args>" line is appended to it first.
		return runWrapLauncher(args)
	case "fail-launcher":
		// A launcher that fails: it writes CLAUSTRUM_TEST_LAUNCH_STDERR to stderr and
		// exits with CLAUSTRUM_TEST_LAUNCH_EXIT. It appends to CLAUSTRUM_TEST_WRAP_LOG
		// like "wrap", and never runs its args.
		if log := os.Getenv("CLAUSTRUM_TEST_WRAP_LOG"); log != "" {
			appendLine(log, "LAUNCH "+strings.Join(args, " "))
		}
		fmt.Fprint(os.Stderr, os.Getenv("CLAUSTRUM_TEST_LAUNCH_STDERR"))
		n, err := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_LAUNCH_EXIT"))
		if err != nil {
			return 1
		}
		return n
	case "selfterm":
		// A launcher that ends itself with SIGTERM (Unix. Windows has no such signal).
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		time.Sleep(10 * time.Second)
		return 1
	case "cli-log":
		// A stand-in CLI that appends "CLI <args> gate=<value>" to
		// CLAUSTRUM_TEST_CLI_LOG and exits 0.
		appendLine(os.Getenv("CLAUSTRUM_TEST_CLI_LOG"),
			"CLI "+strings.Join(args, " ")+" gate="+os.Getenv("CLAUDE_SSH_MANAGED_LAUNCHER"))
	case "cli-stub":
		// A stand-in CLI for the -install bound and order tests. See runCLIStub.
		return runCLIStub()
	default:
		// "slow:N": sleep N seconds, then exit 0.
		if secs, ok := strings.CutPrefix(mode, "slow:"); ok {
			n, err := strconv.Atoi(secs)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			time.Sleep(time.Duration(n) * time.Second)
			return 0
		}
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

// runWrapLauncher implements the "wrap" helper mode. See runHelper.
func runWrapLauncher(args []string) int {
	if log := os.Getenv("CLAUSTRUM_TEST_WRAP_LOG"); log != "" {
		appendLine(log, "LAUNCH "+strings.Join(args, " "))
	}
	fmt.Fprint(os.Stderr, "WRAP:"+strings.Join(args, " ")+"\n")
	if len(args) == 0 {
		return 2
	}
	// An unset next mode does not leave the helper mode empty, or this binary
	// runs the whole suite. An unknown mode exits 2 instead.
	next := os.Getenv("CLAUSTRUM_TEST_WRAP_NEXT")
	if next == "" {
		next = "wrap-next-unset"
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER="+next)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	return 0
}

// runCLIStub implements the "cli-stub" helper mode. Its environment steers it:
//
//   - CLAUSTRUM_TEST_STUB_LOG names a file. The stub appends a START line with
//     its argv[0], its pid, the process ids of stubProcIDs and the launcher gate
//     value. Then it appends a DIR line with the sorted names of the folder of
//     argv[0], joined by commas.
//   - CLAUSTRUM_TEST_STUB_CHILD starts a "sleep" child of this binary that lives
//     30 s (a leak budget) and writes its pid to CLAUSTRUM_TEST_STUB_CHILDPID.
//     "group" keeps the child in the process group of the stub. "session" puts it
//     in a new session. "hold" does that too, and the child keeps the stdout and
//     stderr of the stub.
//   - CLAUSTRUM_TEST_STUB_IGNORETERM=1 makes the stub ignore SIGTERM.
//   - CLAUSTRUM_TEST_STUB_MS is how long the stub sleeps, in milliseconds.
//   - CLAUSTRUM_TEST_STUB_EXIT is its exit code, 0 when it is not set.
func runCLIStub() int {
	if os.Getenv("CLAUSTRUM_TEST_STUB_IGNORETERM") == "1" {
		ignoreSigterm()
	}
	if log := os.Getenv("CLAUSTRUM_TEST_STUB_LOG"); log != "" {
		appendLine(log, fmt.Sprintf("START argv0=%s pid=%d %s gate=%s", os.Args[0], os.Getpid(),
			stubProcIDs(), os.Getenv("CLAUDE_SSH_MANAGED_LAUNCHER")))
		var names []string
		if ents, err := os.ReadDir(filepath.Dir(os.Args[0])); err == nil {
			for _, e := range ents {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		appendLine(log, "DIR "+strings.Join(names, ","))
	}
	if kind := os.Getenv("CLAUSTRUM_TEST_STUB_CHILD"); kind != "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "30")
		child.Env = sysproc.ReplaceOrAppendEnv(os.Environ(), "CLAUSTRUM_TEST_HELPER", "sleep")
		if kind != "group" {
			stubNewSession(child)
		}
		if kind == "hold" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(os.Getenv("CLAUSTRUM_TEST_STUB_CHILDPID"), []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_STUB_MS"))
	time.Sleep(time.Duration(ms) * time.Millisecond)
	code, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_STUB_EXIT"))
	return code
}

// appendLine appends line and a newline to the file at path.
func appendLine(path, line string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

// homeEnvVar is the variable that os.UserHomeDir reads on this platform.
// Setting the wrong one leaves the real home in place.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// jsonString returns s as a JSON string literal, as the daemon encodes it.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stubOsExit and catchExit stand in for the exit stub of package main. No code
// of this package calls os.Exit, so catchExit never reports an exit. An exit
// ends the test binary, and the run fails.
func stubOsExit(t *testing.T) { t.Helper() }

func catchExit(f func()) (code int, exited bool) {
	f()
	return 0, false
}
