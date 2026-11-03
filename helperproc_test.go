package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/shellenv"
)

// TestMain doubles the test binary as a tiny cross-platform fixture command
// (the stdlib helper-process pattern): when CLAUSTRUM_TEST_HELPER is set, the
// binary acts as an echo/cat/sleep/… stand-in instead of running the tests.
// This removes the suite's dependency on /bin/sh-style fixtures so it also
// runs on the Windows CI leg, and keeps the streamed bytes byte-identical
// across OSes (no CRLF translation, no cmd.exe quoting).
func TestMain(m *testing.M) {
	// Intercept the exec-child trampoline first, exactly as main does: a spawn under
	// a run/<clientId>/ socket re-execs this test binary as `--exec-child …`, which
	// must run the trampoline (stamp the child identity, restore held env, exec the
	// real target) rather than the suite. A no-op on non-linux and for any other argv.
	maybeRunExecChild()

	// Every helper re-exec is a copy of THIS binary, and under -race the tsan
	// runtime sleeps atexit_sleep_ms (default 1000ms) in the main thread before
	// the process exits — so each fixture spawn costs a wall-clock second no
	// matter how trivial the fixture is. The suite spawns dozens of them
	// (TestWorktreeCreateLingeringDescendant alone spawns ~30 stub gits), which
	// measured as 48s of the 109s `go test -race ./...` run. Publishing the knob
	// here hands it to every child through buildEnv's os.Environ() base, and
	// leaves the parent's own tsan settings untouched (tsan parses GORACE at
	// startup, and we are already past it). The children run a dozen lines of
	// fixture code and hold no threads at exit, so the sleep buys no detection.
	// An operator-supplied GORACE still wins.
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}

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
		// Spawning a fixture must not start the user's login shell to look for an
		// SSH agent. The tests that cover the hand-off swap in their own stub.
		shellenv.AgentSocket = func() string { return "" }
		// A test that boots runServe starts no login shell for the PATH read either.
		// The tests of the read set their own extractor.
		shellenv.LoginPATHExtractor = func(context.Context) (string, error) {
			return "", errors.New("the suite stubs the login-shell read")
		}
		if err := makeFixtureTemplateRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture template root:", err)
			os.Exit(1)
		}
		code := m.Run()
		removeFixtureTemplates()
		os.Exit(code)
	}
	os.Exit(runHelper(mode, os.Args[1:]))
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

// appendLine appends line and a newline to the file at path.
func appendLine(path, line string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
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
	case "read-line":
		// Farewell fixture: read one line of stdin, print "got:<line>" and exit 0.
		var line []byte
		b := make([]byte, 1)
		for {
			if n, err := os.Stdin.Read(b); n == 0 || err != nil || b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		fmt.Print("got:" + string(line) + "\n")
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
	case "ignore-term":
		// Escalation fixture (Unix): ignore SIGTERM so killAndWait's graceful
		// signal is a no-op and it must escalate to SIGKILL. Announce readiness on
		// stdout so the test only kills once the handler is installed.
		ignoreSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "ignore-hup":
		// Unattended-end fixture (Unix): ignore SIGHUP, so the end must go on to
		// the SIGKILL. Announce readiness on stdout, as ignore-term does.
		ignoreSighup()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exit0":
		// Exit-line fixture (Unix): on SIGTERM, exit with code 0. Announce readiness
		// on stdout so the test only signals once the handler is installed.
		exitZeroOnSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exit0-gate":
		// Reap fixture (Unix): on SIGTERM, wait until the file args[0] exists, then
		// exit with code 0. So the test decides the moment at which the child ends.
		exitZeroOnSigtermAfter(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exec":
		// Reap fixture (Unix): on SIGTERM, replace this program with args[0], run as
		// the "sleep 60" helper. The pid and the start value stay, the program changes.
		execOnSigterm(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "filter-mark":
		// A stand-in git filter. It appends one line to the file args[0], to prove
		// that it ran, and then copies stdin to stdout as cat does.
		appendLine(args[0], "ran")
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
	case "git-envlog":
		// Stand-in `git` that records two variables of its environment and fails.
		// Reached through a PATH symlink named `git`. Each call appends one line
		// to the file that CLAUSTRUM_GITSTUB_ENVLOG names.
		line := ""
		for _, name := range strings.Fields(os.Getenv("CLAUSTRUM_GITSTUB_ENVNAMES")) {
			if v, ok := os.LookupEnv(name); ok {
				line += name + " set " + v + ";"
			} else {
				line += name + " absent;"
			}
		}
		appendLine(os.Getenv("CLAUSTRUM_GITSTUB_ENVLOG"), line)
		return 1
	case "job-flags": // Windows: print the limit flags of the own job, as "0x2800"
		fmt.Print(ownJobFlags() + "\n")
	case "lookupenv": // print "<name> set <value>" or "<name> absent" for args[0]
		if v, ok := os.LookupEnv(args[0]); ok {
			fmt.Print(args[0] + " set " + v + "\n")
		} else {
			fmt.Print(args[0] + " absent\n")
		}
	case "bridge":
		// Fixture for TestBridgeExitsZeroOnClosedStdout: run the real main in -bridge mode.
		os.Args = []string{"claustrum", "-bridge", "-socket", os.Getenv("CLAUSTRUM_TEST_BRIDGE_SOCK")}
		main()
	case "sigpipe":
		// Behavior fixture for TestIgnoreSigpipeSurvivesClosedStdout: ignore SIGPIPE,
		// wait for the parent's go-ahead (sent only after it has closed the stdout read
		// end), then write to stdout. With the ignore in place the write fails with
		// EPIPE and we exit 0; without it SIGPIPE kills us (exit 141).
		ignoreSigpipeDefault()
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		_, _ = os.Stdout.Write([]byte("to-a-closed-pipe"))
	case "stdout3": // integration fixture: three stdout lines, exit 0
		fmt.Print("l0\nl1\nl2\n")
	case "stderr-exit5": // integration fixture: one stderr line, exit 5
		fmt.Fprint(os.Stderr, "err\n")
		return 5
	case "both-streams": // one stdout line, then one stderr line, exit 0
		fmt.Print("out\n")
		fmt.Fprint(os.Stderr, "err\n")
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
	case "tree-stdio":
		// Like "tree-stdout", but the grandchild inherits stdout AND stderr of this
		// process. A kill of this process alone then leaves both pipes of the daemon
		// open until the exit drain gives up.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
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
	case "exit-leave-kid":
		// Start a grandchild sleeper that inherits the stdout and stderr of this
		// process, write its pid to args[0], and exit 0 at once. The pipes stay open
		// while the grandchild lives.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "wrap":
		// A managed-launcher stand-in, the shape of the wrapper of the measured rows: it writes
		// "WRAP:<args>" to stderr, then runs args[0] with args[1:] and exits with its code. The
		// run gets CLAUSTRUM_TEST_HELPER=$CLAUSTRUM_TEST_WRAP_NEXT, so the command is this
		// binary in another mode. When CLAUSTRUM_TEST_WRAP_LOG names a file, a "LAUNCH <args>"
		// line is appended to it first, to prove the launcher ran.
		return runWrapLauncher(args)
	case "printenvs":
		// print <name>=<value> for each arg, one per line, "" when absent.
		for _, name := range args {
			fmt.Print(name + "=" + os.Getenv(name) + "\n")
		}
	case "environ":
		// print the whole environment, one entry per line, in its order.
		for _, e := range os.Environ() {
			fmt.Print(e + "\n")
		}
	case "detach-launch":
		// A stand-in for the -serve launcher. It waits for the go-ahead on stdin, so
		// the test can put this process in a job first. It then starts a detached
		// sleeper the way daemonizeWithToken starts the daemon child
		// (detachSysProcAttr and startDetached), writes its pid to args[0] and exits.
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.SysProcAttr = detachSysProcAttr()
		if err := startDetached(child); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "spawn-hold":
		// A stand-in for a daemon that owns one child: spawn a sleeper through the
		// production process manager, write its pid to args[0], then linger until the
		// test ends this process hard. On Windows args[1] "job" puts the child in a job
		// of its own, "keep" in a job of jobsKeep, and "no-job" in none. Before the pid
		// it writes the job state of the child (childJobState) to args[0] + ".job".
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		m := newProcManager()
		if len(args) > 1 && args[1] == "job" {
			m.jobs = jobsOn
		}
		if len(args) > 1 && args[1] == "keep" {
			m.jobs = jobsKeep
		}
		p, err := m.spawn(&conn{nc: client}, "held", exe, []string{"60"}, "", map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"}, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0]+".job", []byte(childJobState(p)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(p.pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	case "breakaway-start":
		// Windows: start a sleeper outside the job of this process
		// (CREATE_BREAKAWAY_FROM_JOB), write its pid to args[0], then linger until the
		// test ends this process. A job that does not allow breakaway fails the start.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		breakawayStart(child)
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	case "runlock-hold":
		// Run-dir eviction fixture (Unix): open the lock file, take the flock, write
		// an owner record naming this process as a serve daemon, announce readiness by
		// creating a ready-file, then linger so lockRunFolder must evict it. args[0] =
		// lock path, args[1] = "term-ignore" to swallow SIGTERM (escalation case),
		// args[2] = ready-file path.
		return runlockHoldFixture(args)
	case "runlock-hold-empty":
		// Late-lock fixture (Unix): hold the flock on an empty lock file, as a process
		// that is not a daemon does. args[0] = lock path, args[1] = ready-file path,
		// args[2] = a file whose creation makes the fixture let go and exit 0.
		return runlockHoldEmptyFixture(args)
	case "peer-netns":
		// Peer check fixture (Linux): a daemon whose caller has another network
		// namespace. See peerNetnsFixture.
		return peerNetnsFixture()
	case "peer-dial":
		// Peer check fixture: dial the unix socket args[0]. With args[1] = "exit" it
		// exits at once, with the connection open. Otherwise it writes args[1] and a
		// newline, and copies what it reads to stdout until EOF or an error.
		nc, err := net.Dial("unix", args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if args[1] == "exit" {
			return 0
		}
		_, _ = nc.Write([]byte(args[1] + "\n"))
		_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.Copy(os.Stdout, nc)
	default:
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
